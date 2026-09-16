package gateway

import (
	"context"
	"log/slog"
	"time"

	"github.com/udisondev/l2go/internal/conn"
	"github.com/udisondev/l2go/internal/encode"
	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/transport"
)

// Порядок фаз предсессионной стейт-машины и close-after-fail — порт
// последовательности GameClient/GameServer пакетов канона L2J Interlude
// (Mobius CT_0_Interlude @43ac8878): ProtocolVersion → KeyPacket →
// AuthLogin → CharSelectionInfo/CharCreate* → CharacterSelect → CharSelected;
// GSLoginFail завершает коннект.

// onFrame — диспетчер кадра по фазе коннекта.
func (g *Gateway) onFrame(gc *gconn, frame []byte) {
	if len(frame) == 0 {
		return
	}
	switch gc.phase {
	case phHandshake:
		g.onProtocolVersion(gc, frame)
	case phAuth:
		g.onAuthLogin(gc, frame)
	case phList:
		g.onListFrame(gc, frame)
	case phSelected:
		g.onSelectedFrame(gc, frame)
	case phWorld:
		g.onStationaryFrame(gc, frame)
	default: // phValidating, phCreating: любой кадр — вне окна
		g.failLogin(gc, protocol.GSReasonAccessFailedTryLater)
	}
}

// onProtocolVersion: версия 746 → KeyPacket (ключ из OnOpen, кадр plain,
// марка crypt на всех последующих); чужая → KeyPacket(result=0) + разрыв.
func (g *Gateway) onProtocolVersion(gc *gconn, frame []byte) {
	if frame[0] != protocol.OpProtocolVersion {
		g.failLoginRaw(gc, protocol.GSReasonNoText, false)
		return
	}
	v, ok := protocol.NewProtocolVersionView(frame)
	if !ok || v.Version() != protocol.ProtocolVersionInterlude {
		// Канон: чужая версия — KeyPacket(result=0) + разрыв
		// (Mobius CT_0_Interlude ProtocolVersion.java/KeyPacket.java).
		wire := make([]byte, protocol.KeyPacketSize)
		protocol.WriteKeyPacket(wire, 0, gc.key[:], false, gameServerID)
		g.replyRaw(gc, wire, false)
		g.teardown(gc, false)
		return
	}
	keyWire := make([]byte, protocol.KeyPacketSize)
	protocol.WriteKeyPacket(keyWire, 1, gc.key[:], true, gameServerID)
	g.replyRaw(gc, keyWire, false)
	gc.cryptOn = true
	gc.phase = phAuth
	// Режим presession — с обработки AuthLogin: до того живёт абсолютный
	// HandshakeTimeout фазы accept→AuthLogin (dribble не продлевает).
}

// onAuthLogin: нормализация → домен → асинхронная ValidateSession;
// повторный AuthLogin при валидации в полёте — вне окна, разрыв.
func (g *Gateway) onAuthLogin(gc *gconn, frame []byte) {
	if frame[0] != protocol.OpAuthLogin {
		g.failLogin(gc, protocol.GSReasonAccessFailedTryLater)
		return
	}
	v, ok := protocol.NewAuthLoginView(frame)
	if !ok {
		g.failLogin(gc, protocol.GSReasonAccessFailedTryLater)
		return
	}
	account, ok := v.Account()
	if !ok {
		g.failLogin(gc, protocol.GSReasonAccessFailedTryLater)
		return
	}
	norm, err := persist.NormalizeLogin(account)
	if err != nil {
		g.failLogin(gc, protocol.GSReasonAccessFailedTryLater)
		return
	}
	l1, ok1 := v.LoginKey1()
	l2, ok2 := v.LoginKey2()
	p1, ok3 := v.PlayKey1()
	p2, ok4 := v.PlayKey2()
	if !ok1 || !ok2 || !ok3 || !ok4 {
		g.failLogin(gc, protocol.GSReasonAccessFailedTryLater)
		return
	}
	gc.sessionID = p1 // канон: SessionID списков/выбора = playOk1
	gc.account = norm
	gc.phase = phValidating
	g.conn.SetReadMode(gc.id, conn.ModePresession)
	go func() {
		valid, err := g.validator.ValidateSession(context.Background(), norm, l1, l2, p1, p2)
		select {
		case g.completions <- completion{conn: gc.id, kind: cpValidated, valid: valid, err: err}:
		case <-time.After(loginlinkValidateCap):
			// Канал завершений ≥ MaxConns и in-flight ≤ коннектов —
			// исчерпание здесь процесс уже деградировал; не виснуть.
		}
	}()
}

// loginlinkValidateCap — страховка отправки завершения: сам вызов стыка
// ограничен 3 с (loginlink.ValidateTimeout), канал — cap ≥ MaxConns.
const loginlinkValidateCap = 10 * time.Second

// gameServerID — ID записи этого GS в ServerList LS (фаза 3: единственный).
const gameServerID = int32(1)

// onValidated: при valid=true — бинд с вытеснением старого; провал —
// GSLoginFail, старый не тронут. err — fail-closed.
func (g *Gateway) onValidated(gc *gconn, cp completion) {
	if cp.err != nil {
		slog.Warn("gateway: LS недоступен — fail-closed", "err", cp.err)
		g.failLogin(gc, protocol.GSReasonAccessFailedTryLater)
		return
	}
	if !cp.valid {
		// Канон: провал сверки ключей — SYSTEM_ERROR_LOGIN_LATER
		// (LoginServerThread PlayerAuthResponse isAuthed=false).
		g.failLogin(gc, protocol.GSReasonSystemErrorLoginLater)
		return
	}
	if old, busy := g.accounts[gc.account]; busy && old != gc.id {
		oldGc := g.conns[old]
		if oldGc != nil {
			g.displaced.Add(1)
			g.teardown(oldGc, true)
		}
	}
	g.accounts[gc.account] = gc.id
	g.boundN.Add(1)
	gc.phase = phList
	g.requestCharList(gc)
}

// requestCharList — письмо списка персист-актору (Corr=connID).
func (g *Gateway) requestCharList(gc *gconn) {
	g.persistRequest(gc, persist.Request{Op: persist.OpCharList, Corr: uint64(gc.id), Account: gc.account})
}

// persistRequest — единый запрос персисту: ожидание ключуется монотонным
// seq (устаревший таймер таймаута не убивает следующее ожидание — сверка в
// onCompletion), таймер снимается ответом и teardown.
func (g *Gateway) persistRequest(gc *gconn, req persist.Request) {
	gc.waitSeq++
	gc.pending = &pending{op: req.Op, seq: gc.waitSeq}
	body, err := persist.EncodeRequest(req)
	if err != nil {
		slog.Error("gateway: кодирование запроса персиста", "op", req.Op, "err", err)
		gc.pending = nil
		g.failLogin(gc, protocol.GSReasonSystemErrorLoginLater)
		return
	}
	seq := gc.waitSeq
	gc.pending.timer = time.AfterFunc(g.cfg.PersistTimeout, func() {
		select {
		case g.completions <- completion{conn: gc.id, kind: cpPersistTimeout, seq: seq}:
		case <-time.After(loginlinkValidateCap):
		}
	})
	g.reg.Send(transport.Envelope{
		To:      transport.Addr{Entity: g.cfg.Persist},
		FromID:  g.id,
		Kind:    transport.KindPersistRequest,
		Payload: body,
	})
}

// onListFrame — фаза списка: NewChar, CharacterCreate, CharacterSelect.
func (g *Gateway) onListFrame(gc *gconn, frame []byte) {
	switch frame[0] {
	case protocol.OpCNewCharacter:
		g.replyTemplates(gc)
	case protocol.OpCCharacterDelete:
		// Анти-скоуп удаления: отказ с причиной канона, коннект жив
		// (CharDeleteFail.java — порт в protocol).
		wire := make([]byte, protocol.CharDeleteFailSize)
		protocol.WriteCharDeleteFail(wire, protocol.CharDeleteReasonDeletionFailed)
		g.reply(gc, wire)
	case protocol.OpCCharacterCreate:
		g.onCreate(gc, frame)
	case protocol.OpCharacterSelect:
		g.onSelect(gc, frame)
	default:
		g.failLogin(gc, protocol.GSReasonAccessFailedTryLater)
	}
}

// onCreate: локальная валидация доменов, запрос персисту; ответ — по письму.
func (g *Gateway) onCreate(gc *gconn, frame []byte) {
	v, ok := protocol.NewCharacterCreateView(frame)
	if !ok {
		g.replyCreateFail(gc, protocol.CharCreateReasonIncorrectName)
		return
	}
	name, ok := v.Name()
	if !ok {
		g.replyCreateFail(gc, protocol.CharCreateReasonIncorrectName)
		return
	}
	// Порядок канона: сначала домен символов (INCORRECT_NAME), потом длина
	// (REASON_16_ENG_CHARS); домен и длина различаются отдельно.
	if !isAlnumASCII(name) {
		g.replyCreateFail(gc, protocol.CharCreateReasonIncorrectName)
		return
	}
	if len(name) > persist.MaxNameLen {
		g.replyCreateFail(gc, protocol.CharCreateReasonNameTooLong)
		return
	}
	// Единственный шаблон фазы: чужая раса/класс — отказ канона, не подмена
	// (CharacterCreate.java: шаблон недоступен → CREATION_FAILED).
	if int(v.Race()) != persist.HumanFighter.Race || int(v.ClassID()) != persist.HumanFighter.ClassID {
		g.replyCreateFail(gc, protocol.CharCreateReasonCreationFailed)
		return
	}
	sex, hs, hc, face := v.Sex(), v.HairStyle(), v.HairColor(), v.Face()
	if err := persist.ValidateAppearance(int(sex), int(hs), int(hc), int(face)); err != nil {
		g.replyCreateFail(gc, protocol.CharCreateReasonCreationFailed)
		return
	}
	gc.phase = phCreating
	g.persistRequest(gc, persist.Request{
		Op: persist.OpCreateChar, Corr: uint64(gc.id), Account: gc.account,
		Name: name, Sex: int(sex), HairStyle: int(hs), HairColor: int(hc), Face: int(face),
	})
}

// replyCreate — ответ персиста на создание: Ok и свежий CharSelectionInfo
// следом (канон CharacterCreate.initNewChar: sources списка у клиента —
// только серверные ответы).
func (g *Gateway) replyCreate(gc *gconn, reply persist.Reply) {
	gc.phase = phList
	if !reply.OK {
		g.replyCreateFail(gc, createFailReason(reply.Code, reply.Err))
		return
	}
	wire := make([]byte, protocol.CharCreateOkSize)
	protocol.WriteCharCreateOk(wire)
	g.reply(gc, wire)
	g.requestCharList(gc)
}

// onSelect: слот валидируется на применении (0 ≤ slot < count, занят).
func (g *Gateway) onSelect(gc *gconn, frame []byte) {
	v, ok := protocol.NewCharacterSelectView(frame)
	if !ok {
		g.failLogin(gc, protocol.GSReasonAccessFailedTryLater)
		return
	}
	slot := v.CharSlot()
	if slot < 0 || int(slot) >= len(gc.chars) {
		g.failLogin(gc, protocol.GSReasonAccessFailedTryLater)
		return
	}
	rec := gc.chars[slot]
	gc.char = &rec
	data := selectedData(rec, gc.sessionID)
	wire := make([]byte, protocol.CharSelectedSize(data))
	protocol.WriteCharSelected(wire, data)
	g.reply(gc, wire)
	gc.phase = phSelected
}

// onSelectedFrame: EnterWorld — контрольное письмо региону.
func (g *Gateway) onSelectedFrame(gc *gconn, frame []byte) {
	if frame[0] != protocol.OpCEnterWorld {
		g.failLogin(gc, protocol.GSReasonAccessFailedTryLater)
		return
	}
	if _, ok := protocol.NewEnterWorldView(frame); !ok || gc.char == nil {
		// char ставится SelectChar; здесь его нет — состояние коннекта
		// несогласовано с фазой, детерминированный отказ.
		g.failLogin(gc, protocol.GSReasonAccessFailedTryLater)
		return
	}
	char, err := transport.EncodeLetter(*gc.char)
	if err != nil {
		slog.Error("gateway: кодирование снимка персонажа", "err", err)
		g.failLogin(gc, protocol.GSReasonAccessFailedTryLater)
		return
	}
	g.sendRegion(transport.KindEnterWorld,
		transport.EnterWorldMsg{Conn: uint64(gc.id), Account: gc.account, Char: char})
	gc.phase = phWorld
	g.conn.SetReadMode(gc.id, conn.ModeStationary)
}

// onStationaryFrame — белый список опкодов фазы 3; MoveToLocation
// коалесится (последний побеждает, и под капом inbox — заменой старого).
func (g *Gateway) onStationaryFrame(gc *gconn, frame []byte) {
	switch frame[0] {
	case protocol.OpCMoveToLocation:
		if i := findMove(gc.inbox); i >= 0 {
			gc.inbox[i] = frame
			g.coalesced.Add(1)
			return
		}
		g.appendInbox(gc, frame)
	case protocol.OpCValidatePosition, protocol.OpCCannotMoveAnymore,
		protocol.OpCSay2, protocol.OpLogout, protocol.OpCRequestRestart:
		g.appendInbox(gc, frame)
	default:
		g.unknownOps.Add(1)
	}
}

func (g *Gateway) appendInbox(gc *gconn, frame []byte) {
	if len(gc.inbox) >= g.cfg.InboxCap {
		g.inboxDropped.Add(1)
		return
	}
	gc.inbox = append(gc.inbox, frame)
}

func findMove(inbox [][]byte) int {
	for i, f := range inbox {
		if len(f) > 0 && f[0] == protocol.OpCMoveToLocation {
			return i
		}
	}
	return -1
}

// failLogin — GSLoginFail + close-after-fail (канон Mobius
// CT_0_Interlude: LoginFail завершает коннект, повторная попытка — новым
// коннектом; прецедент close-after-fail login-ноги — LoginController).
func (g *Gateway) failLogin(gc *gconn, reason protocol.GSLoginFailReason) {
	g.failLoginRaw(gc, reason, gc.cryptOn)
}

func (g *Gateway) failLoginRaw(gc *gconn, reason protocol.GSLoginFailReason, crypt bool) {
	wire := make([]byte, protocol.GSLoginFailSize)
	protocol.WriteGSLoginFail(wire, reason)
	g.replyRaw(gc, wire, crypt)
	g.teardown(gc, false)
}

// reply* — отправка кадров через encode-стейдж с маркой крипты коннекта.
func (g *Gateway) reply(gc *gconn, wire []byte) {
	g.replyRaw(gc, wire, gc.cryptOn)
}

func (g *Gateway) replyRaw(gc *gconn, wire []byte, crypt bool) {
	g.stage.Push(encode.ClientID(gc.id), wire, crypt)
}

// replyCharList — CharSelectionInfo из записей персиста.
func (g *Gateway) replyCharList(gc *gconn) {
	entries := make([]protocol.CharSelectionEntry, len(gc.chars))
	for i, r := range gc.chars {
		entries[i] = selectionEntry(r, gc.sessionID)
	}
	wire := make([]byte, protocol.CharSelectionInfoSize(entries))
	protocol.WriteCharSelectionInfo(wire, entries, lastSeenSlot(gc.chars))
	g.reply(gc, wire)
}

// replyTemplates — CharTemplates: единственный шаблон фазы (Human Fighter).
func (g *Gateway) replyTemplates(gc *gconn) {
	tpl := []protocol.CharTemplate{{
		Race: int32(persist.HumanFighter.Race), ClassID: int32(persist.HumanFighter.ClassID),
		Str: int32(persist.HumanFighter.Str), Dex: int32(persist.HumanFighter.Dex),
		Con: int32(persist.HumanFighter.Con), Int: int32(persist.HumanFighter.Int),
		Wit: int32(persist.HumanFighter.Wit), Men: int32(persist.HumanFighter.Men),
	}}
	wire := make([]byte, protocol.CharTemplatesSize(len(tpl)))
	protocol.WriteCharTemplates(wire, tpl)
	g.reply(gc, wire)
}

func (g *Gateway) replyCreateFail(gc *gconn, reason protocol.CharCreateFailReason) {
	wire := make([]byte, protocol.CharCreateFailSize)
	protocol.WriteCharCreateFail(wire, reason)
	g.reply(gc, wire)
}

// isAlnumASCII — домен имени создания: 1+ букв/цифр ASCII (длину проверяет
// отдельная ветка причин канона).
func isAlnumASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9') {
			return false
		}
	}
	return len(s) > 0
}

// lastSeenSlot — предвыбор записи с последним входом (канон: при отсутствии
// активного CharSelectionInfo отмечает последнего lastAccess).
func lastSeenSlot(chars []persist.CharRecord) int {
	best, bestAt := -1, int64(0)
	for i, r := range chars {
		if r.LastSeenUnix > bestAt {
			best, bestAt = i, r.LastSeenUnix
		}
	}
	return best
}

// selectionEntry — CharRecord → запись CharSelectionInfo (SessionID —
// playOk1 сессии коннекта).
func selectionEntry(r persist.CharRecord, sessionID int32) protocol.CharSelectionEntry {
	return protocol.CharSelectionEntry{
		Name:        r.Name,
		CharID:      int32(r.Slot),
		LoginName:   r.Account,
		SessionID:   sessionID,
		Sex:         int32(r.Sex),
		Race:        int32(r.Race),
		BaseClassID: int32(r.ClassID),
		ClassID:     int32(r.ClassID),
		CurHP:       float64(r.HP),
		CurMP:       float64(r.MP),
		MaxHP:       float64(r.HP),
		MaxMP:       float64(r.MP),
		Level:       int32(r.Level),
		Exp:         r.Exp,
		HairStyle:   int32(r.HairStyle),
		HairColor:   int32(r.HairColor),
		Face:        int32(r.Face),
	}
}

// selectedData — CharRecord → аргумент писателя CharSelected.
func selectedData(r persist.CharRecord, sessionID int32) protocol.CharSelectedData {
	return protocol.CharSelectedData{
		Name: r.Name, CharID: int32(r.Slot), SessionID: sessionID,
		Sex: int32(r.Sex), Race: int32(r.Race), ClassID: int32(r.ClassID),
		X: int32(r.X), Y: int32(r.Y), Z: int32(r.Z),
		CurHP: float64(r.HP), CurMP: float64(r.MP),
		Level: int32(r.Level), Exp: r.Exp,
		STR: int32(persist.HumanFighter.Str), DEX: int32(persist.HumanFighter.Dex),
		CON: int32(persist.HumanFighter.Con), INT: int32(persist.HumanFighter.Int),
		WIT: int32(persist.HumanFighter.Wit), MEN: int32(persist.HumanFighter.Men),
		GameTime: 0, // ноль безвреден; игровое время клиенту несёт слиток входа (P3.7)
	}
}

// createFailReason — код ошибки персиста → причина канона
// (CharCreateFail.java; коды — контракт persist.Reply.Code, не тексты).
func createFailReason(code, errMsg string) protocol.CharCreateFailReason {
	switch code {
	case persist.CodeCharLimit:
		return protocol.CharCreateReasonTooManyCharacters
	case persist.CodeNameTaken:
		return protocol.CharCreateReasonNameAlreadyExists
	case persist.CodeNameInvalid:
		return protocol.CharCreateReasonIncorrectName
	case persist.CodeAppearance:
		return protocol.CharCreateReasonCreationFailed
	default:
		slog.Error("gateway: неизвестный код отказа персиста", "code", code, "err", errMsg)
		return protocol.CharCreateReasonCreationFailed
	}
}
