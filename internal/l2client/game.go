// GameClient — клиент GameServer: синхронный хендшейк (ProtocolVersion →
// KeyPacket → AuthLogin → CharSelectionInfo → CharacterSelect → CharSelected)
// и стационарная фаза с горутиной чтения и командами каналом. Общей мутации
// нет: ReadPump передаёт сырые кадры (собственный буфер на кадр), расшифровка,
// диспетчеризация и трафик-лог — только в цикле Run. Порядок хендшейка —
// канон L2J Mobius master CT_0_Interlude (43ac8878); порт семантики —
// udisondev/interlude@34fe4c8 pkg/l2client/game.go.

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
		return nil, fmt.Errorf("%w к %s: %w", errDial, addr, err)
	}
	setNoDelay(conn)
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
	if err := writeRecord(gc.conn, gc.opts.timeout(), wire[:]); err != nil {
		return fmt.Errorf("стадия ProtocolVersion: %w", err)
	}
	gc.logSend(protocol.NameProtocolVersion, Field{K: "version", V: num32(protocol.ProtocolVersionInterlude)})

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
	// Ключ сессии — учётные данные: в полях трафик-лога только его размер.
	gc.logRecv(protocol.NameKeyPacket, v.Fields()...)
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
	// Аккаунт и ключи сессии — учётные данные: не печатаются.
	gc.logSend(protocol.NameAuthLogin, Field{K: "keys", V: num(4)})

	reply, err := gc.readDec()
	if err != nil {
		return nil, fmt.Errorf("стадия CharSelectionInfo: %w", err)
	}
	if len(reply) == 0 {
		return nil, fmt.Errorf("стадия CharSelectionInfo: пустой кадр")
	}
	switch reply[0] {
	case protocol.OpCharSelectInfo:
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
		gc.logRecv(protocol.NameCharSelectInfo, v.Fields()...)
		return entries, nil
	case protocol.OpGSLoginFail:
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
	gc.logSend(protocol.NameCharacterSelect, Field{K: "slot", V: num32(slot)})

	reply, err := gc.readDec()
	if err != nil {
		return fmt.Errorf("стадия CharSelected: %w", err)
	}
	if len(reply) == 0 {
		return fmt.Errorf("стадия CharSelected: пустой кадр")
	}
	switch reply[0] {
	case protocol.OpCharSelected:
		v, ok := protocol.NewCharSelectedView(reply)
		if !ok {
			return fmt.Errorf("стадия CharSelected: обрезанное тело (%d Б)", len(reply))
		}
		gc.logRecv(protocol.NameCharSelected, v.Fields()...)
		return nil
	case protocol.OpGSLoginFail:
		v, ok := protocol.NewGSLoginFailView(reply)
		if !ok {
			return fmt.Errorf("стадия LoginFail: обрезанное тело (%d Б)", len(reply))
		}
		gc.logRecv(protocol.NameLoginFail, v.Fields()...)
		return fmt.Errorf("выбор персонажа отклонён: reason=0x%02X", v.Reason())
	default:
		return fmt.Errorf("неожиданный ответ выбора персонажа: опкод 0x%02X", reply[0])
	}
}

// framesCap — буфер канала кадров: сглаживает паки сервера, не задерживая
// чтение сокета.
const framesCap = 16

// CreateChar — синхронная стадия создания: NewChar → CharTemplates →
// CharacterCreate → CharCreateOk и свежий CharSelectionInfo следом (канон
// initNewChar). Возвращает записи обновлённого списка.
func (gc *GameClient) CreateChar(d protocol.CharacterCreateData) ([]protocol.CharSelectionEntry, error) {
	var nc [protocol.NewCharacterSize]byte
	protocol.WriteNewCharacter(nc[:])
	if err := gc.sendEnc(nc[:]); err != nil {
		return nil, fmt.Errorf("стадия NewChar: %w", err)
	}
	gc.logSend(protocol.NameNewCharacter)
	reply, err := gc.readDec()
	if err != nil {
		return nil, fmt.Errorf("стадия CharTemplates: %w", err)
	}
	if reply[0] != protocol.OpCharTemplates {
		return nil, fmt.Errorf("стадия CharTemplates: неожиданный опкод 0x%02X", reply[0])
	}
	tv, ok := protocol.NewCharTemplatesView(reply)
	if !ok {
		return nil, fmt.Errorf("стадия CharTemplates: обрезанное тело (%d Б)", len(reply))
	}
	gc.logRecv(protocol.NameCharTemplates, tv.Fields()...)

	wire := make([]byte, protocol.CharacterCreateSize(d))
	protocol.WriteCharacterCreate(wire, d)
	if err := gc.sendEnc(wire); err != nil {
		return nil, fmt.Errorf("стадия CharacterCreate: %w", err)
	}
	gc.logSend(protocol.NameCharacterCreate, Field{K: "name", V: d.Name})

	reply, err = gc.readDec()
	if err != nil {
		return nil, fmt.Errorf("стадия CharCreateOk: %w", err)
	}
	if len(reply) == 0 {
		return nil, fmt.Errorf("стадия CharCreateOk: пустой кадр")
	}
	switch reply[0] {
	case protocol.OpCharCreateOk:
		gc.logRecv(protocol.NameCharCreateOk)
		return gc.readCharList("свежий список после создания")
	case protocol.OpCharCreateFail:
		v, ok := protocol.NewCharCreateFailView(reply)
		if !ok {
			return nil, fmt.Errorf("стадия CharCreateFail: обрезанное тело (%d Б)", len(reply))
		}
		gc.logRecv(protocol.NameCharCreateFail, v.Fields()...)
		return nil, fmt.Errorf("создание отклонено: reason=0x%02X", v.Reason())
	default:
		return nil, fmt.Errorf("неожиданный ответ создания: опкод 0x%02X", reply[0])
	}
}

// readCharList читает CharSelectionInfo (общий хвост Auth и CreateChar).
func (gc *GameClient) readCharList(stage string) ([]protocol.CharSelectionEntry, error) {
	reply, err := gc.readDec()
	if err != nil {
		return nil, fmt.Errorf("стадия %s: %w", stage, err)
	}
	if len(reply) == 0 {
		return nil, fmt.Errorf("стадия %s: пустой кадр", stage)
	}
	if reply[0] != protocol.OpCharSelectInfo {
		return nil, fmt.Errorf("стадия %s: неожиданный опкод 0x%02X", stage, reply[0])
	}
	v, ok := protocol.NewCharSelectionInfoView(reply)
	if !ok {
		return nil, fmt.Errorf("стадия %s: обрезанное тело (%d Б)", stage, len(reply))
	}
	entries := make([]protocol.CharSelectionEntry, 0, v.Count())
	for i := 0; i < v.Count(); i++ {
		e, ok := v.Char(i)
		if !ok {
			return nil, fmt.Errorf("стадия %s: запись %d обрезана", stage, i)
		}
		entries = append(entries, e)
	}
	gc.logRecv(protocol.NameCharSelectInfo, v.Fields()...)
	return entries, nil
}

// EnterWorld — команда стационарной фазы: маркер входа (канонный порядок —
// сразу после CharSelected; hwinfo-поля нулевые, серверу безразличны).
func (gc *GameClient) EnterWorld() error {
	var wire [protocol.EnterWorldSize]byte
	protocol.WriteEnterWorld(wire[:])
	return gc.command(wire[:], protocol.NameEnterWorld)
}

// MoveToLocation — команда стационарной фазы: намерение движения (цель,
// точка отправления, режим 0 — клавиатура / 1 — мышь). Пишатель и
// представление — movement.go протокола.
func (gc *GameClient) MoveToLocation(targetX, targetY, targetZ, originX, originY, originZ, mode int32) error {
	var wire [protocol.MoveToLocationSize]byte
	protocol.WriteMoveToLocation(wire[:], targetX, targetY, targetZ, originX, originY, originZ, mode)
	return gc.command(wire[:], protocol.NameMoveToLocation,
		Field{K: "target", V: num32(targetX)})
}

// SendRaw — отправка сырого кадра C→GS (доставляет Run): живые прогоны и
// тесты нестандартных клиентских кадров.
func (gc *GameClient) SendRaw(wire []byte, name string) error {
	return gc.command(wire, name)
}

// Say2 — реплика в канал чата (стационарная фаза; ALL = protocol.ChatGeneral).
func (gc *GameClient) Say2(text string, chatType protocol.ChatType) error {
	wire := make([]byte, protocol.Say2Size(text, chatType, ""))
	protocol.WriteSay2(wire, text, chatType, "")
	return gc.command(wire, protocol.NameSay2, Field{K: "text", V: protocol.Quote(text)})
}

// ValidatePosition отправляет периодическую синхронизацию позиции (~1/с канона).
func (gc *GameClient) ValidatePosition(x, y, z, heading int32) error {
	var wire [protocol.ValidatePositionSize]byte
	protocol.WriteValidatePosition(wire[:], x, y, z, heading, 0)
	return gc.command(wire[:], protocol.NameValidatePosition,
		Field{K: "x", V: num32(x)}, Field{K: "y", V: num32(y)})
}

// command — отправка команды стационарной фазы (отправляет только Run).
func (gc *GameClient) command(wire []byte, name string, fields ...Field) error {
	select {
	case gc.commands <- command{wire: wire, name: name, fields: fields}:
		return nil
	case <-gc.done:
		return ErrClosed
	}
}

// priorityBurst — сколько готовых кадров обрабатывается подряд с приоритетом
// над командами: ограничивает голодание команд/ctx при непрерывном потоке.
const priorityBurst = 8

// Run — стационарная фаза: единственный владелец криптодвижка, диспетчера и
// трафик-лога. Завершается чистым закрытием сервера (EOF → nil), ошибкой
// канала, отменой контекста или Close.
func (gc *GameClient) Run(ctx context.Context) error {
	frames := make(chan []byte, framesCap)
	pumpErr := make(chan error, 1)
	go gc.readPump(frames, pumpErr)
	defer func() {
		_ = gc.Close()
		// дренаж: разблокирует ReadPump (отправитель не зависает в send);
		// кадры после выхода Run отбрасываются — лог уже не пишется
		for range frames {
		}
	}()
	for {
		// приоритет входящих (с ограничением priorityBurst): готовые кадры
		// логируются раньше команд — порядок строк детерминирован, а поток
		// кадров не голодает отмену и команды бесконечно.
		for range priorityBurst {
			select {
			case f, ok := <-frames:
				if !ok {
					return pumpOutcome(pumpErr)
				}
				gc.handleFrame(f)
				continue
			default:
			}
			break
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

// pumpOutcome — исход завершения ReadPump: EOF, nil (done-плечо) и закрытие
// соединения по инициативе клиента — чистое завершение.
func pumpOutcome(pumpErr <-chan error) error {
	select {
	case err := <-pumpErr:
		if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return fmt.Errorf("чтение game-канала: %w", err)
	default:
		return nil
	}
}

// Logout — команда стационарной фазы; после выхода Run или Close — ошибка.
func (gc *GameClient) Logout() error {
	var wire [protocol.LogoutSize]byte
	protocol.WriteLogout(wire[:])
	select {
	case gc.commands <- command{wire: wire[:], name: protocol.NameLogout}:
		return nil
	case <-gc.done:
		return ErrClosed
	}
}

// RequestRestart — команда стационарной фазы: сервер фазы 3 отвечает отказом
// канона (RestartResponse(false)+ActionFailed), коннект жив.
func (gc *GameClient) RequestRestart() error {
	var wire [protocol.RequestRestartSize]byte
	protocol.WriteRequestRestart(wire[:])
	select {
	case gc.commands <- command{wire: wire[:], name: protocol.NameRequestRestart}:
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
				pumpErr <- nil // done-плечо: Run не должен ждать ошибку вечно
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
// фазы: пустое тело — фолбэк без расшифровки, типизированный — имя и поля из
// protocol (константа + Fields представления), прочий — имя из каталога +
// hex-фолбэк.
func (gc *GameClient) handleFrame(f []byte) {
	if len(f) == 0 {
		gc.logRecvHex(protocol.GameServerFrameName(f), f)
		return
	}
	if gc.crypt != nil {
		if err := gc.crypt.Decrypt(f); err != nil {
			slog.Debug("game: расшифровка кадра", "err", err)
			gc.logRecvHex(protocol.GameServerFrameName(f), f)
			return
		}
	}
	var name string
	var fields []Field
	typed := false
	switch f[0] {
	case protocol.OpCharSelectInfo:
		if v, ok := protocol.NewCharSelectionInfoView(f); ok {
			name, fields, typed = protocol.NameCharSelectInfo, v.Fields(), true
		}
	case protocol.OpCharSelected:
		if v, ok := protocol.NewCharSelectedView(f); ok {
			if _, ok2 := v.Name(); ok2 {
				name, fields, typed = protocol.NameCharSelected, v.Fields(), true
			}
		}
	case protocol.OpUserInfo:
		// UserInfo — сердце слитка входа: имя/позиция — e2e-ассерты (P3.7).
		if v, ok := protocol.NewUserInfoView(f); ok {
			name, fields, typed = protocol.NameUserInfo, v.Fields(), true
		}
	case protocol.OpCharInfo:
		// CharInfo — ввод чужого игрока в известность (join AoI, P3.8).
		if v, ok := protocol.NewCharInfoView(f); ok {
			name, fields, typed = protocol.NameCharInfo, v.Fields(), true
		}
	case protocol.OpNpcInfo:
		// NpcInfo — ввод NPC в известность (join AoI, P3.10).
		if v, ok := protocol.NewNpcInfoView(f); ok {
			name, fields, typed = protocol.NameNpcInfo, v.Fields(), true
		}
	case protocol.OpDeleteObject:
		// DeleteObject — уход из известности (join AoI, P3.8).
		if v, ok := protocol.NewDeleteObjectView(f); ok {
			name, fields, typed = protocol.NameDeleteObject, v.Fields(), true
		}
	case protocol.OpCharMoveToLocation:
		// CharMoveToLocation — авторитетный стрим движения (P3.9).
		if v, ok := protocol.NewCharMoveToLocationView(f); ok {
			name, fields, typed = protocol.NameCharMoveToLocation, v.Fields(), true
		}
	case protocol.OpStopMove:
		// StopMove — авторитетная остановка (P3.9).
		if v, ok := protocol.NewStopMoveView(f); ok {
			name, fields, typed = protocol.NameStopMove, v.Fields(), true
		}
	case protocol.OpValidateLocation:
		// ValidateLocation — snap-back коррекция себе (P3.9).
		if v, ok := protocol.NewValidateLocationView(f); ok {
			name, fields, typed = protocol.NameValidateLocation, v.Fields(), true
		}
	case protocol.OpCreatureSay:
		// CreatureSay — реплика существа в радиусе речи (чат ALL, P3.11).
		if v, ok := protocol.NewCreatureSayView(f); ok {
			name, fields, typed = protocol.NameCreatureSay, v.Fields(), true
		}
	}
	if typed {
		gc.logRecv(name, fields...)
		return
	}
	gc.logRecvHex(protocol.GameServerFrameName(f), f)
}

// sendEnc шифрует и пишет кадр (включённое шифрование).
func (gc *GameClient) sendEnc(wire []byte) error {
	if err := gc.conn.SetWriteDeadline(time.Now().Add(gc.opts.timeout())); err != nil {
		return fmt.Errorf("дедлайн записи: %w", err)
	}
	if err := gc.crypt.Encrypt(wire); err != nil {
		return err
	}
	return writeRecord(gc.conn, gc.opts.timeout(), wire)
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

func (gc *GameClient) logRecvHex(name string, body []byte) {
	if gc.opts.Traffic != nil {
		LogRecvHex(gc.opts.Traffic, name, body)
	}
}
