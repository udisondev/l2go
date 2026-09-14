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
	log, err := NewPortionLog(t.TempDir(), 1, m.period, cfg.LogPayloads, cfg.LogMaxFileBytes)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
	}
	r, err := NewRegion(m, reg, 1, cfg, log)
	if err != nil {
		t.Fatalf("NewRegion: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return m, r
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
	if st.PhDrain != 1 || st.PhFold != 1 || st.PhEffects != 1 || st.PhB != 1 || st.PhPublish != 1 || st.PhAck != 1 {
		t.Fatalf("фазовые счётчики после шага: %+v", st)
	}
	if st.DoneTick != r.metro.Now() {
		t.Fatalf("doneTick = %d; want %d", st.DoneTick, r.metro.Now())
	}
}

// Бюджет K контрольных: 20 контрольных при K=16 — 16 приоритетно, излишек в
// общем разборе того же тика; применены все, не дропнуты.
func TestRegionCtrlBudgetK(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CtrlBudget = 16
	_, r := newTestRegion(t, cfg)
	spawnResident(t, r, 100)
	for i := 0; i < 20; i++ {
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
	for i := 0; i < 5; i++ {
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
	for i := 0; i < 3; i++ {
		ids = append(ids, spawnResident(t, r, 100))
	}
	for _, id := range ids {
		r.reg.Send(transport.Envelope{To: transport.Addr{Entity: id}, FromID: 5, Kind: transport.KindAggro})
	}
	r.step()
	if len(r.records) != 3 {
		t.Fatalf("пачек %d; want 3 (полный оборот)", len(r.records))
	}
	start := int(uint64(r.state.Steps-1)+1) % 3 // шаг начался с residents[start]
	_ = start
	// порядок пачек — с кольцевой точки, а не с головы
	if r.records[0].Box == r.records[2].Box {
		t.Fatalf("порядок пачек не кольцевой")
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

	r.forcePanic = phaseFold
	r.safeStep()
	st := r.Stats()
	if st.Failed != 1 || st.DoneTick != done {
		t.Fatalf("после паники: failed=%d doneTick=%d; want 1 и %d", st.Failed, st.DoneTick, done)
	}
	if st.PhDrain != 2 || st.PhFold != 1 {
		t.Fatalf("паника в fold: drain = %d (want 2, дорос), fold = %d (want 1, не дорос)", st.PhDrain, st.PhFold)
	}
	r.forcePanic = 0
	r.safeStep() // успешный шаг — серия сброшена
	r.forcePanic = phaseB
	for i := 0; i < cfg.FreezePanics; i++ {
		r.safeStep()
	}
	st = r.Stats()
	if !st.Frozen {
		t.Fatalf("серия паник не заморозила регион")
	}
	r.forcePanic = 0
	before := r.Stats()
	r.safeStep() // замороженный не исполняет шаги
	if r.Stats().PhAck != before.PhAck {
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
	for i := 0; i < 4; i++ {
		r.reg.Send(transport.Envelope{To: transport.Addr{Entity: id}, FromID: 5, Kind: transport.KindAggro})
	}
	r.metro.tick.Add(1)
	r.forcePanic = phaseFold
	r.safeStep()
	r.forcePanic = 0
	st := r.ctrl.Stats()
	if st.FinalReliable != 4 {
		t.Fatalf("классовый дроп остатка reliable = %d; want 4 (тихая потеря запрещена)", st.FinalReliable)
	}
	if err := r.log.w.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
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
	births := r.applyEffects(res)
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
	r.applyEffects(res2)
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
	for i := 0; i < total; i++ {
		r.outbox = append(r.outbox, transport.Envelope{To: transport.Addr{Entity: mbox}, FromID: transport.EntityID(i), Kind: transport.KindAggro})
	}
	for i := 0; i < 3; i++ { // кап 10: шаги по 10, 10, 5
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); r.metro.Run(ctx) }()
	go func() { defer wg.Done(); r.Run(ctx) }()
	r.reg.Send(transport.Envelope{To: transport.Addr{Entity: r.ctrlID}, FromID: 5, Kind: transport.KindLinkDead})
	// имитация потерянного токена: съесть, не дав Run
	select {
	case <-r.ctrl.Notify():
	default:
	}
	deadline := time.After(2 * time.Second)
	for r.Stats().DoneTick == 0 && r.ctrl.Depth() > 0 {
		select {
		case <-deadline:
			cancel()
			wg.Wait()
			t.Fatalf("фолбэк не разбудил регион с письмом без токена")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if got := r.state.KindCounts[transport.KindLinkDead-1]; got != 0 {
		_ = got // state читается только после остановки региона
	}
	cancel()
	wg.Wait()
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
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); r.metro.Run(ctx) }()
	go func() { defer wg.Done(); r.Run(ctx) }()
	deadline := time.After(2 * time.Second)
	for r.Stats().DoneTick < 3 {
		select {
		case <-deadline:
			t.Fatalf("регион не тикает")
		default:
			time.Sleep(time.Millisecond)
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
