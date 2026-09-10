// LoginClient — последовательный request-response клиент LoginServer:
// стадии хендшейка, логина, списка серверов и выбора сервера. Каждая
// типизированная стадия читает ответ через конструктор представления P1.4:
// обрезанное тело — детерминированная ошибка стадии, не паника.

package l2client

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/udisondev/l2go/internal/crypto"
	"github.com/udisondev/l2go/internal/protocol"
)

// Опкоды пакетов логин-флоу и их имена трафик-лога (значения — из каталога
// опкодов пакета protocol; машинная связь констант с каталогом —
// TestLogNamesCatalog, экспорта опкодов из protocol нет).
const (
	opRequestAuthLogin     = 0x00                   // C→LS
	opRequestServerLogin   = 0x02                   // C→LS
	opRequestServerList    = 0x05                   // C→LS
	opAuthGameGuard        = 0x07                   // C→LS
	opAccountKicked        = 0x02                   // LS→C
	opGGAuth               = 0x0B                   // LS→C
	opLoginFail            = 0x01                   // LS→C
	opLoginOk              = 0x03                   // LS→C
	opServerList           = 0x04                   // LS→C
	opPlayFail             = 0x06                   // LS→C
	opPlayOk               = 0x07                   // LS→C
	opInit                 = 0x00                   // LS→C
	nameAuthGameGuard      = "AUTH_GAME_GUARD"      // C→LS
	nameRequestAuthLogin   = "REQUEST_AUTH_LOGIN"   // C→LS
	nameRequestServerList  = "REQUEST_SERVER_LIST"  // C→LS
	nameRequestServerLogin = "REQUEST_SERVER_LOGIN" // C→LS
	nameAccountKicked      = "ACCOUNT_KICKED"       // LS→C
	nameGGAuth             = "GG_AUTH"              // LS→C
	nameInit               = "INIT"                 // LS→C
	nameLoginFail          = "LOGIN_FAIL"           // LS→C
	nameLoginOk            = "LOGIN_OK"             // LS→C
	namePlayFail           = "PLAY_FAIL"            // LS→C
	namePlayOk             = "PLAY_OK"              // LS→C
	nameServerList         = "SERVER_LIST"          // LS→C
)

// rsaExponent — публичная экспонента RSA login-протокола L2 (канон).
const rsaExponent = 65537

// errDial — маркер ошибок подключения (адрес — в контексте ошибки).
var errDial = errors.New("подключение")

// Options — параметры клиента: стадийный таймаут и писатель трафик-лога.
type Options struct {
	Timeout time.Duration // дедлайн стадий чтения/записи; 0 — 10с
	Traffic io.Writer     // детерминированный трафик-лог; nil — лог не ведётся
}

const defaultTimeout = 10 * time.Second

func (o Options) timeout() time.Duration {
	if o.Timeout <= 0 {
		return defaultTimeout
	}
	return o.Timeout
}

// GameEndpoint — адрес игровой ноги и ключи сессии после SelectServer.
type GameEndpoint struct {
	Addr               string
	PlayOk1, PlayOk2   int32
	LoginOk1, LoginOk2 int32
}

// LoginClient — соединение с LoginServer.
type LoginClient struct {
	conn      net.Conn
	crypt     *crypto.LoginCrypt
	opts      Options
	pub       *rsa.PublicKey
	sessionID int32
	loginOk1  int32
	loginOk2  int32
	playOk1   int32
	playOk2   int32
	servers   []protocol.ServerListEntry
}

// DialLogin подключается к LoginServer.
func DialLogin(ctx context.Context, addr string, opts Options) (*LoginClient, error) {
	d := net.Dialer{Timeout: opts.timeout()}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%w к %s: %w", errDial, addr, err)
	}
	setNoDelay(conn)
	return &LoginClient{conn: conn, crypt: crypto.NewLoginCrypt(), opts: opts}, nil
}

// setNoDelay выставляет TCP_NODELAY (явно, по букве задачи; Go-рантайм
// включает его по умолчанию).
func setNoDelay(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		if err := tcp.SetNoDelay(true); err != nil {
			slog.Debug("TCP_NODELAY", "err", err)
		}
	}
}

// Close закрывает соединение логина.
func (lc *LoginClient) Close() error { return lc.conn.Close() }

// readFrame читает одну запись провода [uint16 LE длина][тело] с дедлайном.
func readFrame(conn net.Conn, timeout time.Duration) ([]byte, error) {
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, fmt.Errorf("дедлайн чтения: %w", err)
	}
	var hdr [2]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return nil, wrapNet("чтение заголовка кадра", err)
	}
	n := int(hdr[0]) | int(hdr[1])<<8
	if n < 2 {
		return nil, fmt.Errorf("длина кадра %d: %w", n, protocol.ErrFrameLength)
	}
	body := make([]byte, n-2)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, wrapNet("чтение тела кадра", err)
	}
	return body, nil
}

// writeRecord пишет запись провода [uint16 LE длина всей записи][кадр] —
// единственное место префикса длины.
func writeRecord(conn net.Conn, timeout time.Duration, frame []byte) error {
	if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("дедлайн записи: %w", err)
	}
	buf := make([]byte, 2+len(frame))
	buf[0] = byte(len(buf))
	buf[1] = byte(len(buf) >> 8)
	copy(buf[2:], frame)
	if _, err := conn.Write(buf); err != nil {
		return wrapNet("запись кадра", err)
	}
	return nil
}

// wrapNet оборачивает сетевую ошибку контекстом стадии; таймауты помечены.
func wrapNet(stage string, err error) error {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return fmt.Errorf("%s: таймаут стадии: %w", stage, err)
	}
	return fmt.Errorf("%s: %w", stage, err)
}

// Handshake — стадии Init и пробы GameGuard: чтение Init (статический BF +
// обратный XOR), установка динамического ключа, RSA-публикация, AuthGameGuard
// со сверкой sessionID в ответе GGAuth.
func (lc *LoginClient) Handshake() error {
	frame, err := readFrame(lc.conn, lc.opts.timeout())
	if err != nil {
		return fmt.Errorf("стадия Init: %w", err)
	}
	if err := lc.crypt.DecryptInit(frame); err != nil {
		return fmt.Errorf("стадия Init: %w", err)
	}
	v, ok := protocol.NewInitView(frame)
	if !ok {
		return fmt.Errorf("стадия Init: обрезанное тело (%d Б)", len(frame))
	}
	lc.sessionID = v.SessionID()
	if err := lc.crypt.SetKey(v.BlowfishKey()); err != nil {
		return fmt.Errorf("стадия Init: %w", err)
	}
	mod, err := crypto.RSAUnscrambleModulus(v.Modulus())
	if err != nil {
		return fmt.Errorf("стадия Init: %w", err)
	}
	lc.pub = &rsa.PublicKey{N: new(big.Int).SetBytes(mod), E: rsaExponent}
	lc.logRecv(nameInit,
		Field{K: "session", V: num32(v.SessionID())},
		Field{K: "revision", V: fmt.Sprintf("0x%04X", v.Revision())})

	var guard [protocol.AuthGameGuardSize]byte
	protocol.WriteAuthGameGuard(guard[:], lc.sessionID)
	if err := lc.writeEnc(guard[:]); err != nil {
		return fmt.Errorf("стадия AuthGameGuard: %w", err)
	}
	lc.logSend(nameAuthGameGuard, Field{K: "session", V: num32(lc.sessionID)})

	reply, err := lc.readDec()
	if err != nil {
		return fmt.Errorf("стадия GGAuth: %w", err)
	}
	gg, ok := protocol.NewGGAuthView(reply)
	if !ok {
		return fmt.Errorf("стадия GGAuth: обрезанное тело (%d Б)", len(reply))
	}
	if gg.Response() != lc.sessionID {
		return fmt.Errorf("стадия GGAuth: чужой session %d; want %d", gg.Response(), lc.sessionID)
	}
	lc.logRecv(nameGGAuth, Field{K: "response", V: num32(gg.Response())})
	return nil
}

// Login отправляет учётные данные (plain-блок → RSA → wire) и читает ответ:
// LoginOk сохраняет ключи сессии; LoginFail и прочее — ошибки с reason.
func (lc *LoginClient) Login(user, pass string) error {
	var block [protocol.RequestAuthLoginPlainSize]byte
	if err := protocol.WriteRequestAuthLoginPlain(block[:], user, pass); err != nil {
		return fmt.Errorf("стадия RequestAuthLogin: %w", err)
	}
	ct, err := crypto.RSAEncryptNoPadding(lc.pub, block[:])
	if err != nil {
		return fmt.Errorf("стадия RequestAuthLogin: %w", err)
	}
	var wire [protocol.RequestAuthLoginSize]byte
	protocol.WriteRequestAuthLogin(wire[:], ct)
	if err := lc.writeEnc(wire[:]); err != nil {
		return fmt.Errorf("стадия RequestAuthLogin: %w", err)
	}
	// Учётные данные не печатаются: только шифротекст (знание пассивного слушателя).
	lc.logSend(nameRequestAuthLogin, Field{K: "rsa", V: hexs(ct)})

	reply, err := lc.readDec()
	if err != nil {
		return fmt.Errorf("стадия LoginOk: %w", err)
	}
	switch reply[0] {
	case opLoginOk:
		v, ok := protocol.NewLoginOkView(reply)
		if !ok {
			return fmt.Errorf("стадия LoginOk: обрезанное тело (%d Б)", len(reply))
		}
		lc.loginOk1, lc.loginOk2 = v.LoginOkID1(), v.LoginOkID2()
		lc.logRecv(nameLoginOk,
			Field{K: "loginOk1", V: num32(lc.loginOk1)},
			Field{K: "loginOk2", V: num32(lc.loginOk2)})
		return nil
	case opLoginFail:
		v, ok := protocol.NewLoginFailView(reply)
		if !ok {
			return fmt.Errorf("стадия LoginFail: обрезанное тело (%d Б)", len(reply))
		}
		lc.logRecv(nameLoginFail, Field{K: "reason", V: fmt.Sprintf("0x%02X", v.Reason())})
		return fmt.Errorf("логин отклонён: reason=0x%02X", v.Reason())
	case opAccountKicked:
		v, ok := protocol.NewAccountKickedView(reply)
		if !ok {
			return fmt.Errorf("стадия AccountKicked: обрезанное тело (%d Б)", len(reply))
		}
		lc.logRecv(nameAccountKicked, Field{K: "reason", V: fmt.Sprintf("0x%02X", v.Reason())})
		return fmt.Errorf("аккаунт исключён: reason=0x%02X", v.Reason())
	default:
		return fmt.Errorf("неожиданный ответ логина: опкод 0x%02X", reply[0])
	}
}

// ServerList запрашивает и возвращает список серверов и счётчики персонажей.
func (lc *LoginClient) ServerList() ([]protocol.ServerListEntry, []protocol.ServerChars, error) {
	var wire [protocol.RequestServerListSize]byte
	protocol.WriteRequestServerList(wire[:], lc.loginOk1, lc.loginOk2)
	if err := lc.writeEnc(wire[:]); err != nil {
		return nil, nil, fmt.Errorf("стадия ServerList: %w", err)
	}
	lc.logSend(nameRequestServerList,
		Field{K: "loginOk1", V: num32(lc.loginOk1)},
		Field{K: "loginOk2", V: num32(lc.loginOk2)})

	reply, err := lc.readDec()
	if err != nil {
		return nil, nil, fmt.Errorf("стадия ServerList: %w", err)
	}
	if len(reply) == 0 {
		return nil, nil, fmt.Errorf("стадия ServerList: пустой ответ")
	}
	if reply[0] != opServerList {
		if reply[0] == opLoginFail {
			if v, ok := protocol.NewLoginFailView(reply); ok {
				lc.logRecv(nameLoginFail, Field{K: "reason", V: fmt.Sprintf("0x%02X", v.Reason())})
				return nil, nil, fmt.Errorf("список серверов отклонён: reason=0x%02X", v.Reason())
			}
		}
		return nil, nil, fmt.Errorf("неожиданный ответ списка серверов: опкод 0x%02X", reply[0])
	}
	v, ok := protocol.NewServerListView(reply)
	if !ok {
		return nil, nil, fmt.Errorf("стадия ServerList: обрезанное тело (%d Б)", len(reply))
	}
	servers := make([]protocol.ServerListEntry, 0, v.Count())
	for i := 0; i < v.Count(); i++ {
		s, ok := v.Server(i)
		if !ok {
			return nil, nil, fmt.Errorf("стадия ServerList: запись %d обрезана", i)
		}
		servers = append(servers, s)
	}
	var chars []protocol.ServerChars
	if n, ok := v.CharsCount(); ok {
		for i := 0; i < n; i++ {
			c, ok := v.Chars(i)
			if !ok {
				return nil, nil, fmt.Errorf("стадия ServerList: счётчик %d обрезан", i)
			}
			chars = append(chars, c)
		}
	}
	lc.servers = servers
	lc.logRecv(nameServerList, serverListFields(v)...)
	return servers, chars, nil
}

// SelectServer выбирает игровой сервер и возвращает его адрес с ключами
// сессии (PlayOk) — вход game-фазы.
func (lc *LoginClient) SelectServer(id byte) (GameEndpoint, error) {
	var wire [protocol.RequestServerLoginSize]byte
	protocol.WriteRequestServerLogin(wire[:], lc.loginOk1, lc.loginOk2, id)
	if err := lc.writeEnc(wire[:]); err != nil {
		return GameEndpoint{}, fmt.Errorf("стадия PlayOk: %w", err)
	}
	lc.logSend(nameRequestServerLogin, Field{K: "server", V: num(int64(id))})

	reply, err := lc.readDec()
	if err != nil {
		return GameEndpoint{}, fmt.Errorf("стадия PlayOk: %w", err)
	}
	switch reply[0] {
	case opPlayOk:
		v, ok := protocol.NewPlayOkView(reply)
		if !ok {
			return GameEndpoint{}, fmt.Errorf("стадия PlayOk: обрезанное тело (%d Б)", len(reply))
		}
		lc.playOk1, lc.playOk2 = v.PlayOkID1(), v.PlayOkID2()
		lc.logRecv(namePlayOk,
			Field{K: "playOk1", V: num32(lc.playOk1)},
			Field{K: "playOk2", V: num32(lc.playOk2)})
	case opPlayFail:
		v, ok := protocol.NewPlayFailView(reply)
		if !ok {
			return GameEndpoint{}, fmt.Errorf("стадия PlayFail: обрезанное тело (%d Б)", len(reply))
		}
		lc.logRecv(namePlayFail, Field{K: "reason", V: fmt.Sprintf("0x%02X", v.Reason())})
		return GameEndpoint{}, fmt.Errorf("выбор сервера отклонён: reason=0x%02X", v.Reason())
	default:
		return GameEndpoint{}, fmt.Errorf("неожиданный ответ выбора сервера: опкод 0x%02X", reply[0])
	}
	for _, s := range lc.servers {
		if s.ID == id {
			ep := GameEndpoint{
				Addr:     fmt.Sprintf("%d.%d.%d.%d:%d", s.IP[0], s.IP[1], s.IP[2], s.IP[3], s.Port),
				PlayOk1:  lc.playOk1,
				PlayOk2:  lc.playOk2,
				LoginOk1: lc.loginOk1,
				LoginOk2: lc.loginOk2,
			}
			// адрес game-ноги не входит в трафик-лог (детерминизм golden) —
			// наблюдаемость через slog
			slog.Debug("выбран game-сервер", "id", id, "addr", ep.Addr)
			return ep, nil
		}
	}
	return GameEndpoint{}, fmt.Errorf("выбранный сервер %d отсутствует в списке", id)
}

// readDec читает кадр и расшифровывает его динамическим ключом.
func (lc *LoginClient) readDec() ([]byte, error) {
	frame, err := readFrame(lc.conn, lc.opts.timeout())
	if err != nil {
		return nil, err
	}
	if err := lc.crypt.Decrypt(frame); err != nil {
		return nil, err
	}
	return frame, nil
}

// writeEnc шифрует payload динамическим ключом и пишет запись провода.
func (lc *LoginClient) writeEnc(payload []byte) error {
	buf := make([]byte, 2+len(payload)+crypto.MaxFrameOverhead)
	n, err := lc.crypt.Encrypt(buf[2:], payload)
	if err != nil {
		return err
	}
	return writeRecord(lc.conn, lc.opts.timeout(), buf[2:2+n])
}

func (lc *LoginClient) logRecv(name string, fields ...Field) {
	if lc.opts.Traffic != nil {
		LogRecv(lc.opts.Traffic, name, fields...)
	}
}

func (lc *LoginClient) logSend(name string, fields ...Field) {
	if lc.opts.Traffic != nil {
		LogSend(lc.opts.Traffic, name, fields...)
	}
}

// serverListFields — поля ServerList: записи без ip/port (детерминизм golden
// при эфемерном порте GS-ноги сценария; адрес — slog Debug стадии выбора).
func serverListFields(v protocol.ServerListView) []Field {
	fields := []Field{
		{K: "count", V: num(int64(v.Count()))},
		{K: "last", V: num(int64(v.LastServer()))},
	}
	charsN, hasChars := v.CharsCount()
	for i := 0; i < v.Count(); i++ {
		s, ok := v.Server(i)
		if !ok {
			break
		}
		suffix := ""
		if hasChars && i < charsN {
			if c, ok := v.Chars(i); ok {
				suffix = fmt.Sprintf(" chars=%d", c.CharCount)
				if len(c.DeleteTimes) > 0 {
					var times []string
					for _, ts := range c.DeleteTimes {
						times = append(times, num32(ts))
					}
					suffix += " del=" + strings.Join(times, ",")
				}
			}
		}
		fields = append(fields, Field{K: "s" + strconv.Itoa(i+1), V: fmt.Sprintf(
			"{id=%d cur=%d max=%d age=%d pvp=%s status=%d type=%d brackets=%s%s}",
			s.ID, s.CurrentPlayers, s.MaxPlayers, s.AgeLimit, boolean(s.PvP),
			s.Status, s.ServerType, boolean(s.Brackets), suffix)})
	}
	return fields
}
