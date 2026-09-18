package world

// Разворачивание NPC-населения на регионе и в логе порций (группы E/F
// тест-плана): DeployNPCs до Run → применение первым шагом → известность
// NpcInfo в enter-радиусе, NPC не наблюдатели, повторный вход — тот же
// набор по id, паник-окна письма, roundtrip/реплей разворота из лога.

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/replica"
	"github.com/udisondev/l2go/internal/transport"
)

// miniStatic-срез (центр (1000,1000), радиус 10000) разворачивает 6 NPC:
// орк (1000,2000), стак ×2 в (500,500), near_terr ×3; skip: fake/RaidBoss/
// SiegeGuard/целиком-banned; far_terr и торговец — вне среза.
const wantNPCResidents = 6

func newNPCHarness(t *testing.T, radius int32) *enterHarness {
	t.Helper()
	h := buildEnterHarness(t, DefaultConfig(), nil)
	if err := h.r.DeployNPCs(miniStatic(t), transport.NPCDeployMsg{
		CenterX: 1000, CenterY: 1000, Radius: radius}); err != nil {
		t.Fatalf("DeployNPCs: %v", err)
	}
	h.startRun(t)
	return h
}

// E1: guards DeployNPCs — до Run ок; повторно/без статики/нулевый радиус —
// ошибки; ровно одно письмо сам себе (слепой push через транспорт).
func TestRegionDeployNPCsGuards(t *testing.T) {
	_, r := newTestRegion(t, DefaultConfig())
	static := miniStatic(t)
	if err := r.DeployNPCs(static, transport.NPCDeployMsg{CenterX: 1, CenterY: 1, Radius: 10}); err != nil {
		t.Fatalf("DeployNPCs до Run: %v", err)
	}
	if err := r.DeployNPCs(static, transport.NPCDeployMsg{Radius: 10}); err == nil {
		t.Errorf("повторный DeployNPCs прошёл; want ошибка")
	}
	if err := r.DeployNPCs(nil, transport.NPCDeployMsg{Radius: 10}); err == nil {
		t.Errorf("DeployNPCs без статики прошёл; want ошибка")
	}
	if err := r.DeployNPCs(static, transport.NPCDeployMsg{Radius: 0}); err == nil {
		t.Errorf("DeployNPCs с нулевым радиусом прошёл; want ошибка")
	}
	batch := r.ctrl.ExtractInto(r.ctrlToken, nil)
	if len(batch) != 1 {
		t.Fatalf("писем в ctrl-ящике %d; want 1", len(batch))
	}
	env := batch[0]
	if env.Kind != transport.KindDeployNPCs || env.FromID != r.ctrlID ||
		env.To.Entity != r.ctrlID {
		t.Errorf("письмо разворота: %+v; want сам себе, KindDeployNPCs", env)
	}
}

// E2: применение — первый шаг разворачивает население; далее не растёт;
// население в дампе отличимо и сортировано (машина по дампу).
func TestRegionDeployNPCsBirthsApplied(t *testing.T) {
	h := newNPCHarness(t, 10000)
	waitForResidents(t, h.r, wantNPCResidents)
	for i := 0; i < 3; i++ {
		waitTick(t, h.r)
	}
	if got := h.r.Stats().Residents; got != wantNPCResidents {
		t.Fatalf("население растёт после разворота: %d", got)
	}
	ents := h.r.entsProj()
	perTemplate := make(map[int32]int)
	ids := make([]int, 0, len(ents))
	for _, e := range ents {
		ids = append(ids, int(e.ID))
		if e.Npc != nil {
			perTemplate[e.Npc.TemplateID]++
		}
	}
	if got := perTemplate[20551]; got != 3 {
		t.Errorf("NPC 20551 = %d; want 3 (count near_terr)", got)
	}
	if got := perTemplate[20550]; got != 3 {
		t.Errorf("NPC 20550 = %d; want 3 (точка + стак×2)", got)
	}
	if !sort.IntsAreSorted(ids) {
		t.Errorf("население не отсортировано по ID (D3)")
	}
}

// E3: dead-letter разворота (radius 0 в письме при валидном отправителе):
// население нулевое, регион жив (не заморожен).
func TestRegionDeployDeadLetterCounters(t *testing.T) {
	h := buildEnterHarness(t, DefaultConfig(), nil)
	body, err := transport.EncodeLetter(transport.NPCDeployMsg{Radius: 0})
	if err != nil {
		t.Fatal(err)
	}
	h.reg.Send(transport.Envelope{
		To: transport.Addr{Entity: h.r.CtrlID()}, FromID: h.r.CtrlID(),
		Kind: transport.KindDeployNPCs, Payload: body})
	h.startRun(t)
	// Первый шаг может случиться при тике 0 (doneTick 0→0 невидим waitTick):
	// ждём исполнения шага по фазовым счётчикам.
	waitCond(t, h.r, func(s RegionStats) bool { return s.PhaseAck >= 1 })
	waitForResidents(t, h.r, 0)
	if h.r.Stats().Frozen {
		t.Fatalf("регион заморожен dead-letter'ом разворота")
	}
}

// npcInfoKnown — множество objID NPC из пушей коллектора.
func npcInfoKnown(c *pushCollector) map[uint64]bool {
	out := make(map[uint64]bool)
	for _, p := range c.Snapshot() {
		if len(p.Frame) == 0 || p.Frame[0] != protocol.OpNpcInfo || len(p.Frame) < 5 {
			continue
		}
		out[uint64(uint32(p.Frame[1])|uint32(p.Frame[2])<<8|
			uint32(p.Frame[3])<<16|uint32(p.Frame[4])<<24)] = true
	}
	return out
}

// waitNPCFrames — поллинг набора NpcInfo в коллекторе до want кадров
// (бюджет): шаг с рождением игрока гонит кадры join той же фазой AoI,
// но поллинг Residents опережает phaseB — оракул сами кадры, не население.
func waitNPCFrames(t *testing.T, h *enterHarness, want int) map[uint64]bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		known := npcInfoKnown(h.pushes)
		if len(known) >= want {
			return known
		}
		if time.Now().After(deadline) {
			t.Fatalf("NpcInfo-кадры не пришли: %d; want %d", len(known), want)
		}
		time.Sleep(3 * time.Millisecond)
	}
}

// E4: вход игрока в толпе — NpcInfo только NPC в enter-радиусе (3500);
// стационарные шаги без новых вводов (чурн нулевой).
func TestRegionNPCIntroduceRadiusSet(t *testing.T) {
	h := newNPCHarness(t, 10000)
	waitForResidents(t, h.r, wantNPCResidents)
	h.enterConn(t, 31, mkRecAt("npcacc", "NpcGuy", 1000, 1000))
	known := waitNPCFrames(t, h, wantNPCResidents)
	if len(known) != wantNPCResidents {
		t.Fatalf("введено NPC %d; want %d (все развёрнутые в enter-радиусе)", len(known), wantNPCResidents)
	}
	for _, p := range h.pushes.Snapshot() {
		if len(p.Frame) == 0 || p.Frame[0] != protocol.OpNpcInfo {
			continue
		}
		x := int32(uint32(p.Frame[13]) | uint32(p.Frame[14])<<8 | uint32(p.Frame[15])<<16 | uint32(p.Frame[16])<<24)
		y := int32(uint32(p.Frame[17]) | uint32(p.Frame[18])<<8 | uint32(p.Frame[19])<<16 | uint32(p.Frame[20])<<24)
		dx, dy := int64(x)-1000, int64(y)-1000
		if dx*dx+dy*dy > 3500*3500 {
			t.Errorf("NpcInfo вне enter-радиуса: (%d,%d)", x, y)
		}
	}
	h.pushes.Reset()
	for i := 0; i < 3; i++ {
		waitTick(t, h.r)
	}
	if got := len(npcInfoKnown(h.pushes)); got != 0 {
		t.Errorf("стационарные шаги ввели %d NPC повторно (чурн)", got)
	}
}

// E5: NPC — не наблюдатели: кадры уходят только на коннекты игроков.
func TestRegionNPCNotObserver(t *testing.T) {
	h := newNPCHarness(t, 10000)
	waitForResidents(t, h.r, wantNPCResidents)
	h.enterConn(t, 32, mkRecAt("npcacc2", "NpcGuy2", 1000, 1000))
	for _, p := range h.pushes.Snapshot() {
		if p.Client != 32 {
			t.Fatalf("кадр ушёл клиенту %d; want только игроку 32", p.Client)
		}
	}
}

// E6: повторный вход — набор NpcInfo идентичен по id (структурная
// идентичность: NPC не умирают, ID монотонны, сид не участвует).
func TestRegionNPCReenterSameSetById(t *testing.T) {
	h := newNPCHarness(t, 10000)
	waitForResidents(t, h.r, wantNPCResidents)
	h.enterConn(t, 33, mkRecAt("reacc", "ReGuy", 1000, 1000))
	first := waitNPCFrames(t, h, wantNPCResidents)
	// Logout из ящика игрока — полный выход.
	ent := transport.Addr{Entity: transport.EntityID(playerEntity(h, 33))}
	h.reg.Send(transport.Envelope{To: ent, FromID: h.gwID,
		Kind: transport.KindClientFrame, Payload: []byte{protocol.OpLogout}})
	waitCond(t, h.r, func(RegionStats) bool { return h.r.Stats().Residents == wantNPCResidents })
	h.pushes.Reset()
	h.enterConn(t, 34, mkRecAt("reacc", "ReGuy", 1000, 1000))
	second := waitNPCFrames(t, h, wantNPCResidents)
	if len(second) != len(first) {
		t.Fatalf("наборы разного размера: %d vs %d", len(second), len(first))
	}
	for id := range first {
		if !second[id] {
			t.Errorf("NPC %d пропал из набора повторного входа", id)
		}
	}
}

// E7: паник-окна письма разворота: phaseDrain — письмо не изъято, разворот
// следующим шагом; phaseFold — письмо потеряно классово (однократность:
// население нулевое навсегда, slog-алерт — наблюдаемость).
func TestRegionDeployPanicWindows(t *testing.T) {
	t.Run("phaseDrain — разворот следующим шагом", func(t *testing.T) {
		h := buildEnterHarness(t, DefaultConfig(), nil)
		if err := h.r.DeployNPCs(miniStatic(t), transport.NPCDeployMsg{
			CenterX: 1000, CenterY: 1000, Radius: 100}); err != nil {
			t.Fatal(err)
		}
		h.startRun(t)
		h.r.forcePanic.Store(uint32(phaseDrain))
		waitCond(t, h.r, func(s RegionStats) bool { return s.Failed >= 1 })
		h.r.forcePanic.Store(0)
		// радиус 100: только near_terr (∩ квадрата [900..1100]).
		waitForResidents(t, h.r, 3)
	})
	t.Run("phaseFold — письмо потеряно, население нулевое", func(t *testing.T) {
		h := buildEnterHarness(t, DefaultConfig(), nil)
		if err := h.r.DeployNPCs(miniStatic(t), transport.NPCDeployMsg{
			CenterX: 1000, CenterY: 1000, Radius: 10000}); err != nil {
			t.Fatal(err)
		}
		h.startRun(t)
		h.r.forcePanic.Store(uint32(phaseFold))
		waitCond(t, h.r, func(s RegionStats) bool { return s.Failed >= 1 })
		h.r.forcePanic.Store(0)
		waitForResidents(t, h.r, 0)
	})
}

// E8: фазовые счётчики шага растут на NPC-населении.
func TestRegionNPCStepPhaseCounters(t *testing.T) {
	h := newNPCHarness(t, 10000)
	waitForResidents(t, h.r, wantNPCResidents)
	before := h.r.Stats()
	for i := 0; i < 3; i++ {
		waitTick(t, h.r)
	}
	after := h.r.Stats()
	if after.PhaseFold <= before.PhaseFold || after.PhaseAoI <= before.PhaseAoI || after.PhaseB <= before.PhaseB {
		t.Fatalf("фазовые счётчики не растут: %+v → %+v", before, after)
	}
}

// E10: остановка при NPC-населении — выход Run чист (сохранитель
// игнорирует NPC: Player==nil).
func TestRegionShutdownWithNPCPopulation(t *testing.T) {
	h := newNPCHarness(t, 10000)
	waitForResidents(t, h.r, wantNPCResidents)
	h.rCancel()
	waitCond(t, h.r, func(RegionStats) bool { return h.regionDone() })
}

// F1: roundtrip разворота в логе порций (v4): Births несут полные NPC-скины
// бит-в-бит; версия строго 4.
func TestPortionLogNPCBirthRoundtrip(t *testing.T) {
	static := miniStatic(t)
	var res StepResult
	deploySpawns(7, 500, &State{}, static, transport.NPCDeployMsg{
		CenterX: 1000, CenterY: 1000, Radius: 10000}, emptyGeo, &res)
	if len(res.Births) != wantNPCResidents {
		t.Fatalf("Births = %d; want %d", len(res.Births), wantNPCResidents)
	}
	applied := make([]AppliedBirth, len(res.Births))
	for i := range res.Births {
		e := res.Births[i].Ent
		applied[i] = AppliedBirth{ID: transport.EntityID(100 + i), Ent: &e}
	}
	l := newTestLog(t, true, 1<<20)
	if err := l.LogStep(StepInput{Tick: 500, Delta: 0, Births: applied}); err != nil {
		t.Fatalf("LogStep: %v", err)
	}
	_, steps, _, err := readAll(t, l)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(steps) != 1 || len(steps[0].Births) != len(applied) {
		t.Fatalf("запись разворота: steps=%d births=%d", len(steps), len(steps[0].Births))
	}
	for i, got := range steps[0].Births {
		want := applied[i]
		if got.ID != want.ID || got.Ent.Pos != want.Ent.Pos || got.Ent.Heading != want.Ent.Heading {
			t.Errorf("birth[%d] = (%d, %+v); want (%d, %+v)", i, got.ID, got.Ent, want.ID, want.Ent)
		}
		if got.Ent.Npc == nil || *got.Ent.Npc != *want.Ent.Npc {
			t.Errorf("birth[%d].Npc = %+v; want %+v", i, got.Ent.Npc, want.Ent.Npc)
		}
	}
}

// F2: реплей разворота из лога — письма шага разворота через ту же свёртку
// дают идентичные Births (payload=true обязателен).
func TestPortionLogReplayDeployDigest(t *testing.T) {
	static := miniStatic(t)
	msg := transport.NPCDeployMsg{CenterX: 1000, CenterY: 1000, Radius: 10000}
	body, err := transport.EncodeLetter(msg)
	if err != nil {
		t.Fatal(err)
	}
	deployEnv := transport.Envelope{
		To: transport.Addr{Entity: 7}, FromID: 7, Kind: transport.KindDeployNPCs, Payload: body}
	l := newTestLog(t, true, 1<<20)
	if err := l.LogStep(StepInput{Tick: 500, Delta: 0,
		Portions: []PortionRecord{{Box: 7, Mark: 1, Envs: []transport.Envelope{deployEnv}}}}); err != nil {
		t.Fatalf("LogStep: %v", err)
	}
	_, steps, _, err := readAll(t, l)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	portions := make([]Portion, len(steps[0].Portions))
	for i, p := range steps[0].Portions {
		portions[i] = Portion{Region: 7, Tick: steps[0].Tick, Envs: p.Envs}
	}
	// Живой прогон: тот же шаг свёртки.
	live := State{}
	liveRes := Fold(500, 0, stepRNG(7, 500), &live, nil, portions, nil,
		Env{Region: 7, Rules: func() Rules { r := testRules(); r.From = 7; return r }(), GM: emptyGeo, Static: static})
	// Реплей ×2: бит-в-бит.
	replay := func() []Birth {
		st := State{}
		res := Fold(500, 0, stepRNG(7, 500), &st, nil, portions, nil,
			Env{Region: 7, Rules: func() Rules { r := testRules(); r.From = 7; return r }(), GM: emptyGeo, Static: static})
		return res.Births
	}
	r1, r2 := replay(), replay()
	if len(liveRes.Births) != wantNPCResidents {
		t.Fatalf("живой разворот = %d; want %d", len(liveRes.Births), wantNPCResidents)
	}
	if !equalBirths(r1, liveRes.Births) || !equalBirths(r2, liveRes.Births) {
		t.Fatalf("реплей разошёлся с живым разворотом")
	}
}

// D3: гигантские Name/Title в NpcInfo — размер кадра растёт, не паникует,
// View навигирует (корнер макс).
func TestComposeJoinNpcInfoHugeNameTitle(t *testing.T) {
	r := &Region{}
	huge := strings.Repeat("Щ", 4096)
	pushes := r.composeJoin([]replica.Event{{Obs: replica.Observer{ConnID: 1},
		Target: replica.Record{Entity: 7, Kind: replica.RecordKindNPC,
			Name: huge, Title: huge}, Kind: replica.EventIntroduce}})
	if len(pushes) != 1 {
		t.Fatalf("кадров = %d; want 1", len(pushes))
	}
	view, ok := protocol.NewNpcInfoView(pushes[0].Frame)
	if !ok {
		t.Fatalf("NpcInfo-кадр не навигируется")
	}
	if got, okV := view.Name(); !okV || got != huge {
		t.Errorf("Name roundtrip отказ")
	}
}
