package login

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/udisondev/l2go/internal/crypto"
	"github.com/udisondev/l2go/internal/l2client"
	"github.com/udisondev/l2go/internal/loginlink"
	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/pkg/mtls"
)

// env — контур живого LS: персист, стык с зарегистрированным GS, login-сервер.
type env struct {
	t          *testing.T
	root       string
	accounts   *persist.Accounts
	sessions   *loginlink.Sessions
	link       *loginlink.Server
	linkClient *loginlink.Client
	gs         *grpc.Server
	srv        *Server
	addr       string
}

func startEnv(t *testing.T, mutate func(cfg *Config)) *env {
	t.Helper()
	e := &env{t: t}
	e.root = t.TempDir()
	var err error
	e.accounts, err = persist.OpenAccounts(e.root, true)
	if err != nil {
		t.Fatalf("OpenAccounts: %v", err)
	}
	e.sessions = loginlink.NewSessions(5 * time.Minute)
	e.link = loginlink.NewServer(e.sessions)

	stack := newTLSStack(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen link: %v", err)
	}
	e.gs = grpc.NewServer(loginlink.GRPCServerOptions(stack.server)...)
	loginlink.RegisterLoginLinkServer(e.gs, e.link)
	go func() { _ = e.gs.Serve(ln) }()
	t.Cleanup(e.gs.Stop)

	e.linkClient, err = loginlink.Dial(loginlink.ClientConfig{
		Addr: ln.Addr().String(), HexID: []byte("hex-gs"),
		Host: "127.0.0.1", Port: 7777, Name: "Bartz", TLS: stack.client,
	})
	if err != nil {
		t.Fatalf("link Dial: %v", err)
	}
	// t.Context() отменяется перед Cleanup: Run завершится до Close.
	go func() { _ = e.linkClient.Run(t.Context()) }()
	t.Cleanup(func() { _ = e.linkClient.Close() })
	wctx, wcancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer wcancel()
	if err := e.linkClient.WaitRegistered(wctx); err != nil {
		t.Fatalf("GS не зарегистрировался: %v", err)
	}

	cfg := Config{MaxConns: 256, HandshakeTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	if mutate != nil {
		mutate(&cfg)
	}
	e.startLogin(cfg)
	return e
}

// startLogin поднимает login-сервер (переоткрытие персиста = рестарт сервера,
// подмен зависимостей на лету — нет).
func (e *env) startLogin(cfg Config) {
	e.t.Helper()
	var err error
	e.srv, err = New(cfg, e.accounts, e.sessions, e.link)
	if err != nil {
		e.t.Fatalf("login.New: %v", err)
	}
	loginLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		e.t.Fatalf("listen login: %v", err)
	}
	e.addr = loginLn.Addr().String()
	srv := e.srv
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(loginLn) }()
	e.t.Cleanup(func() {
		srv.Close(2 * time.Second)
		select {
		case <-serveDone:
		case <-time.After(3 * time.Second):
			e.t.Errorf("Serve не завершился после Close")
		}
	})
}

// restartLogin переоткрывает персист (кэш после файловой правки) и поднимает
// новый login-сервер; возвращает его адрес.
func (e *env) restartLogin() string {
	e.t.Helper()
	acc, err := persist.OpenAccounts(e.root, true)
	if err != nil {
		e.t.Fatalf("OpenAccounts: %v", err)
	}
	e.accounts = acc
	e.startLogin(Config{MaxConns: 256, HandshakeTimeout: 5 * time.Second, IdleTimeout: time.Minute})
	return e.addr
}

// banAccount переводит аккаунт в banned честной файловой правкой: конверт
// персиста разбирается, Banned=true, чексумма пересчитывается (reader
// сворачивает data перед сравнением — компактные байты совпадают).
func (e *env) banAccount(login string) {
	e.t.Helper()
	path := filepath.Join(e.root, "accounts", login+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		e.t.Fatalf("файл аккаунта: %v", err)
	}
	var envelope struct {
		Schema int             `json:"schema"`
		Data   json.RawMessage `json:"data"`
		SHA256 string          `json:"sha256"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		e.t.Fatalf("конверт аккаунта: %v", err)
	}
	var rec persist.AccountRecord
	if err := json.Unmarshal(envelope.Data, &rec); err != nil {
		e.t.Fatalf("запись аккаунта: %v", err)
	}
	rec.Banned = true
	data, err := json.Marshal(rec)
	if err != nil {
		e.t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	envelope.Data = data
	envelope.SHA256 = hex.EncodeToString(sum[:])
	out, err := json.Marshal(&envelope)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		e.t.Fatalf("правка аккаунта: %v", err)
	}
}

// waitFor поллит cond до timeout: замена sleep-ожиданиям асинхронных событий.
func waitFor(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("условие не наступило за %s: %s", timeout, desc)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func dial(t *testing.T, addr string) *l2client.LoginClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	lc, err := l2client.DialLogin(ctx, addr, l2client.Options{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("DialLogin: %v", err)
	}
	t.Cleanup(func() { _ = lc.Close() })
	if err := lc.Handshake(); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	return lc
}

func TestHappyPath(t *testing.T) {
	e := startEnv(t, nil)
	lc := dial(t, e.addr)
	if err := lc.Login("sergei", "pass123"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	servers, _, err := lc.ServerList()
	if err != nil {
		t.Fatalf("ServerList: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("ServerList: %d записей; want 1", len(servers))
	}
	s := servers[0]
	if s.ID != 1 || s.IP != [4]byte{127, 0, 0, 1} || s.Port != 7777 {
		t.Fatalf("запись = %+v; want ID=1 IP=127.0.0.1 port=7777", s)
	}
	// Дефолты записи — канон (F20): status up, тип Free, pvp.
	if !s.PvP || s.Status != serverStatusUp || s.ServerType != serverTypeFree || s.Brackets || s.AgeLimit != 0 {
		t.Fatalf("дефолты записи = pvp=%v status=%d type=0x%X brackets=%v age=%d; want pvp/up/free/false/0",
			s.PvP, s.Status, s.ServerType, s.Brackets, s.AgeLimit)
	}
	ep, err := lc.SelectServer(servers[0].ID)
	if err != nil {
		t.Fatalf("SelectServer: %v", err)
	}
	// Ключи совпали с сессией: валидация через стык успешна (машинная сверка).
	valid, err := e.linkClient.ValidateSession(t.Context(), "sergei",
		ep.LoginOk1, ep.LoginOk2, ep.PlayOk1, ep.PlayOk2)
	if err != nil || !valid {
		t.Fatalf("ValidateSession(ключи GameEndpoint) = (%v, %v); want (true, nil)", valid, err)
	}
	// Однократность изъятия: replay теми же ключами невалиден.
	valid, err = e.linkClient.ValidateSession(context.Background(), "sergei",
		ep.LoginOk1, ep.LoginOk2, ep.PlayOk1, ep.PlayOk2)
	if err != nil || valid {
		t.Fatalf("ValidateSession(replay) = (%v, %v); want (false, nil)", valid, err)
	}
}

func TestVerdicts(t *testing.T) {
	e := startEnv(t, nil)
	lc := dial(t, e.addr)
	if err := lc.Login("sergei", "pass123"); err != nil {
		t.Fatalf("Login(создание): %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.root, "accounts", "sergei.json")); err != nil {
		t.Fatalf("файл аккаунта не создан авто-созданием: %v", err)
	}

	// Неверный пароль → LoginFail(ReasonUserOrPassWrong).
	lc2 := dial(t, e.addr)
	err := lc2.Login("sergei", "wrong")
	if err == nil || !strings.Contains(err.Error(), "0x02") {
		t.Fatalf("Login(неверный пароль) = %v; want reason 0x02", err)
	}

	// Повторная попытка на закрытом коннекте — не обслуживается (F2).
	if err := lc2.Login("sergei", "pass123"); err == nil {
		t.Fatal("повторный Login после LoginFail: want ошибка закрытого коннекта")
	}

	// Banned → AccountKicked(KickPermanentlyBanned) — файловой правкой (F35 P3.3).
	e.banAccount("sergei")
	lc3 := dial(t, e.restartLogin())
	err = lc3.Login("sergei", "pass123")
	if err == nil || !strings.Contains(err.Error(), "0x20") {
		t.Fatalf("Login(banned) = %v; want reason 0x20", err)
	}
}

func TestClosedModeNoFile(t *testing.T) {
	e := startEnv(t, nil)
	closed, err := persist.OpenAccounts(e.root, false)
	if err != nil {
		t.Fatalf("OpenAccounts: %v", err)
	}
	e.accounts = closed
	e.startLogin(Config{MaxConns: 256, HandshakeTimeout: 5 * time.Second, IdleTimeout: time.Minute})
	lc := dial(t, e.addr)
	if err := lc.Login("ghost", "pass123"); err == nil || !strings.Contains(err.Error(), "0x02") {
		t.Fatalf("Login(закрытый режим) = %v; want reason 0x02", err)
	}
	if _, err := os.Stat(filepath.Join(e.root, "accounts", "ghost.json")); !os.IsNotExist(err) {
		t.Fatalf("файл несуществующего аккаунта создан в закрытом режиме: %v", err)
	}
}

func TestAccountInUse(t *testing.T) {
	e := startEnv(t, nil)
	first := dial(t, e.addr)
	if err := first.Login("sergei", "pass123"); err != nil {
		t.Fatalf("Login(first): %v", err)
	}
	second := dial(t, e.addr)
	err := second.Login("sergei", "pass123")
	if err == nil || !strings.Contains(err.Error(), "0x07") {
		t.Fatalf("Login(second) = %v; want reason 0x07 (AccountInUse)", err)
	}
	// Старый коннект кикается: следующая операция падает закрытым коннектом.
	if _, _, err := first.ServerList(); err == nil {
		t.Fatal("ServerList(first) после вытеснения: want ошибка закрытого коннекта")
	}
	// Сессия удалена: следующая попытка проходит (замена со следующей попытки).
	third := dial(t, e.addr)
	if err := third.Login("sergei", "pass123"); err != nil {
		t.Fatalf("Login(third, замена со следующей попытки): %v", err)
	}
}

func TestServerLoginUnknownServer(t *testing.T) {
	e := startEnv(t, nil)
	lc := dial(t, e.addr)
	if err := lc.Login("sergei", "pass123"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	_, err := lc.SelectServer(99)
	if err == nil || !strings.Contains(err.Error(), "0x0F") {
		t.Fatalf("SelectServer(99) = %v; want reason 0x0F (ServerOverloaded)", err)
	}
}

func TestEvilInputs(t *testing.T) {
	e := startEnv(t, nil)

	// Мусорный первый кадр: детерминированное закрытие, не паника.
	conn, err := net.Dial("tcp", e.addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := readRecord(conn); err != nil {
		t.Fatalf("Init не прочитан: %v", err)
	}
	frame := make([]byte, 24)
	for i := range frame {
		frame[i] = byte(i * 7)
	}
	var rec [26]byte
	rec[0], rec[1] = 26, 0
	copy(rec[2:], frame)
	if _, err := conn.Write(rec[:]); err != nil {
		t.Fatalf("запись мусора: %v", err)
	}
	if _, err := expectClose(conn, 3*time.Second); err != nil {
		t.Fatalf("мусорный кадр: %v", err)
	}

	// Заявленная длина сверх кэпа: закрытие без чтения тела.
	conn2, err := net.Dial("tcp", e.addr)
	if err != nil {
		t.Fatalf("dial2: %v", err)
	}
	defer conn2.Close()
	if _, err := readRecord(conn2); err != nil {
		t.Fatalf("Init не прочитан: %v", err)
	}
	if _, err := conn2.Write([]byte{0xFF, 0xFF}); err != nil {
		t.Fatalf("запись кэп-нарушения: %v", err)
	}
	if _, err := expectClose(conn2, 3*time.Second); err != nil {
		t.Fatalf("кадр сверх кэпа: %v", err)
	}

	// Пакет вне стейт-машины: ServerList до логина → закрытие.
	lc := dial(t, e.addr)
	if _, _, err := lc.ServerList(); err == nil {
		t.Fatal("ServerList без Login: want ошибка закрытия")
	}
}

func TestHandshakeAbsoluteDeadline(t *testing.T) {
	// F25: dribble байтами не продлевает хендшейк — закрытие по абсолютному
	// дедлайну фазы.
	e := startEnv(t, func(cfg *Config) { cfg.HandshakeTimeout = 300 * time.Millisecond })
	conn, err := net.Dial("tcp", e.addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := readRecord(conn); err != nil {
		t.Fatalf("Init не прочитан: %v", err)
	}
	// Половина кадра AuthGameGuard, затем тишина.
	half := make([]byte, 12)
	half[0], half[1] = 13, 0 // заявленная длина 11
	if _, err := conn.Write(half); err != nil {
		t.Fatalf("запись dribble: %v", err)
	}
	if _, err := expectClose(conn, 3*time.Second); err != nil {
		t.Fatal("dribble-коннект не закрыт по абсолютному дедлайну")
	}
}

func TestConnLimit(t *testing.T) {
	// Третий коннект сверх лимита отклоняется до Init.
	e := startEnv(t, func(cfg *Config) { cfg.MaxConns = 2 })
	hold := []*l2client.LoginClient{dial(t, e.addr), dial(t, e.addr)}
	defer func() {
		for _, lc := range hold {
			_ = lc.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	third, err := l2client.DialLogin(ctx, e.addr, l2client.Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("третий коннект: диал упал неожиданно: %v", err)
	}
	defer third.Close()
	if err := third.Handshake(); err == nil {
		t.Fatal("третий коннект сверх лимита обслужен (Init получен): want отказ до Init")
	}
}

func TestParallelLogins(t *testing.T) {
	// Стресс (под -race в CI): параллельные входы + валидации стыка.
	e := startEnv(t, nil)
	const n = 6
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		account := fmt.Sprintf("user%02d", i)
		wg.Go(func() {
			lc, err := l2client.DialLogin(t.Context(), e.addr, l2client.Options{Timeout: 5 * time.Second})
			if err != nil {
				errs <- err
				return
			}
			defer lc.Close()
			if err := lc.Handshake(); err != nil {
				errs <- err
				return
			}
			if err := lc.Login(account, "pass123"); err != nil {
				errs <- fmt.Errorf("%s: %w", account, err)
				return
			}
			valid, err := e.linkClient.ValidateSession(t.Context(), account, 1, 2, 3, 4)
			switch {
			case err != nil:
				errs <- fmt.Errorf("%s: стык недоступен: %w", account, err)
			case valid:
				errs <- fmt.Errorf("%s: валидация левыми ключами прошла", account)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// rawClient — тестовый провод с доступом к крипте (злые sessionID/ключи).
type rawClient struct {
	t         *testing.T
	conn      net.Conn
	lc        *crypto.LoginCrypt
	sessionID int32
	loginOk1  int32
	loginOk2  int32
	pub       *rsa.PublicKey
}

func dialRaw(t *testing.T, addr string) *rawClient {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	rc := &rawClient{t: t, conn: conn, lc: crypto.NewLoginCrypt()}
	var header [2]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		t.Fatalf("заголовок Init: %v", err)
	}
	n := int(header[0]) | int(header[1])<<8
	frame := make([]byte, n-2)
	if _, err := io.ReadFull(conn, frame); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := rc.lc.DecryptInit(frame); err != nil {
		t.Fatalf("DecryptInit: %v", err)
	}
	v, ok := protocol.NewInitView(frame)
	if !ok {
		t.Fatal("InitView")
	}
	rc.sessionID = v.SessionID()
	mod, err := crypto.RSAUnscrambleModulus(v.Modulus())
	if err != nil {
		t.Fatalf("RSAUnscrambleModulus: %v", err)
	}
	rc.pub = &rsa.PublicKey{N: new(big.Int).SetBytes(mod), E: 65537}
	if err := rc.lc.SetKey(v.BlowfishKey()); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	return rc
}

func (rc *rawClient) send(payload []byte) {
	rc.t.Helper()
	frame := make([]byte, len(payload)+crypto.MaxFrameOverhead)
	n, err := rc.lc.Encrypt(frame, payload)
	if err != nil {
		rc.t.Fatalf("шифрование: %v", err)
	}
	total := n + 2
	var header [2]byte
	header[0] = byte(total)
	header[1] = byte(total >> 8)
	if _, err := rc.conn.Write(append(header[:], frame[:n]...)); err != nil {
		rc.t.Fatalf("запись: %v", err)
	}
}

func (rc *rawClient) read() []byte {
	rc.t.Helper()
	var header [2]byte
	if _, err := io.ReadFull(rc.conn, header[:]); err != nil {
		rc.t.Fatalf("чтение заголовка: %v", err)
	}
	n := int(header[0]) | int(header[1])<<8
	frame := make([]byte, n-2)
	if _, err := io.ReadFull(rc.conn, frame); err != nil {
		rc.t.Fatalf("чтение кадра: %v", err)
	}
	if err := rc.lc.Decrypt(frame); err != nil {
		rc.t.Fatalf("расшифровка: %v", err)
	}
	return frame
}

// expectFailClose требует отказный пакет (опкод+причина) и закрытие коннекта
// за within: close-after-fail пишет до EOF, оракул строгий.
func (rc *rawClient) expectFailClose(within time.Duration, wantOp, wantReason byte) {
	rc.t.Helper()
	raw, err := expectClose(rc.conn, within)
	if err != nil {
		rc.t.Fatal(err)
	}
	frame, err := protocol.NextFrame(raw)
	if err != nil {
		rc.t.Fatalf("отказный кадр до закрытия не получен: %v", err)
	}
	if err := rc.lc.Decrypt(frame); err != nil {
		rc.t.Fatalf("расшифровка отказного кадра: %v", err)
	}
	if frame[0] != wantOp {
		rc.t.Fatalf("отказный опкод = 0x%02X; want 0x%02X", frame[0], wantOp)
	}
	v, ok := protocol.NewLoginFailView(frame)
	if !ok || byte(v.Reason()) != wantReason {
		rc.t.Fatalf("причина = %#v; want 0x%02X", v, wantReason)
	}
}

// expectClose читает до EOF и возвращает данные, полученные до него
// (отказный пакет при close-after-fail); таймаут чтения без EOF — провал
// «коннект не закрыт», оракул закрытия строгий.
func expectClose(conn net.Conn, within time.Duration) ([]byte, error) {
	if err := conn.SetReadDeadline(time.Now().Add(within)); err != nil {
		return nil, err
	}
	var got []byte
	buf := make([]byte, 512)
	for {
		n, err := conn.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return nil, fmt.Errorf("коннект не закрыт за %s", within)
			}
			return got, nil // EOF/RST: закрытие состоялось
		}
	}
}

// readRecord читает одну запись [uint16 длина][тело] сырого провода.
func readRecord(conn net.Conn) ([]byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return nil, err
	}
	n := int(header[0]) | int(header[1])<<8
	body := make([]byte, n-2)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, err
	}
	return body, nil
}

// login — полный флоу raw-клиента до LoginOk; отказ — фаталь.
func (rc *rawClient) login(user, pass string) {
	rc.t.Helper()
	rc.gg()
	if err := rc.auth(user, pass); err != nil {
		rc.t.Fatalf("Login: %v", err)
	}
}

// gg — фаза AuthGameGuard.
func (rc *rawClient) gg() {
	rc.t.Helper()
	var wire [protocol.AuthGameGuardSize]byte
	protocol.WriteAuthGameGuard(wire[:], rc.sessionID)
	rc.send(wire[:])
	rc.read() // GGAuth
}

// auth — RequestAuthLogin без фатали: (nil — LoginOk, ключи сохранены).
func (rc *rawClient) auth(user, pass string) error {
	rc.t.Helper()
	var plain [protocol.RequestAuthLoginPlainSize]byte
	if err := protocol.WriteRequestAuthLoginPlain(plain[:], user, pass); err != nil {
		return err
	}
	ct, err := crypto.RSAEncryptNoPadding(rc.pub, plain[:])
	if err != nil {
		return err
	}
	var wire [protocol.RequestAuthLoginSize]byte
	protocol.WriteRequestAuthLogin(wire[:], ct)
	rc.send(wire[:])
	reply := rc.read()
	if reply[0] != protocol.OpLoginOk {
		return fmt.Errorf("опкод ответа 0x%02X", reply[0])
	}
	v, ok := protocol.NewLoginOkView(reply)
	if !ok {
		return errors.New("LoginOkView")
	}
	rc.loginOk1, rc.loginOk2 = v.LoginOkID1(), v.LoginOkID2()
	return nil
}

func TestGGWrongSessionID(t *testing.T) {
	e := startEnv(t, nil)
	rc := dialRaw(t, e.addr)
	var wire [protocol.AuthGameGuardSize]byte
	protocol.WriteAuthGameGuard(wire[:], rc.sessionID+1)
	rc.send(wire[:])
	rc.expectFailClose(3*time.Second, protocol.OpLoginFail, byte(protocol.ReasonAccessFailed))
}

func TestWrongLoginPair(t *testing.T) {
	// F9: RequestServerList с неверной парой loginOk в authed-состоянии →
	// LoginFail(ReasonAccessFailed) + закрытие.
	e := startEnv(t, nil)
	rc := dialRaw(t, e.addr)
	rc.login("sergei", "pass123")
	var req [protocol.RequestServerListSize]byte
	protocol.WriteRequestServerList(req[:], 12345, 67890)
	rc.send(req[:])
	rc.expectFailClose(3*time.Second, protocol.OpLoginFail, byte(protocol.ReasonAccessFailed))
}

func TestPacketAfterPlayOk(t *testing.T) {
	e := startEnv(t, nil)
	lc := dial(t, e.addr)
	if err := lc.Login("sergei", "pass123"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, _, err := lc.ServerList(); err != nil {
		t.Fatalf("ServerList: %v", err)
	}
	if _, err := lc.SelectServer(1); err != nil {
		t.Fatalf("SelectServer: %v", err)
	}
	// Окно после PlayOk закрыто: повторный ServerList → закрытие (F11).
	if _, _, err := lc.ServerList(); err == nil {
		t.Fatal("ServerList после PlayOk: want закрытие коннекта")
	}
}

// tlsStack — mTLS-пара для контура (сервер/клиент стыка против одного CA).
type tlsStack struct {
	server, client *tls.Config
}

func newTLSStack(t *testing.T) tlsStack {
	t.Helper()
	m, err := mtls.GenerateMaterial("тест login", nil)
	if err != nil {
		t.Fatalf("материал mTLS: %v", err)
	}
	dir := t.TempDir()
	if err := mtls.WriteMaterial(dir, m); err != nil {
		t.Fatalf("запись материала: %v", err)
	}
	stack := tlsStack{}
	stack.server, err = mtls.ServerConfig(
		filepath.Join(dir, mtls.CAFile), filepath.Join(dir, mtls.ServerCertFile), filepath.Join(dir, mtls.ServerKeyFile))
	if err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}
	stack.client, err = mtls.ClientConfig(
		filepath.Join(dir, mtls.CAFile), filepath.Join(dir, mtls.ClientCertFile), filepath.Join(dir, mtls.ClientKeyFile), "localhost")
	if err != nil {
		t.Fatalf("ClientConfig: %v", err)
	}
	return stack
}

func TestEvilAuthLogin(t *testing.T) {
	// Не-расшифровываемый AuthLogin: мусорный RSA-блок → расшифровка даёт
	// мусор → null-путь вердикта (0x02) и закрытие; обрыв посреди блоба —
	// закрытие по абсолютному дедлайну фазы.
	e := startEnv(t, nil)

	rc := dialRaw(t, e.addr)
	var gg [protocol.AuthGameGuardSize]byte
	protocol.WriteAuthGameGuard(gg[:], rc.sessionID)
	rc.send(gg[:])
	rc.read() // GGAuth
	nonce := make([]byte, 128)
	for i := range nonce {
		nonce[i] = byte(0xA5 ^ i) // детерминированный не-нулевой мусор
	}
	var wire [protocol.RequestAuthLoginSize]byte
	protocol.WriteRequestAuthLogin(wire[:], nonce)
	rc.send(wire[:])
	rc.expectFailClose(3*time.Second, protocol.OpLoginFail, byte(protocol.ReasonUserOrPassWrong))

	// Обрыв посреди RSA-блоба: половина кадра и тишина — дедлайн фазы.
	e2 := startEnv(t, func(cfg *Config) { cfg.HandshakeTimeout = 400 * time.Millisecond })
	rc2 := dialRaw(t, e2.addr)
	protocol.WriteAuthGameGuard(gg[:], rc2.sessionID)
	rc2.send(gg[:])
	rc2.read()
	half := make([]byte, 40)
	half[0], half[1] = protocol.RequestAuthLoginSize, 0 // заявлен полный размер
	for i := 2; i < len(half); i++ {
		half[i] = 0x5A
	}
	if _, err := rc2.conn.Write(half); err != nil {
		t.Fatalf("запись обрыва: %v", err)
	}
	if _, err := expectClose(rc2.conn, 3*time.Second); err != nil {
		t.Fatalf("обрыв посреди RSA-блоба: %v", err)
	}
}

func TestEmptyLogin(t *testing.T) {
	// Пустой/пробельный логин — null-путь (0x02) до авто-создания, файла нет.
	e2 := startEnv(t, nil)
	rc2 := dialRaw(t, e2.addr)
	var gg [protocol.AuthGameGuardSize]byte
	protocol.WriteAuthGameGuard(gg[:], rc2.sessionID)
	rc2.send(gg[:])
	rc2.read()
	var plain [protocol.RequestAuthLoginPlainSize]byte
	if err := protocol.WriteRequestAuthLoginPlain(plain[:], "  ", "pass123"); err != nil {
		t.Fatalf("plain-блок: %v", err)
	}
	ct, err := crypto.RSAEncryptNoPadding(rc2.pub, plain[:])
	if err != nil {
		t.Fatal(err)
	}
	var wire [protocol.RequestAuthLoginSize]byte
	protocol.WriteRequestAuthLogin(wire[:], ct)
	rc2.send(wire[:])
	rc2.expectFailClose(3*time.Second, protocol.OpLoginFail, byte(protocol.ReasonUserOrPassWrong))
	if _, err := os.Stat(filepath.Join(e2.root, "accounts", ".json")); !os.IsNotExist(err) {
		t.Fatalf("файл пустого логина создан: %v", err)
	}
}

func TestWrongLoginPairServerLogin(t *testing.T) {
	// F9, вторая ветка: RequestServerLogin с неверной парой loginOk → 0x15.
	e := startEnv(t, nil)
	rc := dialRaw(t, e.addr)
	rc.login("sergei", "pass123")
	var req [protocol.RequestServerLoginSize]byte
	protocol.WriteRequestServerLogin(req[:], 111, 222, 1)
	rc.send(req[:])
	rc.expectFailClose(3*time.Second, protocol.OpLoginFail, byte(protocol.ReasonAccessFailed))
}

func TestMixedCaseLogin(t *testing.T) {
	// F24 на живом флоу: вход «SerGei», валидация стыка «sergei» — одна сессия.
	e := startEnv(t, nil)
	lc := dial(t, e.addr)
	if err := lc.Login("SerGei", "pass123"); err != nil {
		t.Fatalf("Login(SerGei): %v", err)
	}
	if _, _, err := lc.ServerList(); err != nil {
		t.Fatalf("ServerList: %v", err)
	}
	ep, err := lc.SelectServer(1)
	if err != nil {
		t.Fatalf("SelectServer: %v", err)
	}
	valid, err := e.linkClient.ValidateSession(t.Context(), "sergei",
		ep.LoginOk1, ep.LoginOk2, ep.PlayOk1, ep.PlayOk2)
	if err != nil || !valid {
		t.Fatalf("ValidateSession(sergei после входа SerGei) = (%v, %v); want (true, nil)", valid, err)
	}
}

func TestIdleFullFrameDeadline(t *testing.T) {
	// F25, idle-семантика: дедлайн перезаводится только полным кадром —
	// частичный прогресс байтами после LoginOk не продлевает жизнь коннекта.
	e := startEnv(t, func(cfg *Config) { cfg.IdleTimeout = 400 * time.Millisecond })
	rc := dialRaw(t, e.addr)
	rc.login("sergei", "pass123")
	var req [protocol.RequestServerListSize]byte
	protocol.WriteRequestServerList(req[:], rc.loginOk1, rc.loginOk2)
	// Полный кадр в половине окна — обслуживается (перезавод).
	time.Sleep(150 * time.Millisecond)
	rc.send(req[:])
	reply := rc.read()
	if reply[0] != protocol.OpServerList {
		t.Fatalf("ServerList после перезавода: опкод 0x%02X", reply[0])
	}
	// Частичный кадр (заголовок) и тишина — закрытие по idle.
	time.Sleep(150 * time.Millisecond)
	if _, err := rc.conn.Write([]byte{protocol.RequestServerListSize, 0}); err != nil {
		t.Fatalf("запись частичного кадра: %v", err)
	}
	if _, err := expectClose(rc.conn, 3*time.Second); err != nil {
		t.Fatalf("частичный кадр не закрыт по idle-дедлайну: %v", err)
	}
}

func TestSessionLiveness(t *testing.T) {
	// Канон LoginClient.onDisconnection: сессия умирает вместе с коннектом,
	// если клиент не ушёл на GS; после PlayOk — переживает закрытие LS.
	e := startEnv(t, nil)

	lc := dial(t, e.addr)
	if err := lc.Login("sergei", "pass123"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	_ = lc.Close() // до PlayOk
	waitFor(t, 3*time.Second, "сессия умирает с коннектом до PlayOk", func() bool {
		return e.sessions.Len() == 0
	})

	lc2 := dial(t, e.addr)
	if err := lc2.Login("sergei", "pass123"); err != nil {
		t.Fatalf("Login(повторный): %v", err)
	}
	if _, _, err := lc2.ServerList(); err != nil {
		t.Fatalf("ServerList: %v", err)
	}
	ep, err := lc2.SelectServer(1)
	if err != nil {
		t.Fatalf("SelectServer: %v", err)
	}
	_ = lc2.Close() // после PlayOk: сессия нужна GS для ValidateSession
	valid, err := e.linkClient.ValidateSession(t.Context(), "sergei",
		ep.LoginOk1, ep.LoginOk2, ep.PlayOk1, ep.PlayOk2)
	if err != nil || !valid {
		t.Fatalf("ValidateSession после закрытия LS-коннекта (ушёл на GS) = (%v, %v); want (true, nil)", valid, err)
	}
}

func TestParallelDoubleLogin(t *testing.T) {
	// М1-регресс: гонка двойного логина одного аккаунта — ровно один вход
	// успешен, проигравший вытесняет сессию (канон F5), двух валидируемых
	// сессий не остаётся никогда; следующая попытка проходит.
	e := startEnv(t, nil)
	for range 10 {
		start := make(chan struct{})
		results := make(chan error, 2)
		alive := make([]*l2client.LoginClient, 0, 2)
		var mu sync.Mutex
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() {
				lc, err := l2client.DialLogin(t.Context(), e.addr, l2client.Options{Timeout: 5 * time.Second})
				if err != nil {
					results <- err
					return
				}
				mu.Lock()
				alive = append(alive, lc)
				mu.Unlock()
				if err := lc.Handshake(); err != nil {
					results <- err
					return
				}
				<-start
				results <- lc.Login("dupe", "pass123")
			})
		}
		time.Sleep(50 * time.Millisecond) // оба коннекта дошли до фазы Auth
		close(start)
		wg.Wait()
		close(results)
		defer func() {
			for _, lc := range alive {
				_ = lc.Close()
			}
		}()
		wins, fails := 0, 0
		for err := range results {
			switch {
			case err == nil:
				wins++
			case strings.Contains(err.Error(), "0x07"):
				fails++
			default:
				t.Fatalf("неожиданная ошибка двойного входа: %v", err)
			}
		}
		// Допустимы 1/1 и 0/2: победитель может быть вытеснен до отправки
		// LoginOk (kicked-флаг отвергает запись) — эквивалент последовательного
		// канона. Инвариант: не более одного успеха, отказы только 0x07,
		// живых сессий после гонки нет (F70).
		if wins > 1 || wins+fails != 2 {
			t.Fatalf("двойной вход: успехов %d, отказов 0x07 %d; want 1/1 или 0/2", wins, fails)
		}
		if n := e.sessions.Len(); n != 0 {
			t.Fatalf("живых сессий после гонки = %d; want 0 (вытеснение)", n)
		}
	}
	// Замена со следующей попытки.
	lc := dial(t, e.addr)
	if err := lc.Login("dupe", "pass123"); err != nil {
		t.Fatalf("Login после гонки: %v", err)
	}
}

func TestDoubleLoginKickObservable(t *testing.T) {
	// F37-фальсификатор: в гонке двойного входа победитель обязан получить
	// кик на свой сокет (0x07 + закрытие) — в TOCTOU-варианте кик терялся и
	// сокет победителя молчал до idle. Гоняем до победы raw-клиента.
	e := startEnv(t, nil)
	rcWon := false
	for range 20 {
		rc := dialRaw(t, e.addr)
		rc.gg()
		lc, err := l2client.DialLogin(t.Context(), e.addr, l2client.Options{Timeout: 5 * time.Second})
		if err != nil {
			t.Fatalf("DialLogin: %v", err)
		}
		if err := lc.Handshake(); err != nil {
			t.Fatalf("Handshake: %v", err)
		}
		authErr := make(chan error, 1)
		go func() { authErr <- rc.auth("dupe", "pass123") }()
		lcErr := lc.Login("dupe", "pass123")
		rcErr := <-authErr
		switch {
		case rcErr == nil:
			// raw-клиент победил: кик обязан прийти на его сокет.
			rc.expectFailClose(5*time.Second, protocol.OpLoginFail, byte(protocol.ReasonAccountInUse))
			rcWon = true
			_ = lc.Close()
		case lcErr == nil:
			_ = rc.conn.Close()
		default:
			// «Оба отклонены» — легитимный F70-интерливинг (кик победителя
			// до отправки его LoginOk): победителя нет, наблюдать нечего.
			_ = rc.conn.Close()
			_ = lc.Close()
		}
		if rcWon {
			return
		}
	}
	t.Fatal("raw-клиент не победил ни в одной из 20 гонок — кик победителя не наблюдали")
}
