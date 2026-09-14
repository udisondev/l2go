package loginlink

import (
	"context"
	"crypto/tls"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

// Пары keepalive стыка (F26): клиент пингует каждые KeepaliveTime, сервер
// режет пингующее чаще KeepaliveMinPing. Односторонний клиентский пинг против
// дефолтного сервера дал бы флап too_many_pings; без пинга half-open TCP
// (power loss/NAT) держал бы ghost-запись реестра 2ч20м…∞.
const (
	KeepaliveTime    = 30 * time.Second
	KeepaliveTimeout = 15 * time.Second
	KeepaliveMinPing = 10 * time.Second
)

// Server — серверная половина стыка: gRPC-сервис над стором сессий и реестром
// GS; LS-сторона. Регистрация GS живёт, пока поток открыт: обрыв убирает
// запись (guard принадлежности), повторная регистрация по hexID — замена с
// терминальным Displaced старому потоку.
type Server struct {
	UnimplementedLoginLinkServer
	sessions *Sessions
	reg      *registry
}

// NewServer — сервер стыка над готовым стором сессий (один стор на процесс:
// login-флоу и хендлеры делят состояние).
func NewServer(sessions *Sessions) *Server {
	return &Server{sessions: sessions, reg: newRegistry()}
}

// GRPCServerOptions — опции grpc.Server стыка: mTLS (клиентский сертификат
// обязателен, проверка против CA — требование владельца: левый GS не
// зарегистрируется) + keepalive-пара. nil-конфиг — нарушение программного
// контракта wire-up: стыка без TLS нет.
func GRPCServerOptions(tlsCfg *tls.Config) []grpc.ServerOption {
	if tlsCfg == nil {
		panic("loginlink: GRPCServerOptions: tls-конфиг обязателен (mTLS стыка)")
	}
	return []grpc.ServerOption{
		grpc.Creds(credentials.NewTLS(tlsCfg)),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    KeepaliveTime,
			Timeout: KeepaliveTimeout,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime: KeepaliveMinPing,
		}),
	}
}

// RegisterGameServer — поток жизни регистрации: ack, затем события (Kick;
// Displaced — терминально). Возврат хендлера закрывает поток.
func (s *Server) RegisterGameServer(req *RegisterGameServerRequest,
	stream LoginLink_RegisterGameServerServer) error {
	rec, err := s.reg.register(string(req.GetHexId()), req.GetHost(), req.GetPort(), req.GetName())
	if err != nil {
		return err
	}
	defer s.reg.unregister(rec)
	if err := stream.Send(&RegisterEvent{
		Kind: &RegisterEvent_Ack{Ack: &RegisterAck{ServerId: uint32(rec.entry.ID)}},
	}); err != nil {
		return err
	}
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case <-rec.displaced:
			// Best-effort: клиент обязан завершиться и без доставки события.
			_ = stream.Send(&RegisterEvent{Kind: &RegisterEvent_Displaced{}})
			return nil
		case <-s.reg.shutdownC:
			return nil // без Displaced: клиент реконнектится с паузой
		case msg := <-rec.kick:
			if err := stream.Send(&RegisterEvent{
				Kind: &RegisterEvent_Kick{Kick: &Kick{Account: msg.account, Reason: msg.reason}},
			}); err != nil {
				return err
			}
		}
	}
}

// ValidateSession — атомарная сверка четырёх ключей с изъятием сессии.
func (s *Server) ValidateSession(_ context.Context, req *ValidateSessionRequest) (*ValidateSessionReply, error) {
	valid := s.sessions.Consume(req.GetAccount(),
		req.GetLoginOk1(), req.GetLoginOk2(), req.GetPlayOk1(), req.GetPlayOk2())
	return &ValidateSessionReply{Valid: valid}, nil
}

// Kick — рассылка кика всем живым GS; реализация P3.4 — заглушка с журналом
// (потребитель — P3.6+ через транспорт).
func (s *Server) Kick(account, reason string) {
	s.reg.kickAll(account, reason)
}

// Servers — записи реестра по возрастанию ID (построение ServerList в login).
func (s *Server) Servers() []GameServerEntry {
	return s.reg.list()
}

// ShutdownStreams останавливает стык: регистрации закрываются (хендлеры
// возвращаются), новые отклоняются; вызывается до GracefulStop в wire-up —
// вечные потоки регистрации иначе вешают GracefulStop.
func (s *Server) ShutdownStreams() {
	s.reg.shutdown()
}
