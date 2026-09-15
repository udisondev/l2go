package gateway

import (
	"context"
	"log/slog"
	"strings"
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
		g.failLoginRaw(gc, 0, false)
		return
	}
	v, ok := protocol.NewProtocolVersionView(frame)
	if !ok || v.Version() != protocol.ProtocolVersionInterlude {
		// Канон: чужая версия — KeyPacket(result=0) + разрыв (CryptInit).
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
	g.conn.SetReadMode(gc.id, conn.ModePresession)
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
	gc.account = norm
	gc.phase = phValidating
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
		g.failLogin(gc, protocol.GSReasonAccessFailedTryLater)
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
	gc.phase = phList
	g.requestCharList(gc)
}

// requestCharList — письмо персист-актору (Corr=connID) + таймер таймаута.
func (g *Gateway) requestCharList(gc *gconn) {
	g.persistRequest(gc, persist.OpCharList)
}

func (g *Gateway) persistRequest(gc *gconn, op string) {
	gc.pending = &pending{op: op}
	body, err := persist.EncodeRequest(persist.Request{Op: op, Corr: uint64(gc.id), Account: gc.account})
	if err != nil {
		slog.Error("gateway: кодирование запроса персиста", "op", op, "err", err)
		g.failLogin(gc, protocol.GSReasonSystemErrorLoginLater)
		return
	}
	g.reg.Send(transport.Envelope{
		To:      transport.Addr{Entity: g.cfg.Persist},
		FromID:  g.id,
		Kind:    transport.KindPersistRequest,
		Payload: body,
	})
	time.AfterFunc(g.cfg.PersistTimeout, func() {
		select {
		case g.completions <- completion{conn: gc.id, kind: cpPersistTimeout}:
		default:
		}
	})
}

// onListFrame — фаза списка: NewChar, CharacterCreate, CharacterSelect.
func (g *Gateway) onListFrame(gc *gconn, frame []byte) {
	switch frame[0] {
	case protocol.OpCNewCharacter:
		g.replyTemplates(gc)
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
	if !ok || !persist.ValidName(name) {
		g.replyCreateFail(gc, protocol.CharCreateReasonIncorrectName)
		return
	}
	sex, hs, hc, face := v.Sex(), v.HairStyle(), v.HairColor(), v.Face()
	if err := persist.ValidateAppearance(int(sex), int(hs), int(hc), int(face)); err != nil {
		g.replyCreateFail(gc, protocol.CharCreateReasonCreationFailed)
		return
	}
	gc.phase = phCreating
	body, err := persist.EncodeRequest(persist.Request{
		Op: persist.OpCreateChar, Corr: uint64(gc.id), Account: gc.account,
		Name: name, Sex: int(sex), HairStyle: int(hs), HairColor: int(hc), Face: int(face),
	})
	if err != nil {
		slog.Error("gateway: кодирование запроса создания", "err", err)
		g.replyCreateFail(gc, protocol.CharCreateReasonCreationFailed)
		gc.phase = phList
		return
	}
	gc.pending = &pending{op: persist.OpCreateChar}
	g.reg.Send(transport.Envelope{
		To:      transport.Addr{Entity: g.cfg.Persist},
		FromID:  g.id,
		Kind:    transport.KindPersistRequest,
		Payload: body,
	})
	time.AfterFunc(g.cfg.PersistTimeout, func() {
		select {
		case g.completions <- completion{conn: gc.id, kind: cpPersistTimeout}:
		default:
		}
	})
}

// replyCreate — ответ персиста на создание.
func (g *Gateway) replyCreate(gc *gconn, reply persist.Reply) {
	gc.phase = phList
	if !reply.OK {
		g.replyCreateFail(gc, createFailReason(reply.Err))
		return
	}
	wire := make([]byte, protocol.CharCreateOkSize)
	protocol.WriteCharCreateOk(wire)
	g.reply(gc, wire)
	// Канон: сервер подтверждает созданием только Ok; свежий список клиент
	// перечитает новым коннектом (CharSelectionInfo идёт в ответ на AuthLogin).
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
	wire := make([]byte, protocol.CharSelectedSize(selectedData(rec)))
	protocol.WriteCharSelected(wire, selectedData(rec))
	g.reply(gc, wire)
	gc.phase = phSelected
}

// onSelectedFrame: EnterWorld — контрольное письмо региону.
func (g *Gateway) onSelectedFrame(gc *gconn, frame []byte) {
	if frame[0] != protocol.OpCEnterWorld {
		g.failLogin(gc, protocol.GSReasonAccessFailedTryLater)
		return
	}
	if _, ok := protocol.NewEnterWorldView(frame); !ok {
		g.failLogin(gc, protocol.GSReasonAccessFailedTryLater)
		return
	}
	g.sendRegion(transport.KindEnterWorld,
		enterWorldMsg{Conn: uint64(gc.id), Account: gc.account, Char: *gc.char})
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
		protocol.OpCSay2, protocol.OpLogout:
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

// failLogin — GSLoginFail + close-after-fail (канон: LoginFail завершает
// коннект, повторная попытка — новым коннектом).
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
		entries[i] = selectionEntry(r)
	}
	wire := make([]byte, protocol.CharSelectionInfoSize(entries))
	protocol.WriteCharSelectionInfo(wire, entries, -1)
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

// selectionEntry — CharRecord → запись CharSelectionInfo.
func selectionEntry(r persist.CharRecord) protocol.CharSelectionEntry {
	return protocol.CharSelectionEntry{
		Name:        r.Name,
		CharID:      int32(r.Slot),
		LoginName:   r.Account,
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
func selectedData(r persist.CharRecord) protocol.CharSelectedData {
	return protocol.CharSelectedData{
		Name: r.Name, CharID: int32(r.Slot),
		Sex: int32(r.Sex), Race: int32(r.Race), ClassID: int32(r.ClassID),
		X: int32(r.X), Y: int32(r.Y), Z: int32(r.Z),
		CurHP: float64(r.HP), CurMP: float64(r.MP),
		Level: int32(r.Level), Exp: r.Exp,
		STR: int32(persist.HumanFighter.Str), DEX: int32(persist.HumanFighter.Dex),
		CON: int32(persist.HumanFighter.Con), INT: int32(persist.HumanFighter.Int),
		WIT: int32(persist.HumanFighter.Wit), MEN: int32(persist.HumanFighter.Men),
		GameTime: 0, // заполняется миром из тиков (D1) с P3.7
	}
}

// createFailReason — строка ошибки персиста → причина канона
// (CharCreateFail.java: соответствие текстов домена — по смыслу причины).
func createFailReason(errMsg string) protocol.CharCreateFailReason {
	switch {
	case strings.Contains(errMsg, "лимит персонажей"):
		return protocol.CharCreateReasonTooManyCharacters
	case strings.Contains(errMsg, "имя занято"):
		return protocol.CharCreateReasonNameAlreadyExists
	case strings.Contains(errMsg, "имя"):
		return protocol.CharCreateReasonIncorrectName
	default:
		return protocol.CharCreateReasonCreationFailed
	}
}
