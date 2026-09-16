package world

// Свёртка входа/выхода (P3.7): вход/бинд/вытеснение, LinkDead/grace, Logout,
// ретраи сохранений, дропы, детерминизм. Fold — чистая функция: оракулы —
// StepResult{Out,Pushes,Births,Retires} и State.

import (
	"bytes"
	"math/rand/v2"
	"testing"

	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/transport"
)

func mkRec(account, name string, slot int) persist.CharRecord {
	return persist.CharRecord{
		Account: account, Slot: slot, Name: name, ClassID: 0, Race: 0,
		Level: 1, HP: 80, MP: 30, X: -71338, Y: 258271, Z: -3104,
	}
}

func enterMsg(conn uint64, acc string, rec persist.CharRecord) transport.Envelope {
	body, err := transport.EncodeLetter(transport.EnterWorldMsg{
		Conn: conn, Account: acc, Char: mustJSONChar(rec)})
	if err != nil {
		panic("тест: кодирование EnterWorldMsg: " + err.Error())
	}
	return transport.Envelope{
		To: transport.Addr{Entity: 1}, FromID: 901,
		Kind: transport.KindEnterWorld, Payload: body,
	}
}

func mustJSONChar(r persist.CharRecord) []byte {
	b, err := transport.EncodeLetter(r)
	if err != nil {
		panic(err)
	}
	return b
}

func linkDeadMsg(conn uint64) transport.Envelope {
	body, err := transport.EncodeLetter(transport.ConnRefMsg{Conn: conn})
	if err != nil {
		panic("тест: кодирование ConnRefMsg: " + err.Error())
	}
	return transport.Envelope{
		To: transport.Addr{Entity: 1}, FromID: 901,
		Kind: transport.KindLinkDead, Payload: body,
	}
}

func clientFrame(id transport.EntityID, op byte) transport.Envelope {
	return transport.Envelope{
		To: transport.Addr{Entity: id}, FromID: 901,
		Kind: transport.KindClientFrame, Payload: []byte{op},
	}
}

func portion(envs ...transport.Envelope) []Portion {
	return []Portion{{Region: 1, Tick: 1, Envs: envs}}
}

func fold1(tick Tick, st *State, ents []*Entity, envs ...transport.Envelope) StepResult {
	return Fold(tick, 1, rand.New(rand.NewPCG(1, uint64(tick))), st, ents, portion(envs...), nil, testRules())
}

func applyBirth(st *State, res StepResult) []*Entity {
	var ents []*Entity
	id := transport.EntityID(100)
	for _, b := range res.Births {
		id++
		b.Ent.ID = id
		ents = append(ents, &b.Ent)
		if b.Ent.Player != nil {
			st.ResolveBirth(b.Ent.Player.Rec.Account, b.Ent.Player.ConnID, id)
		}
	}
	return ents
}

// Вход: рождение с позицией снимка, тени после материализации, очистка SaveQ.
func TestFoldEnterWorldBirth(t *testing.T) {
	t.Parallel()
	st := newState()
	st.SaveQ["acc"] = &saveState{Account: "acc"} // мёртвый ретрай старого снимка
	res := fold1(10, st, nil, enterMsg(7, "acc", mkRec("acc", "Vasya", 0)))
	if len(res.Births) != 1 {
		t.Fatalf("рождений %d; want 1", len(res.Births))
	}
	b := res.Births[0]
	if b.Ent.Player == nil || b.Ent.Player.Rec.Name != "Vasya" || !b.Ent.Player.PendingTeleport {
		t.Fatalf("рождение без игрока: %+v", b.Ent.Player)
	}
	if b.Ent.Pos.X != -71338 {
		t.Fatalf("позиция рождения не из снимка: %+v", b.Ent.Pos)
	}
	if len(st.SaveQ) != 0 {
		t.Fatal("SaveQ аккаунта не очищен перезаходом")
	}
	ents := applyBirth(st, res)
	if len(st.Conns) != 1 || st.Conns[7] != ents[0].ID {
		t.Fatalf("Conns после материализации: %+v", st.Conns)
	}
}

// Вытеснение: повторный вход аккаунта ретирит старую сущность БЕЗ персиста.
func TestFoldEnterWorldDisplacesWithoutPersist(t *testing.T) {
	t.Parallel()
	st := newState()
	res := fold1(10, st, nil, enterMsg(7, "acc", mkRec("acc", "Vasya", 0)))
	ents := applyBirth(st, res)
	oldID := ents[0].ID

	res2 := fold1(11, st, ents, enterMsg(8, "acc", mkRec("acc", "Vasya", 0)))
	if len(res2.Retires) != 1 || res2.Retires[0].ID != oldID {
		t.Fatalf("старая сущность не ретирена: %+v", res2.Retires)
	}
	for _, env := range res2.Out {
		if env.Kind == transport.KindPersistRequest {
			t.Fatal("вытеснение отправило персист старого снимка")
		}
	}
	ents2 := applyBirth(st, res2)
	if sh := st.Accounts["acc"]; sh.ID != ents2[len(ents2)-1].ID {
		t.Fatalf("тень аккаунта не перезаписана: %+v", sh)
	}
}

// LinkDead: grace-запись; повторный LinkDead — no-op; неизвестный conn —
// dead-letter.
func TestFoldLinkDeadGraceRecord(t *testing.T) {
	t.Parallel()
	st := newState()
	res := fold1(10, st, nil, enterMsg(7, "acc", mkRec("acc", "Vasya", 0)))
	ents := applyBirth(st, res)

	res2 := fold1(11, st, ents, linkDeadMsg(7))
	if len(res2.Retires) != 0 {
		t.Fatal("LinkDead ретирит немедленно; want grace")
	}
	if len(st.Leaving) != 1 || st.Leaving[0].Deadline != 11+Tick(testRules().GraceTicks) {
		t.Fatalf("grace-запись: %+v", st.Leaving)
	}
	// Истечение на границе: deadline-1 — ещё жив.
	fold1(st.Leaving[0].Deadline-1, st, ents)
	if len(st.Leaving) != 1 {
		t.Fatal("grace истёк раньше дедлайна")
	}
	res3 := fold1(st.Leaving[0].Deadline, st, ents)
	if len(res3.Retires) != 1 {
		t.Fatal("grace не истёк на дедлайне")
	}
	found := false
	for _, env := range res3.Out {
		if env.Kind == transport.KindPersistRequest {
			found = true
		}
	}
	if !found {
		t.Fatal("экспирация без сохранения")
	}
	for _, p := range res3.Pushes {
		if p.Frame[0] == protocol.OpUserInfo || bytes.HasPrefix(p.Frame, []byte{0x7e}) {
			t.Fatal("экспирация шлёт кадры в мёртвый сокет")
		}
	}
	// неизвестный conn — dead-letter
	before := st.DeadLetters
	fold1(99, st, nil, linkDeadMsg(404))
	if st.DeadLetters != before+1 {
		t.Fatal("неизвестный LinkDead не учтён dead-letter'ом")
	}
}

// Logout: остановка движения, сохранение (Corr=EntityID), Retire, LeaveWorld,
// ConnClose, развязка.
func TestFoldLogoutFrameEffects(t *testing.T) {
	t.Parallel()
	st := newState()
	res := fold1(10, st, nil, enterMsg(7, "acc", mkRec("acc", "Vasya", 0)))
	ents := applyBirth(st, res)
	id := ents[0].ID
	ents[0].Moving = true

	res2 := fold1(11, st, ents, clientFrame(id, protocol.OpLogout))
	if ents[0].Moving {
		t.Fatal("движение не остановлено")
	}
	if len(res2.Retires) != 1 || res2.Retires[0].ID != id {
		t.Fatalf("Retires: %+v", res2.Retires)
	}
	var sawSave, sawClose bool
	for _, env := range res2.Out {
		if env.Kind == transport.KindPersistRequest {
			sawSave = true
			req, err := persist.DecodeRequest(env.Payload)
			if err != nil || req.Corr != uint64(id) || req.Op != persist.OpSaveChar {
				t.Fatalf("письмо сохранения: %+v err=%v corr=%d", req, err, req.Corr)
			}
		}
		if env.Kind == transport.KindConnClose {
			sawClose = true
		}
	}
	if !sawSave || !sawClose {
		t.Fatalf("Out без сохранения/закрытия: save=%v close=%v", sawSave, sawClose)
	}
	sawLeave := false
	for _, p := range res2.Pushes {
		if len(p.Frame) > 0 && p.Frame[0] == 0x7e {
			sawLeave = true
			if p.Client != 7 || !p.Crypt {
				t.Fatalf("LeaveWorld: client=%d crypt=%v", p.Client, p.Crypt)
			}
		}
	}
	if !sawLeave {
		t.Fatal("LeaveWorld не запушен")
	}
	if len(st.Conns) != 0 || len(st.Accounts) != 0 {
		t.Fatalf("развязка: conns=%v accounts=%v", st.Conns, st.Accounts)
	}
	if len(st.SaveQ) != 1 {
		t.Fatalf("SaveQ: %d; want 1", len(st.SaveQ))
	}
}

// Развязка Accounts условна: EnterWorld новой сессии раньше Logout старой в
// одном шаге — бинд новой не стирается.
func TestFoldLogoutConditionalAccountsUnbind(t *testing.T) {
	t.Parallel()
	st := newState()
	res := fold1(10, st, nil, enterMsg(7, "acc", mkRec("acc", "Vasya", 0)))
	ents := applyBirth(st, res)
	oldID := ents[0].ID

	res2 := fold1(11, st, ents,
		enterMsg(8, "acc", mkRec("acc", "Vasya", 0)),
		clientFrame(oldID, protocol.OpLogout))
	ents2 := applyBirth(st, res2)
	newID := ents2[len(ents2)-1].ID
	if sh := st.Accounts["acc"]; sh.ID != newID {
		t.Fatalf("бинд новой сессии стёрт развязкой: %+v", sh)
	}
	if st.Conns[8] != newID {
		t.Fatalf("Conns[8] = %d; want %d", st.Conns[8], newID)
	}
}

// RequestRestart: отказ канона, коннект жив.
func TestFoldRequestRestartFrame(t *testing.T) {
	t.Parallel()
	st := newState()
	res := fold1(10, st, nil, enterMsg(7, "acc", mkRec("acc", "Vasya", 0)))
	ents := applyBirth(st, res)

	res2 := fold1(11, st, ents, clientFrame(ents[0].ID, protocol.OpCRequestRestart))
	if len(res2.Retires) != 0 || len(res2.Out) != 0 {
		t.Fatalf("рестарт изменил мир: %+v", res2)
	}
	if len(res2.Pushes) != 2 ||
		res2.Pushes[0].Frame[0] != 0x5f || res2.Pushes[1].Frame[0] != 0x25 {
		t.Fatalf("пуши рестарта: %+v", res2.Pushes)
	}
}

// Кадры Leaving/неизвестным — дроп с метрикой, позиция не меняется.
func TestFoldFrameInGraceDropped(t *testing.T) {
	t.Parallel()
	st := newState()
	res := fold1(10, st, nil, enterMsg(7, "acc", mkRec("acc", "Vasya", 0)))
	ents := applyBirth(st, res)
	fold1(11, st, ents, linkDeadMsg(7))

	before := st.DroppedFrames
	posBefore := ents[0].Pos
	fold1(12, st, ents, clientFrame(ents[0].ID, protocol.OpCValidatePosition))
	if st.DroppedFrames != before+1 {
		t.Fatal("кадр Leaving-сущности не учтён дропом")
	}
	if ents[0].Pos != posBefore {
		t.Fatalf("позиция Leaving-сущности изменилась: %+v → %+v", posBefore, ents[0].Pos)
	}
	fold1(12, st, ents, clientFrame(99999, protocol.OpCValidatePosition))
	if st.DroppedFrames != before+2 {
		t.Fatal("кадр неизвестной сущности не учтён дропом")
	}
}

// Ретраи: строго через SaveRetryTicks; ok гасит; валидационный отказ — стоп;
// IO — повтор; поздний ответ — no-op.
func TestFoldSaveRetryAndReplies(t *testing.T) {
	t.Parallel()
	st := newState()
	res := fold1(10, st, nil, enterMsg(7, "acc", mkRec("acc", "Vasya", 0)))
	ents := applyBirth(st, res)
	id := ents[0].ID

	fold1(11, st, ents, clientFrame(id, protocol.OpLogout))

	// Тихое ожидание: до t+SaveRetryTicks повторов нет.
	rules := testRules()
	for tk := Tick(12); tk < Tick(11)+Tick(rules.SaveRetryTicks); tk++ {
		r := fold1(tk, st, ents)
		for _, env := range r.Out {
			if env.Kind == transport.KindPersistRequest {
				t.Fatalf("повтор раньше каденса на тике %d", tk)
			}
		}
	}
	// На t+SaveRetryTicks — повтор.
	r := fold1(Tick(11)+Tick(rules.SaveRetryTicks), st, ents)
	saw := 0
	for _, env := range r.Out {
		if env.Kind == transport.KindPersistRequest {
			saw++
		}
	}
	if saw != 1 {
		t.Fatalf("повторов на каденсе: %d; want 1", saw)
	}

	// ok гасит очередь.
	okReply, _ := persist.EncodeReply(persist.Reply{Op: persist.OpSaveChar, Corr: uint64(id), OK: true})
	fold1(Tick(11)+Tick(rules.SaveRetryTicks)+1, st, ents,
		transport.Envelope{To: transport.Addr{Entity: 1}, FromID: 900,
			Kind: transport.KindPersistReply, Payload: okReply})
	if len(st.SaveQ) != 0 {
		t.Fatal("ok не погасил SaveQ")
	}
}

func TestFoldPersistReplyValidationStopsAndStaleNoOp(t *testing.T) {
	t.Parallel()
	st := newState()
	res := fold1(10, st, nil, enterMsg(7, "acc", mkRec("acc", "Vasya", 0)))
	ents := applyBirth(st, res)
	id := ents[0].ID
	fold1(11, st, ents, clientFrame(id, protocol.OpLogout))

	// валидационный отказ — стоп ретраев
	bad, _ := persist.EncodeReply(persist.Reply{Op: persist.OpSaveChar, Corr: uint64(id), Code: "name_invalid"})
	fold1(12, st, ents, transport.Envelope{
		To: transport.Addr{Entity: 1}, FromID: 900,
		Kind: transport.KindPersistReply, Payload: bad})
	q := st.SaveQ["acc"]
	if q == nil || !q.Dead || st.Unsavable != 1 {
		t.Fatalf("валидационный отказ: q=%v unsavable=%d", q, st.Unsavable)
	}
	for tk := Tick(13); tk < 13+Tick(testRules().SaveRetryTicks)*2; tk++ {
		r := fold1(tk, st, ents)
		for _, env := range r.Out {
			if env.Kind == transport.KindPersistRequest {
				t.Fatal("Dead-очередь ретраится")
			}
		}
	}

	// поздний ответ без ожидания (очередь уже погашена ok) — no-op без
	// паники; новая очередь перезахода не тронута
	okRep, _ := persist.EncodeReply(persist.Reply{Op: persist.OpSaveChar, Corr: uint64(id), OK: true})
	fold1(98, st, ents, transport.Envelope{
		To: transport.Addr{Entity: 1}, FromID: 900,
		Kind: transport.KindPersistReply, Payload: okRep})
	r4 := fold1(99, st, nil, enterMsg(9, "acc", mkRec("acc", "Vasya", 0)))
	ents = applyBirth(st, r4)
	fold1(100, st, ents, transport.Envelope{
		To: transport.Addr{Entity: 1}, FromID: 900,
		Kind: transport.KindPersistReply, Payload: okRep})
	if q := st.SaveQ["acc"]; q != nil {
		t.Fatal("поздний ответ воскресил очередь")
	}
}

// Детерминизм: два прогона одной серии — бит-в-бит Dump и эффекты.
func TestFoldDeterministicDumpAndEffects(t *testing.T) {
	run := func() ([]byte, []StepResult) {
		st := newState()
		var results []StepResult
		var ents []*Entity
		r1 := fold1(10, st, nil,
			enterMsg(7, "acc", mkRec("acc", "Vasya", 0)),
			enterMsg(8, "acc2", mkRec("acc2", "Petya", 0)))
		ents = applyBirth(st, r1)
		results = append(results, r1)
		r2 := fold1(11, st, ents, linkDeadMsg(7), linkDeadMsg(8))
		results = append(results, r2)
		r3 := fold1(12, st, ents, clientFrame(ents[1].ID, protocol.OpLogout))
		results = append(results, r3)
		return st.Dump(ents), results
	}
	d1, r1 := run()
	d2, r2 := run()
	if !bytes.Equal(d1, d2) {
		t.Fatal("Dump недетерминирован")
	}
	for i := range r1 {
		if len(r1[i].Out) != len(r2[i].Out) || len(r1[i].Pushes) != len(r2[i].Pushes) ||
			len(r1[i].Births) != len(r2[i].Births) || len(r1[i].Retires) != len(r2[i].Retires) {
			t.Fatalf("шаг %d: эффекты недетерминированы", i)
		}
	}
}

// Пачка одного шага [EnterWorld(c1), LinkDead(c1), EnterWorld(c2)] — окно
// F7/инв-М1: зомби-рождения не остаётся, свежий снимок не перезаписывается.
func TestFoldSameStepDisplacement(t *testing.T) {
	t.Parallel()
	st := newState()
	res := fold1(10, st, nil,
		enterMsg(1, "acc", mkRec("acc", "Vasya", 0)),
		linkDeadMsg(1),
		enterMsg(2, "acc", mkRec("acc", "Vasya", 0)))
	if len(res.Births) != 2 {
		t.Fatalf("рождений %d; want 2", len(res.Births))
	}
	if !res.Births[0].Ent.Player.EnterLeaving {
		t.Fatal("первое рождение не помечено EnterLeaving")
	}
	if !res.Births[0].Ent.Player.DisplacedSameStep {
		t.Fatal("первое рождение не вытеснено повторным входом той же пачки")
	}
	// Материализация: актор пропускает вытеснённое, спавнит только второе.
	id := transport.EntityID(100)
	var ents []*Entity
	for _, b := range res.Births {
		if b.Ent.Player != nil && b.Ent.Player.DisplacedSameStep {
			continue
		}
		id++
		b.Ent.ID = id
		ents = append(ents, &b.Ent)
		st.ResolveBirth(b.Ent.Player.Rec.Account, b.Ent.Player.ConnID, id)
	}
	if len(ents) != 1 {
		t.Fatalf("сущностей после материализации %d; want 1", len(ents))
	}
	// Экспирация «зомби» невозможна (его нет в Leaving); сохранение
	// единственной сущности владеет аккаунтом.
	fold1(11, st, ents, linkDeadMsg(2))
	res3 := fold1(11+Tick(testRules().GraceTicks), st, ents)
	saves := 0
	for _, env := range res3.Out {
		if env.Kind == transport.KindPersistRequest {
			saves++
		}
	}
	if saves != 1 {
		t.Fatalf("сохранений на экспирации: %d; want 1", saves)
	}
}

// C5-хвост: экспирация не шлёт KindConnClose.
func TestFoldGraceExpiryNoConnClose(t *testing.T) {
	t.Parallel()
	st := newState()
	res := fold1(10, st, nil, enterMsg(7, "acc", mkRec("acc", "Vasya", 0)))
	ents := applyBirth(st, res)
	fold1(11, st, ents, linkDeadMsg(7))
	res2 := fold1(11+Tick(testRules().GraceTicks), st, ents)
	for _, env := range res2.Out {
		if env.Kind == transport.KindConnClose {
			t.Fatal("экспирация шлёт ConnClose в мёртвый сокет")
		}
	}
}

// C14: IO-отказ — повтор по каденсу (не немедленный, не Dead).
func TestFoldPersistReplyIORetryCadence(t *testing.T) {
	t.Parallel()
	st := newState()
	res := fold1(10, st, nil, enterMsg(7, "acc", mkRec("acc", "Vasya", 0)))
	ents := applyBirth(st, res)
	id := ents[0].ID
	fold1(11, st, ents, clientFrame(id, protocol.OpLogout))

	ioRep, _ := persist.EncodeReply(persist.Reply{Op: persist.OpSaveChar, Corr: uint64(id), Code: persist.CodeIO})
	fold1(12, st, ents, transport.Envelope{
		To: transport.Addr{Entity: 1}, FromID: 900,
		Kind: transport.KindPersistReply, Payload: ioRep})
	q := st.SaveQ["acc"]
	if q == nil || q.Dead {
		t.Fatal("IO-отказ убил очередь (want каденс)")
	}
	for tk := Tick(13); tk < 12+Tick(testRules().SaveRetryTicks); tk++ {
		r := fold1(tk, st, ents)
		for _, env := range r.Out {
			if env.Kind == transport.KindPersistRequest {
				t.Fatalf("IO-повтор раньше каденса: тик %d", tk)
			}
		}
	}
	r := fold1(12+Tick(testRules().SaveRetryTicks), st, ents)
	saw := 0
	for _, env := range r.Out {
		if env.Kind == transport.KindPersistRequest {
			saw++
		}
	}
	if saw != 1 {
		t.Fatalf("IO-повторов на каденсе: %d; want 1", saw)
	}
}

// C17: глубокое сравнение эффектов двух прогонов (порядок Out/Pushes).
func TestFoldDeterministicEffectsDeep(t *testing.T) {
	t.Parallel()
	run := func() StepResult {
		st := newState()
		r1 := fold1(10, st, nil,
			enterMsg(7, "acc", mkRec("acc", "Vasya", 0)),
			enterMsg(8, "acc2", mkRec("acc2", "Petya", 0)))
		ents := applyBirth(st, r1)
		return fold1(11, st, ents,
			clientFrame(ents[0].ID, protocol.OpLogout),
			clientFrame(ents[1].ID, protocol.OpCRequestRestart))
	}
	a, b := run(), run()
	if len(a.Out) != len(b.Out) || len(a.Pushes) != len(b.Pushes) {
		t.Fatalf("длины эффектов: Out %d/%d Pushes %d/%d",
			len(a.Out), len(b.Out), len(a.Pushes), len(b.Pushes))
	}
	for i := range a.Out {
		if string(a.Out[i].Payload) != string(b.Out[i].Payload) || a.Out[i].Kind != b.Out[i].Kind {
			t.Fatalf("Out[%d] недетерминирован", i)
		}
	}
	for i := range a.Pushes {
		if string(a.Pushes[i].Frame) != string(b.Pushes[i].Frame) || a.Pushes[i].Client != b.Pushes[i].Client {
			t.Fatalf("Pushes[%d] недетерминирован", i)
		}
	}
}

// C16-хвост: битый JSON записи — dead-letter.
func TestFoldEnterWorldBrokenCharJSON(t *testing.T) {
	t.Parallel()
	st := newState()
	env := transport.Envelope{
		To: transport.Addr{Entity: 1}, FromID: 901,
		Kind:    transport.KindEnterWorld,
		Payload: append([]byte(`{"conn":7,"account":"acc","char":`), []byte("{bad")...),
	}
	res := Fold(10, 1, rand.New(rand.NewPCG(1, 10)), st, nil, portion(env), nil, testRules())
	if len(res.Births) != 0 || st.DeadLetters != 1 {
		t.Fatalf("битый Char: births=%d deadLetters=%d", len(res.Births), st.DeadLetters)
	}
	// несогласованный аккаунт записи с конвертом — тоже dead-letter.
	body, _ := transport.EncodeLetter(transport.EnterWorldMsg{
		Conn: 7, Account: "acc", Char: mustJSONChar(mkRec("other", "Vasya", 0))})
	res2 := Fold(11, 1, rand.New(rand.NewPCG(1, 11)), st, nil,
		portion(transport.Envelope{To: transport.Addr{Entity: 1}, FromID: 901,
			Kind: transport.KindEnterWorld, Payload: body}), nil, testRules())
	if len(res2.Births) != 0 || st.DeadLetters != 2 {
		t.Fatalf("чужой аккаунт записи: births=%d deadLetters=%d", len(res2.Births), st.DeadLetters)
	}
}
