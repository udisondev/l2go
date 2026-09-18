package world

// Юнит-тесты чата свёртки (P3.11, тест-план S6b). Оракулы — счётчики State,
// кадры res.Pushes (разбор NewCreatureSayView), эффекты Retires/SaveQ.

import (
	"encoding/binary"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/transport"
)

// opCreatureSay — опкод кадра CreatureSay (пиннут golden-тестами P3.5).
const opCreatureSay = 0x4A

// sayLetter — клиентский кадр Say2 в конверте ящика сущности.
func sayLetter(id transport.EntityID, text string, chatType protocol.ChatType) transport.Envelope {
	b := make([]byte, protocol.Say2Size(text, chatType, ""))
	protocol.WriteSay2(b, text, chatType, "")
	return transport.Envelope{
		To: transport.Addr{Entity: id}, FromID: testRules().Gateway,
		Kind: transport.KindClientFrame, Payload: b,
	}
}

// sayRawLetter — кадр Say2 сырыми байтами (злые входы: битые суррогаты,
// мусорный тип с произвольным D).
func sayRawLetter(id transport.EntityID, payload []byte) transport.Envelope {
	return transport.Envelope{
		To: transport.Addr{Entity: id}, FromID: testRules().Gateway,
		Kind: transport.KindClientFrame, Payload: payload,
	}
}

// chatEnt — житель-игрок с готовым спам-бакетом (arrange: при рождении
// свёртка ставит CAP; прямая постройка теста должна повторить это).
func chatEnt(id transport.EntityID, conn uint64, x, y, z int32) *Entity {
	e := playerEnt(id, x, y, z)
	e.Player.ConnID = conn
	e.Player.ChatBudget = chatSayCapMS
	return e
}

// foldChat — шаг свёртки без гео-зависимости (чат гео не читает).
func foldChat(tick Tick, delta uint64, st *State, ents []*Entity, envs ...transport.Envelope) StepResult {
	return Fold(tick, delta, rand.New(rand.NewPCG(1, uint64(tick))), st, ents,
		portion(envs...), nil, testEnv(nil))
}

// sayPushes — CreatureSay-кадры клиента (в порядке пушей).
func sayPushes(res StepResult, client uint64) []protocol.CreatureSayView {
	var out []protocol.CreatureSayView
	for _, p := range res.Pushes {
		if p.Client == client && pushOp(p) == opCreatureSay {
			if v, ok := protocol.NewCreatureSayView(p.Frame); ok {
				out = append(out, v)
			}
		}
	}
	return out
}

// Доставка и эхо: получатели в радиусе получают ровно один кадр, отправитель
// — ровно одно эхо; до реализации ветка молчала (красный «0 пушей»).
func TestFoldSay2DeliversToRadiusRecipients(t *testing.T) {
	t.Parallel()
	st := newState()
	ents := []*Entity{
		chatEnt(101, 7, syncPos.X, syncPos.Y, syncPos.Z),
		chatEnt(102, 8, syncPos.X+500, syncPos.Y, syncPos.Z),
		chatEnt(103, 9, syncPos.X, syncPos.Y+500, syncPos.Z),
	}
	res := foldChat(10, 0, st, ents, sayLetter(101, "hello", protocol.ChatGeneral))
	for _, tc := range []struct{ client uint64 }{{7}, {8}, {9}} {
		if got := len(sayPushes(res, tc.client)); got != 1 {
			t.Errorf("клиент %d: CreatureSay = %d; want 1 (пуши: %+v)", tc.client, got, res.Pushes)
		}
	}
}

// Поля кадра: objID = ObjectIDBase+ID, тип ALL, имя отправителя, текст
// дословно (мис-роутинг полей — твин P3.10-F31).
func TestFoldSay2PayloadFields(t *testing.T) {
	t.Parallel()
	st := newState()
	ents := []*Entity{chatEnt(201, 7, syncPos.X, syncPos.Y, syncPos.Z)}
	res := foldChat(10, 0, st, ents, sayLetter(201, "привет, мир", protocol.ChatGeneral))
	got := sayPushes(res, 7)
	if len(got) != 1 {
		t.Fatalf("эхо = %d; want 1", len(got))
	}
	v := got[0]
	if v.SenderObjID() != int32(0x10000000+201) {
		t.Errorf("objID = %d; want %d", v.SenderObjID(), 0x10000000+201)
	}
	if v.Type() != protocol.ChatGeneral {
		t.Errorf("тип = %d; want ChatGeneral(0)", v.Type())
	}
	if name, _ := v.SenderName(); name != ents[0].Player.Rec.Name {
		t.Errorf("имя = %q; want %q", name, ents[0].Player.Rec.Name)
	}
	if text, _ := v.Text(); text != "привет, мир" {
		t.Errorf("текст = %q; want дословно %q", text, "привет, мир")
	}
}

// Радиус 3D: граница 1250 включительно по каждой оси, 1251 — нет; int64-охрана
// (dx=50000: int32-квадрат переполнился бы в минус и «пропустил» бы далёкого).
func TestFoldSay2Radius3DBoundaryTable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		dx      int32
		dy      int32
		dz      int32
		deliver bool
	}{
		{"dx=1250 ровно", 1250, 0, 0, true},
		{"dy=1250 ровно", 0, 1250, 0, true},
		{"dz=1250 ровно", 0, 0, 1250, true},
		{"диагональ в радиусе", 700, 700, 700, true},
		{"dx=1251", 1251, 0, 0, false},
		{"dy=1251", 0, 1251, 0, false},
		{"dz=1251", 0, 0, 1251, false},
		{"далёкий dx=50000 (int64-охрана)", 50000, 0, 0, false},
	} {
		st := newState()
		ents := []*Entity{
			chatEnt(301, 7, syncPos.X, syncPos.Y, syncPos.Z),
			chatEnt(302, 8, syncPos.X+tc.dx, syncPos.Y+tc.dy, syncPos.Z+tc.dz),
		}
		res := foldChat(10, 0, st, ents, sayLetter(301, "anyone?", protocol.ChatGeneral))
		if got := len(sayPushes(res, 8)); (got == 1) != tc.deliver {
			t.Errorf("%s: получатель %s получил %d кадров; want доставку=%v",
				tc.name, "302", got, tc.deliver)
		}
	}
}

// Отбор получателей: NPC в радиусе не получает (не-игрок, nil-ConnID без
// паники — твин P3.9-F43), Leaving-игрок не получает, отправитель — эхо.
func TestFoldSay2RecipientFilterTable(t *testing.T) {
	t.Parallel()
	st := newState()
	npc := playerEnt(402, syncPos.X+100, syncPos.Y, syncPos.Z)
	npc.Player = nil
	npc.Npc = &NpcSkin{TemplateID: 1}
	leaving := chatEnt(403, 9, syncPos.X+100, syncPos.Y, syncPos.Z)
	addLeaving(st, leaveState{Entity: 403, Deadline: 1000})
	ents := []*Entity{
		chatEnt(401, 7, syncPos.X, syncPos.Y, syncPos.Z),
		npc,
		leaving,
	}
	res := foldChat(10, 0, st, ents, sayLetter(401, "hi", protocol.ChatGeneral))
	if got := len(sayPushes(res, 7)); got != 1 {
		t.Errorf("эхо отправителя = %d; want 1", got)
	}
	for _, tc := range []struct {
		name   string
		client uint64
	}{
		{"NPC (ConnID 0 — кадра быть не должно)", 0},
		{"Leaving-игрок", 9},
	} {
		if got := len(sayPushes(res, tc.client)); got != 0 {
			t.Errorf("%s: кадров = %d; want 0", tc.name, got)
		}
	}
}

// Эхо — последний кадр ветки (каноничный порядок пушей: рассылка, затем
// sendPacket себе — ChatGeneral.java).
func TestFoldSay2EchoLastInBranch(t *testing.T) {
	t.Parallel()
	st := newState()
	ents := []*Entity{
		chatEnt(501, 7, syncPos.X, syncPos.Y, syncPos.Z),
		chatEnt(502, 8, syncPos.X+100, syncPos.Y, syncPos.Z),
	}
	res := foldChat(10, 0, st, ents, sayLetter(501, "order", protocol.ChatGeneral))
	lastSender, lastIdx := -1, -1
	for i, p := range res.Pushes {
		if pushOp(p) == opCreatureSay && p.Client == 7 {
			lastSender, lastIdx = 7, i
		}
	}
	if lastSender != 7 {
		t.Fatal("эхо отсутствует")
	}
	for i, p := range res.Pushes {
		if pushOp(p) == opCreatureSay && i > lastIdx {
			t.Errorf("кадр после эха (index %d > %d): client=%d", i, lastIdx, p.Client)
		}
	}
}

// Мусорный тип — «packet hack» канона: ActionFailed + полный уход (Retire,
// ConnClose, SaveQ, развязка) — как OpLogout.
func TestFoldSay2GarbageTypeDisconnects(t *testing.T) {
	t.Parallel()
	for _, raw := range []int32{-1, 22, 0x7FFFFFFF} {
		st := newState()
		ents := []*Entity{chatEnt(601, 7, syncPos.X, syncPos.Y, syncPos.Z)}
		b := make([]byte, 0, 16)
		b = append(b, protocol.OpCSay2)
		b = append(b, 0, 0) // пустая строка-терминатор
		b = binary.LittleEndian.AppendUint32(b, uint32(raw))
		res := foldChat(10, 0, st, ents, sayRawLetter(601, b))
		if _, ok := findPush(res, 7, opActionFailed); !ok {
			t.Errorf("тип %d: ActionFailed не отправлен", raw)
		}
		if len(res.Retires) != 1 || res.Retires[0].ID != 601 {
			t.Errorf("тип %d: Retires = %+v; want [601]", raw, res.Retires)
		}
		if _, ok := findPush(res, 7, 0x7E); !ok { // LeaveWorld
			t.Errorf("тип %d: LeaveWorld не отправлен", raw)
		}
		if len(st.SaveQ) != 1 {
			t.Errorf("тип %d: SaveQ = %d; want 1", raw, len(st.SaveQ))
		}
		if len(st.Conns) != 0 || len(st.Accounts) != 0 {
			t.Errorf("тип %d: тени не развязаны: conns=%d accounts=%d", raw, len(st.Conns), len(st.Accounts))
		}
		if st.ChatDropped != 0 {
			t.Errorf("тип %d: ChatDropped = %d; want 0 (разрыв, не дроп-класс)", raw, st.ChatDropped)
		}
	}
}

// Пустой текст — тот же разрыв канона, не дроп-класс.
func TestFoldSay2EmptyTextDisconnects(t *testing.T) {
	t.Parallel()
	st := newState()
	ents := []*Entity{chatEnt(701, 7, syncPos.X, syncPos.Y, syncPos.Z)}
	res := foldChat(10, 0, st, ents, sayLetter(701, "", protocol.ChatGeneral))
	if len(res.Retires) != 1 {
		t.Fatalf("Retires = %d; want 1 (разрыв)", len(res.Retires))
	}
	if st.ChatDropped != 0 {
		t.Errorf("ChatDropped = %d; want 0", st.ChatDropped)
	}
	if _, ok := findPush(res, 7, opActionFailed); !ok {
		t.Error("ActionFailed не отправлен")
	}
}

// Контент-дропы: \b, литеральный U+FFFD, длина > 105 ASCII — ChatDropped,
// коннект жив (нет Retire), кадров нет.
func TestFoldSay2ContentDropsTable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		text string
	}{
		{"item-link \\b", "look\x08ID=123"},
		{"литеральный U+FFFD", "bad \uFFFD text"},
		{"106 ASCII", strings.Repeat("a", 106)},
	} {
		st := newState()
		ents := []*Entity{chatEnt(801, 7, syncPos.X, syncPos.Y, syncPos.Z)}
		res := foldChat(10, 0, st, ents, sayLetter(801, tc.text, protocol.ChatGeneral))
		if st.ChatDropped != 1 {
			t.Errorf("%s: ChatDropped = %d; want 1", tc.name, st.ChatDropped)
		}
		if len(res.Retires) != 0 {
			t.Errorf("%s: коннект порван (Retires=%d); want жив", tc.name, len(res.Retires))
		}
		if got := len(sayPushes(res, 7)); got != 0 {
			t.Errorf("%s: кадров = %d; want 0", tc.name, got)
		}
	}
}

// Длина мерится по сырым UTF-16 юнитам поля, не рунам: 105 астральных рун —
// 210 юнитов → дроп; 52 пары + 1 BMP-руна — 105 юнитов → проходит.
func TestFoldSay2LengthRawUnitsTable(t *testing.T) {
	t.Parallel()
	astral := strings.Repeat("𝄞", 105)     // U+1D11E: пара суррогатов
	mixed := strings.Repeat("𝄞", 52) + "a" // 52×2+1 = 105 юнитов
	for _, tc := range []struct {
		name    string
		text    string
		deliver bool
	}{
		{"105 ASCII", strings.Repeat("a", 105), true},
		{"105 астральных рун (210 юнитов)", astral, false},
		{"52 пары + 1 BMP (105 юнитов)", mixed, true},
	} {
		st := newState()
		ents := []*Entity{chatEnt(901, 7, syncPos.X, syncPos.Y, syncPos.Z)}
		res := foldChat(10, 0, st, ents, sayLetter(901, tc.text, protocol.ChatGeneral))
		if got := len(sayPushes(res, 7)); (got == 1) != tc.deliver {
			t.Errorf("%s: доставлено %d; want %v", tc.name, got, tc.deliver)
		}
	}
}

// Битый UTF-16 (lone surrogate): писатель такое не порождает — ручной кадр;
// дроп с метрикой, не паника. Литеральный U+FFFD неотличим от декод-отказа —
// дропается вместе с ним (over-drop, комментарий TextMeasure).
func TestFoldSay2LoneSurrogateDropped(t *testing.T) {
	t.Parallel()
	st := newState()
	ents := []*Entity{chatEnt(1001, 7, syncPos.X, syncPos.Y, syncPos.Z)}
	b := []byte{protocol.OpCSay2}
	b = binary.LittleEndian.AppendUint16(b, 0xD800) // lone high surrogate
	b = binary.LittleEndian.AppendUint16(b, 'A')
	b = binary.LittleEndian.AppendUint16(b, 0) // терминатор строки
	b = binary.LittleEndian.AppendUint32(b, uint32(protocol.ChatGeneral))
	res := foldChat(10, 0, st, ents, sayRawLetter(1001, b))
	if st.ChatDropped != 1 {
		t.Errorf("ChatDropped = %d; want 1 (битый суррогат)", st.ChatDropped)
	}
	if got := len(sayPushes(res, 7)); got != 0 {
		t.Errorf("кадров = %d; want 0", got)
	}
	if len(res.Retires) != 0 {
		t.Error("коннект порван; want жив")
	}
}

// Битая структура кадра (нетерминированное поле) — DroppedFrames (класс
// структурной валидации, отличается от контент-дропов чата).
func TestFoldSay2TruncatedFrameDropped(t *testing.T) {
	t.Parallel()
	st := newState()
	ents := []*Entity{chatEnt(1101, 7, syncPos.X, syncPos.Y, syncPos.Z)}
	res := foldChat(10, 0, st, ents, sayRawLetter(1101, []byte{protocol.OpCSay2, 'h', 'i'}))
	if st.DroppedFrames != 1 {
		t.Errorf("DroppedFrames = %d; want 1", st.DroppedFrames)
	}
	if st.ChatDropped != 0 || st.ChatIgnored != 0 || st.ChatFlooded != 0 {
		t.Errorf("чат-счётчики тронуты: dropped=%d ignored=%d flooded=%d; want 0/0/0",
			st.ChatDropped, st.ChatIgnored, st.ChatFlooded)
	}
	if got := len(sayPushes(res, 7)); got != 0 {
		t.Errorf("кадров = %d; want 0", got)
	}
}

// Не-ALL каналы — ignore со счётчиком, коннект жив, кадров нет; whisper с
// адресатом — валидный кадр (не парсим адресат).
func TestFoldSay2NonAllChannelIgnoredTable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		t    protocol.ChatType
	}{
		{"whisper", protocol.ChatWhisper},
		{"party", protocol.ChatParty},
		{"shout", protocol.ChatShout},
	} {
		st := newState()
		ents := []*Entity{chatEnt(1201, 7, syncPos.X, syncPos.Y, syncPos.Z)}
		res := foldChat(10, 0, st, ents, sayLetter(1201, "psst", tc.t))
		if st.ChatIgnored != 1 {
			t.Errorf("%s: ChatIgnored = %d; want 1", tc.name, st.ChatIgnored)
		}
		if len(res.Retires) != 0 {
			t.Errorf("%s: коннект порван; want жив", tc.name)
		}
		if got := len(sayPushes(res, 7)); got != 0 {
			t.Errorf("%s: кадров = %d; want 0", tc.name, got)
		}
	}
}

// Токен-бакет: интервал 500 мс, буст 1 — вторая реплика вплотную душится,
// после рефилла (5 тиков × 100 мс) проходит; простой не копит сверх CAP
// (клэмп); списание только при доставке (дроп-ветки бакет не трогают);
// рождение = CAP (первая реплика новичка доставлена).
func TestFoldSay2FloodBucketTable(t *testing.T) {
	t.Parallel()
	st := newState()
	sender := chatEnt(1301, 7, syncPos.X, syncPos.Y, syncPos.Z)
	ents := []*Entity{sender}

	res := foldChat(10, 0, st, ents, sayLetter(1301, "one", protocol.ChatGeneral))
	if got := len(sayPushes(res, 7)); got != 1 {
		t.Fatalf("первая реплика: кадров = %d; want 1", got)
	}
	if sender.Player.ChatBudget != 0 {
		t.Errorf("бакет после доставки = %d; want 0", sender.Player.ChatBudget)
	}

	res = foldChat(11, 0, st, ents, sayLetter(1301, "two", protocol.ChatGeneral))
	if got := len(sayPushes(res, 7)); got != 0 {
		t.Errorf("вторая вплотную: кадров = %d; want 0", got)
	}
	if st.ChatFlooded != 1 {
		t.Errorf("ChatFlooded = %d; want 1", st.ChatFlooded)
	}
	if sender.Player.ChatBudget != 0 {
		t.Errorf("задушенная реплика списала бакет: %d; want 0", sender.Player.ChatBudget)
	}

	_ = foldChat(12, 5, st, ents) // рефилл 500 мс
	if sender.Player.ChatBudget != chatSayCapMS {
		t.Fatalf("бакет после рефилла = %d; want CAP %d", sender.Player.ChatBudget, chatSayCapMS)
	}

	// Контент-дроп бакета не списывает и не ждёт: валидная сразу после дропа проходит.
	st2 := newState()
	s := chatEnt(1302, 7, syncPos.X, syncPos.Y, syncPos.Z)
	ents2 := []*Entity{s}
	_ = foldChat(10, 0, st2, ents2, sayLetter(1302, "bad \x08 drop", protocol.ChatGeneral))
	res = foldChat(11, 0, st2, ents2, sayLetter(1302, "good", protocol.ChatGeneral))
	if got := len(sayPushes(res, 7)); got != 1 {
		t.Errorf("валидная после контент-дропа: кадров = %d; want 1 (дроп не списывает)", got)
	}
}

// Простой >> интервала не копит бакет сверх CAP: две немедленные реплики
// после долгого простоя — вторая дроп (CAP-клэмп).
func TestFoldSay2FloodCapClampAfterIdle(t *testing.T) {
	t.Parallel()
	st := newState()
	sender := chatEnt(1401, 7, syncPos.X, syncPos.Y, syncPos.Z)
	ents := []*Entity{sender}
	_ = foldChat(10, 100, st, ents) // 10 с простоя
	if sender.Player.ChatBudget != chatSayCapMS {
		t.Fatalf("бакет после простоя = %d; want CAP (клэмп)", sender.Player.ChatBudget)
	}
	_ = foldChat(110, 0, st, ents, sayLetter(1401, "first", protocol.ChatGeneral))
	res := foldChat(111, 0, st, ents, sayLetter(1401, "second", protocol.ChatGeneral))
	if got := len(sayPushes(res, 7)); got != 0 {
		t.Errorf("вторая после простоя: кадров = %d; want 0 (буста нет)", got)
	}
	if st.ChatFlooded != 1 {
		t.Errorf("ChatFlooded = %d; want 1", st.ChatFlooded)
	}
}

// Сосед не страдает: реплика второго отправителя той же пачкой доставлена —
// бакеты per-sender. Клиент спамера получает реплику соседа (он получатель),
// но не своё задушенное эхо.
func TestFoldSay2FloodNeighborUnaffected(t *testing.T) {
	t.Parallel()
	st := newState()
	spammer := chatEnt(1501, 7, syncPos.X, syncPos.Y, syncPos.Z)
	neighbor := chatEnt(1502, 8, syncPos.X+100, syncPos.Y, syncPos.Z)
	ents := []*Entity{spammer, neighbor}
	// спамер уже потратил бакет предыдущим шагом
	_ = foldChat(10, 0, st, ents, sayLetter(1501, "spam1", protocol.ChatGeneral))
	res := foldChat(11, 0, st, ents,
		sayLetter(1501, "spam2", protocol.ChatGeneral),
		sayLetter(1502, "honest", protocol.ChatGeneral))
	texts := func(client uint64) []string {
		var out []string
		for _, v := range sayPushes(res, client) {
			s, _ := v.Text()
			out = append(out, s)
		}
		return out
	}
	if got := texts(7); len(got) != 1 || got[0] != "honest" {
		t.Errorf("клиент спамера получил %q; want только реплику соседа [honest] (своё эхо задушено)", got)
	}
	if got := texts(8); len(got) != 1 || got[0] != "honest" {
		t.Errorf("клиент соседа получил %q; want [honest] (задушенная spam2 не доставлена, honest-эхо есть)", got)
	}
	if st.ChatFlooded != 1 {
		t.Errorf("ChatFlooded = %d; want 1 (только спамер)", st.ChatFlooded)
	}
}

// FIFO отправителя: две доставляемые реплики с рефилл-паузой — обе доставлены,
// порядок сохранён у получателя и в эхе.
func TestFoldSay2SenderFIFOWithRefillPause(t *testing.T) {
	t.Parallel()
	st := newState()
	a := chatEnt(1601, 7, syncPos.X, syncPos.Y, syncPos.Z)
	b := chatEnt(1602, 8, syncPos.X+100, syncPos.Y, syncPos.Z)
	ents := []*Entity{a, b}
	r1 := foldChat(10, 0, st, ents, sayLetter(1601, "first", protocol.ChatGeneral))
	_ = foldChat(11, 5, st, ents) // рефилл-пауза 500 мс
	r2 := foldChat(12, 0, st, ents, sayLetter(1601, "second", protocol.ChatGeneral))
	v1 := sayPushes(r1, 8)
	v2 := sayPushes(r2, 8)
	if len(v1) != 1 || len(v2) != 1 {
		t.Fatalf("получатель: кадров по шагам = %d/%d; want 1/1", len(v1), len(v2))
	}
	t1, _ := v1[0].Text()
	t2, _ := v2[0].Text()
	if t1 != "first" || t2 != "second" {
		t.Errorf("FIFO: %q затем %q; want first→second", t1, t2)
	}
	e1, _ := sayPushes(r1, 7)[0].Text()
	e2, _ := sayPushes(r2, 7)[0].Text()
	if e1 != "first" || e2 != "second" {
		t.Errorf("FIFO эха: %q затем %q; want first→second", e1, e2)
	}
}

// Refill бакета — единицы dt×PeriodNS (инвариант 7: период из Rules, не
// хардкод): dt=0 не меняет, delta=2 клэмпит к CAP, нестандартный период
// даёт свой масштаб.
func TestFoldAdvanceChatBudgetRefillUnits(t *testing.T) {
	t.Parallel()
	st := newState()
	e := chatEnt(1701, 7, syncPos.X, syncPos.Y, syncPos.Z)
	e.Player.ChatBudget = 0
	_ = foldChat(10, 0, st, []*Entity{e})
	if e.Player.ChatBudget != 0 {
		t.Errorf("dt=0: бакет = %d; want 0", e.Player.ChatBudget)
	}
	_ = foldChat(11, 2, st, []*Entity{e}) // 200 мс
	if e.Player.ChatBudget != 200 {
		t.Errorf("delta=2 @100мс: бакет = %d; want 200", e.Player.ChatBudget)
	}
	_ = foldChat(12, 100, st, []*Entity{e}) // клэмп
	if e.Player.ChatBudget != chatSayCapMS {
		t.Errorf("клэмп: бакет = %d; want CAP %d", e.Player.ChatBudget, chatSayCapMS)
	}
	// Нестандартный период: 200 мс × delta 1 = +200.
	st2 := newState()
	e2 := chatEnt(1702, 7, syncPos.X, syncPos.Y, syncPos.Z)
	e2.Player.ChatBudget = 0
	env := testEnv(nil)
	env.Rules.PeriodNS = 200_000_000
	_ = Fold(10, 1, rand.New(rand.NewPCG(1, 10)), st2, []*Entity{e2}, nil, nil, env)
	if e2.Player.ChatBudget != 200 {
		t.Errorf("период 200мс: бакет = %d; want 200 (период из Rules)", e2.Player.ChatBudget)
	}
}

// Классовый гвард: Say2 от Leaving-сущности и неизвестного ID — DroppedFrames,
// без рассылки и паники.
func TestFoldSay2FrameFromLeavingOrUnknown(t *testing.T) {
	t.Parallel()
	st := newState()
	leaving := chatEnt(1801, 7, syncPos.X, syncPos.Y, syncPos.Z)
	addLeaving(st, leaveState{Entity: 1801, Deadline: 1000})
	watcher := chatEnt(1802, 8, syncPos.X+100, syncPos.Y, syncPos.Z)
	ents := []*Entity{leaving, watcher}
	res := foldChat(10, 0, st, ents,
		sayLetter(1801, "ghost", protocol.ChatGeneral),
		sayLetter(9999, "unknown", protocol.ChatGeneral))
	if st.DroppedFrames != 2 {
		t.Errorf("DroppedFrames = %d; want 2", st.DroppedFrames)
	}
	if got := len(sayPushes(res, 8)); got != 0 {
		t.Errorf("наблюдатель получил кадров = %d; want 0", got)
	}
}

// Интерливинги пачек: [Logout, Say2] — Say2 после ухода применён безвредно
// (двойной уход = прецедент двойного Logout: второй Remove no-op); [Say2,
// LinkDead] — доставка + grace-удержание.
func TestFoldSay2BatchInterleavingsTable(t *testing.T) {
	t.Parallel()
	t.Run("Say2 затем LinkDead", func(t *testing.T) {
		st := newState()
		a := chatEnt(1901, 7, syncPos.X, syncPos.Y, syncPos.Z)
		ents := []*Entity{a}
		st.Conns[7] = 1901
		st.Accounts[a.Player.Rec.Account] = shadowEntity{ID: 1901, Conn: 7}
		ld, _ := transport.EncodeLetter(transport.ConnRefMsg{Conn: 7})
		res := foldChat(10, 0, st, ents,
			sayLetter(1901, "last words", protocol.ChatGeneral),
			transport.Envelope{To: transport.Addr{Entity: 1}, FromID: testRules().Gateway,
				Kind: transport.KindLinkDead, Payload: ld})
		if got := len(sayPushes(res, 7)); got != 1 {
			t.Errorf("реплика перед обрывом: кадров = %d; want 1", got)
		}
		if len(st.Leaving) != 1 || st.Leaving[0].Entity != 1901 {
			t.Errorf(" grace-удержание: %+v; want entity 1901", st.Leaving)
		}
	})
	t.Run("мусорный Say2 дважды одной пачкой", func(t *testing.T) {
		st := newState()
		a := chatEnt(1902, 7, syncPos.X, syncPos.Y, syncPos.Z)
		st.Conns[7] = 1902
		st.Accounts[a.Player.Rec.Account] = shadowEntity{ID: 1902, Conn: 7}
		b := make([]byte, 0, 12)
		b = append(b, protocol.OpCSay2, 0, 0)
		b = binary.LittleEndian.AppendUint32(b, 999)
		res := foldChat(10, 0, st, []*Entity{a},
			sayRawLetter(1902, b), sayRawLetter(1902, b))
		if len(res.Retires) < 1 {
			t.Fatalf("Retires = %d; want ≥1 (уход состоялся)", len(res.Retires))
		}
		if len(st.SaveQ) != 1 {
			t.Errorf("SaveQ = %d; want 1 (аккаунт один)", len(st.SaveQ))
		}
	})
}

// Детерминизм счётчиков: два прогона одного сценария — бит-в-бит равные дампы
// (реплей-сходимость P3.12).
func TestFoldSay2CountersDeterministicDump(t *testing.T) {
	t.Parallel()
	run := func() []byte {
		st := newState()
		a := chatEnt(2001, 7, syncPos.X, syncPos.Y, syncPos.Z)
		b := chatEnt(2002, 8, syncPos.X+100, syncPos.Y, syncPos.Z)
		ents := []*Entity{a, b}
		_ = foldChat(10, 0, st, ents,
			sayLetter(2001, "ok", protocol.ChatGeneral),
			sayLetter(2001, "spam", protocol.ChatGeneral),
			sayLetter(2001, "bad \x08", protocol.ChatGeneral),
			sayLetter(2001, "psst", protocol.ChatWhisper))
		_ = foldChat(11, 5, st, ents, sayLetter(2002, "reply", protocol.ChatGeneral))
		return st.Dump(ents)
	}
	d1, d2 := run(), run()
	if string(d1) != string(d2) {
		t.Error("дампы двух прогонов различаются (недетерминизм)")
	}
}
