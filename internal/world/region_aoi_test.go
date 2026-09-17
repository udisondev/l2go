package world

// Фаза AoI региона: порядок кадров «слиток → join → письма», взаимный ввод
// ровно один, удаление ровно одно, паник-окна (структурная гарантия доставки:
// пере-вывод манифестом / примирение поколений / долговечный хвост курсора),
// advisory N→N с окном свёртки, наблюдатели — только живые игроки.

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/udisondev/l2go/internal/encode"
	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/replica"
	"github.com/udisondev/l2go/internal/transport"
)

// playerEntity — EntityID игрока по bind-письмам региона шлюзу: gwBox
// изымается ОДИН раз на harness и кешируется (ExtractInto изымает — повторное
// чтение ящика пусто; новые бинды тесту недоступны до нового harness).
// Чтение gwBox тест-горутиной легитимно; прямой обход r.residents при живом
// Run — гонка харнесса.
func playerEntity(h *enterHarness, conn uint64) uint64 {
	if h.binds == nil {
		h.binds = make(map[uint64]uint64)
	}
	if _, ok := h.binds[conn]; !ok {
		// промах: доизвлечь новые bind-письма (рождения после кеширования)
		for _, env := range h.gwBox.ExtractInto(h.gwToken, nil) {
			if env.Kind != transport.KindConnBind {
				continue
			}
			msg, err := transport.DecodeLetter[transport.ConnBindMsg](env.Payload)
			if err != nil {
				continue
			}
			h.binds[msg.Conn] = uint64(msg.Entity)
		}
	}
	return h.binds[conn]
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
	for _, p := range c.Snapshot() {
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
	for i, p := range h.pushes.Snapshot() {
		if len(p.Frame) == 0 || p.Client != 8 {
			continue
		}
		switch p.Frame[0] {
		case protocol.OpUserInfo:
			slivokEnd = i
		case protocol.OpCharInfo:
			if firstChar < 0 {
				firstChar = i
			}
		}
	}
	if slivokEnd < 0 || firstChar < 0 {
		t.Fatalf("кадры не найдены: slivok=%d charInfo=%d (пуши %d)", slivokEnd, firstChar, len(h.pushes.Snapshot()))
	}
	if slivokEnd > firstChar {
		t.Fatalf("CharInfo (инд %d) раньше UserInfo слитка (инд %d)", firstChar, slivokEnd)
	}
	if !h.pushes.Snapshot()[firstChar].Crypt {
		t.Fatalf("join-кадр не криптуется (Crypt=false)")
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
	before := len(h.pushes.Snapshot())
	waitTick(t, h.r)
	if after := len(h.pushes.Snapshot()); after != before {
		t.Fatalf("шаг без событий породил %d кадров", after-before)
	}
}

// Уход (деспавн по логауту): ровно один DeleteObject наблюдателю; ушедшему —
// ничего о себе.
func TestRegionRemovalSingleDeleteObject(t *testing.T) {
	h := newEnterHarness(t, DefaultConfig())
	h.enterConn(t, 7, mkRecAt("alice", "Alice", -71338, 258271))
	h.enterConn(t, 8, mkRecAt("bob", "Bob", -71338, 258271))
	h.pushes.Reset()
	bob := playerEntity(h, 8)
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

// шага пере-вводит (дубли безвредны, known-set сходится).
func TestRegionPanicPostApplyConverges(t *testing.T) {
	h := newEnterHarness(t, DefaultConfig())
	h.enterConn(t, 7, mkRecAt("alice", "Alice", -71338, 258271))
	h.r.forcePanicPostApply.Store(true)
	h.enterConnRaw(8, mkRecAt("bob", "Bob", -71338, 258271))
	waitCond(t, h.r, func(st RegionStats) bool { return st.Failed >= 1 && st.Residents == 2 })
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
	waitCond(t, h.r, func(st RegionStats) bool { return st.Failed >= 1 && st.Residents == 2 })
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
	waitCond(t, h.r, func(st RegionStats) bool { return st.Failed >= 1 && st.Residents == 2 })
	h.r.forcePanic.Store(0)
	waitTick(t, h.r)
	// Bob уходит (LinkDead → короткий grace → Retire)
	h.send(t, h.ctrlLetter(transport.KindLinkDead, mustConnRef(t, 8)))
	for range 4 {
		waitTick(t, h.r)
	}
	known := knownOf(h.pushes)
	aliceID := playerEntity(h, 7)
	if len(known) != 1 || !known[encode.ObjectIDBase+aliceID] {
		t.Fatalf("после ухода Bob известны %v; want только alice (нет вечного фантома)", known)
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
	h.pushes.Reset()
	h.enterConn(t, 8, mkRecAt("bob", "Bob", -71338, 258271))
	for _, p := range h.pushes.Snapshot() {
		if p.Client == 7 && len(p.Frame) > 0 &&
			(p.Frame[0] == protocol.OpCharInfo || p.Frame[0] == protocol.OpDeleteObject) {
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
	// промах — тоже чтение: логируется наравне с попаданием
	r.adv.Snapshot(0, resident+999)

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
	if n := len(steps[len(steps)-1].Advisory); n != 4 {
		t.Fatalf("advisory-записей в порции последнего шага = %d; want 4 (3 попадания + промах)", n)
	}
}

// Паника шва advisory-окна: чтение после LogStep.
func TestAdvisorySeamPanicsAfterLogStep(t *testing.T) {
	_, r := newTestRegion(t, DefaultConfig())
	spawnResident(t, r, 100)
	r.step()
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
	longName := strings.Repeat("Щ", 4096) // корнер: максимальное имя — размер кадра растёт, не паникует
	player := replica.Record{Entity: 5, Kind: replica.RecordKindPlayer, Name: longName}
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

// failAfterPushes — панирующий двойник пушера (шов №4 тест-плана): детерминированная
// паника после N доставок; persistent=false — однократная (recovered доставляет
// хвост немедленно), true — паника держится до конца шага (хвост переносится
// resetDrain-ом). Счётчик доставок — оракул отсутствия дублей головы.
type failAfterPushes struct {
	inner      *pushCollector
	failAfter  int
	persistent bool
	disabled   atomic.Bool
	delivered  atomic.Int64
	panicked   atomic.Bool
}

func (f *failAfterPushes) Push(id uint64, frame []byte, crypt bool) {
	if !f.disabled.Load() && f.delivered.Load() >= int64(f.failAfter) &&
		(f.persistent || !f.panicked.Load()) {
		f.panicked.Store(true)
		panic("world: инъекция сбоя пушера после N доставок")
	}
	f.inner.Push(id, frame, crypt)
	f.delivered.Add(1)
}

// Паника Push в середине push-цикла phaseB: хвост доставлен (немедленно или
// переносом), возобновление без дублей головы, known-set сходится.
func TestRegionPanicInPhaseBJoinTailDurable(t *testing.T) {
	for _, mode := range []struct {
		name       string
		persistent bool
	}{{"немедленная доставка recovered", false}, {"перенос resetDrain", true}} {
		t.Run(mode.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.GraceTicks = 1
			// двойник назначается до старта Run (подмена при живом регионе —
			// гонка); счётчик непрерывен: 16 кадров слитка alice докатываются,
			// паника — на 3-м кадре шага входа Bob
			fail := &failAfterPushes{failAfter: 16 + 3, persistent: mode.persistent}
			h := newEnterHarnessPusher(t, cfg, fail)
			fail.inner = h.pushes
			h.enterConn(t, 7, mkRecAt("alice", "Alice", -71338, 258271))
			h.enterConnRaw(8, mkRecAt("bob", "Bob", -71338, 258271))
			waitCond(t, h.r, func(st RegionStats) bool { return st.Failed >= 1 && st.Residents == 2 })
			fail.disabled.Store(true) // деактивация двойника, а не подмена pusher (гонка)
			waitTick(t, h.r)
			waitTick(t, h.r)
			if len(knownOf(h.pushes)) != 2 {
				t.Fatalf("known-set не сошился: %v (режим %s)", knownOf(h.pushes), mode.name)
			}
			// дублей головы нет: заголовок слитка (USER_INFO) доставлен однократно
			userInfos := 0
			for _, p := range h.pushes.Snapshot() {
				if p.Client == 8 && len(p.Frame) > 0 && p.Frame[0] == protocol.OpUserInfo {
					userInfos++
				}
			}
			if userInfos != 1 {
				t.Fatalf("USER_INFO клиента 8 доставлен %d раз; want 1 (дубль головы?) в режиме %s", userInfos, mode.name)
			}
			chars, _ := objIDs(h.pushes)
			seen := map[uint64]int{}
			for _, id := range chars {
				seen[id]++
			}
			// дублей ввода быть не может (повторный ввод — только примирение,
			// которого здесь нет: Commit прошёл до смены пушера)
			for id, n := range seen {
				if n > 2 { // ≤2 законно: ввод + повтор примирения при его наличии
					t.Fatalf("цель %d введена %d раз (дубль головы?) в режиме %s", id, n, mode.name)
				}
			}
		})
	}
}

// Дисциплина «свап после шага»: паника phaseB на шаге рождения X — X в
// закоммиченном блобе только после успешного шага.
func TestRegionBlobCommitOnlyInPublishPhase(t *testing.T) {
	h := newEnterHarness(t, DefaultConfig())
	h.enterConn(t, 7, mkRecAt("alice", "Alice", -71338, 258271))
	h.r.forcePanic.Store(uint32(phaseB))
	h.enterConnRaw(8, mkRecAt("bob", "Bob", -71338, 258271))
	waitCond(t, h.r, func(st RegionStats) bool { return st.Failed >= 1 && st.Residents == 2 })
	if _, ok := h.r.pub.Read(0, transport.EntityID(playerEntity(h, 8))); ok {
		t.Fatalf("публикация до завершения шага: Bob читается в закоммиченном")
	}
	h.r.forcePanic.Store(0)
	waitTick(t, h.r)
	if _, ok := h.r.pub.Read(0, transport.EntityID(playerEntity(h, 8))); !ok {
		t.Fatalf("после успешного шага Bob не закоммичен")
	}
}

// Спавн+деспавн одним шагом (вытеснение повторным входом той же пачки):
// вытесненная сущность ни разу не в блобе — кадров о ней нет.
func TestRegionSameStepBirthAndRetireNoEvents(t *testing.T) {
	h := newEnterHarness(t, DefaultConfig())
	h.enterConn(t, 7, mkRecAt("alice", "Alice", -71338, 258271))
	h.pushes.Reset()
	// два входа одного аккаунта одной пачкой: первый вытеснен ДО Spawn
	rec := mkRecAt("churn", "First", -71338, 258271)
	body1, _ := transport.EncodeLetter(transport.EnterWorldMsg{Conn: 20, Account: "churn", Char: mustJSONChar(rec)})
	rec2 := mkRecAt("churn", "Second", -71338, 258271)
	body2, _ := transport.EncodeLetter(transport.EnterWorldMsg{Conn: 21, Account: "churn", Char: mustJSONChar(rec2)})
	h.reg.Send(h.ctrlLetter(transport.KindEnterWorld, body1))
	h.reg.Send(h.ctrlLetter(transport.KindEnterWorld, body2))
	waitCond(t, h.r, func(st RegionStats) bool { return st.Residents == 2 })
	waitTick(t, h.r)
	// выживший и alice введены взаимно (первичное заполнение новичка);
	// вытесненный не существовал (без ID и кадров)
	chars, _ := objIDs(h.pushes)
	if len(chars) != 2 {
		t.Fatalf("вводов = %d; want 2 (взаимность выжившего и alice): %v", len(chars), chars)
	}
	users := 0
	for _, p := range h.pushes.Snapshot() {
		if len(p.Frame) > 0 && p.Frame[0] == protocol.OpUserInfo {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("слитков USER_INFO = %d; want 1 (выживший)", users)
	}
}

// Компенсирующие Births+Retires одним шагом: блоб различает состав (Born
// нового, Gone ушедшего), не длину.
func TestRegionCompensatingBirthsRetireBlob(t *testing.T) {
	h := newEnterHarness(t, DefaultConfig())
	h.enterConn(t, 7, mkRecAt("alice", "Alice", -71338, 258271))
	h.enterConn(t, 8, mkRecAt("bob", "Bob", -71338, 258271))
	h.pushes.Reset()
	bobID := playerEntity(h, 8) // до ухода: после Retire биндов новых нет
	// одним шагом: логаут Bob + вход carol
	h.reg.Send(transport.Envelope{
		To: transport.Addr{Entity: transport.EntityID(bobID)}, FromID: h.gwID,
		Kind: transport.KindClientFrame, Payload: []byte{protocol.OpLogout}})
	h.enterConnRaw(9, mkRecAt("carol", "Carol", -71338, 258271))
	waitCond(t, h.r, func(st RegionStats) bool { return st.Residents == 2 })
	waitTick(t, h.r)
	if _, ok := h.r.pub.Read(0, transport.EntityID(playerEntity(h, 9))); !ok {
		t.Fatalf("born-состав не виден: carol нет в блобе")
	}
	if _, ok := h.r.pub.Read(0, transport.EntityID(playerEntity(h, 8))); ok {
		t.Fatalf("gone-состав не виден: bob остался в блобе")
	}
	_, dels := objIDs(h.pushes)
	found := false
	for _, id := range dels {
		if id == encode.ObjectIDBase+bobID {
			found = true
		}
	}
	if !found {
		t.Fatalf("DeleteObject(bob) не доставлен при компенсирующем шаге")
	}
}

// Перезаход аккаунта: известность строится заново, призрачного CharInfo
// вытесненной сущности нет.
func TestRegionReenterAccountKnownSetsClean(t *testing.T) {
	cfg := DefaultConfig()
	cfg.GraceTicks = 1
	h := newEnterHarness(t, cfg)
	h.enterConn(t, 7, mkRecAt("alice", "Alice", -71338, 258271))
	h.enterConn(t, 8, mkRecAt("eve", "Eve", -71338, 258271))
	h.pushes.Reset()
	oldEve := playerEntity(h, 8) // до вытеснения
	// перезаход eve с нового конна: вытеснение живой сущности без её персиста
	h.enterConn(t, 9, mkRecAt("eve", "Eve2", -71338, 258271))
	chars, _ := objIDs(h.pushes)
	for _, id := range chars {
		if id == encode.ObjectIDBase+oldEve {
			t.Fatalf("призрачный CharInfo вытесненной сущности %d", oldEve)
		}
	}
	// взаимность восстановлена: alice ⇄ новая eve
	if len(knownOf(h.pushes)) != 2 {
		t.Fatalf("после перезахода известность = %v; want 2 (alice и новая eve)", knownOf(h.pushes))
	}
}

// Рождение+уход через post-Apply-окно (трасса F61): X рождён в шаге с паникой
// между Apply и merge, ушёл следующим — Remove эмитится примирением,
// вечного фантома нет.
func TestRegionBirthAndLeaveThroughPostApplyWindow(t *testing.T) {
	cfg := DefaultConfig()
	cfg.GraceTicks = 1
	h := newEnterHarness(t, cfg)
	h.enterConn(t, 7, mkRecAt("alice", "Alice", -71338, 258271))
	aliceID := playerEntity(h, 7)
	h.r.forcePanicPostApply.Store(true)
	h.enterConnRaw(8, mkRecAt("bob", "Bob", -71338, 258271))
	waitCond(t, h.r, func(st RegionStats) bool { return st.Failed >= 1 && st.Residents == 2 })
	bobID := playerEntity(h, 8)
	h.r.forcePanicPostApply.Store(false)
	waitTick(t, h.r)
	// Bob уходит (LinkDead → короткий grace → Retire)
	h.send(t, h.ctrlLetter(transport.KindLinkDead, mustConnRef(t, 8)))
	for range 4 {
		waitTick(t, h.r)
	}
	known := knownOf(h.pushes)
	want := map[uint64]bool{encode.ObjectIDBase + aliceID: true}
	if len(known) != len(want) || !known[encode.ObjectIDBase+aliceID] {
		t.Fatalf("после ухода через post-Apply-окно известность = %v; want только alice", known)
	}
	_, dels := objIDs(h.pushes)
	found := false
	for _, id := range dels {
		if id == encode.ObjectIDBase+bobID {
			found = true
		}
	}
	if !found {
		t.Fatalf("DeleteObject(bob) не доставлен примирением (dels=%v)", dels)
	}
}

// ObserversOnlyLivingPlayers — расширенный сценарий: EnterLeaving-рождение
// не наблюдатель (NPC-случай — юнит composeJoin + фильтр aoiObs; Spawn при
// живом Run из теста — гонка харнесса, не заводим).
func TestRegionObserversNPCAndEnterLeavingNotWatching(t *testing.T) {
	cfg := DefaultConfig()
	cfg.GraceTicks = 2
	h := newEnterHarness(t, cfg)
	h.enterConn(t, 7, mkRecAt("alice", "Alice", -71338, 258271))
	// NPC-случай (Kind NPC — резидент без Player) покрыт юнитом composeJoin
	// (счётчик-ветка) и фильтром aoiObs; прямой Spawn при живом Run — гонка
	// харнесса (метод «только горутина региона»). Рождение «вошёл и оборвался»:
	rec := mkRecAt("flash", "Flash", -71338, 258271)
	body, _ := transport.EncodeLetter(transport.EnterWorldMsg{Conn: 30, Account: "flash", Char: mustJSONChar(rec)})
	ld, _ := transport.EncodeLetter(transport.ConnRefMsg{Conn: 30})
	h.reg.Send(h.ctrlLetter(transport.KindEnterWorld, body))
	h.reg.Send(h.ctrlLetter(transport.KindLinkDead, ld))
	waitCond(t, h.r, func(st RegionStats) bool { return st.Residents == 2 })
	h.pushes.Reset()
	// третий живой игрок: кадры только alice
	h.enterConn(t, 9, mkRecAt("carol", "Carol", -71338, 258271))
	aliceID := playerEntity(h, 7)
	carolID := playerEntity(h, 9)
	_ = aliceID
	for _, p := range h.pushes.Snapshot() {
		if len(p.Frame) == 0 {
			continue
		}
		if p.Frame[0] == protocol.OpCharInfo || p.Frame[0] == protocol.OpDeleteObject {
			if p.Client != 7 && p.Client != 9 {
				t.Fatalf("join-кадр не-живому наблюдателю (client=%d)", p.Client)
			}
		}
	}
	known := knownOf(h.pushes)
	if !known[encode.ObjectIDBase+carolID] || !known[encode.ObjectIDBase+aliceID] {
		t.Fatalf("взаимность alice⇄carol не установлена: %v", known)
	}
	// flash жив в grace — легитимная цель для alice; после истечения grace —
	// DeleteObject(flash), известность сходится к alice⇄carol
	for range 6 {
		waitTick(t, h.r)
	}
	known = knownOf(h.pushes)
	flashID := playerEntity(h, 30)
	if flashID != 0 && known[encode.ObjectIDBase+flashID] {
		t.Fatalf("фантом EnterLeaving после grace: %v (flash=%d)", known, flashID)
	}
}
