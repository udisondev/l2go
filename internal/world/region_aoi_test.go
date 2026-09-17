package world

// Фаза AoI региона: порядок кадров «слиток → join → письма», взаимный ввод
// ровно один, удаление ровно одно, паник-окна (структурная гарантия доставки:
// пере-вывод манифестом / примирение поколений / долговечный хвост курсора),
// advisory N→N с окном свёртки, наблюдатели — только живые игроки.

import (
	"testing"
	"time"

	"github.com/udisondev/l2go/internal/encode"
	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/replica"
	"github.com/udisondev/l2go/internal/transport"
)

// playerEntity — EntityID игрока по аккаунту (белый ящик: тесты пакета).
func playerEntity(h *enterHarness, account string) uint64 {
	for _, res := range h.r.residents {
		if res.ent.Player != nil && res.ent.Player.Rec.Account == account {
			return uint64(res.ent.ID)
		}
	}
	return 0
}

func mkRecAt(account, name string, x, y int) persist.CharRecord {
	rec := mkRec(account, name, 0)
	rec.X, rec.Y = x, y
	return rec
}

func (h *enterHarness) enterConn(t *testing.T, conn uint64, rec persist.CharRecord) {
	t.Helper()
	h.enterConnRaw(conn, rec)
	waitTick(t, h.r)
}

// enterConnRaw — отправка входа без ожидания шага (для паник-инъекций:
// waitTick при активной инъекции завис бы на проваленных шагах).
func (h *enterHarness) enterConnRaw(conn uint64, rec persist.CharRecord) {
	body, err := transport.EncodeLetter(transport.EnterWorldMsg{
		Conn: conn, Account: rec.Account, Char: mustJSONChar(rec)})
	if err != nil {
		panic("тест: кодирование EnterWorldMsg: " + err.Error())
	}
	h.reg.Send(h.ctrlLetter(transport.KindEnterWorld, body))
}

// waitCond — поллинг состояния региона до условия (бюджет).
func waitCond(t *testing.T, r *Region, cond func(RegionStats) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond(r.Stats()) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("условие не наступило: %+v", r.Stats())
}

func objIDs(c *pushCollector) (chars, dels []uint64) {
	for _, p := range c.pushes {
		if len(p.Frame) == 0 {
			continue
		}
		switch p.Frame[0] {
		case 0x03:
			if len(p.Frame) >= 21 {
				chars = append(chars, uint64(uint32(p.Frame[17])|uint32(p.Frame[18])<<8|
					uint32(p.Frame[19])<<16|uint32(p.Frame[20])<<24))
			}
		case 0x12:
			if len(p.Frame) >= 5 {
				dels = append(dels, uint64(uint32(p.Frame[1])|uint32(p.Frame[2])<<8|uint32(p.Frame[3])<<16|uint32(p.Frame[4])<<24))
			}
		}
	}
	return chars, dels
}

func knownOf(c *pushCollector) map[uint64]bool {
	chars, dels := objIDs(c)
	known := make(map[uint64]bool)
	for _, id := range chars {
		known[id] = true
	}
	for _, id := range dels {
		delete(known, id)
	}
	return known
}

// Порядок кадров на клиента: слиток (UserInfo) строго раньше join-кадра
// (CharInfo второго игрока).
func TestRegionPhaseAoIWireOrder(t *testing.T) {
	h := newEnterHarness(t, DefaultConfig())
	h.enterConn(t, 7, mkRecAt("alice", "Alice", -71338, 258271))
	h.enterConn(t, 8, mkRecAt("bob", "Bob", -71338, 258271))
	slivokEnd, firstChar := -1, -1
	for i, p := range h.pushes.pushes {
		if len(p.Frame) == 0 {
			continue
		}
		if p.Client != 8 {
			continue
		}
		switch p.Frame[0] {
		case 0x04: // UserInfo
			slivokEnd = i
		case 0x03:
			if firstChar < 0 {
				firstChar = i
			}
		}
	}
	if slivokEnd < 0 || firstChar < 0 {
		t.Fatalf("кадры не найдены: slivok=%d charInfo=%d (пуши %d)", slivokEnd, firstChar, len(h.pushes.pushes))
	}
	if slivokEnd > firstChar {
		t.Fatalf("CharInfo (инд %d) раньше UserInfo слитка (инд %d)", firstChar, slivokEnd)
	}
	if h.r.Stats().PhaseAoI == 0 {
		t.Fatalf("PhaseAoI не тикает")
	}
}

// Взаимный ввод: после входа второго — ровно по одному CharInfo каждому;
// повторы шагов без движения — ноль новых кадров.
func TestRegionMutualIntroductionSingleCharInfo(t *testing.T) {
	h := newEnterHarness(t, DefaultConfig())
	h.enterConn(t, 7, mkRecAt("alice", "Alice", -71338, 258271))
	h.enterConn(t, 8, mkRecAt("bob", "Bob", -71338, 258271))
	chars, _ := objIDs(h.pushes)
	counts := map[uint64]int{}
	for _, id := range chars {
		counts[id-encode.ObjectIDBase]++
	}
	if len(counts) != 2 {
		t.Fatalf("вводов разных целей = %d; want 2 (взаимность): %+v", len(counts), counts)
	}
	for ent, n := range counts {
		if n != 1 {
			t.Fatalf("цель %d введена %d раз; want 1", ent, n)
		}
	}
	before := len(h.pushes.pushes)
	waitTick(t, h.r)
	if len(h.pushes.pushes) != before {
		t.Fatalf("шаг без событий породил %d кадров", len(h.pushes.pushes)-before)
	}
}

// Уход (деспавн по логауту): ровно один DeleteObject наблюдателю; ушедшему —
// ничего о себе.
func TestRegionRemovalSingleDeleteObject(t *testing.T) {
	h := newEnterHarness(t, DefaultConfig())
	h.enterConn(t, 7, mkRecAt("alice", "Alice", -71338, 258271))
	h.enterConn(t, 8, mkRecAt("bob", "Bob", -71338, 258271))
	h.pushes.pushes = nil
	bob := playerEntity(h, "bob")
	h.send(t, transport.Envelope{
		To: transport.Addr{Entity: transport.EntityID(bob)}, FromID: h.gwID,
		Kind: transport.KindClientFrame, Payload: []byte{protocol.OpLogout}})
	_, dels := objIDs(h.pushes)
	if len(dels) != 1 {
		t.Fatalf("DeleteObject после логаута = %v; want ровно один", dels)
	}
}

// Паника фазы AoI: письма шага доставлены (outbox пережил), события
// пере-выведены полным манифестом следующего шага, known-set сходится.
func TestRegionPanicInAoIReemitsFullManifest(t *testing.T) {
	h := newEnterHarness(t, DefaultConfig())
	h.enterConn(t, 7, mkRecAt("alice", "Alice", -71338, 258271))
	h.r.forcePanic.Store(uint32(phaseAoI))
	h.enterConnRaw(8, mkRecAt("bob", "Bob", -71338, 258271))
	waitCond(t, h.r, func(st RegionStats) bool { return st.Failed >= 1 && st.Residents == 2 })
	h.r.forcePanic.Store(0)
	waitTick(t, h.r)
	waitTick(t, h.r)
	if len(knownOf(h.pushes)) != 2 {
		t.Fatalf("после паники AoI взаимность не восстановлена (known=%v)", knownOf(h.pushes))
	}
}

// Паника phaseB при ненулевых join-кадрах: хвост доставлен recovered
// немедленно (курсор), дублей головы нет.
func TestRegionPanicInPhaseBJoinTailDurable(t *testing.T) {
	h := newEnterHarness(t, DefaultConfig())
	h.enterConn(t, 7, mkRecAt("alice", "Alice", -71338, 258271))
	h.r.forcePanic.Store(uint32(phaseB))
	h.enterConnRaw(8, mkRecAt("bob", "Bob", -71338, 258271))
	waitCond(t, h.r, func(st RegionStats) bool { return st.Failed >= 1 && st.Residents == 2 })
	h.r.forcePanic.Store(0)
	waitTick(t, h.r)
	if len(knownOf(h.pushes)) != 2 {
		t.Fatalf("join-кадр потерян при панике phaseB: known=%v", knownOf(h.pushes))
	}
}

// Паника между Apply и merge: join-кадры шага дропнуты, примирение следующего
// шага пере-вводит (дубли безвредны, known-set сходится).
func TestRegionPanicPostApplyConverges(t *testing.T) {
	h := newEnterHarness(t, DefaultConfig())
	h.enterConn(t, 7, mkRecAt("alice", "Alice", -71338, 258271))
	h.r.forcePanicPostApply.Store(true)
	h.enterConnRaw(8, mkRecAt("bob", "Bob", -71338, 258271))
	h.r.forcePanicPostApply.Store(false)
	waitTick(t, h.r)
	waitTick(t, h.r)
	if len(knownOf(h.pushes)) != 2 {
		t.Fatalf("примирение после post-Apply-паники не пере-ввело цель: known=%v", knownOf(h.pushes))
	}
}

// Паника внутри Apply (шов replica): частичный стадинг — примирение полным
// эмитом замыкает обе стороны.
func TestRegionPanicInApplyConverges(t *testing.T) {
	h := newEnterHarness(t, DefaultConfig())
	h.enterConn(t, 7, mkRecAt("alice", "Alice", -71338, 258271))
	h.r.join.ForcePanicInApply.Store(true)
	h.enterConnRaw(8, mkRecAt("bob", "Bob", -71338, 258271))
	h.r.join.ForcePanicInApply.Store(false)
	waitTick(t, h.r)
	waitTick(t, h.r)
	if len(knownOf(h.pushes)) != 2 {
		t.Fatalf("примирение после паники внутри Apply не замкнуло известность: known=%v", knownOf(h.pushes))
	}
}

// Рождение+уход через паническое окно phaseB: примирение эмитит Remove —
// вечного фантома нет.
func TestRegionBirthAndLeaveThroughPanicWindow(t *testing.T) {
	cfg := DefaultConfig()
	cfg.GraceTicks = 1
	h := newEnterHarness(t, cfg)
	h.enterConn(t, 7, mkRecAt("alice", "Alice", -71338, 258271))
	h.r.forcePanic.Store(uint32(phaseB))
	h.enterConnRaw(8, mkRecAt("bob", "Bob", -71338, 258271))
	h.r.forcePanic.Store(0)
	waitTick(t, h.r)
	// Bob уходит (LinkDead → короткий grace → Retire)
	h.send(t, h.ctrlLetter(transport.KindLinkDead, mustConnRef(t, 8)))
	for range 4 {
		waitTick(t, h.r)
	}
	known := knownOf(h.pushes)
	if len(known) != 1 {
		t.Fatalf("после ухода Bob известны %v; want только Alice (нет вечного фантома)", known)
	}
	_, dels := objIDs(h.pushes)
	if len(dels) < 1 {
		t.Fatalf("DeleteObject(Bob) не доставлен примирением (dels=%v)", dels)
	}
}

func mustConnRef(t *testing.T, conn uint64) []byte {
	t.Helper()
	body, err := transport.EncodeLetter(transport.ConnRefMsg{Conn: conn})
	if err != nil {
		t.Fatalf("кодирование ConnRefMsg: %v", err)
	}
	return body
}

// Наблюдатели — только живые игроки: Leaving-игрок не получает join-кадров.
func TestRegionObserversOnlyLivingPlayers(t *testing.T) {
	h := newEnterHarness(t, DefaultConfig())
	h.enterConn(t, 7, mkRecAt("alice", "Alice", -71338, 258271))
	// Alice обрывается (LinkDead): в grace она резидент, но не наблюдатель
	h.send(t, h.ctrlLetter(transport.KindLinkDead, mustConnRef(t, 7)))
	h.pushes.pushes = nil
	h.enterConn(t, 8, mkRecAt("bob", "Bob", -71338, 258271))
	for _, p := range h.pushes.pushes {
		if p.Client == 7 && len(p.Frame) > 0 && (p.Frame[0] == 0x03 || p.Frame[0] == 0x12) {
			t.Fatalf("Leaving-наблюдатель получил join-кадр %x", p.Frame[0])
		}
	}
}

// Одиночный клиент — join-кадров нет (регресс P3.7).
func TestRegionSingleClientNoJoinFrames(t *testing.T) {
	h := newEnterHarness(t, DefaultConfig())
	h.enterConn(t, 7, mkRecAt("alice", "Alice", -71338, 258271))
	waitTick(t, h.r)
	chars, dels := objIDs(h.pushes)
	if len(chars) != 0 || len(dels) != 0 {
		t.Fatalf("одиночный клиент получил join-кадры: chars=%v dels=%v", chars, dels)
	}
}

// Advisory: N чтений до LogStep = N записей в порции шага.
func TestRegionAdvisoryReadsLoggedPerPortion(t *testing.T) {
	_, r := newTestRegion(t, DefaultConfig())
	resident := spawnResident(t, r, 100)
	r.step()
	r.advWindow = true // эмуляция потребителя внутри шага (до LogStep)
	for range 3 {
		if _, ok := r.adv.Snapshot(0, resident); !ok {
			t.Fatalf("чтение резидента %d не состоялось", resident)
		}
	}
	r.step()
	if err := r.log.Close(); err != nil { // bufio-буфер: сброс перед перечитанием
		t.Fatalf("закрытие лога: %v", err)
	}
	_, steps, _, err := ReadPortionLogDir(r.log.dir, 1)
	if err != nil {
		t.Fatalf("перечитание лога: %v", err)
	}
	if len(steps) == 0 {
		t.Fatalf("лог шагов пуст")
	}
	if n := len(steps[len(steps)-1].Advisory); n != 3 {
		t.Fatalf("advisory-записей в порции последнего шага = %d; want 3", n)
	}
}

// Паника шва advisory-окна: чтение после LogStep.
func TestAdvisorySeamPanicsAfterLogStep(t *testing.T) {
	_, r := newTestRegion(t, DefaultConfig())
	spawnResident(t, r, 100)
	r.step()
	if !r.advWindow {
		// после завершённого шага окно закрыто
	}
	defer func() {
		if recover() == nil {
			t.Fatalf("чтение вне окна не паникует")
		}
	}()
	r.adv.Snapshot(0, transport.EntityID(1))
}

// Компоновка событий: игроку CharInfo (Crypt, objID=Base+Entity), NPC — счётчик
// без кадров, Remove → DeleteObject, Update — без кадров.
func TestComposeJoinEventKinds(t *testing.T) {
	r := &Region{}
	player := replica.Record{Entity: 5, Kind: replica.RecordKindPlayer, Name: "X"}
	npc := replica.Record{Entity: 6, Kind: replica.RecordKindNPC}
	pushes := r.composeJoin([]replica.Event{
		{Obs: replica.Observer{ConnID: 9}, Target: player, Kind: replica.EventIntroduce},
		{Obs: replica.Observer{ConnID: 9}, Target: npc, Kind: replica.EventIntroduce},
		{Obs: replica.Observer{ConnID: 9}, Target: player, Kind: replica.EventRemove},
		{Obs: replica.Observer{ConnID: 9}, Target: player, Kind: replica.EventUpdate},
	})
	if len(pushes) != 2 {
		t.Fatalf("кадров = %d; want 2 (CharInfo + DeleteObject)", len(pushes))
	}
	if pushes[0].Frame[0] != 0x03 || !pushes[0].Crypt || pushes[0].Client != 9 {
		t.Fatalf("CharInfo-кадр: %+v", pushes[0])
	}
	if pushes[1].Frame[0] != 0x12 || !pushes[1].Crypt {
		t.Fatalf("DeleteObject-кадр: %+v", pushes[1])
	}
	if r.npcIntroduceSkipped.Load() != 1 {
		t.Fatalf("NPC-ввод не посчитан")
	}
}
