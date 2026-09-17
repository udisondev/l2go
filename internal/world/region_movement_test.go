package world

// Компонентные тесты движения региона (группы E/G тест-плана): стрим
// наблюдателю, StopMove на прибытие, молчание вне радиуса, паник-хвост,
// финальный снимок с heading. Шаги — вручную из тест-горутины (single-writer,
// детерминизм счёта кадров) или живым Run (паники/сохранитель).

import (
	"sort"
	"testing"

	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/replica"
	"github.com/udisondev/l2go/internal/transport"
)

// moveRegion — регион с ручными шагами и коллектором пушей (детерминированный
// счёт кадров).
func moveRegion(t *testing.T) (*Region, *pushCollector) {
	t.Helper()
	m, err := NewMetronome(DefaultConfig())
	if err != nil {
		t.Fatalf("NewMetronome: %v", err)
	}
	reg := transport.NewRegistry(0)
	log, err := NewPortionLog(t.TempDir(), 1, m.period, true, 1<<20) // payloads: реплей-тесту нужны тела писем
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	pc := &pushCollector{}
	r, err := NewRegion(m, reg, 1, DefaultConfig(), log, pc, emptyGeo)
	if err != nil {
		t.Fatalf("NewRegion: %v", err)
	}
	if err := r.Wire(901, 900); err != nil {
		t.Fatalf("Wire: %v", err)
	}
	return r, pc
}

// spawnPlayer — житель-игрок с коннектом (наблюдатель/движун).
func spawnPlayer(t *testing.T, r *Region, conn uint64, x, y int32) transport.EntityID {
	t.Helper()
	ent := Entity{
		Owner: r.id, Pos: Position{X: x, Y: y},
		HP: 100, Heading: 1,
		Player: &Player{Rec: mkRec("acc"+string(rune('A'+conn-1)), "hero", 0), ConnID: conn,
			PendingTeleport: false, SpeedBudget: speedCAP},
	}
	id, err := r.Spawn(ent)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	r.state.ResolveBirth(ent.Player.Rec.Account, conn, id)
	return id
}

// sendToBox — письмо в ящик жителя (шлюз-двойник теста).
func sendToBox(r *Region, id transport.EntityID, payload []byte) {
	r.reg.Send(transport.Envelope{
		To: transport.Addr{Entity: id}, FromID: 901,
		Kind: transport.KindClientFrame, Payload: payload,
	})
}

// stepN — N шагов региона вручную (по тику метронома каждый).
func stepN(r *Region, n uint64) {
	for range n {
		r.metro.tick.Add(1)
		r.step()
	}
}

// countOp — кадры клиента с опкодом.
func countOp(pc *pushCollector, client uint64, op byte) int {
	n := 0
	for _, p := range pc.Snapshot() {
		if p.Client == client && len(p.Frame) > 0 && p.Frame[0] == op {
			n++
		}
	}
	return n
}

func TestComposeJoinIntroduceMovingDescribed(t *testing.T) {
	r := &Region{}
	moving := replica.Record{Entity: 5, Kind: replica.RecordKindPlayer, Name: "Hero",
		X: 100, Y: 100, DestX: 500, DestY: 100, Moving: true, Heading: 16384}
	standing := moving
	standing.Moving = false
	standing.Entity = 6
	for _, tc := range []struct {
		name         string
		rec          replica.Record
		wantFrames   int
		wantStanding bool
	}{
		{"движущийся: describeState", moving, 2, false},
		{"стоячий: только CharInfo", standing, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pushes := r.composeJoin([]replica.Event{
				{Obs: replica.Observer{ConnID: 9}, Target: tc.rec, Kind: replica.EventIntroduce},
			})
			if len(pushes) != tc.wantFrames {
				t.Fatalf("кадров = %d; want %d", len(pushes), tc.wantFrames)
			}
			if pushes[0].Frame[0] != 0x03 {
				t.Fatalf("первый кадр = %#x; want CharInfo", pushes[0].Frame[0])
			}
			v, ok := protocol.NewCharInfoView(pushes[0].Frame)
			if !ok {
				t.Fatalf("CharInfo не разбирается")
			}
			if v.Standing() != tc.wantStanding || !v.Running() {
				t.Fatalf("Standing=%v Running=%v; want %v, true (флип-флоп-компонента F7)",
					v.Standing(), v.Running(), tc.wantStanding)
			}
			if tc.rec.Moving {
				if pushes[1].Frame[0] != opCharMoveToLocation {
					t.Fatalf("второй кадр = %#x; want CharMoveToLocation", pushes[1].Frame[0])
				}
			}
		})
	}
}

// TestComposeJoinUpdateStreamsOneFramePerStep — апдейт записи → ровно один
// кадр: движущейся — CharMoveToLocation (авторитетный стрим), стоячей — StopMove.
func TestComposeJoinUpdateStreamsOneFramePerStep(t *testing.T) {
	r := &Region{}
	moving := replica.Record{Entity: 5, Kind: replica.RecordKindPlayer, Name: "Hero",
		X: 100, Y: 100, DestX: 500, DestY: 100, Moving: true, Heading: 16384}
	standing := moving
	standing.Moving = false
	for _, tc := range []struct {
		name string
		rec  replica.Record
		want byte
	}{
		{"движущейся", moving, opCharMoveToLocation},
		{"стоячей", standing, opStopMove},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pushes := r.composeJoin([]replica.Event{
				{Obs: replica.Observer{ConnID: 9}, Target: tc.rec, Kind: replica.EventUpdate},
			})
			if len(pushes) != 1 || pushOp(pushes[0]) != tc.want {
				t.Fatalf("кадров %d с глаголом %#x; want 1 с %#x", len(pushes), pushOp(pushes[0]), tc.want)
			}
			if tc.name == "стоячей" {
				// Пейлоад, не только глагол: наблюдатель получает поворот
				// записи (heading — 5-е D после опкода и objID/x/y/z).
				if got := leD32(pushes[0].Frame[17:]); got != 16384 {
					t.Errorf("StopMove heading наблюдателю = %d; want 16384 (живой поворот записи)", got)
				}
			}
		})
	}
}

// leD32 — младшие байты вперёд (разбор пейлоада кадра в тесте).
func leD32(b []byte) int32 {
	return int32(uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24)
}

func TestRecordOfCarriesLiveHeadingAndDest(t *testing.T) {
	e := &Entity{ID: 7, Pos: Position{X: 10, Y: 20, Z: 30}, Dest: Position{X: 110, Y: 20, Z: 30},
		Heading: 4321, Moving: true, Player: &Player{Rec: mkRec("acc", "hero", 0)}}
	e.Player.Rec.Heading = 999 // персист-слепок отличается от живого
	rec := recordOf(e)
	if rec.Heading != 4321 || rec.DestX != 110 || rec.DestY != 20 || rec.DestZ != 30 || !rec.Moving {
		t.Fatalf("recordOf: heading %d dest (%d,%d,%d) moving %v; want живые значения",
			rec.Heading, rec.DestX, rec.DestY, rec.DestZ, rec.Moving)
	}
}

func TestRegionMovementStreamToObserver(t *testing.T) {
	r, pc := moveRegion(t)
	a := spawnPlayer(t, r, 1, syncPos.X, syncPos.Y)
	spawnPlayer(t, r, 2, syncPos.X+100, syncPos.Y) // в enter-радиусе
	stepN(r, 2)                                    // знакомство
	pc.Reset()
	b := make([]byte, 29)
	b[0] = 0x01
	writeMoveFrame(b, syncPos.X+2000, syncPos.Y, syncPos.Z, syncPos.X, syncPos.Y, syncPos.Z)
	sendToBox(r, a, b)
	stepN(r, 3) // движение идёт
	movesA, movesB := countOp(pc, 1, opCharMoveToLocation), countOp(pc, 2, opCharMoveToLocation)
	if movesA != 1 {
		t.Fatalf("эхо себе = %d; want 1 (self-стрим не идёт)", movesA)
	}
	if movesB != 3 {
		t.Fatalf("стрим наблюдателю = %d; want 3 (по кадру на шаг)", movesB)
	}
	if stops := countOp(pc, 2, opStopMove); stops != 0 {
		t.Fatalf("StopMove до прибытия = %d; want 0", stops)
	}
}

// writeMoveFrame — клиентский кадр MoveToLocation в буфер (тестовый писатель).
func writeMoveFrame(dst []byte, tx, ty, tz, ox, oy, oz int32) {
	dst[0] = 0x01
	le := func(i int, v int32) {
		dst[1+4*i] = byte(v)
		dst[2+4*i] = byte(v >> 8)
		dst[3+4*i] = byte(v >> 16)
		dst[4+4*i] = byte(v >> 24)
	}
	le(0, tx)
	le(1, ty)
	le(2, tz)
	le(3, ox)
	le(4, oy)
	le(5, oz)
	le(6, 0)
}

func TestRegionArrivalStopMoveBroadcast(t *testing.T) {
	r, pc := moveRegion(t)
	a := spawnPlayer(t, r, 1, syncPos.X, syncPos.Y)
	spawnPlayer(t, r, 2, syncPos.X+100, syncPos.Y)
	stepN(r, 2)
	pc.Reset()
	b := make([]byte, 29)
	writeMoveFrame(b, syncPos.X+50, syncPos.Y, syncPos.Z, syncPos.X, syncPos.Y, syncPos.Z)
	sendToBox(r, a, b)
	stepN(r, 8) // 50 юн ≈ 5 тиков при 115 юн/с
	if got := countOp(pc, 1, opStopMove); got != 1 {
		t.Fatalf("StopMove себе = %d; want 1", got)
	}
	if got := countOp(pc, 2, opStopMove); got != 1 {
		t.Fatalf("StopMove наблюдателю = %d; want 1", got)
	}
	pc.Reset()
	stepN(r, 1)
	if got := countOp(pc, 2, opCharMoveToLocation); got != 0 {
		t.Fatalf("стрим после остановки = %d; want 0", got)
	}
	// повторный ввод после остановки (умышленная перезаводка известности):
	// CharInfo стоячей записи — Standing=true
	spawnPlayer(t, r, 3, syncPos.X+200, syncPos.Y)
	stepN(r, 1)
	var intro *FramePush
	for i, p := range pc.Snapshot() {
		if p.Client == 3 && pushOp(p) == 0x03 {
			intro = &pc.Snapshot()[i]
			break
		}
	}
	if intro == nil {
		t.Fatalf("ввод новичку после остановки не доставлен")
	}
	v, ok := protocol.NewCharInfoView(intro.Frame)
	if !ok || !v.Standing() || !v.Running() {
		t.Fatalf("CharInfo остановившегося: Standing=%v Running=%v; want true, true", v.Standing(), v.Running())
	}
}

func TestRegionObserverOutsideRadiusSilent(t *testing.T) {
	r, pc := moveRegion(t)
	a := spawnPlayer(t, r, 1, syncPos.X, syncPos.Y)
	spawnPlayer(t, r, 2, syncPos.X+200, syncPos.Y)  // свидетель: в радиусе
	spawnPlayer(t, r, 3, syncPos.X+5000, syncPos.Y) // вне enter-радиуса 3500
	stepN(r, 2)
	pc.Reset()
	b := make([]byte, 29)
	writeMoveFrame(b, syncPos.X+2000, syncPos.Y, syncPos.Z, syncPos.X, syncPos.Y, syncPos.Z)
	sendToBox(r, a, b)
	stepN(r, 3)
	if got := countOp(pc, 3, opCharMoveToLocation) + countOp(pc, 3, opStopMove) + countOp(pc, 3, 0x03); got != 0 {
		t.Fatalf("кадров движения далёкому C = %d; want 0", got)
	}
	if got := countOp(pc, 2, opCharMoveToLocation); got == 0 {
		t.Fatalf("свидетель B в радиусе не получил стрим (молчание не взаимное)")
	}
}

func TestRegionMovementPanicTailDurable(t *testing.T) {
	r, pc := moveRegion(t)
	a := spawnPlayer(t, r, 1, syncPos.X, syncPos.Y)
	spawnPlayer(t, r, 2, syncPos.X+100, syncPos.Y)
	stepN(r, 2)
	b := make([]byte, 29)
	writeMoveFrame(b, syncPos.X+2000, syncPos.Y, syncPos.Z, syncPos.X, syncPos.Y, syncPos.Z)
	sendToBox(r, a, b)
	stepN(r, 1) // движение идёт, стрим течёт
	pc.Reset()  // считаем только паник-шаг и последующие
	r.forcePanic.Store(uint32(phaseB))
	r.metro.tick.Add(1)
	r.safeStep() // recover-политика региона (паника фазы B: кадры — хвостом)
	r.forcePanic.Store(0)
	if r.Stats().Failed < 1 {
		t.Fatalf("паника не учтена: %+v", r.Stats())
	}
	// хвост паник-шага доставлен recovered немедленно: ровно один кадр шага
	if got := countOp(pc, 2, opCharMoveToLocation); got != 1 {
		t.Fatalf("хвост паник-шага = %d кадров; want 1 (доставка recovered, без потери)", got)
	}
	stepN(r, 2)
	if got := countOp(pc, 2, opCharMoveToLocation); got != 3 { // +1 на шаг — стрим продолжается, без дублей
		t.Fatalf("стрим после паники = %d кадров; want 3 (1 хвост + 2 шага)", got)
	}
}

func TestRegionRequiresGeoMap(t *testing.T) {
	m, err := NewMetronome(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	reg := transport.NewRegistry(0)
	log, err := NewPortionLog(t.TempDir(), 1, m.period, false, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	if _, err := NewRegion(m, reg, 1, DefaultConfig(), log, nullPusher{}, nil); err == nil {
		t.Fatal("NewRegion(nil-карта) прошёл молча")
	}
}

func TestRegionFinalSaveSnapshotWithHeading(t *testing.T) {
	h := newEnterHarness(t, DefaultConfig())
	h.enterConn(t, 1, mkRecAt("acc", "hero", int(syncPos.X), int(syncPos.Y)))
	id := transport.EntityID(playerEntity(h, 1))
	b := make([]byte, 29)
	writeMoveFrame(b, syncPos.X+2000, syncPos.Y, syncPos.Z, syncPos.X, syncPos.Y, syncPos.Z)
	sendToBox(h.r, id, b)
	waitTick(t, h.r)
	h.rCancel()
	<-h.rDone // quiesce: чтение жителей региона только после выхода Run
	res := sort.Search(len(h.r.residents), func(i int) bool { return h.r.residents[i].ent.ID >= id })
	if res >= len(h.r.residents) || h.r.residents[res].ent.ID != id {
		t.Fatalf("житель %d не найден", id)
	}
	ent := h.r.residents[res].ent
	wantX, wantH := ent.Pos.X, ent.Heading
	saves := 0
	for _, env := range h.pBox.ExtractInto(h.pToken, nil) {
		if env.Kind != transport.KindPersistRequest {
			continue
		}
		req, err := persist.DecodeRequest(env.Payload)
		if err != nil || req.Op != persist.OpSaveChar {
			continue
		}
		saves++
		if req.Char.X != int(wantX) {
			t.Errorf("снимок X = %d; want %d (текущая позиция, не Dest)", req.Char.X, int(wantX))
		}
		if req.Char.Heading != int(wantH) {
			t.Errorf("снимок heading = %d; want %d (живой)", req.Char.Heading, int(wantH))
		}
	}
	if saves == 0 {
		t.Fatal("финальное сохранение не отправлено")
	}
}

func TestRegionMovementLettersUnderLiveRun(t *testing.T) {
	// живой Run: смесь писем движения/валидации — детектор гонок (ворота -race)
	h := newEnterHarness(t, DefaultConfig())
	h.enterConn(t, 1, mkRecAt("acc", "hero", int(syncPos.X), int(syncPos.Y)))
	id := transport.EntityID(playerEntity(h, 1))
	for i := range 20 {
		b := make([]byte, 29)
		dx := int32(2000)
		if i%2 == 1 {
			dx = -2000
		}
		writeMoveFrame(b, syncPos.X+dx, syncPos.Y, syncPos.Z, syncPos.X, syncPos.Y, syncPos.Z)
		sendToBox(h.r, id, b)
		v := make([]byte, 21)
		v[0] = 0x48
		sendToBox(h.r, id, v)
		waitTick(t, h.r)
	}
	if h.r.Stats().Failed != 0 {
		t.Fatalf("провалы шагов: %+v", h.r.Stats())
	}
}
