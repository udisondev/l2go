// GameClient — клиент GameServer: синхронный хендшейк (ProtocolVersion →
// KeyPacket → AuthLogin → CharSelectionInfo → CharacterSelect → CharSelected)
// и стационарная фаза с горутиной чтения и командами каналом. Общей мутации
// нет: ReadPump передаёт сырые кадры (собственный буфер на кадр), расшифровка,
// диспетчеризация и трафик-лог — только в цикле Run.

package l2client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/udisondev/l2go/internal/crypto"
	"github.com/udisondev/l2go/internal/protocol"
)

// Опкоды game-флоу (значения — из каталога P1.3).
const (
	opProtocolVersion = 0x00 // PROTOCOL_VERSION, C→GS
	opAuthLogin       = 0x08 // AUTH_LOGIN, C→GS
	opLogout          = 0x09 // LOGOUT, C→GS
	opCharacterSelect = 0x0D // CHARACTER_SELECT, C→GS

	opKeyPacket      = 0x00 // KEY_PACKET, GS→C
	opCharSelectInfo = 0x13 // CHAR_SELECT_INFO, GS→C
	opGSLoginFail    = 0x14 // LOGIN_FAIL, GS→C
	opCharSelected   = 0x15 // CHAR_SELECTED, GS→C
)

// ErrClosed — команда после выхода Run или Close: детерминированная ошибка,
// не блокировка.
var ErrClosed = errors.New("клиент закрыт")

// GameClient — соединение с GameServer.
type GameClient struct {
	conn     net.Conn
	crypt    *crypto.GameCrypt
	opts     Options
	commands chan command
	done     chan struct{}
	closeOne sync.Once
}

// command — исходящий кадр стационарной фазы (отправляет только Run).
type command struct {
	wire   []byte
	name   string
	fields []Field
}

// DialGame подключается к GameServer (хендшейк — отдельные стадии).
func DialGame(ctx context.Context, addr string, opts Options) (*GameClient, error) {
	d := net.Dialer{Timeout: opts.timeout()}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%w к %s: %v", errDial, addr, err)
	}
	return &GameClient{
		conn:     conn,
		opts:     opts,
		commands: make(chan command), // небуферизованный: контракт «команда после выхода — ошибка»
		done:     make(chan struct{}),
	}, nil
}

// Close идемпотентен: закрывает done и соединение; разблокирует ReadPump и
// отправителей команд.
func (gc *GameClient) Close() error {
	var err error
	gc.closeOne.Do(func() {
		close(gc.done)
		err = gc.conn.Close()
	})
	return err
}

// Handshake — синхронная стадия: ProtocolVersion открытым текстом, KeyPacket
// открытым текстом (Result==0 — версия отклонена), включение шифрования.
func (gc *GameClient) Handshake() error {
	var wire [protocol.ProtocolVersionSize]byte
	protocol.WriteProtocolVersion(wire[:], protocol.ProtocolVersionInterlude)
	if err := writePlainFrame(gc.conn, gc.opts.timeout(), wire[:]); err != nil {
		return fmt.Errorf("стадия ProtocolVersion: %w", err)
	}
	gc.logSend("PROTOCOL_VERSION", Field{K: "version", V: num32(protocol.ProtocolVersionInterlude)})

	frame, err := readFrame(gc.conn, gc.opts.timeout())
	if err != nil {
		return fmt.Errorf("стадия KeyPacket: %w", err)
	}
	v, ok := protocol.NewKeyPacketView(frame)
	if !ok {
		return fmt.Errorf("стадия KeyPacket: обрезанное тело (%d Б)", len(frame))
	}
	if v.Result() == 0 {
		return fmt.Errorf("стадия KeyPacket: версия протокола отклонена сервером")
	}
	var wireKey [8]byte
	copy(wireKey[:], v.Key())
	gc.crypt = crypto.NewGameCrypt(wireKey)
	gc.crypt.Enable()
	gc.logRecv("KEY_PACKET",
		Field{K: "result", V: num(int64(v.Result()))},
		Field{K: "encryption", V: boolean(v.Encryption())},
		Field{K: "serverID", V: num32(v.ServerID())},
		Field{K: "key", V: hexs(v.Key())})
	return nil
}

// Auth — синхронная стадия входа: AuthLogin с ключами сессии, ответ — список
// персонажей или GSLoginFail.
func (gc *GameClient) Auth(ep GameEndpoint, account string) ([]protocol.CharSelectionEntry, error) {
	wire := make([]byte, protocol.AuthLoginSize(account))
	protocol.WriteAuthLogin(wire, account, ep.PlayOk2, ep.PlayOk1, ep.LoginOk1, ep.LoginOk2)
	if err := gc.sendEnc(wire); err != nil {
		return nil, fmt.Errorf("стадия AuthLogin: %w", err)
	}
	// Аккаунт не печатается — учётные данные.
	gc.logSend("AUTH_LOGIN",
		Field{K: "playKey2", V: num32(ep.PlayOk2)},
		Field{K: "playKey1", V: num32(ep.PlayOk1)},
		Field{K: "loginKey1", V: num32(ep.LoginOk1)},
		Field{K: "loginKey2", V: num32(ep.LoginOk2)})

	reply, err := gc.readDec()
	if err != nil {
		return nil, fmt.Errorf("стадия CharSelectionInfo: %w", err)
	}
	switch reply[0] {
	case opCharSelectInfo:
		v, ok := protocol.NewCharSelectionInfoView(reply)
		if !ok {
			return nil, fmt.Errorf("стадия CharSelectionInfo: обрезанное тело (%d Б)", len(reply))
		}
		entries := make([]protocol.CharSelectionEntry, 0, v.Count())
		for i := 0; i < v.Count(); i++ {
			e, ok := v.Char(i)
			if !ok {
				return nil, fmt.Errorf("стадия CharSelectionInfo: запись %d обрезана", i)
			}
			entries = append(entries, e)
		}
		gc.logRecv("CHAR_SELECT_INFO", charSelectionFields(v)...)
		return entries, nil
	case opGSLoginFail:
		v, ok := protocol.NewGSLoginFailView(reply)
		if !ok {
			return nil, fmt.Errorf("стадия LoginFail: обрезанное тело (%d Б)", len(reply))
		}
		return nil, fmt.Errorf("вход отклонён: reason=0x%02X", v.Reason())
	default:
		return nil, fmt.Errorf("неожиданный ответ входа: опкод 0x%02X", reply[0])
	}
}

// SelectChar — синхронная стадия: выбор слота, ответ CharSelected.
func (gc *GameClient) SelectChar(slot int32) error {
	var wire [protocol.CharacterSelectSize]byte
	protocol.WriteCharacterSelect(wire[:], slot)
	if err := gc.sendEnc(wire[:]); err != nil {
		return fmt.Errorf("стадия CharacterSelect: %w", err)
	}
	gc.logSend("CHARACTER_SELECT", Field{K: "slot", V: num32(slot)})

	reply, err := gc.readDec()
	if err != nil {
		return fmt.Errorf("стадия CharSelected: %w", err)
	}
	v, ok := protocol.NewCharSelectedView(reply)
	if !ok {
		return fmt.Errorf("стадия CharSelected: обрезанное тело (%d Б)", len(reply))
	}
	gc.logRecv("CHAR_SELECTED", charSelectedFields(v)...)
	return nil
}

// Run — стационарная фаза: единственный владелец криптодвижка, диспетчера и
// трафик-лога. Завершается чистым закрытием сервера (EOF → nil), ошибкой
// канала, отменой контекста или Close.
func (gc *GameClient) Run(ctx context.Context) error {
	frames := make(chan []byte, 16)
	pumpErr := make(chan error, 1)
	go gc.readPump(frames, pumpErr)
	defer func() {
		_ = gc.Close()
		for range frames { // дренаж: ReadPump закрывает канал при выходе
		}
	}()
	for {
		// приоритет входящих: кадры в полёте логируются раньше команд —
		// порядок строк детерминирован (короткие паки сервера не голодают).
		select {
		case f, ok := <-frames:
			if !ok {
				return pumpOutcome(pumpErr)
			}
			gc.handleFrame(f)
			continue
		default:
		}
		select {
		case f, ok := <-frames:
			if !ok {
				return pumpOutcome(pumpErr)
			}
			gc.handleFrame(f)
		case c := <-gc.commands:
			if err := gc.sendEnc(c.wire); err != nil {
				return fmt.Errorf("отправка %s: %w", c.name, err)
			}
			if gc.opts.Traffic != nil {
				LogSend(gc.opts.Traffic, c.name, c.fields...)
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-gc.done:
			return nil
		}
	}
}

// pumpOutcome — исход завершения ReadPump: EOF и пустой канал — чистое
// закрытие сервером.
func pumpOutcome(pumpErr <-chan error) error {
	err := <-pumpErr
	if errors.Is(err, io.EOF) || err == nil {
		return nil
	}
	return fmt.Errorf("чтение game-канала: %w", err)
}

// Logout — команда стационарной фазы; после выхода Run или Close — ошибка.
func (gc *GameClient) Logout() error {
	var wire [protocol.LogoutSize]byte
	protocol.WriteLogout(wire[:])
	select {
	case gc.commands <- command{wire: wire[:], name: "LOGOUT"}:
		return nil
	case <-gc.done:
		return ErrClosed
	}
}

// readPump — горутина чтения: сырые кадры (NextFrame, собственный буфер на
// кадр — владение уходит получателю), ошибки — в pumpErr, выход — закрытие
// соединения или done.
func (gc *GameClient) readPump(frames chan []byte, pumpErr chan<- error) {
	defer close(frames)
	var buf []byte
	tmp := make([]byte, 4096)
	for {
		if err := gc.conn.SetReadDeadline(time.Now().Add(gc.opts.timeout())); err != nil {
			pumpErr <- fmt.Errorf("дедлайн чтения: %w", err)
			return
		}
		n, err := gc.conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		for {
			body, ferr := protocol.NextFrame(buf)
			if errors.Is(ferr, protocol.ErrFrameIncomplete) {
				break
			}
			if ferr != nil {
				pumpErr <- fmt.Errorf("разрез потока: %w", ferr)
				return
			}
			owned := make([]byte, len(body))
			copy(owned, body)
			buf = buf[len(body)+2:]
			select {
			case frames <- owned:
			case <-gc.done:
				return
			}
		}
		if err != nil {
			pumpErr <- err
			return
		}
	}
}

// handleFrame расшифровывает и диспетчеризирует входящий кадр стационарной
// фазы: типизированный — поля, прочий — имя из каталога + hex-фолбэк.
func (gc *GameClient) handleFrame(f []byte) {
	if gc.crypt != nil {
		if err := gc.crypt.Decrypt(f); err != nil {
			slog.Warn("game: расшифровка кадра", "err", err)
			return
		}
	}
	var fields []Field
	typed := false
	if len(f) > 0 {
		switch f[0] {
		case opCharSelectInfo:
			if v, ok := protocol.NewCharSelectionInfoView(f); ok {
				fields, typed = charSelectionFields(v), true
			}
		case opCharSelected:
			if v, ok := protocol.NewCharSelectedView(f); ok {
				if _, ok2 := v.Name(); ok2 {
					fields, typed = charSelectedFields(v), true
				}
			}
		}
	}
	if gc.opts.Traffic == nil {
		return
	}
	if typed {
		LogRecv(gc.opts.Traffic, nameOf(f), fields...)
		return
	}
	LogRecvHex(gc.opts.Traffic, unknownName(f), f)
}

// nameOf — имя типизированного пакета (опкод известен диспетчеру).
func nameOf(f []byte) string {
	if len(f) == 0 {
		return "??(0x??)"
	}
	switch f[0] {
	case opCharSelectInfo:
		return "CHAR_SELECT_INFO"
	case opCharSelected:
		return "CHAR_SELECTED"
	}
	return unknownName(f)
}

// sendEnc шифрует и пишет кадр (включённое шифрование).
func (gc *GameClient) sendEnc(wire []byte) error {
	if err := gc.conn.SetWriteDeadline(time.Now().Add(gc.opts.timeout())); err != nil {
		return fmt.Errorf("дедлайн записи: %w", err)
	}
	if err := gc.crypt.Encrypt(wire); err != nil {
		return err
	}
	buf := make([]byte, 2+len(wire))
	buf[0] = byte(len(buf))
	buf[1] = byte(len(buf) >> 8)
	copy(buf[2:], wire)
	if _, err := gc.conn.Write(buf); err != nil {
		return wrapNet("запись кадра", err)
	}
	return nil
}

// readDec читает кадр хендшейка и расшифровывает его.
func (gc *GameClient) readDec() ([]byte, error) {
	frame, err := readFrame(gc.conn, gc.opts.timeout())
	if err != nil {
		return nil, err
	}
	if err := gc.crypt.Decrypt(frame); err != nil {
		return nil, err
	}
	return frame, nil
}

func (gc *GameClient) logRecv(name string, fields ...Field) {
	if gc.opts.Traffic != nil {
		LogRecv(gc.opts.Traffic, name, fields...)
	}
}

func (gc *GameClient) logSend(name string, fields ...Field) {
	if gc.opts.Traffic != nil {
		LogSend(gc.opts.Traffic, name, fields...)
	}
}

// charSelectionFields — поля списка персонажей (читаемое подмножество полей
// записи; полный разбор — представление).
func charSelectionFields(v protocol.CharSelectionInfoView) []Field {
	fields := []Field{{K: "count", V: num(int64(v.Count()))}}
	for i := 0; i < v.Count(); i++ {
		e, ok := v.Char(i)
		if !ok {
			break
		}
		fields = append(fields, Field{K: fmt.Sprintf("[%d]", i), V: fmt.Sprintf(
			"{name=%s id=%d level=%d class=%d base=%d sex=%d race=%d hp=%s/%s mp=%s/%s sp=%d exp=%d karma=%d}",
			Quote(e.Name), e.CharID, e.Level, e.ClassID, e.BaseClassID, e.Sex, e.Race,
			flt(e.CurHP), flt(e.MaxHP), flt(e.CurMP), flt(e.MaxMP), e.SP, e.Exp, e.Karma)})
	}
	return fields
}

// charSelectedFields — поля подтверждения входа.
func charSelectedFields(v protocol.CharSelectedView) []Field {
	fields := make([]Field, 0, 14)
	add := func(k string, val any) {
		fields = append(fields, Field{K: k, V: fmt.Sprintf("%v", val)})
	}
	if name, ok := v.Name(); ok {
		add("name", Quote(name))
	}
	if id, ok := v.CharID(); ok {
		add("id", id)
	}
	if title, ok := v.Title(); ok {
		add("title", Quote(title))
	}
	if lv, ok := v.Level(); ok {
		add("level", lv)
	}
	if c, ok := v.ClassID(); ok {
		add("class", c)
	}
	if x, ok := v.X(); ok {
		add("x", x)
	}
	if y, ok := v.Y(); ok {
		add("y", y)
	}
	if z, ok := v.Z(); ok {
		add("z", z)
	}
	if hp, ok := v.CurHP(); ok {
		add("hp", flt(hp))
	}
	if mp, ok := v.CurMP(); ok {
		add("mp", flt(mp))
	}
	if sp, ok := v.SP(); ok {
		add("sp", sp)
	}
	if exp, ok := v.Exp(); ok {
		add("exp", exp)
	}
	if karma, ok := v.Karma(); ok {
		add("karma", karma)
	}
	if pk, ok := v.PkKills(); ok {
		add("pk", pk)
	}
	if gt, ok := v.GameTime(); ok {
		add("gameTime", gt)
	}
	return fields
}
