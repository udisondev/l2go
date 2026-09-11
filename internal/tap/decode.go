// Декодер журнала: нарезает потоки соединений на кадры, снимает крипту
// (login: static-фаза Init → динамический BF; game: канон границы —
// ProtocolVersion/KeyPacket открытым текстом, дальше GameCrypt) и пишет
// читаемый лог в формате трафик-лога клиента (побайтовый паритет строк —
// канон; расхождение живого сервера — slog, кадр деградирует в hex-строку.
// AuthLogin обеих ног в фикстуры не выгружается (учётные данные/ключи сессии).

package tap

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"

	"github.com/udisondev/l2go/internal/crypto"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/protocol/fixture"
)

// DecodeOptions — адресаты декодирования; nil — продукт не пишется.
type DecodeOptions struct {
	Log      io.Writer // читаемый лог трафика
	Fixtures io.Writer // JSON-массив фикстур (формат protocol/fixture)
}

// legKind — классификация соединения.
type legKind int

const (
	kindUnknown legKind = iota
	kindLogin
	kindGame
)

// legDec — поток одной стороны одного соединения.
type legDec struct {
	buf    []byte
	frames int
}

// connDec — состояние соединения декодера.
type connDec struct {
	kind        legKind
	login       *crypto.LoginCrypt   // login: один движок на соединение (фаза общая)
	initSeen    bool                 // login: Init прошёл S→C
	game        [2]*crypto.GameCrypt // game: движок на направление (каскады сторон)
	legs        [2]*legDec           // [DirCtoS], [DirStoC]
	closed      bool
	warned      bool      // предупреждение о неклассификации — один раз
	pendingOrig [2][]byte // оригинал rewrite, ждёт пару-data
	fixts       []fixture.Fixture
}

// Decode прогоняет журнал через декодер. Обрыв хвоста журнала (запись
// посередине) — не фатален: предупреждение и разбор завершённых соединений.
func Decode(r io.Reader, opts DecodeOptions) error {
	conns := make(map[uint64]*connDec)
	order := make([]uint64, 0, 8)
	jr := newJournalReader(r)
	for {
		rec, err := jr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if errors.Is(err, errBadHeader) {
				return err
			}
			slog.Warn("tap: журнал оборван, разобраны целые записи", "err", err)
			break
		}
		switch rec.Type {
		case recConnOpen:
			conns[rec.ConnID] = &connDec{legs: [2]*legDec{{}, {}}}
			order = append(order, rec.ConnID)
		case recData:
			c := conns[rec.ConnID]
			if c == nil {
				return fmt.Errorf("tap: data для соединения %d без connOpen", rec.ConnID)
			}
			if rec.Dir != DirCtoS && rec.Dir != DirStoC {
				return fmt.Errorf("tap: направление %d вне {1,2}", rec.Dir)
			}
			leg := c.legs[dirIndex(rec.Dir)]
			leg.buf = append(leg.buf, rec.Bytes...)
			if err := decodeReady(c, leg, rec.Dir, opts.Log); err != nil {
				return fmt.Errorf("tap: соединение %d: %w", rec.ConnID, err)
			}
		case recOriginal:
			// оригинал перезаписи: в поток не подмешивается, но фикстуры
			// извлекаются из оригинала (пара к ближайшей data того же направления)
			c := conns[rec.ConnID]
			if c == nil {
				return fmt.Errorf("tap: original для соединения %d без connOpen", rec.ConnID)
			}
			c.pendingOrig[dirIndex(rec.Dir)] = rec.Bytes
		case recConnClose:
			if c := conns[rec.ConnID]; c != nil {
				c.closed = true
				if rec.Err != "" {
					slog.Debug("tap: соединение закрыто с ошибкой", "connID", rec.ConnID, "err", rec.Err)
				}
			}
		default:
			return fmt.Errorf("tap: неизвестный тип записи %d", rec.Type)
		}
	}
	if opts.Fixtures != nil {
		var rows []map[string]any
		for _, id := range order {
			rows = append(rows, fixturesJSON(conns[id])...)
		}
		if rows == nil {
			rows = []map[string]any{}
		}
		enc := json.NewEncoder(opts.Fixtures)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rows); err != nil {
			return fmt.Errorf("tap: выгрузка фикстур: %w", err)
		}
	}
	return nil
}

func dirIndex(dir byte) int {
	if dir == DirStoC {
		return 1
	}
	return 0
}

// decodeReady обрабатывает все полные кадры, накопленные в ноге.
func decodeReady(c *connDec, leg *legDec, dir byte, log io.Writer) error {
	for {
		body, err := protocol.NextFrame(leg.buf)
		if errors.Is(err, protocol.ErrFrameIncomplete) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("разрез потока: %w", err)
		}
		frame := make([]byte, len(body))
		copy(frame, body)
		leg.buf = leg.buf[len(body)+2:]
		// оригинал rewrite изымается только с полным кадром
		idx := dirIndex(dir)
		orig := c.pendingOrig[idx]
		c.pendingOrig[idx] = nil
		if err := decodeFrame(c, leg, dir, frame, orig, log); err != nil {
			return err
		}
		leg.frames++
	}
}

// decodeFrame — классификация (лениво), расшифровка, лог и фикстура одного кадра.
// Неклассифицированное соединение не теряет кадры: hex-строка в лог.
func decodeFrame(c *connDec, leg *legDec, dir byte, frame, orig []byte, log io.Writer) error {
	if c.kind == kindUnknown {
		if err := classify(c, dir, frame); err != nil && !c.warned {
			slog.Warn("tap: соединение не классифицировано, кадры — hex", "err", err)
			c.warned = true
		}
		if c.kind == kindUnknown {
			logHexLineFor(c, dir, frame, log)
			return nil
		}
	}
	switch c.kind {
	case kindLogin:
		return decodeLoginFrame(c, leg, dir, frame, orig, log)
	default:
		return decodeGameFrame(c, leg, dir, frame, log)
	}
}

// classify — определение login/game по первому кадру ноги.
func classify(c *connDec, dir byte, frame []byte) error {
	if dir == DirStoC {
		work := make([]byte, len(frame))
		copy(work, frame)
		lc := crypto.NewLoginCrypt()
		if err := lc.DecryptInit(work); err == nil {
			if _, ok := protocol.NewInitView(work); ok {
				c.kind = kindLogin
				return nil
			}
		}
		return fmt.Errorf("S→C кадр %d Б не похож на Init", len(frame))
	}
	if len(frame) == protocol.ProtocolVersionSize && frame[0] == protocol.OpProtocolVersion {
		c.kind = kindGame
		return nil
	}
	return fmt.Errorf("C→S кадр %d Б не похож на ProtocolVersion", len(frame))
}

// decodeLoginFrame — кадр login-ноги.
func decodeLoginFrame(c *connDec, leg *legDec, dir byte, frame, orig []byte, log io.Writer) error {
	if c.login == nil {
		c.login = crypto.NewLoginCrypt()
	}
	if dir == DirStoC && leg.frames == 0 && !c.initSeen {
		if err := c.login.DecryptInit(frame); err != nil {
			return fmt.Errorf("init: %w", err)
		}
		v, ok := protocol.NewInitView(frame)
		if !ok {
			return fmt.Errorf("init: обрезанное тело (%d байт)", len(frame))
		}
		if err := c.login.SetKey(v.BlowfishKey()); err != nil {
			return fmt.Errorf("init: %w", err)
		}
		c.initSeen = true
		logLoginLine(log, dir, frame)
		addFixture(c, dir, frame, fixture.LoginClient, fixture.LoginServer)
		return nil
	}
	if !c.initSeen {
		// C→S до Init (не встречается на живом флоу) — hex-деградация
		logHexLine(log, dir, frame)
		return nil
	}
	if err := c.login.Decrypt(frame); err != nil {
		slog.Warn("tap: login-кадр не расшифрован", "dir", dir, "err", err)
		logHexLine(log, dir, frame)
		return nil
	}
	// наблюдение канона паддинга: размер провода против ожидания CT0 для
	// пакетов с известной длиной тела (вердикт по живому корпусу — на прогоне)
	slog.Debug("tap: login-кадр", "op", fmt.Sprintf("0x%02X", frame[0]), "wire", len(frame))
	logLoginLine(log, dir, frame)
	// при сработавшем rewrite фикстура извлекается из оригинала
	fixSrc := frame
	if len(orig) >= 3 {
		o := make([]byte, len(orig)-2)
		copy(o, orig[2:])
		if err := c.login.Decrypt(o); err != nil {
			slog.Debug("tap: оригинал rewrite не расшифрован", "err", err)
		} else {
			fixSrc = o
		}
	}
	addFixture(c, dir, fixSrc, fixture.LoginClient, fixture.LoginServer)
	return nil
}

// decodeGameFrame — кадр game-ноги; граница шифрования: первые кадры обеих
// сторон открытым текстом (ProtocolVersion / KeyPacket), дальше GameCrypt —
// по движку на направление (каскады сторон сеются одинаково, но каждый
// движок проходит только свои кадры: расшифровка C→S — зеркало клиентского
// Encrypt-каскада, S→C — серверного). Расхождение живого сервера с каноном
// границы — предупреждение и hex-деградация кадра, не abort разбора.
func decodeGameFrame(c *connDec, leg *legDec, dir byte, frame []byte, log io.Writer) error {
	both := c.legs[0].frames + c.legs[1].frames
	if c.game[dirIndex(dir)] == nil {
		if dir == DirStoC {
			v, ok := protocol.NewKeyPacketView(frame)
			if !ok {
				slog.Warn("tap: первый S→C кадр game-ноги не KeyPacket — hex-деградация",
					"bytes", len(frame))
				logHexLineFor(c, dir, frame, log)
				return nil
			}
			logKeyPacket(log, v)
			var wire [8]byte
			copy(wire[:], v.Key())
			for i := range c.game {
				c.game[i] = crypto.NewGameCrypt(wire)
				c.game[i].Enable()
			}
			addFixture(c, dir, frame, fixture.GameClient, fixture.GameServer)
			return nil
		}
		if dir == DirCtoS && both == 0 {
			v, ok := protocol.NewProtocolVersionView(frame)
			if !ok {
				slog.Warn("tap: первый C→S кадр game-ноги не ProtocolVersion — hex-деградация",
					"bytes", len(frame))
				logHexLineFor(c, dir, frame, log)
				return nil
			}
			logProtocolVersion(log, v)
			addFixture(c, dir, frame, fixture.GameClient, fixture.GameServer)
			return nil
		}
		// движок для направления ещё не создан (нет KeyPacket) — деградация
		slog.Warn("tap: game-кадр до границы шифрования — hex-деградация", "dir", dir)
		logHexLineFor(c, dir, frame, log)
		return nil
	}
	if err := c.game[dirIndex(dir)].Decrypt(frame); err != nil {
		slog.Warn("tap: game-кадр не расшифрован", "dir", dir, "err", err)
		logHexLine(log, dir, frame)
		return nil
	}
	logGameLine(log, dir, frame)
	addFixture(c, dir, frame, fixture.GameClient, fixture.GameServer)
	return nil
}

// addFixture — фикстура кадра: тело после опкода (и sub); AuthLogin исключён.
func addFixture(c *connDec, dir byte, frame []byte, cli, srv fixture.Direction) {
	if len(frame) == 0 {
		return
	}
	d := cli
	if dir == DirStoC {
		d = srv
	}
	op := frame[0]
	sub := uint16(0)
	payload := frame[1:]
	if d == fixture.GameServer && op == protocol.ExGSOpcode {
		if len(frame) < 3 {
			return
		}
		sub = binary.LittleEndian.Uint16(frame[1:3])
		payload = frame[3:]
	}
	switch {
	case d == fixture.LoginClient && op == protocol.OpRequestAuthLogin:
		return // RSA-блоб учётных данных
	case d == fixture.GameClient && op == protocol.OpAuthLogin:
		return // аккаунт и ключи сессии
	}
	name, ok := packetName(d, uint16(op), sub)
	if !ok {
		name = fmt.Sprintf("??(0x%02X)", op)
	}
	c.fixts = append(c.fixts, fixture.Fixture{
		Dir: d, Op: uint16(op), Sub: sub, Name: name, Payload: payload, Origin: fixture.OriginCaptured,
	})
}

func packetName(d fixture.Direction, op, sub uint16) (string, bool) {
	switch d {
	case fixture.LoginClient:
		return protocol.LoginClientPacketName(byte(op))
	case fixture.LoginServer:
		return protocol.LoginServerPacketName(byte(op))
	case fixture.GameClient:
		return protocol.GameClientPacketName(byte(op))
	default:
		if sub != 0 {
			return protocol.GameServerExName(sub)
		}
		return protocol.GameServerPacketName(byte(op))
	}
}

// fixturesJSON — выгрузка фикстур соединения в строковом формате протокола.
func fixturesJSON(c *connDec) []map[string]any {
	rows := make([]map[string]any, 0, len(c.fixts))
	for _, f := range c.fixts {
		rows = append(rows, map[string]any{
			"dir":     string(f.Dir),
			"op":      f.Op,
			"sub":     f.Sub,
			"name":    f.Name,
			"payload": hex.EncodeToString(f.Payload),
			"origin":  f.Origin,
		})
	}
	return rows
}

// --- формат строк лога: побайтово повторяет трафик-лог клиента (паритет прикрыт интеграционным тестом) ---

type logField struct{ K, V string }

func logLine(w io.Writer, dir byte, name string, fields []logField) {
	if w == nil {
		return
	}
	var b strings.Builder
	if dir == DirStoC {
		b.WriteString("← ")
	} else {
		b.WriteString("→ ")
	}
	b.WriteString(name)
	for _, f := range fields {
		b.WriteByte(' ')
		b.WriteString(f.K)
		b.WriteByte('=')
		b.WriteString(f.V)
	}
	b.WriteByte('\n')
	_, _ = w.Write([]byte(b.String()))
}

func logHexLine(w io.Writer, dir byte, frame []byte) {
	logLine(w, dir, unknownLineName(dir, frame), []logField{{K: "hex", V: hexBytes(frame)}})
}

// logHexLineFor — hex-деградация с именем по каталогу класса соединения:
// login-кадры не получают игровые имена по случайному совпадению опкода.
func logHexLineFor(c *connDec, dir byte, frame []byte, w io.Writer) {
	name := "??(0x??)"
	if len(frame) > 0 {
		if c.kind == kindLogin {
			var ok bool
			if dir == DirStoC {
				name, ok = protocol.LoginServerPacketName(frame[0])
			} else {
				name, ok = protocol.LoginClientPacketName(frame[0])
			}
			if !ok {
				name = fmt.Sprintf("??(0x%02X)", frame[0])
			}
		} else {
			name = unknownLineName(dir, frame)
		}
	}
	logLine(w, dir, name, []logField{{K: "hex", V: hexBytes(frame)}})
}

func unknownLineName(dir byte, body []byte) string {
	if len(body) == 0 {
		return "??(0x??)"
	}
	var name string
	var ok bool
	if dir == DirStoC {
		if body[0] == protocol.ExGSOpcode {
			if len(body) < 3 {
				return "??(0xFE)"
			}
			sub := binary.LittleEndian.Uint16(body[1:3])
			if name, ok = protocol.GameServerExName(sub); ok {
				return name
			}
			return fmt.Sprintf("??(0xFE:0x%04X)", sub)
		}
		name, ok = protocol.GameServerPacketName(body[0])
	} else {
		name, ok = protocol.GameClientPacketName(body[0])
	}
	if !ok {
		return fmt.Sprintf("??(0x%02X)", body[0])
	}
	return name
}

const hexDumpMax = 64

func hexBytes(b []byte) string {
	h := hex.EncodeToString(b)
	if len(b) <= hexDumpMax {
		return h
	}
	return h[:2*hexDumpMax] + fmt.Sprintf(" [+%dБ]", len(b)-hexDumpMax)
}

func logNum(v int64) string   { return strconv.FormatInt(v, 10) }
func logNum32(v int32) string { return logNum(int64(v)) }

func logLoginLine(w io.Writer, dir byte, body []byte) {
	if len(body) == 0 {
		logHexLine(w, dir, body)
		return
	}
	var name string
	var fields []logField
	typed := false
	if dir == DirStoC {
		switch body[0] {
		case protocol.OpInit:
			if v, ok := protocol.NewInitView(body); ok {
				name, typed = protocol.NameInit, true
				fields = []logField{
					{K: "session", V: logNum32(v.SessionID())},
					{K: "revision", V: fmt.Sprintf("0x%04X", v.Revision())},
				}
			}
		case protocol.OpGGAuth:
			if v, ok := protocol.NewGGAuthView(body); ok {
				name, typed = protocol.NameGGAuth, true
				fields = []logField{{K: "response", V: logNum32(v.Response())}}
			}
		case protocol.OpLoginOk:
			if v, ok := protocol.NewLoginOkView(body); ok {
				name, typed = protocol.NameLoginOk, true
				fields = []logField{
					{K: "loginOk1", V: logNum32(v.LoginOkID1())},
					{K: "loginOk2", V: logNum32(v.LoginOkID2())},
				}
			}
		case protocol.OpLoginFail:
			if v, ok := protocol.NewLoginFailView(body); ok {
				name, typed = protocol.NameLoginFail, true
				fields = []logField{{K: "reason", V: fmt.Sprintf("0x%02X", v.Reason())}}
			}
		case protocol.OpAccountKicked:
			if v, ok := protocol.NewAccountKickedView(body); ok {
				name, typed = protocol.NameAccountKicked, true
				fields = []logField{{K: "reason", V: fmt.Sprintf("0x%02X", v.Reason())}}
			}
		case protocol.OpServerList:
			if v, ok := protocol.NewServerListView(body); ok {
				name, typed = protocol.NameServerList, true
				fields = serverListLogFields(v)
			}
		case protocol.OpPlayOk:
			if v, ok := protocol.NewPlayOkView(body); ok {
				name, typed = protocol.NamePlayOk, true
				fields = []logField{
					{K: "playOk1", V: logNum32(v.PlayOkID1())},
					{K: "playOk2", V: logNum32(v.PlayOkID2())},
				}
			}
		case protocol.OpPlayFail:
			if v, ok := protocol.NewPlayFailView(body); ok {
				name, typed = protocol.NamePlayFail, true
				fields = []logField{{K: "reason", V: fmt.Sprintf("0x%02X", v.Reason())}}
			}
		}
	} else {
		switch body[0] {
		case protocol.OpRequestAuthLogin:
			if v, ok := protocol.NewRequestAuthLoginView(body); ok {
				name, typed = protocol.NameRequestAuthLogin, true
				fields = []logField{{K: "rsa", V: hex.EncodeToString(v.RSABlock())}}
			}
		case protocol.OpRequestServerList:
			if v, ok := protocol.NewRequestServerListView(body); ok {
				name, typed = protocol.NameRequestServerList, true
				fields = []logField{
					{K: "loginOk1", V: logNum32(v.LoginOkID1())},
					{K: "loginOk2", V: logNum32(v.LoginOkID2())},
				}
			}
		case protocol.OpRequestServerLogin:
			name, typed = protocol.NameRequestServerLogin, true
			id := byte(0)
			if len(body) > 9 {
				id = body[9]
			}
			fields = []logField{{K: "server", V: logNum(int64(id))}}
		case protocol.OpAuthGameGuard:
			if len(body) >= 5 {
				name, typed = protocol.NameAuthGameGuard, true
				fields = []logField{{K: "session", V: logNum32(int32(binary.LittleEndian.Uint32(body[1:])))}}
			}
		default:
			if n, ok := protocol.LoginClientPacketName(body[0]); ok {
				name, typed = n, true
			}
		}
	}
	if typed {
		logLine(w, dir, name, fields)
		return
	}
	var fallback string
	if dir == DirStoC {
		if n, ok := protocol.LoginServerPacketName(body[0]); ok {
			fallback = n
		}
	} else if n, ok := protocol.LoginClientPacketName(body[0]); ok {
		fallback = n
	}
	if fallback == "" {
		fallback = fmt.Sprintf("??(0x%02X)", body[0])
	}
	logLine(w, dir, fallback, []logField{{K: "hex", V: hexBytes(body)}})
}

// serverListLogFields — поля ServerList без ip/port (детерминизм лога при подмене адреса rewrite).
func serverListLogFields(v protocol.ServerListView) []logField {
	fields := []logField{
		{K: "count", V: logNum(int64(v.Count()))},
		{K: "last", V: logNum(int64(v.LastServer()))},
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
						times = append(times, logNum32(ts))
					}
					suffix += " del=" + strings.Join(times, ",")
				}
			}
		}
		fields = append(fields, logField{K: "s" + strconv.Itoa(i+1), V: fmt.Sprintf(
			"{id=%d cur=%d max=%d age=%d pvp=%t status=%d type=%d brackets=%t%s}",
			s.ID, s.CurrentPlayers, s.MaxPlayers, s.AgeLimit, s.PvP,
			s.Status, s.ServerType, s.Brackets, suffix)})
	}
	return fields
}

func logProtocolVersion(w io.Writer, v protocol.ProtocolVersionView) {
	logLine(w, DirCtoS, protocol.NameProtocolVersion,
		[]logField{{K: "version", V: logNum32(v.Version())}})
}

func logKeyPacket(w io.Writer, v protocol.KeyPacketView) {
	logLine(w, DirStoC, protocol.NameKeyPacket, []logField{
		{K: "result", V: logNum(int64(v.Result()))},
		{K: "encryption", V: strconv.FormatBool(v.Encryption())},
		{K: "serverID", V: logNum32(v.ServerID())},
		{K: "key", V: hex.EncodeToString(v.Key())},
	})
}

func logGameLine(w io.Writer, dir byte, body []byte) {
	if len(body) == 0 {
		logLine(w, dir, "??(0x??)", []logField{{K: "hex", V: ""}})
		return
	}
	if dir == DirCtoS {
		switch body[0] {
		case protocol.OpAuthLogin:
			if len(body) >= 17 {
				off := len(body) - 16
				fields := []logField{
					{K: "playKey2", V: logNum32(int32(binary.LittleEndian.Uint32(body[off:])))},
					{K: "playKey1", V: logNum32(int32(binary.LittleEndian.Uint32(body[off+4:])))},
					{K: "loginKey1", V: logNum32(int32(binary.LittleEndian.Uint32(body[off+8:])))},
					{K: "loginKey2", V: logNum32(int32(binary.LittleEndian.Uint32(body[off+12:])))},
				}
				logLine(w, dir, protocol.NameAuthLogin, fields)
				return
			}
		case protocol.OpCharacterSelect:
			if len(body) >= 5 {
				logLine(w, dir, protocol.NameCharacterSelect, []logField{
					{K: "slot", V: logNum32(int32(binary.LittleEndian.Uint32(body[1:])))}})
				return
			}
		case protocol.OpLogout:
			logLine(w, dir, protocol.NameLogout, nil)
			return
		}
		if n, ok := protocol.GameClientPacketName(body[0]); ok {
			logLine(w, dir, n, []logField{{K: "hex", V: hexBytes(body)}})
			return
		}
		logLine(w, dir, fmt.Sprintf("??(0x%02X)", body[0]), []logField{{K: "hex", V: hexBytes(body)}})
		return
	}
	switch body[0] {
	case protocol.OpCharSelectInfo:
		if v, ok := protocol.NewCharSelectionInfoView(body); ok {
			logLine(w, dir, protocol.NameCharSelectInfo, charSelectionLogFields(v))
			return
		}
	case protocol.OpCharSelected:
		if v, ok := protocol.NewCharSelectedView(body); ok {
			if _, ok2 := v.Name(); ok2 {
				logLine(w, dir, protocol.NameCharSelected, charSelectedLogFields(v))
				return
			}
		}
	}
	logLine(w, dir, unknownLineName(dir, body), []logField{{K: "hex", V: hexBytes(body)}})
}

// charSelectionLogFields — читаемое подмножество полей записи списка персонажей.
func charSelectionLogFields(v protocol.CharSelectionInfoView) []logField {
	fields := []logField{{K: "count", V: logNum(int64(v.Count()))}}
	for i := 0; i < v.Count(); i++ {
		e, ok := v.Char(i)
		if !ok {
			break
		}
		fields = append(fields, logField{K: fmt.Sprintf("[%d]", i), V: fmt.Sprintf(
			"{name=%s id=%d level=%d class=%d base=%d sex=%d race=%d hp=%s/%s mp=%s/%s sp=%d exp=%d karma=%d}",
			logQuote(e.Name), e.CharID, e.Level, e.ClassID, e.BaseClassID, e.Sex, e.Race,
			logFlt(e.CurHP), logFlt(e.MaxHP), logFlt(e.CurMP), logFlt(e.MaxMP), e.SP, e.Exp, e.Karma)})
	}
	return fields
}

// charSelectedLogFields — читаемое подмножество полей подтверждения входа.
func charSelectedLogFields(v protocol.CharSelectedView) []logField {
	var fields []logField
	add := func(k string, val any) { fields = append(fields, logField{K: k, V: fmt.Sprintf("%v", val)}) }
	if name, ok := v.Name(); ok {
		add("name", logQuote(name))
	}
	if id, ok := v.CharID(); ok {
		add("id", id)
	}
	if title, ok := v.Title(); ok {
		add("title", logQuote(title))
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
		add("hp", logFlt(hp))
	}
	if mp, ok := v.CurMP(); ok {
		add("mp", logFlt(mp))
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

func logFlt(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// logQuote — строковое поле: кавычки, управляющие руны и эскейпы
func logQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7F:
			fmt.Fprintf(&b, "\\x%02X", r)
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
