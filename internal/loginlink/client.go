package loginlink

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"crypto/tls"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

// Константы клиентской половины (настройка флагами не даётся: субъект один —
// владелец топологии; F19/F31).
const (
	// ReRegisterPause — пауза между попытками регистрации; канал построен с
	// ConnectParams Backoff = паузе (Multiplier 1, Jitter 0): дефолтный backoff
	// канала растёт до 120 с и молча аннулировал бы её.
	ReRegisterPause = 5 * time.Second
	// ValidateTimeout — кап-дедлайн одного ValidateSession (F23): зависший LS
	// не блокирует горутину коннекта шлюза бесконечно; err ≠ valid=false.
	ValidateTimeout = 3 * time.Second
)

// ErrDisplaced — регистрация вытеснена новой с тем же hexID: терминально,
// реконнект запрещён (петля замен двух GS с одним hexID).
var ErrDisplaced = errors.New("регистрация вытеснена")

// ClientConfig — параметры регистрации GS (данные, не настройка поведения).
type ClientConfig struct {
	Addr  string
	HexID []byte
	Host  string
	Port  int32
	Name  string
	// TLS — конфиг mTLS-клиента (ClientTLSConfig): сервер проверяется против
	// CA, клиентская пара предъявляется серверу; nil — нарушение контракта
	// wire-up, стыка без mTLS нет.
	TLS *tls.Config
	// OnKick — наблюдатель Kick-событий; nil — только журнал (заглушка P3.4,
	// потребитель P3.6+ пойдёт через транспорт).
	OnKick func(account, reason string)
}

// Client — клиентская половина стыка (GS-процесс): держит регистрацию
// (Run), валидирует сессии входящих игроков (ValidateSession).
type Client struct {
	cfg        ClientConfig
	conn       *grpc.ClientConn
	stub       LoginLinkClient
	registered chan struct{} // закрыт первым ack
	regOnce    sync.Once     // повторные ack (реконнект) не закрывают дважды
	closeOnce  sync.Once
}

// Dial подготавливает соединение (mTLS + keepalive-пара с сервером;
// backoff = паузе); сам поток регистрации держит Run — его запускает
// вызывающий.
func Dial(cfg ClientConfig) (*Client, error) {
	if cfg.TLS == nil {
		return nil, fmt.Errorf("loginlink: Dial(%s): tls-конфиг обязателен (mTLS стыка)", cfg.Addr)
	}
	conn, err := grpc.NewClient(cfg.Addr,
		grpc.WithTransportCredentials(credentials.NewTLS(cfg.TLS)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                KeepaliveTime,
			Timeout:             KeepaliveTimeout,
			PermitWithoutStream: true,
		}),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay:  ReRegisterPause,
				Multiplier: 1,
				Jitter:     0,
				MaxDelay:   ReRegisterPause,
			},
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("loginlink: Dial(%s): %w", cfg.Addr, err)
	}
	return &Client{
		cfg:        cfg,
		conn:       conn,
		stub:       NewLoginLinkClient(conn),
		registered: make(chan struct{}),
	}, nil
}

// Run держит регистрацию: поток → обрыв → пауза → повтор; ErrDisplaced —
// терминально (без реконнекта); отмена ctx завершает цикл.
func (c *Client) Run(ctx context.Context) error {
	for {
		err := c.registerOnce(ctx)
		switch {
		case errors.Is(err, ErrDisplaced):
			return err
		case ctx.Err() != nil:
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(ReRegisterPause):
		}
	}
}

func (c *Client) registerOnce(ctx context.Context) error {
	stream, err := c.stub.RegisterGameServer(ctx, &RegisterGameServerRequest{
		HexId: c.cfg.HexID, Host: c.cfg.Host, Port: c.cfg.Port, Name: c.cfg.Name,
	})
	if err != nil {
		return err
	}
	for {
		ev, err := stream.Recv()
		if err != nil {
			return err
		}
		switch k := ev.GetKind().(type) {
		case *RegisterEvent_Ack:
			c.regOnce.Do(func() { close(c.registered) })
		case *RegisterEvent_Kick:
			if k.Kick == nil {
				continue
			}
			if c.cfg.OnKick != nil {
				c.cfg.OnKick(k.Kick.GetAccount(), k.Kick.GetReason())
			}
			slog.Info("loginlink: kick (заглушка)", "account", k.Kick.GetAccount(),
				"reason", k.Kick.GetReason())
		case *RegisterEvent_Displaced:
			return ErrDisplaced
		}
	}
}

// WaitRegistered блокирует до первого ack регистрации (готовность контура
// e2e/wire-up до открытия игрового порта).
func (c *Client) WaitRegistered(ctx context.Context) error {
	select {
	case <-c.registered:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ValidateSession сверяет ключи сессии на LS: (false, nil) — протокольный
// отказ; (_, err) — LS недоступен, у потребителя P3.6 политика fail-closed.
func (c *Client) ValidateSession(ctx context.Context, account string,
	loginOk1, loginOk2, playOk1, playOk2 int32) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, ValidateTimeout)
	defer cancel()
	reply, err := c.stub.ValidateSession(ctx, &ValidateSessionRequest{
		Account:  account,
		LoginOk1: loginOk1, LoginOk2: loginOk2,
		PlayOk1: playOk1, PlayOk2: playOk2,
	})
	if err != nil {
		return false, fmt.Errorf("loginlink: ValidateSession(%s): %w", account, err)
	}
	return reply.GetValid(), nil
}

// Close закрывает соединение (идемпотентно).
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		err = c.conn.Close()
	})
	return err
}
