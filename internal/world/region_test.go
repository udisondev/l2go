package world

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/l2go/internal/transport"
)

// newTestRegion — регион с тестовым конфигом и логом во временном каталоге.
func newTestRegion(t *testing.T, cfg Config) (*Metronome, *Region) {
	t.Helper()
	cfg.LogMaxFileBytes = 1 << 20
	m, err := NewMetronome(cfg)
	if err != nil {
		t.Fatalf("NewMetronome: %v", err)
	}
	reg := transport.NewRegistry(0)
	log, err := NewPortionLog(t.TempDir(), 1, cfg.LogPayloads, cfg.LogMaxFileBytes)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
	}
	r, err := NewRegion(m, reg, 1, cfg, log, nullPusher{}, emptyGeo)
	if err != nil {
		t.Fatalf("NewRegion: %v", err)
	}
	if err := r.Wire(901, 900); err != nil {
		t.Fatalf("Wire: %v", err)
	}
	// Осознанный игнор ошибки: регион закрывает лог сам (Run на выходе), а
	// тесты поломки писателя рвут файл мимо Close — результат повторного
	// закрытия не диагностичен.
	t.Cleanup(func() { _ = log.Close() })
	return m, r
}

// Злые входы конструктора: nil-метроном и nil-реестр отклоняются guard'ом
// до регистрации контрольного ящика.
func TestRegionNilDepsRejected(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LogMaxFileBytes = 1 << 20
	m, err := NewMetronome(cfg)
	if err != nil {
		t.Fatalf("NewMetronome: %v", err)
	}
	reg := transport.NewRegistry(0)
	log, err := NewPortionLog(t.TempDir(), 1, cfg.LogPayloads, cfg.LogMaxFileBytes)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
	}
	t.Cleanup(func() {
		if err := log.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	cases := []struct {
		name  string
		metro *Metronome
		reg   *transport.Registry
	}{
		{name: "nil-метроном", metro: nil, reg: reg},
		{name: "nil-реестр", metro: m, reg: nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewRegion(c.metro, c.reg, 1, cfg, log, nullPusher{}, emptyGeo); err == nil {
				t.Errorf("NewRegion с %s прошёл; want ошибка валидации", c.name)
			}
		})
	}
}

func spawnResident(t *testing.T, r *Region, hp int32) transport.EntityID {
	t.Helper()
	id, err := r.Spawn(Entity{Owner: r.id, HP: hp})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	return id
}

// dt по счётчику тиков: дропнутый такт виден как LastDelta=2; монотонность
// номера шага (Now() на старте шага) — регресса нет.
func TestRegionDeltaFromTickCounter(t *testing.T) {
	_, r := newTestRegion(t, DefaultConfig())
	spawnResident(t, r, 100)
	m := r.metro
	m.tick.Store(10)
	r.step()
	if r.state.LastDelta != 0 {
		t.Fatalf("первый шаг после рождения: LastDelta = %d; want 0", r.state.LastDelta)
	}
	m.tick.Store(11)
	r.step()
	if r.state.LastDelta != 1 {
		t.Fatalf("LastDelta = %d; want 1", r.state.LastDelta)
	}
	// такт 12 дропнут (канал не читался): следующий шаг видит delta=2
	m.tick.Store(13)
	r.step()
	if r.state.LastDelta != 2 {
		t.Fatalf("после дропа такта LastDelta = %d; want 2", r.state.LastDelta)
	}
	// повторный шаг того же тика (немедленное пробуждение) — delta=0, не регресс
	r.step()
	if r.state.LastDelta != 0 {
		t.Fatalf("повторный шаг тика: LastDelta = %d; want 0", r.state.LastDelta)
	}
}

// Фазы шага: счётчики по разу на шаг; сброс серии паник успешным шагом.
func TestRegionPhaseCounters(t *testing.T) {
	_, r := newTestRegion(t, DefaultConfig())
	spawnResident(t, r, 100)
	r.step()
	st := r.Stats()
	if st.PhaseDrain != 1 || st.PhaseFold != 1 || st.PhaseEffects != 1 || st.PhaseAoI != 1 || st.PhaseB != 1 || st.PhasePublish != 1 || st.PhaseAck != 1 {
		t.Fatalf("фазовые счётчики после шага: %+v", st)
	}
	if st.DoneTick != r.metro.Now() {
		t.Fatalf("doneTick = %d; want %d", st.DoneTick, r.metro.Now())
	}
	// публикация — блоб в фазе publish (снапшот-заготовка P3.2 заменена)
	if got := r.pub.Committed(); got == nil {
		t.Fatalf("шаг не опубликовал блоб")
	}
}

// Бюджет K контрольных: 20 контрольных при K=16 — 16 приоритетно, излишек в
// общем разборе того же тика; применены все, не дропнуты.
func TestRegionCtrlBudgetK(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CtrlBudget = 16
	cfg.DrainBudget = 5 // контрольные вне бюджета дрена: применяются всегда
	_, r := newTestRegion(t, cfg)
	spawnResident(t, r, 100)
	for range 20 {
		r.reg.Send(transport.Envelope{To: transport.Addr{Entity: r.ctrlID}, FromID: 5, Kind: transport.KindEnterWorld})
	}
	r.step()
	if got := r.state.KindCounts[transport.KindEnterWorld-1]; got != 20 {
		t.Fatalf("применено контрольных %d; want 20 (излишек не дропается)", got)
	}
	// приоритетная порция ≤K и порция излишека — обе в этом шаге
	if len(r.records) < 2 {
		t.Fatalf("пачек контрольных %d; want ≥2 (приоритет ≤K + излишек)", len(r.records))
	}
	if n := len(r.records[0].Envs); n != 16 {
		t.Fatalf("приоритетная порция = %d; want 16 (K)", n)
	}
	if n := len(r.records[1].Envs); n != 4 {
		t.Fatalf("порция излишека = %d; want 4", n)
	}
	if r.ctrl.Depth() != 0 {
		t.Fatalf("контрольный ящик не опустошён: %d", r.ctrl.Depth())
	}
}

// drainBudget: остаток ≥1 ⇒ пачка дренируется целиком (перерасход), остаток
// 0 ⇒ стоп — не дренированные ящики остаются следующим шагам.
func TestRegionDrainBudget(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DrainBudget = 3
	_, r := newTestRegion(t, cfg)
	a := spawnResident(t, r, 100)
	b := spawnResident(t, r, 100)
	for range 5 {
		r.reg.Send(transport.Envelope{To: transport.Addr{Entity: a}, FromID: 5, Kind: transport.KindAggro})
		r.reg.Send(transport.Envelope{To: transport.Addr{Entity: b}, FromID: 5, Kind: transport.KindXP})
	}
	r.step() // start = tick mod len: первый по кольцу ящик — 5 писем (перерасход), второй не тронут
	if got := r.state.KindCounts[transport.KindAggro-1] + r.state.KindCounts[transport.KindXP-1]; got != 5 {
		t.Fatalf("применено писем %d; want 5 (первая пачка целиком, вторая — следующим шагом)", got)
	}
	r.step()
	if got := r.state.KindCounts[transport.KindAggro-1] + r.state.KindCounts[transport.KindXP-1]; got != 10 {
		t.Fatalf("после второго шага применено %d; want 10", got)
	}
}

// Кольцевой старт: окно дрена смещается функцией тика, полный оборот — ящик
// в хвосте сортировки дренируется тем же шагом при достаточном бюджете.
func TestRegionDrainRingStart(t *testing.T) {
	_, r := newTestRegion(t, DefaultConfig())
	var ids []transport.EntityID
	for range 3 {
		ids = append(ids, spawnResident(t, r, 100))
	}
	for _, id := range ids {
		r.reg.Send(transport.Envelope{To: transport.Addr{Entity: id}, FromID: 5, Kind: transport.KindAggro})
	}
	const n = 7 // 7 mod 3 = 1: обход с жителя[1], не с головы
	r.metro.tick.Store(n)
	r.step()
	if len(r.records) != 3 {
		t.Fatalf("пачек %d; want 3 (полный оборот)", len(r.records))
	}
	want := []transport.EntityID{ids[1], ids[2], ids[0]} // кольцо c tick mod len
	for i, rec := range r.records {
		if rec.Box != want[i] {
			t.Fatalf("пачка[%d] = ящик %d; want %d (кольцевой старт с tick mod len)", i, rec.Box, want[i])
		}
	}
}

// Сон и пробуждение: пустой регион деактивируется; пробуждение контрольным
// письмом — немедленный шаг с delta=0 (часы сна не зачисляются).
func TestRegionSleepWakeDeltaZero(t *testing.T) {
	_, r := newTestRegion(t, DefaultConfig())
	id := spawnResident(t, r, 100)
	r.step()
	r.Remove(id) // деактивация — в syncMembership Remove
	if r.activeFlag {
		t.Fatalf("пустой регион остался в активном сете")
	}
	r.metro.tick.Add(1000)
	r.reg.Send(transport.Envelope{To: transport.Addr{Entity: r.ctrlID}, FromID: 5, Kind: transport.KindLinkDead})
	// пробуждение — без тика метронома: имитируем notify-ветку Run
	select {
	case <-r.ctrl.Notify():
	default:
		t.Fatalf("пробуждающего токена нет")
	}
	r.safeStep()
	if r.state.LastDelta != 0 {
		t.Fatalf("после сна LastDelta = %d; want 0 (симуляционное время не тёкло)", r.state.LastDelta)
	}
	if st := r.Stats(); st.DoneTick == 0 || r.ctrl.Depth() != 0 {
		t.Fatalf("пробуждение не обработало письмо: %+v depth=%d", st, r.ctrl.Depth())
	}
}

// Паника → recover: тик провален (doneTick стоит), failed растёт, серия
// растёт; N повторных — заморозка (шаги не исполняются); успешный шаг
// обнуляет серию.
func TestRegionRecoverAndFreeze(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FreezePanics = 3
	_, r := newTestRegion(t, cfg)
	spawnResident(t, r, 100)
	r.metro.tick.Add(5)
	r.step()
	done := r.Stats().DoneTick

	r.forcePanic.Store(uint32(phaseFold))
	r.safeStep()
	st := r.Stats()
	if st.Failed != 1 || st.DoneTick != done {
		t.Fatalf("после паники: failed=%d doneTick=%d; want 1 и %d", st.Failed, st.DoneTick, done)
	}
	if st.PhaseDrain != 2 || st.PhaseFold != 1 {
		t.Fatalf("паника в fold: drain = %d (want 2, дорос), fold = %d (want 1, не дорос)", st.PhaseDrain, st.PhaseFold)
	}
	r.forcePanic.Store(0)
	r.safeStep() // успешный шаг — серия сброшена
	if st := r.Stats(); st.Failed != 1 {
		t.Fatalf("после успеха серия не сброшена: failed = %d", st.Failed)
	}
	// паника в фазе B: drain/fold доросли, B/publish/ack — нет (дельтами)
	beforeB := r.Stats()
	r.forcePanic.Store(uint32(phaseB))
	r.safeStep()
	st = r.Stats()
	if st.PhaseDrain != beforeB.PhaseDrain+1 || st.PhaseFold != beforeB.PhaseFold+1 {
		t.Fatalf("паника в B: drain/fold не доросли: %+v против %+v", st, beforeB)
	}
	if st.PhaseB != beforeB.PhaseB || st.PhasePublish != beforeB.PhasePublish || st.PhaseAck != beforeB.PhaseAck {
		t.Fatalf("паника в B: счётчики B/publish/ack доросли: %+v против %+v", st, beforeB)
	}
	r.safeStep() // 2-я в серии: сброс после успеха держит счётчик ниже порога
	if r.Stats().Frozen {
		t.Fatalf("заморозка раньше серии порога: сброс streak успехом не работает")
	}
	r.safeStep() // 3-я подряд → заморозка
	st = r.Stats()
	if !st.Frozen {
		t.Fatalf("серия паник не заморозила регион")
	}
	r.forcePanic.Store(0)
	before := r.Stats()
	r.safeStep() // замороженный не исполняет шаги
	if r.Stats().PhaseAck != before.PhaseAck {
		t.Fatalf("замороженный регион исполняет шаги")
	}
}

// Паника посреди применения: неприменённый остаток пачки классово дропнут
// (метрика), маркер сбойного шага в логе.
func TestRegionPanicDropsBatchAndMarks(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FreezePanics = 100
	_, r := newTestRegion(t, cfg)
	id := spawnResident(t, r, 100)
	for range 4 {
		r.reg.Send(transport.Envelope{To: transport.Addr{Entity: id}, FromID: 5, Kind: transport.KindAggro})
	}
	r.metro.tick.Add(1)
	r.forcePanic.Store(uint32(phaseFold))
	r.safeStep()
	r.forcePanic.Store(0)
	st := r.ctrl.Stats()
	if st.FinalReliable != 4 {
		t.Fatalf("классовый дроп остатка reliable = %d; want 4 (тихая потеря запрещена)", st.FinalReliable)
	}
	// регион шагается синхронно из теста — Flush из этой же горутины легален;
	// до сброса буфера файл пуст (заголовок ленивый), чтение — после Flush
	if err := r.log.w.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	_, steps, _, err := ReadPortionLogDir(r.log.dir, r.log.region)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(steps) != 0 {
		t.Fatalf("паник-шаг попал в лог порций: %d записей; want 0", len(steps))
	}
	_, _, panics, err := ReadPortionLogDir(r.log.dir, r.log.region)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(panics) != 1 || panics[0].Phase != phaseFold {
		t.Fatalf("маркер паники: %+v; want фаза fold", panics)
	}
}

// Эффекты населения: Births/Retires применяются актором после дрена — вставка
// и удаление отсортированного слайса, кэш проекции перестраивается, лог несёт
// присвоенные EntityID.
func TestRegionPopulationEffects(t *testing.T) {
	_, r := newTestRegion(t, DefaultConfig())
	res := StepResult{
		Births:  []Birth{{Ent: Entity{Owner: 1, HP: 55}}, {Ent: Entity{Owner: 1, HP: 66}}},
		Retires: []Retire{{ID: 999}},
	}
	r.metro.tick.Add(1)
	births := r.applyEffects(res, 1)
	r.step()
	if got := len(r.residents); got != 2 {
		t.Fatalf("жителей %d; want 2", got)
	}
	if r.residents[0].ent.ID >= r.residents[1].ent.ID {
		t.Fatalf("слайс не отсортирован по id")
	}
	if len(r.ents) != 2 || r.ents[0] != r.residents[0].ent {
		t.Fatalf("кэш проекции не перестроен")
	}
	if len(births) != 2 || births[0].ID != r.residents[0].ent.ID {
		t.Fatalf("AppliedBirth не несут присвоенных ID: %+v", births)
	}
	// retire живого
	res2 := StepResult{Retires: []Retire{{ID: r.residents[0].ent.ID}}}
	r.applyEffects(res2, 1)
	if len(r.residents) != 1 {
		t.Fatalf("retire не удалил жителя")
	}
}

// Outbox фазы B: кап на шаг, излишек переносится; порядок — сначала backlog,
// затем свежие (порядок отправителя).
func TestRegionPhaseBCap(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PhaseBCap = 10
	_, r := newTestRegion(t, cfg)
	var sent []uint64
	box := &transport.Mailbox{}
	mbox := r.reg.Register(box)
	if err := box.Claim(uint64(mbox)); err != nil {
		t.Fatalf("claim: %v", err)
	}
	const total = 25
	for i := range total {
		r.outbox = append(r.outbox, transport.Envelope{To: transport.Addr{Entity: mbox}, FromID: transport.EntityID(i), Kind: transport.KindAggro})
	}
	for range 3 { // кап 10: шаги по 10, 10, 5
		r.phaseB()
		batch := box.Extract(uint64(mbox))
		for _, env := range batch {
			sent = append(sent, uint64(env.FromID))
		}
	}
	if len(sent) != total {
		t.Fatalf("отправлено %d; want %d (кап 10: 10+10+5 за 3 шага)", len(sent), total)
	}
	for i := 1; i < len(sent); i++ {
		if sent[i] < sent[i-1] {
			t.Fatalf("порядок отправителя нарушен: %v", sent)
		}
	}
	if len(r.outbox) != 0 {
		t.Fatalf("backlog не исчерпан: %d", len(r.outbox))
	}
}

// Пробуждение через heartbeat-фолбэк: письмо в контрольном ящике без токена
// (остаточное окно F36) — фолбэк-звонок будит регион ≤ H тиков.
func TestRegionHeartbeatFallbackWakes(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Hz = 1000
	cfg.HeartbeatTicks = 2
	_, r := newTestRegion(t, cfg)
	r.Remove(spawnResident(t, r, 1)) // регион спит без жителей
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// Имитация потерянного токена (окно F36): письмо+съедание ДО старта Run —
	// регион гарантированно стартует без токена и обязан разбудиться фолбэком;
	// после старта Run токен может законно уйти региону — default не ошибка.
	r.reg.Send(transport.Envelope{To: transport.Addr{Entity: r.ctrlID}, FromID: 5, Kind: transport.KindLinkDead})
	select {
	case <-r.ctrl.Notify():
	default:
	}
	var wg sync.WaitGroup
	wg.Go(func() { r.metro.Run(ctx) })
	wg.Go(func() { r.Run(ctx) })
	deadline := time.After(2 * time.Second)
	for r.Stats().DoneTick == 0 && r.ctrl.Depth() > 0 {
		select {
		case <-deadline:
			cancel()
			wg.Wait()
			t.Fatalf("фолбэк не разбудил регион с письмом без токена")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	cancel()
	wg.Wait() // state читается только после остановки региона
	if got := r.state.KindCounts[transport.KindLinkDead-1]; got != 1 {
		t.Fatalf("письмо применено %d раз; want 1", got)
	}
}

// Выход по ctx: лог закрыт горутиной региона, хвост сброшен — файл читается.
func TestRegionCloseLogOnExit(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Hz = 1000
	_, r := newTestRegion(t, cfg)
	spawnResident(t, r, 100)
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	wg.Go(func() { r.metro.Run(ctx) })
	wg.Go(func() { r.Run(ctx) })
	deadline := time.After(2 * time.Second)
	for r.Stats().DoneTick < 3 {
		select {
		case <-deadline:
			t.Fatalf("регион не тикает")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	cancel()
	wg.Wait()
	_, steps, _, err := ReadPortionLogDir(r.log.dir, r.log.region)
	if err != nil {
		t.Fatalf("лог после выхода не читается (хвост не сброшен): %v", err)
	}
	if len(steps) < 3 {
		t.Fatalf("записей %d; want ≥3 (каждый шаг пишется)", len(steps))
	}
}

// Равное число рождений и удалений в шаге: кэш проекции перестраивается по
// поколению населения, не по длине (мажор S8: fold ходит по мёртвой проекции).
func TestRegionPopulationCompensatingStep(t *testing.T) {
	_, r := newTestRegion(t, DefaultConfig())
	a := spawnResident(t, r, 100)
	r.step()
	res := StepResult{
		Births:  []Birth{{Ent: Entity{Owner: 1, HP: 55}}},
		Retires: []Retire{{ID: a}},
	}
	r.applyEffects(res, 1)
	r.metro.tick.Add(1)
	r.step()
	if len(r.ents) != 1 || r.ents[0].ID != r.residents[0].ent.ID {
		t.Fatalf("проекция устарела: ents=%v residents[0]=%d", idsOf(r.ents), r.residents[0].ent.ID)
	}
	if r.ents[0].Beat != r.metro.Now() {
		t.Fatalf("новорождённый без heartbeat: Beat = %d; want %d", r.ents[0].Beat, r.metro.Now())
	}
}

func idsOf(ents []*Entity) []transport.EntityID {
	ids := make([]transport.EntityID, 0, len(ents))
	for _, e := range ents {
		ids = append(ids, e.ID)
	}
	return ids
}

// Письмо в окне перечита (между изъятием и AckNotify) + паника fold: волна
// перечита классово дропнута — тихая потеря reliable запрещена (F3).
func TestRegionPanicDropsRereadWave(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FreezePanics = 100
	_, r := newTestRegion(t, cfg)
	spawnResident(t, r, 100)
	// дрен вручную: изъятие, письмо в окне, AckNotify-перечит подхватывает
	r.ctrlBatch = r.ctrl.ExtractInto(r.ctrlToken, r.ctrlBatch[:0])
	r.reg.Send(transport.Envelope{To: transport.Addr{Entity: r.ctrlID}, FromID: 5, Kind: transport.KindEnterWorld})
	r.rereadBuf = r.ctrl.ExtractInto(r.ctrlToken, r.rereadBuf[:0])
	r.restBuf = append(r.restBuf[:0], r.rereadBuf...)
	r.stepTick = r.metro.Now()
	r.curPhase = phaseFold
	r.recovered("проба окна перечита")
	st := r.ctrl.Stats()
	if st.FinalReliable != 1 {
		t.Fatalf("волна перечита не классово дропнута: FinalReliable = %d; want 1", st.FinalReliable)
	}
}

// Ошибка записи лога (не EOF) — заморозка региона + лог-алерт: лог порций —
// обязательство D5, молчаливая дыра недопустима (F27).
func TestRegionFreezeOnLogWriteError(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FreezePanics = 100
	_, r := newTestRegion(t, cfg)
	spawnResident(t, r, 100)
	r.step()
	if err := r.log.file.Close(); err != nil { // писатель сломан: следующий кадр за буфером даст ошибку записи
		t.Fatalf("close: %v", err)
	}
	// Шаг с волной контрольных (2048 > бюджета дрена): порция записей
	// выталкивает хвост bufio-буфера в закрытый файл — ошибка записи.
	for range 2048 {
		r.reg.Send(transport.Envelope{To: transport.Addr{Entity: r.ctrlID}, FromID: 5, Kind: transport.KindEnterWorld})
	}
	r.metro.tick.Add(1)
	r.step()
	if !r.Stats().Frozen {
		t.Fatalf("ошибка записи лога не заморозила регион")
	}
}

// Дизъюнктный покров recover: 20 контрольных при K=16 → FinalReliable ровно 20
// (излишек не считается дважды), волна перечита не теряется.
func TestRegionPanicDropCoverDisjoint(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FreezePanics = 100
	_, r := newTestRegion(t, cfg)
	spawnResident(t, r, 100)
	for range 20 {
		r.reg.Send(transport.Envelope{To: transport.Addr{Entity: r.ctrlID}, FromID: 5, Kind: transport.KindEnterWorld})
	}
	r.metro.tick.Add(1)
	r.forcePanic.Store(uint32(phaseFold))
	r.safeStep()
	r.forcePanic.Store(0)
	if got := r.ctrl.Stats().FinalReliable; got != 20 {
		t.Fatalf("FinalReliable = %d; want 20 (излишек сверх K не считается дважды)", got)
	}
	if got := r.state.KindCounts[transport.KindEnterWorld-1]; got != 0 {
		t.Fatalf("письма паник-шага применены: %d; want 0", got)
	}
}
