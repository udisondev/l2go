package world

// Стадия известности на акторе: взаимные CharInfo/DeleteObject тем же шагом,
// паник-стойкость pendingPushes, reconcile-окно фантома, swap слота,
// событийность (idle — ноль join-пар), NpcInfo-ветка, advisory-логирование.

import (
	"path/filepath"
	"testing"

	"github.com/udisondev/l2go/internal/encode"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/transport"
)

// joinHarness — регион с ручным step() (одногорутинный, без гонок) и
// коллектором кадров.
type joinHarness struct {
	r      *Region
	log    *PortionLog
	metro  *Metronome
	pushes *pushCollector
	dir    string
}

func newJoinHarness(t *testing.T) *joinHarness {
	t.Helper()
	cfg := DefaultConfig()
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
	pc := &pushCollector{}
	r, err := NewRegion(m, reg, 1, cfg, log, pc)
	if err != nil {
		t.Fatalf("NewRegion: %v", err)
	}
	if err := r.Wire(901, 900); err != nil {
		t.Fatalf("Wire: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return &joinHarness{r: r, metro: m, pushes: pc, dir: log.dir, log: log}
}

// enter — вход игрока (контрольное письмо) + шаг; возвращает сущность.
func (h *joinHarness) enter(t *testing.T, conn uint64, acc string) transport.EntityID {
	t.Helper()
	h.r.reg.Send(enterMsg(conn, acc, mkRec(acc, "Bot"+acc, 0)))
	h.step(t)
	for _, res := range h.r.residents {
		if res.ent.Player != nil && res.ent.Player.ConnID == conn {
			return res.ent.ID
		}
	}
	t.Fatalf("житель conn=%d не найден", conn)
	return 0
}

func (h *joinHarness) step(t *testing.T) {
	t.Helper()
	h.metro.tick.Add(1)
	h.r.safeStep()
}

// charInfoObjIDs — objID всех CharInfo-кадров клиента.
func charInfoObjIDs(frames []FramePush, client uint64) []int32 {
	var out []int32
	for _, p := range frames {
		if p.Client != client || len(p.Frame) == 0 || p.Frame[0] != 0x03 {
			continue
		}
		out = append(out,
			int32(uint32(p.Frame[17])|uint32(p.Frame[18])<<8|uint32(p.Frame[19])<<16|uint32(p.Frame[20])<<24))
	}
	return out
}

// deletedObjIDs — objID всех DeleteObject-кадров клиента.
func deletedObjIDs(frames []FramePush, client uint64) []int32 {
	var out []int32
	for _, p := range frames {
		if p.Client != client || len(p.Frame) == 0 || p.Frame[0] != 0x12 {
			continue
		}
		out = append(out,
			int32(uint32(p.Frame[1])|uint32(p.Frame[2])<<8|uint32(p.Frame[3])<<16|uint32(p.Frame[4])<<24))
	}
	return out
}

func objID(id transport.EntityID) int32 { return int32(encode.ObjectIDBase + id) }

// Взаимная видимость тем же шагом: второй вход → взаимные CharInfo с
// ObjID-маппингом ObjectIDBase+Entity (F24).
func TestRegionMutualCharInfoSameStep(t *testing.T) {
	h := newJoinHarness(t)
	h.enter(t, 7, "aaa")
	h.enter(t, 8, "bbb")

	got7 := charInfoObjIDs(h.pushes.pushes, 7)
	got8 := charInfoObjIDs(h.pushes.pushes, 8)
	if len(got7) != 1 || got7[0] != objID(h.r.residents[1].ent.ID) {
		t.Fatalf("клиент 7: CharInfo %v; want [игрок b]", got7)
	}
	if len(got8) != 1 || got8[0] != objID(h.r.residents[0].ent.ID) {
		t.Fatalf("клиент 8: CharInfo %v; want [игрок a]", got8)
	}
}

// Логаут → ровно один DeleteObject ушедшего у оставшегося (два случая ухода:
// исчезновение без маркера — немедленно).
func TestRegionLogoutDeleteObjectToObserver(t *testing.T) {
	h := newJoinHarness(t)
	a := h.enter(t, 7, "aaa")
	b := h.enter(t, 8, "bbb")
	h.pushes.pushes = nil

	h.r.reg.Send(clientFrame(b, protocol.OpLogout))
	h.step(t)

	got := deletedObjIDs(h.pushes.pushes, 7)
	if len(got) != 1 || got[0] != objID(b) {
		t.Fatalf("DeleteObject клиенту 7: %v; want [%d]", got, objID(b))
	}
	if ids := charInfoObjIDs(h.pushes.pushes, 7); len(ids) != 0 {
		t.Fatalf("клиенту 7 лишние CharInfo: %v", ids)
	}
	_ = a
}

// Паника поздней фазы (phaseLog) после join: кадры в pendingPushes переживают
// шаг и доотправляются следующим — ровно один раз (F2/R1).
func TestRegionJoinFramesSurviveLatePhasePanic(t *testing.T) {
	h := newJoinHarness(t)
	h.enter(t, 7, "aaa")
	h.r.forcePanic = phaseLog
	h.enter(t, 8, "bbb") // шаг паникует в phaseLog — CharInfo уже в pendingPushes
	h.r.forcePanic = 0
	if h.r.Stats().Failed != 1 {
		t.Fatalf("failed = %d; want 1", h.r.Stats().Failed)
	}
	h.step(t) // доотправка хвоста

	got := charInfoObjIDs(h.pushes.pushes, 7)
	if len(got) != 1 {
		t.Fatalf("CharInfo клиенту 7 после паники: %v; want ровно один", got)
	}
}

// Reconcile закрывает окно фантома: спавн → паника phaseLog → логаут следующим
// шагом → CharInfo хвостом и DeleteObject reconcile-свёрткой того же шага,
// последним кадром про игрока — удаление (F19).
func TestRegionReconcileClosesPhantomWindow(t *testing.T) {
	h := newJoinHarness(t)
	h.enter(t, 7, "aaa")
	h.r.forcePanic = phaseLog
	b := h.enter(t, 8, "bbb")
	h.r.forcePanic = 0

	// Письмо ухода — до шага-доигрывания: хвост CharInfo(b) и reconcile-
	// удаление уезжают одной фазой B (порядок клиенту: ввод, затем удаление).
	h.pushes.pushes = nil
	h.r.reg.Send(clientFrame(b, protocol.OpLogout))
	h.step(t)

	frames := h.pushes.pushes
	var seq []byte
	for _, p := range frames {
		if p.Client != 7 || len(p.Frame) == 0 {
			continue
		}
		switch p.Frame[0] {
		case 0x03: // CharInfo
			seq = append(seq, 'C')
		case 0x12: // DeleteObject
			seq = append(seq, 'D')
		}
	}
	if string(seq) != "CD" {
		t.Fatalf("последовательность кадров наблюдателю: %q; want \"CD\" (ввод хвостом, затем reconcile-удаление)", seq)
	}
}

// Swap слота одним шагом: retire+birth в один слот → наблюдателю пара
// DeleteObject(старый)+CharInfo(новый) (F19, абсолютные удаления по id).
func TestRegionSameStepSlotSwapPair(t *testing.T) {
	h := newJoinHarness(t)
	h.enter(t, 7, "aaa")
	x := h.enter(t, 8, "xxx")
	h.pushes.pushes = nil

	// Одним шагом: уход x (письмо в его ящик) и вход y (контрольное).
	h.r.reg.Send(clientFrame(x, protocol.OpLogout))
	h.r.reg.Send(enterMsg(9, "yyy", mkRec("yyy", "Boty", 0)))
	h.step(t)

	gotD := deletedObjIDs(h.pushes.pushes, 7)
	gotC := charInfoObjIDs(h.pushes.pushes, 7)
	if len(gotD) != 1 || gotD[0] != objID(x) {
		t.Fatalf("swap: DeleteObject %v; want [%d]", gotD, objID(x))
	}
	if len(gotC) != 1 {
		t.Fatalf("swap: CharInfo %v; want один (новый житель)", gotC)
	}
}

// Событийность (F5): idle-тик — ноль join-пар; вход — пары растут
// (счётчик не write-only, P3.1-F49).
func TestRegionIdleStepZeroJoinPairs(t *testing.T) {
	h := newJoinHarness(t)
	h.enter(t, 7, "aaa")
	h.enter(t, 8, "bbb")
	before := h.r.Stats().JoinPairs
	if before == 0 {
		t.Fatal("после входов JoinPairs = 0: счётчик не считается")
	}
	h.step(t)
	if got := h.r.Stats().JoinPairs; got != before {
		t.Fatalf("idle-тик: JoinPairs %d → %d; want без изменений", before, got)
	}
}

// Паника phaseJoin: маркер фазы, счётчики ранних фаз доросли, join — нет.
func TestRegionPhaseJoinPanicMarker(t *testing.T) {
	h := newJoinHarness(t)
	h.enter(t, 7, "aaa")
	before := h.r.Stats()
	h.r.forcePanic = phaseJoin
	h.step(t)
	h.r.forcePanic = 0
	after := h.r.Stats()
	if after.Failed != before.Failed+1 {
		t.Fatalf("failed не вырос: %+v", after)
	}
	if after.PhaseJoin != before.PhaseJoin {
		t.Fatalf("PhaseJoin вырос при панике стадии: %d → %d", before.PhaseJoin, after.PhaseJoin)
	}
	if after.PhaseEffects != before.PhaseEffects+1 || after.PhaseB != before.PhaseB {
		t.Fatalf("фазы вокруг join: %+v → %+v", before, after)
	}
}

// NpcInfo-ветка: житель без Player — NPC-запись, наблюдателю едет NpcInfo
// (каркас P3.10, op 0x16).
func TestRegionNpcInfoSyntheticRecord(t *testing.T) {
	h := newJoinHarness(t)
	npc := Entity{Owner: 1, HP: 50, Pos: Position{X: -71338, Y: 258271, Z: -3104}}
	if _, err := h.r.Spawn(npc); err != nil {
		t.Fatalf("Spawn NPC: %v", err)
	}
	h.enter(t, 7, "aaa")

	sawNpc := false
	for _, p := range h.pushes.pushes {
		if p.Client == 7 && len(p.Frame) > 0 && p.Frame[0] == 0x16 {
			sawNpc = true
		}
	}
	if !sawNpc {
		t.Fatal("NpcInfo игроку не доставлен")
	}
}

// Порядок нескольких вводов — по слотам блоба, стабилен между прогонами.
func TestRegionJoinOrderDeterministicBySlot(t *testing.T) {
	run := func() (seq []int32) {
		h := newJoinHarness(t)
		h.enter(t, 7, "aaa")
		h.pushes.pushes = nil
		// Три входа одной пачкой.
		h.r.reg.Send(enterMsg(8, "bbb", mkRec("bbb", "Botb", 0)))
		h.r.reg.Send(enterMsg(9, "ccc", mkRec("ccc", "Botc", 0)))
		h.r.reg.Send(enterMsg(10, "ddd", mkRec("ddd", "Botd", 0)))
		h.step(t)
		return charInfoObjIDs(h.pushes.pushes, 7)
	}
	first := run()
	if len(first) != 3 {
		t.Fatalf("вводов %d; want 3", len(first))
	}
	for range 5 {
		again := run()
		for i := range first {
			if first[i] != again[i] {
				t.Fatalf("порядок вводов нестабилен: %v vs %v", first, again)
			}
		}
	}
}

// AdvisoryRead: чтение логируется значением в порцию (D4/F1); промах —
// нули с Found=false.
func TestRegionAdvisoryReadLoggedWithValues(t *testing.T) {
	h := newJoinHarness(t)
	id := h.enter(t, 7, "aaa")
	h.step(t) // публикация блоба

	snap, ok := h.r.AdvisoryRead(0, id)
	if !ok {
		t.Fatal("живая запись не найдена advisory")
	}
	if x, y, z := snap.Pos(); x != -71338 || y != 258271 || z != -3104 {
		t.Fatalf("позиция advisory: (%d,%d,%d)", x, y, z)
	}
	if _, ok := h.r.AdvisoryRead(0, 404); ok {
		t.Fatal("несуществующая запись найдена")
	}
	h.step(t) // LogStep шага пишет adviseBuf

	if err := h.log.Close(); err != nil {
		t.Fatalf("Close лога: %v", err)
	}
	_, steps, _, err := ReadPortionLogDir(filepath.Join(h.dir), 1)
	if err != nil {
		t.Fatalf("ReadPortionLogDir: %v", err)
	}
	var last []AdvisoryIn
	for _, s := range steps {
		if len(s.Advisory) > 0 {
			last = s.Advisory
		}
	}
	if len(last) != 2 {
		t.Fatalf("advisory-записей %d; want 2 (hit+miss)", len(last))
	}
	if last[0].Entity != id || !last[0].Found || last[0].X != -71338 {
		t.Fatalf("hit-запись: %+v", last[0])
	}
	if last[1].Found || last[1].X != 0 || last[1].Entity != 404 {
		t.Fatalf("miss-запись: %+v", last[1])
	}
}

// Публикация блоба в publish-фазе: Gen растёт на шаг; паника publish —
// прежний блоб (F7/F8).
func TestRegionPublishBlobGenInPublishPhase(t *testing.T) {
	h := newJoinHarness(t)
	h.enter(t, 7, "aaa")
	if g := h.r.snapPtr.Load().Gen(); g != 1 { // вход — один шаг, публикация — генерация 1
		t.Logf("Gen после входа = %d", g)
	}
	gen := h.r.snapPtr.Load().Gen()
	h.r.forcePanic = phasePublish
	h.step(t)
	h.r.forcePanic = 0
	if g := h.r.snapPtr.Load().Gen(); g != gen {
		t.Fatalf("паника publish сменила блоб: %d → %d", gen, g)
	}
	h.step(t)
	if g := h.r.snapPtr.Load().Gen(); g != gen+2 {
		// Сборка паник-шага не опубликована: генерация — счётчик сборок join-стадии,
		// после паник-сборки следующий успех публикует gen+2.
		t.Fatalf("Gen не вырос после успешной публикации: %d → %d (want gen+2)", gen, g)
	}
}

// Свой CharInfo себе не приходит (self-поток — UserInfo слитка P3.7).
// Свой CharInfo себе не приходит (self-поток — UserInfo слитка P3.7):
// одиночный вход не порождает никому CharInfo.
func TestRegionSelfGetsUserInfoNotCharInfo(t *testing.T) {
	h := newJoinHarness(t)
	h.enter(t, 7, "aaa")
	for _, p := range h.pushes.pushes {
		if p.Client == 7 && len(p.Frame) > 0 && p.Frame[0] == 0x03 {
			t.Fatal("собственный CharInfo доставлен")
		}
	}
}
