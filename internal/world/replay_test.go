package world

// Реплей-гейт D6 (P3.12): детерминизм свёртки по записанному логу — два
// прогона бит-в-бит, живой дамп равен реплею, мутация лога детектируется;
// FinalDump региона; шов времени как второй источник тика.

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/transport"
)

// ——— крафт синт-сессии (чистые кадры, без fs) ———

func replayEnterLetter(conn uint64, account, name string, x, y int) transport.Envelope {
	return transport.Envelope{
		To: transport.Addr{Entity: testRules().From}, FromID: testRules().Gateway,
		Kind:    transport.KindEnterWorld,
		Payload: mustJSONEnter(conn, mkRecAt(account, name, x, y)),
	}
}

func replayLinkDeadLetter(conn uint64) transport.Envelope {
	body, err := transport.EncodeLetter(transport.ConnRefMsg{Conn: conn})
	if err != nil {
		panic("тест: кодирование ConnRefMsg: " + err.Error())
	}
	return transport.Envelope{
		To: transport.Addr{Entity: testRules().From}, FromID: testRules().Gateway,
		Kind: transport.KindLinkDead, Payload: body,
	}
}

func replayMoveLetter(id transport.EntityID) transport.Envelope {
	b := make([]byte, 29)
	writeMoveFrame(b, 1000, 2000, -3100, 0, 0, -3100)
	return transport.Envelope{
		To: transport.Addr{Entity: id}, FromID: testRules().Gateway,
		Kind: transport.KindClientFrame, Payload: b,
	}
}

func replayLogoutLetter(id transport.EntityID) transport.Envelope {
	return transport.Envelope{
		To: transport.Addr{Entity: id}, FromID: testRules().Gateway,
		Kind: transport.KindClientFrame, Payload: []byte{protocol.OpLogout},
	}
}

// replayBirthEnt — рождение, зеркалящее выход foldEnterWorld с материализацией
// актора (ID/Owner присвоены, бакеты при рождении = CAP).
func replayBirthEnt(id transport.EntityID, conn uint64, rec persist.CharRecord) Entity {
	return Entity{ID: id, Owner: 7,
		Pos:     Position{X: int32(rec.X), Y: int32(rec.Y), Z: int32(rec.Z)},
		Heading: int32(rec.Heading) & 0xFFFF, HP: int32(rec.HP),
		Player: &Player{Rec: rec, ConnID: conn, PendingTeleport: true,
			SpeedBudget: speedCAP, ChatBudget: chatSayCapMS}}
}

// scenarioFrames — синт-сессия S: вход → чат → движение (шаг delta=2) →
// пустой шаг → второй вход → LinkDead → пара [EnterWorld, LinkDead] одного
// конна (порядок-чувствительная пара свопа) → экспирация grace → Logout →
// финальный пустой. Leaving/SaveQ непусты к концу (F4: детектор не слеп на
// их обходах). swap=true — пара в порядке [LinkDead, EnterWorld].
func scenarioFrames(swap bool) (FileHeader, []LogFrame) {
	rules := testRules()
	hdr := FileHeader{
		Version: 6, Payloads: true, Region: 7, Session: 12345,
		PeriodNS: uint64(rules.PeriodNS), GraceTicks: uint64(rules.GraceTicks),
		SaveRetryTicks: uint64(rules.SaveRetryTicks),
		Persist:        rules.Persist, Gateway: rules.Gateway, CtrlFrom: rules.From,
	}
	hero := mkRecAt("aaa", "Hero", 100, 200)
	bobby := mkRecAt("bbb", "Bobby", 300, 400)
	cara := mkRecAt("ccc", "Cara", 500, 600)
	pair := []transport.Envelope{replayEnterLetter(9, "ccc", "Cara", 500, 600), replayLinkDeadLetter(9)}
	if swap {
		pair[0], pair[1] = pair[1], pair[0]
	}
	// рождение пары: живой логгер пишет EnterLeaving по порядку A (fold
	// погасил рождение pending-LinkDead), порядок B — обычное рождение.
	caraEnt := replayBirthEnt(103, 9, cara)
	caraEnt.Player.EnterLeaving = !swap
	steps := []StepRecord{
		{Tick: 1, Delta: 1,
			Portions: []PortionRecord{{Box: rules.From, Envs: []transport.Envelope{
				replayEnterLetter(1, "aaa", "Hero", 100, 200)}}},
			Births: []BirthRecord{{ID: 101, Ent: replayBirthEnt(101, 1, hero)}}},
		{Tick: 2, Delta: 1,
			Portions: []PortionRecord{{Box: 101, Envs: []transport.Envelope{sayLetter(101, "hi", protocol.ChatGeneral)}}}},
		{Tick: 3, Delta: 2, // пропуск тика: delta=2
			Portions: []PortionRecord{{Box: 101, Envs: []transport.Envelope{replayMoveLetter(101)}}}},
		{Tick: 4, Delta: 1}, // пустой шаг
		{Tick: 5, Delta: 1,
			Portions: []PortionRecord{{Box: rules.From, Envs: []transport.Envelope{
				replayEnterLetter(3, "bbb", "Bobby", 300, 400)}}},
			Births: []BirthRecord{{ID: 102, Ent: replayBirthEnt(102, 3, bobby)}}},
		{Tick: 6, Delta: 1,
			Portions: []PortionRecord{{Box: 102, Envs: []transport.Envelope{sayLetter(102, "yo", protocol.ChatGeneral)}}}},
		{Tick: 8, Delta: 1,
			Portions: []PortionRecord{{Box: rules.From, Envs: []transport.Envelope{replayLinkDeadLetter(1)}}}},
		{Tick: 10, Delta: 1},
		{Tick: 12, Delta: 1,
			Portions: []PortionRecord{{Box: rules.From, Envs: pair}},
			Births:   []BirthRecord{{ID: 103, Ent: caraEnt}}},
		// экспирация Hero (дедлайн 8+50=58) и Cara в порядке A (12+50=62):
		// ретраи сохранений оставляют SaveQ непустыми к концу сессии
		{Tick: 59, Delta: 1, Retires: []Retire{{ID: 101}}},
		{Tick: 61, Delta: 1,
			Portions: []PortionRecord{{Box: 102, Envs: []transport.Envelope{replayLogoutLetter(102)}}},
			Retires:  []Retire{{ID: 102}}},
	}
	if !swap {
		steps = append(steps, StepRecord{Tick: 63, Delta: 1, Retires: []Retire{{ID: 103}}})
	}
	steps = append(steps, StepRecord{Tick: 64, Delta: 1})
	frames := make([]LogFrame, len(steps))
	for i := range steps {
		st := steps[i]
		frames[i] = LogFrame{Step: &st}
	}
	return hdr, frames
}

// Два прогона одной сессии на одних слайсах — бит-в-бит (гатлинг D3);
// Leaving/SaveQ сессии S непусты — обходы карт свёртки под детектором.
func TestReplaySessionTwoRunsBitExact(t *testing.T) {
	hdr, frames := scenarioFrames(false)
	r1, err := Replay(hdr, frames, nil, emptyGeo)
	if err != nil {
		t.Fatalf("Replay #1: %v", err)
	}
	r2, err := Replay(hdr, frames, nil, emptyGeo)
	if err != nil {
		t.Fatalf("Replay #2: %v", err)
	}
	if !bytes.Equal(r1.Dump, r2.Dump) {
		t.Fatalf("повторный реплей разошёлся: len %d vs %d", len(r1.Dump), len(r2.Dump))
	}
	if r1.Steps == 0 || r1.Letters == 0 {
		t.Fatalf("пустой прогон: steps=%d letters=%d", r1.Steps, r1.Letters)
	}
	if len(r1.Dump) == 0 {
		t.Fatal("дамп пуст")
	}
}

// Перестановка двух писем ([EnterWorld, LinkDead] одного конна ↔ своп):
// порядок A — EnterLeaving-рождение (Leaving), порядок B — DeadLetters и
// обычное рождение; дампы обязаны различаться (детектор не слеп).
func TestReplaySwapLettersChangesDump(t *testing.T) {
	hdrA, framesA := scenarioFrames(false)
	hdrB, framesB := scenarioFrames(true)
	if hdrA.Session != hdrB.Session {
		t.Fatal("варианты сессии разошлись по sessionID — сравнение нечестно")
	}
	dA, err := Replay(hdrA, framesA, nil, emptyGeo)
	if err != nil {
		t.Fatalf("Replay A: %v", err)
	}
	dB, err := Replay(hdrB, framesB, nil, emptyGeo)
	if err != nil {
		t.Fatalf("Replay B: %v", err)
	}
	if bytes.Equal(dA.Dump, dB.Dump) {
		t.Fatal("перестановка писем не меняет дамп — детектор слеп")
	}
}

// Сессия без payloads — ошибка на входе: конверты без тел дали бы мусорный
// дамп (все содержательные письма легли бы в DeadLetters).
func TestReplayRejectsPayloadlessSession(t *testing.T) {
	hdr, frames := scenarioFrames(false)
	hdr.Payloads = false
	if _, err := Replay(hdr, frames, nil, emptyGeo); err == nil {
		t.Fatal("Replay(payloads=false) прошёл молча; want ошибка")
	}
}

// Правила из заголовка валидируются: нулевой период/окна/адресаты — ошибка
// (лог недоверен и в правилах).
func TestReplayRejectsInvalidHeaderRules(t *testing.T) {
	for _, c := range []struct {
		name string
		mut  func(*FileHeader)
	}{
		{"PeriodNS=0", func(h *FileHeader) { h.PeriodNS = 0 }},
		{"GraceTicks=0", func(h *FileHeader) { h.GraceTicks = 0 }},
		{"SaveRetryTicks=0", func(h *FileHeader) { h.SaveRetryTicks = 0 }},
		{"Persist=0", func(h *FileHeader) { h.Persist = 0 }},
		{"Gateway=0", func(h *FileHeader) { h.Gateway = 0 }},
		{"CtrlFrom=0", func(h *FileHeader) { h.CtrlFrom = 0 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			hdr, frames := scenarioFrames(false)
			c.mut(&hdr)
			if _, err := Replay(hdr, frames, nil, emptyGeo); err == nil {
				t.Errorf("Replay(%s=0) прошёл; want ошибка", c.name)
			}
		})
	}
}

// Маркер паники обрывает достоверность: шаги строго до маркера, флаг поднят,
// дамп частичный (не выдаётся за полный).
func TestReplayStopsAtPanicMarker(t *testing.T) {
	hdr, frames := scenarioFrames(false)
	cut := 5
	marker := PanicRecord{Tick: frames[cut].Step.Tick, Phase: 2}
	mutated := append([]LogFrame(nil), frames[:cut]...)
	mutated = append(mutated, LogFrame{Panic: &marker})
	mutated = append(mutated, frames[cut:]...)
	res, err := Replay(hdr, mutated, nil, emptyGeo)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if !res.StoppedAtPanic {
		t.Fatal("StoppedAtPanic не поднят")
	}
	if res.Steps != uint64(cut) {
		t.Fatalf("Steps = %d; want %d (строго до маркера)", res.Steps, cut)
	}
	full, err := Replay(hdr, frames, nil, emptyGeo)
	if err != nil {
		t.Fatalf("Replay полный: %v", err)
	}
	if bytes.Equal(res.Dump, full.Dump) {
		t.Fatal("частичный дамп совпал с полным — обрыв не наблюдаем")
	}
}

// Расхождение ID записи и сущности рождения — ошибка разбора (злой вход:
// фантомное население).
func TestReplayBirthIDMismatchRejected(t *testing.T) {
	hdr, frames := scenarioFrames(false)
	bad := append([]LogFrame(nil), frames...)
	bad[0].Step.Births[0].ID = 999
	if _, err := Replay(hdr, bad, nil, emptyGeo); err == nil {
		t.Fatal("рождение с расходящимся ID принято; want ошибка")
	}
}

// Сид RNG — (region, tick) из заголовка и записи: другой регион или сдвиг
// тиков меняет дамп; идентичные копии — равны.
func TestReplayRNGSeededByRegionAndTick(t *testing.T) {
	hdr, frames := scenarioFrames(false)
	base, err := Replay(hdr, frames, nil, emptyGeo)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	hdrOther := hdr
	hdrOther.Region = 8
	other, err := Replay(hdrOther, frames, nil, emptyGeo)
	if err != nil {
		t.Fatalf("Replay(другой регион): %v", err)
	}
	if bytes.Equal(base.Dump, other.Dump) {
		t.Fatal("другой регион не меняет дамп — сид не от заголовка")
	}
	shifted := make([]LogFrame, len(frames))
	for i, f := range frames {
		st := *f.Step // копия значения: Tick — поле записи, не общий слайс
		st.Tick += 7
		shifted[i] = LogFrame{Step: &st}
	}
	shift, err := Replay(hdr, shifted, nil, emptyGeo)
	if err != nil {
		t.Fatalf("Replay(сдвиг тиков): %v", err)
	}
	if bytes.Equal(base.Dump, shift.Dump) {
		t.Fatal("сдвиг тиков не меняет дамп — сид не от тика записи")
	}
	again, err := Replay(hdr, frames, nil, emptyGeo)
	if err != nil {
		t.Fatalf("Replay повтор: %v", err)
	}
	if !bytes.Equal(base.Dump, again.Dump) {
		t.Fatal("идентичные входы дали разные дампы")
	}
}

// Вход реплея не мутируется: указатели Player/Npc и байты transfer-записей
// разделяются с кадрами — Fold пишет Beat/бакеты/движение.
func TestReplayInputDeepImmutable(t *testing.T) {
	hdr, frames := scenarioFrames(false)
	frames[0].Step.Births[0].Ent.Transfers = []TransferRecord{
		{ID: 1, Phase: 1, Payload: []byte{9, 9}, Precondition: []byte{7}},
	}
	snapshot := make([]LogFrame, len(frames))
	for i := range frames {
		snapshot[i] = frames[i]
		if frames[i].Step != nil {
			st := *frames[i].Step
			snapshot[i].Step = &st
		}
	}
	deepCopyFrames(snapshot)
	if _, err := Replay(hdr, frames, nil, emptyGeo); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if !reflect.DeepEqual(frames, snapshot) {
		t.Fatal("Replay мутировал входные кадры")
	}
}

// deepCopyFrames — глубокая копия для снимка (Births/Portions/Advisory и
// указатели сущностей), чтобы DeepEqual ловил мутации, а не разделяемость.
func deepCopyFrames(dst []LogFrame) {
	for i := range dst {
		if dst[i].Step == nil {
			continue
		}
		st := dst[i].Step
		st.Births = append([]BirthRecord(nil), st.Births...)
		for bi := range st.Births {
			ent := st.Births[bi].Ent
			if ent.Player != nil {
				p := *ent.Player
				ent.Player = &p
			}
			if ent.Transfers != nil {
				tr := append([]TransferRecord(nil), ent.Transfers...)
				for ti := range tr {
					tr[ti].Payload = append([]byte(nil), tr[ti].Payload...)
					tr[ti].Precondition = append([]byte(nil), tr[ti].Precondition...)
				}
				ent.Transfers = tr
			}
		}
		st.Retires = append([]Retire(nil), st.Retires...)
		st.Portions = append([]PortionRecord(nil), st.Portions...)
		for pi := range st.Portions {
			st.Portions[pi].Envs = append([]transport.Envelope(nil), st.Portions[pi].Envs...)
			for ei := range st.Portions[pi].Envs {
				st.Portions[pi].Envs[ei].Payload = append([]byte(nil), st.Portions[pi].Envs[ei].Payload...)
			}
		}
		st.Advisory = append([]AdvisoryIn(nil), st.Advisory...)
	}
}

// ——— FinalDump региона ———

// До запуска Run дамп не вычислен (контракт doc: nil).
func TestRegionFinalDumpNilBeforeRun(t *testing.T) {
	h := buildEnterHarness(t, DefaultConfig(), nil)
	if d := h.r.FinalDump(); d != nil {
		t.Fatalf("FinalDump до Run = %d байт; want nil", len(d))
	}
}

// После остановки дамп стабилен, равен свёртке на момент выхода и несёт
// рождённые сущности целиком (Owner региона — F1).
func TestRegionFinalDumpStableAfterStop(t *testing.T) {
	h := newEnterHarness(t, DefaultConfig())
	h.send(t, h.ctrlLetter(transport.KindEnterWorld,
		mustJSONEnter(7, mkRec("acc", "Vasya", 0))))
	waitForResidents(t, h.r, 1)

	h.rCancel()
	select {
	case <-h.rDone:
	case <-time.After(3 * time.Second):
		t.Fatal("регион не остановился")
	}
	d1, d2 := h.r.FinalDump(), h.r.FinalDump()
	if len(d1) == 0 {
		t.Fatal("FinalDump пуст после остановки")
	}
	if !bytes.Equal(d1, d2) {
		t.Fatal("FinalDump нестабилен между чтениями")
	}
	// сущности дампа — целиком (appendEntity), включая Owner владельца;
	// чтение внутренних структур легально: горутина региона завершилась
	var want []byte
	for _, e := range h.r.entsProj() {
		want = appendEntity(want, e)
	}
	if !bytes.Contains(d1, want) {
		t.Fatal("FinalDump не содержит сериализацию населения (Owner/поля)")
	}
}

// Ранний возврат Run (регион без Wire) тоже оставляет дамп — defer
// зарегистрирован первым.
func TestRegionFinalDumpCoversEarlyReturn(t *testing.T) {
	cfg := DefaultConfig()
	m, err := NewMetronome(cfg)
	if err != nil {
		t.Fatalf("NewMetronome: %v", err)
	}
	reg := transport.NewRegistry(0)
	log, err := NewPortionLog(t.TempDir(), 1, false, 1<<20)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
	}
	t.Cleanup(func() {
		if err := log.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	r, err := NewRegion(m, reg, 1, cfg, log, nullPusher{}, emptyGeo)
	if err != nil {
		t.Fatalf("NewRegion: %v", err)
	}
	// без Wire: rules невалидны — Run выходит сразу
	r.Run(t.Context())
	if d := r.FinalDump(); d == nil {
		t.Fatal("FinalDump после раннего возврата Run = nil; want дамп пустого состояния")
	}
}

// Wire доходит до заголовка лога: полный реплей-контракт сессии.
func TestRegionWireFeedsSetRulesToLog(t *testing.T) {
	h := newEnterHarness(t, DefaultConfig())
	h.send(t, h.ctrlLetter(transport.KindEnterWorld,
		mustJSONEnter(7, mkRec("acc", "Vasya", 0)))) // хотя бы один шаг — заголовок ленивый

	if err := h.r.log.w.Flush(); err != nil { // тест читает при живом писателе
		t.Fatalf("flush: %v", err)
	}
	hdr, _, err := ReadPortionFrames(h.r.log.dir, h.r.log.region)
	if err != nil {
		t.Fatalf("ReadPortionFrames: %v", err)
	}
	cfg := h.r.cfg
	want := Rules{GraceTicks: cfg.GraceTicks, SaveRetryTicks: cfg.SaveRetryTicks,
		PeriodNS: int64(cfg.Period()), Persist: h.pID, Gateway: h.gwID, From: h.r.CtrlID()}
	if hdr.Region != h.r.id || hdr.GraceTicks != uint64(want.GraceTicks) ||
		hdr.SaveRetryTicks != uint64(want.SaveRetryTicks) || hdr.PeriodNS != uint64(want.PeriodNS) ||
		hdr.Persist != want.Persist || hdr.Gateway != want.Gateway || hdr.CtrlFrom != want.From || hdr.Session == 0 {
		t.Fatalf("заголовок %+v; want контракт Wire %+v", hdr, want)
	}
}

// ——— шов времени как источник тика ———

// Шов тика (второй источник критерия «оба»): регион под живым Run без
// Metronome.Run — тики инъектируются строго возрастающими Store, будильник —
// контрольное письмо; между инъекциями шаг подтверждается doneTick. Итог:
// FinalDump == Replay(лог) бит-в-бит.
func TestReplaySeamTicks(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.GraceTicks = 4
	cfg.SaveRetryTicks = 2
	cfg.Hz = 50
	m, err := NewMetronome(cfg)
	if err != nil {
		t.Fatalf("NewMetronome: %v", err)
	}
	reg := transport.NewRegistry(64)
	log, err := NewPortionLog(dir, 1, true, 1<<20)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
	}
	r, err := NewRegion(m, reg, 1, cfg, log, nullPusher{}, emptyGeo)
	if err != nil {
		t.Fatalf("NewRegion: %v", err)
	}
	var gwBox, pBox transport.Mailbox
	gwID := reg.Register(&gwBox)
	if err := gwBox.Claim(uint64(gwID)); err != nil {
		t.Fatal(err)
	}
	pID := reg.Register(&pBox)
	if err := pBox.Claim(uint64(pID)); err != nil {
		t.Fatal(err)
	}
	if err := r.Wire(gwID, pID); err != nil {
		t.Fatalf("Wire: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()

	// wakeLetter — безвредное контрольное письмо (неизвестный свёртке тип:
	// только счёт Letters/KindCounts) — будит шаг на инъекционном тике.
	wake := transport.Envelope{To: transport.Addr{Entity: r.CtrlID()}, FromID: gwID,
		Kind: transport.KindXP}
	waitTickExact := func(n Tick) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if r.Stats().DoneTick >= n {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		t.Fatalf("шаг на тике %d не наступил (doneTick=%d)", n, r.Stats().DoneTick)
	}
	inject := func(n Tick, letters ...transport.Envelope) {
		t.Helper()
		m.tick.Store(uint64(n)) // строго возрастает; будит шаг письмо ниже
		for _, e := range letters {
			reg.Send(e)
		}
		waitTickExact(n)
	}

	inject(5, transport.Envelope{To: transport.Addr{Entity: r.CtrlID()}, FromID: gwID,
		Kind: transport.KindEnterWorld, Payload: mustJSONEnter(1, mkRec("aaa", "Hero", 0))})
	// ID рождённого — из бинд-письма шлюзу (почтовый протокол, без чтения
	// внутренних структур живого региона)
	batch := gwBox.ExtractInto(uint64(gwID), nil)
	gwBox.AckNotify()
	var heroID transport.EntityID
	for _, env := range batch {
		if env.Kind != transport.KindConnBind {
			continue
		}
		m, err := transport.DecodeLetter[transport.ConnBindMsg](env.Payload)
		if err != nil || m.Conn != 1 {
			t.Fatalf("бинд: %+v err=%v", m, err)
		}
		heroID = m.Entity
	}
	if heroID == 0 {
		t.Fatal("бинд-письмо не найдено — ID рождённого недоступен")
	}

	say := sayLetter(heroID, "seam", protocol.ChatGeneral)
	inject(7, say, wake)
	mv := replayMoveLetter(heroID)
	inject(9, mv, wake)
	inject(11, transport.Envelope{To: transport.Addr{Entity: r.CtrlID()}, FromID: gwID,
		Kind:    transport.KindLinkDead,
		Payload: mustEncodeConnRef(1)}) // Hero в grace (дедлайн 11+4=15)
	inject(16, wake) // экспирация: сохранение + уход
	inject(18, wake) // пустой шаг хвостом (ретраи сохранения в SaveQ)
	inject(20, wake)

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("регион не остановился")
	}
	live := r.FinalDump()
	if len(live) == 0 {
		t.Fatal("FinalDump пуст")
	}
	hdr, frames, err := ReadPortionFrames(dir, 1)
	if err != nil {
		t.Fatalf("ReadPortionFrames: %v", err)
	}
	res, err := Replay(hdr, frames, nil, emptyGeo)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if !bytes.Equal(live, res.Dump) {
		t.Fatalf("реплей шва времени ≠ живой дамп: len %d vs %d", len(res.Dump), len(live))
	}
}

// mustEncodeConnRef — тело KindLinkDead для ручных сценариев.
func mustEncodeConnRef(conn uint64) []byte {
	body, err := transport.EncodeLetter(transport.ConnRefMsg{Conn: conn})
	if err != nil {
		panic("тест: кодирование ConnRefMsg: " + err.Error())
	}
	return body
}

// ——— бенчмарк ———

// benchScenario — вход → смесь (чат/движение/пустые) → Logout; N шагов.
func benchScenario(steps int) (FileHeader, []LogFrame) {
	rules := testRules()
	hdr := FileHeader{Version: 6, Payloads: true, Region: 7, Session: 7,
		PeriodNS: uint64(rules.PeriodNS), GraceTicks: uint64(rules.GraceTicks),
		SaveRetryTicks: uint64(rules.SaveRetryTicks),
		Persist:        rules.Persist, Gateway: rules.Gateway, CtrlFrom: rules.From}
	rec := mkRecAt("aaa", "Hero", 100, 200)
	frames := make([]LogFrame, 0, steps+2)
	st := func(s StepRecord) {
		frames = append(frames, LogFrame{Step: &s})
	}
	st(StepRecord{Tick: 1, Delta: 1,
		Portions: []PortionRecord{{Box: rules.From, Envs: []transport.Envelope{
			replayEnterLetter(1, "aaa", "Hero", 100, 200)}}},
		Births: []BirthRecord{{ID: 101, Ent: replayBirthEnt(101, 1, rec)}}})
	for i := 2; i < steps+1; i++ {
		switch i % 10 {
		case 0:
			st(StepRecord{Tick: Tick(i), Delta: 1,
				Portions: []PortionRecord{{Box: 101, Envs: []transport.Envelope{
					sayLetter(101, "bench", protocol.ChatGeneral)}}}})
		case 5:
			st(StepRecord{Tick: Tick(i), Delta: 1,
				Portions: []PortionRecord{{Box: 101, Envs: []transport.Envelope{
					replayMoveLetter(101)}}}})
		default:
			st(StepRecord{Tick: Tick(i), Delta: 1})
		}
	}
	st(StepRecord{Tick: Tick(steps + 1), Delta: 1,
		Portions: []PortionRecord{{Box: 101, Envs: []transport.Envelope{
			replayLogoutLetter(101)}}},
		Retires: []Retire{{ID: 101}}})
	return hdr, frames
}

// BenchmarkReplay — пропускная способность реплея на смеси вход/чат/движение
// (T2: состав закреплён); живость — машинным ассертом вне цикла (шаги и
// письма посчитаны), machine-пыль крафта — вне измеряемого пути.
func BenchmarkReplay(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run(stepsName(n), func(b *testing.B) {
			hdr, frames := benchScenario(n)
			res, err := Replay(hdr, frames, nil, emptyGeo)
			if err != nil {
				b.Fatalf("Replay: %v", err)
			}
			if res.Steps != uint64(n+1) || res.Letters == 0 || len(res.Dump) == 0 {
				b.Fatalf("мёртвый бенч: steps=%d (want %d) letters=%d dump=%d",
					res.Steps, n+1, res.Letters, len(res.Dump))
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := Replay(hdr, frames, nil, emptyGeo); err != nil {
					b.Fatalf("Replay: %v", err)
				}
			}
		})
	}
}

// stepsName — имя под-бенчмарка по числу шагов.
func stepsName(n int) string {
	switch n {
	case 100:
		return "steps-100"
	case 1000:
		return "steps-1000"
	default:
		return "steps-" + itoa(n)
	}
}

// itoa — десятичная запись без fmt в горячем пути бенчмарка.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
