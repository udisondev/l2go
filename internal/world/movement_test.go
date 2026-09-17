package world

// Юнит-тесты кинематики и анти-чита свёртки (план P3.9, группы A–C тест-плана).
// Оракулы — значения полей Entity/Player, метрики State, кадры res.Pushes.

import (
	"encoding/binary"
	"math/rand/v2"
	"testing"

	"github.com/udisondev/l2go/internal/geo"
	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/replica"
	"github.com/udisondev/l2go/internal/transport"
)

// Опкоды GS→C-кадров движения для разбора пушей в тестах (канон Interlude,
// пиннут golden-тестами P3.5; в пакете не экспортируются — вне потребностей
// прод-кода).
const (
	opCharMoveToLocation = 0x01
	opStopMove           = 0x47
	opValidateLocation   = 0x61
	opActionFailed       = 0x25
)

// moveLetter — клиентский кадр MoveToLocation в конверте ящика сущности.
func moveLetter(id transport.EntityID, tx, ty, tz int32) transport.Envelope {
	b := make([]byte, protocol.MoveToLocationSize)
	protocol.WriteMoveToLocation(b, tx, ty, tz, 0, 0, 0, 0)
	return transport.Envelope{
		To: transport.Addr{Entity: id}, FromID: testRules().Gateway,
		Kind: transport.KindClientFrame, Payload: b,
	}
}

func validateLetter(id transport.EntityID, x, y, z, heading int32) transport.Envelope {
	b := make([]byte, protocol.ValidatePositionSize)
	protocol.WriteValidatePosition(b, x, y, z, heading, 0)
	return transport.Envelope{
		To: transport.Addr{Entity: id}, FromID: testRules().Gateway,
		Kind: transport.KindClientFrame, Payload: b,
	}
}

func cannotMoveLetter(id transport.EntityID, x, y, z, heading int32) transport.Envelope {
	b := make([]byte, protocol.CannotMoveAnymoreSize)
	protocol.WriteCannotMoveAnymore(b, x, y, z, heading)
	return transport.Envelope{
		To: transport.Addr{Entity: id}, FromID: testRules().Gateway,
		Kind: transport.KindClientFrame, Payload: b,
	}
}

// foldM — шаг свёртки с картой и delta (движение требует и то и другое).
func foldM(tick Tick, delta uint64, st *State, ents []*Entity, gm *geo.Map, envs ...transport.Envelope) StepResult {
	return Fold(tick, delta, rand.New(rand.NewPCG(1, uint64(tick))), st, ents,
		portion(envs...), nil, testRules(), gm)
}

// playerEnt — житель-игрок на заданной позиции.
func playerEnt(id transport.EntityID, x, y, z int32) *Entity {
	return &Entity{
		ID: id, Owner: 1, Pos: Position{X: x, Y: y, Z: z},
		Heading: 1, HP: 100,
		Player: &Player{Rec: mkRec("acc", "hero", 0), ConnID: 7},
	}
}

// pushOp — опкод первого байта кадра (0 — пуши нет).
func pushOp(p FramePush) byte {
	if len(p.Frame) == 0 {
		return 0
	}
	return p.Frame[0]
}

// findPush — первый пуш клиента с данным опкодом.
func findPush(res StepResult, client uint64, op byte) (FramePush, bool) {
	for _, p := range res.Pushes {
		if p.Client == client && pushOp(p) == op {
			return p, true
		}
	}
	return FramePush{}, false
}

// pushD — int32-поле кадра по индексу (после опкода, LE; раскладка писателей P3.5).
func pushD(p FramePush, i int) int32 {
	return int32(binary.LittleEndian.Uint32(p.Frame[1+4*i:]))
}

// startMove — прямой старт отрезка без гео-клампа (arrange для advance-тестов).
func startMove(e *Entity, dx, dy, dz int32) {
	e.Moving = true
	e.Dest = Position{X: e.Pos.X + dx, Y: e.Pos.Y + dy, Z: e.Pos.Z + dz}
	e.MoveFrom = e.Pos
	e.MoveDist = distMilli(e.MoveFrom, e.Dest)
	e.MoveDone = 0
	e.Heading = calcHeading(e.MoveFrom, e.Dest)
}

// TestFoldCannotMoveAnymoreHeadingNormalization — домен [0,65536) на любом
// int32 письма: маска, не знаконосный Go-% (F6/F17), включая край 65536→0.
func TestFoldCannotMoveAnymoreHeadingNormalization(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		heading int32
		want    int32
	}{
		{70000, 4464},
		{-70000, 61072},
		{65535, 65535},
		{65536, 0},
		{0, 0},
	} {
		st := newState()
		ents := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
		startMove(ents[0], 500, 0, 0)
		foldM(10, 0, st, ents, emptyGeo, cannotMoveLetter(101, syncPos.X+10, syncPos.Y, 0, tc.heading))
		if got := ents[0].Heading; got != tc.want {
			t.Errorf("heading(%d) = %d; want %d", tc.heading, got, tc.want)
		}
	}
}

// TestFoldBirthSpeedBucketAtCAP — рождение наливает бакет до CAP (иначе честный
// вход флагался бы первым отчётом).
func TestFoldBirthSpeedBucketAtCAP(t *testing.T) {
	t.Parallel()
	st := newState()
	rec := mkRecAt("bca", "Hero", int(syncPos.X), int(syncPos.Y))
	res := Fold(10, 1, rand.New(rand.NewPCG(1, 10)), st, []*Entity{},
		portion(enterMsg(1, "bca", rec)), nil, testRules(), emptyGeo)
	if len(res.Births) != 1 {
		t.Fatalf("рождение: %d", len(res.Births))
	}
	b := applyBirth(st, res)
	if b[0].Player.SpeedBudget != speedCAP {
		t.Fatalf("бакет рождения = %d; want CAP %d", b[0].Player.SpeedBudget, speedCAP)
	}
	if b[0].Player.PendingTeleport != true {
		t.Fatalf("PendingTeleport при рождении не поставлен")
	}
}

// TestFoldHeadingDomainAtBirth — запись персиста за trust-границей: heading
// рождения нормализуется в домен [0,65536).
func TestFoldHeadingDomainAtBirth(t *testing.T) {
	t.Parallel()
	st := newState()
	rec := mkRecAt("bcb", "Hero", int(syncPos.X), int(syncPos.Y))
	rec.Heading = -70000
	res := Fold(10, 1, rand.New(rand.NewPCG(1, 10)), st, []*Entity{},
		portion(enterMsg(1, "bcb", rec)), nil, testRules(), emptyGeo)
	ents := applyBirth(st, res)
	if ents[0].Heading != 61072 {
		t.Fatalf("heading рождения = %d; want 61072 (маска домена)", ents[0].Heading)
	}
}

// syncPos — позиция, безопасная для старта (центр ячейки синтетического мира).
var syncPos = Position{X: int32(geo.GeoToWorldX(16*2048 + 100)), Y: int32(geo.GeoToWorldY(16*2048 + 100)), Z: 0}

func TestFoldMoveStartsSameStep(t *testing.T) {
	t.Parallel()
	st := newState()
	ents := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
	res := foldM(10, 1, st, ents, emptyGeo, moveLetter(101, syncPos.X+115, syncPos.Y, syncPos.Z))
	e := ents[0]
	if !e.Moving || e.MoveDone != 11500 || e.MoveDist != 115000 {
		t.Fatalf("старт = moving %v, done %d, dist %d; want true, 11500, 115000", e.Moving, e.MoveDone, e.MoveDist)
	}
	p, ok := findPush(res, 7, opCharMoveToLocation)
	if !ok {
		t.Fatalf("эхо CharMoveToLocation себе отсутствует; pushes %v", res.Pushes)
	}
	if got := pushD(p, 1); got != syncPos.X+115 { // dstX — поле 1 после objID
		t.Errorf("эхо dstX = %d; want %d (клампнутая цель)", got, syncPos.X+115)
	}
}

func TestFoldAdvanceArrivalTable(t *testing.T) {
	t.Parallel()
	// 115 юн при runSpeed 115 и периоде 100 мс: прибытие ровно на 10-м тике;
	// 120 юн — перелёт MoveDone>MoveDist на 11-м. Интерполяция от From — без
	// накопления ошибки округления.
	for _, tc := range []struct {
		name  string
		dist  int32
		ticks int
		wantX int32 // позиция на прибытии — точно Dest
	}{
		{"ровно", 115, 10, 115},
		{"перелёт", 120, 11, 120},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newState()
			ents := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
			startMove(ents[0], tc.dist, 0, 0)
			var res StepResult
			for i := range tc.ticks {
				res = foldM(10+Tick(i), 1, st, ents, emptyGeo)
			}
			e := ents[0]
			if e.Moving || e.Pos.X != syncPos.X+tc.wantX || e.MoveDist != 0 || e.MoveDone != 0 {
				t.Fatalf("прибытие: moving %v pos %d dist %d done %d; want false, %d, 0, 0",
					e.Moving, e.Pos.X-syncPos.X, e.MoveDist, e.MoveDone, tc.wantX)
			}
			if _, ok := findPush(res, 7, opStopMove); !ok {
				t.Errorf("StopMove себе на прибытии отсутствует")
			}
			// промежуточная позиция — интерполяция от From (доля пройденного)
			ents2 := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
			startMove(ents2[0], 115, 0, 0)
			for i := range 5 {
				foldM(10+Tick(i), 1, st, ents2, emptyGeo)
			}
			if got := ents2[0].Pos.X - syncPos.X; got != 57 { // floor(115·5/10)
				t.Errorf("позиция на 5-м тике = %d; want 57", got)
			}
		})
	}
}

func TestFoldAdvanceAfterTickDropDelta2(t *testing.T) {
	t.Parallel()
	st := newState()
	ents := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
	startMove(ents[0], 115, 0, 0)
	foldM(10, 2, st, ents, emptyGeo) // дроп тика: 200 мс одним шагом
	if got := ents[0].MoveDone; got != 23000 {
		t.Fatalf("MoveDone после delta=2 = %d; want 23000", got)
	}
	if got := ents[0].Pos.X - syncPos.X; got != 23 { // floor(115·2/10)
		t.Errorf("Pos.X после delta=2 = %d; want 23", got)
	}
}

func TestFoldAdvanceFollowsPeriodNotTickCount(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		periodNS int64
		want     int64
	}{
		{"канон 10 Гц", 100_000_000, 11500},
		{"200 мс", 200_000_000, 23000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newState()
			ents := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
			startMove(ents[0], 115, 0, 0)
			rules := testRules()
			rules.PeriodNS = tc.periodNS
			Fold(10, 1, rand.New(rand.NewPCG(1, 10)), st, ents, portion(), nil, rules, emptyGeo)
			if got := ents[0].MoveDone; got != tc.want {
				t.Errorf("MoveDone при периоде %d нс = %d; want %d (дистанция следует периоду, не числу тиков)",
					tc.periodNS, got, tc.want)
			}
		})
	}
}

func TestFoldRetargetFromAuthoritativePosition(t *testing.T) {
	t.Parallel()
	st := newState()
	ents := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
	startMove(ents[0], 115, 0, 0)
	for i := range 3 {
		foldM(10+Tick(i), 1, st, ents, emptyGeo)
	}
	cur := ents[0].Pos
	res := foldM(13, 1, st, ents, emptyGeo, moveLetter(101, syncPos.X-100, syncPos.Y, syncPos.Z))
	e := ents[0]
	// ретаргет тем же шагом уезжает первым тиком нового отрезка (письма → advance)
	if e.MoveFrom != cur || e.MoveDone != 11500 || !e.Moving {
		t.Fatalf("ретаргет: from %v (want %v), done %d, moving %v", e.MoveFrom, cur, e.MoveDone, e.Moving)
	}
	p, ok := findPush(res, 7, opCharMoveToLocation)
	if !ok {
		t.Fatalf("эхо ретаргета отсутствует")
	}
	if got := pushD(p, 4); got != cur.X { // curX — поле 4 (objID + dst 3D)
		t.Errorf("эхо ретаргета curX = %d; want %d (авторитетная позиция)", got, cur.X)
	}
}

func TestFoldMoveZeroAndNearZeroDistance(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		dx   int32
		want bool // ожидаемый старт
	}{
		{"в себя", 0, false},
		{"в той же ячейке", 8, false},
		{"за ячейку", 24, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newState()
			ents := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
			res := foldM(10, 1, st, ents, emptyGeo, moveLetter(101, syncPos.X+tc.dx, syncPos.Y, syncPos.Z))
			if ents[0].Moving != tc.want {
				t.Fatalf("старт при dx=%d: moving %v; want %v", tc.dx, ents[0].Moving, tc.want)
			}
			if !tc.want {
				if _, ok := findPush(res, 7, opStopMove); !ok {
					t.Errorf("немедленный StopMove отсутствует (кламп ≈ текущей)")
				}
				if _, ok := findPush(res, 7, opActionFailed); !ok {
					t.Errorf("ActionFailed отсутствует (канон отвечает на отказ — молчание клинит инпут клиента, KT4-4)")
				}
			}
		})
	}
}

func TestFoldMovementNegativeDirection(t *testing.T) {
	t.Parallel()
	st := newState()
	ents := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
	startMove(ents[0], -115, 0, 0)
	for i := range 5 {
		foldM(10+Tick(i), 1, st, ents, emptyGeo)
	}
	if got := ents[0].Pos.X - syncPos.X; got != -57 {
		t.Errorf("запад: позиция 5-го тика = %d; want -57", got)
	}
	for i := range 5 {
		foldM(15+Tick(i), 1, st, ents, emptyGeo)
	}
	if ents[0].Moving || ents[0].Pos.X != syncPos.X-115 {
		t.Errorf("прибытие на запад: moving %v pos %d; want false, %d",
			ents[0].Moving, ents[0].Pos.X-syncPos.X, int32(-115))
	}
}

func TestFoldSpeedBudgetRefillClamp(t *testing.T) {
	t.Parallel()
	st := newState()
	ents := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
	ents[0].Player.SpeedBudget = speedCAP - 20000
	foldM(10, 1, st, ents, emptyGeo)
	if got := ents[0].Player.SpeedBudget; got != speedCAP-8500 {
		t.Errorf("refill = %d; want %d", got, speedCAP-8500)
	}
	foldM(11, 1, st, ents, emptyGeo)
	if got := ents[0].Player.SpeedBudget; got != speedCAP {
		t.Errorf("clamp = %d; want CAP %d", got, speedCAP)
	}
}

func TestFoldTwoArrivalsSameStep(t *testing.T) {
	t.Parallel()
	run := func() (int, []byte) {
		st := newState()
		ents := []*Entity{
			playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z),
			playerEnt(102, syncPos.X, syncPos.Y+1000, syncPos.Z),
		}
		ents[1].Player.ConnID = 8
		startMove(ents[0], 115, 0, 0)
		startMove(ents[1], 0, 115, 0)
		var res StepResult
		for i := range 10 {
			res = foldM(10+Tick(i), 1, st, ents, emptyGeo)
		}
		stops := 0
		for _, p := range res.Pushes {
			if pushOp(p) == opStopMove {
				stops++
			}
		}
		if ents[0].Moving || ents[1].Moving {
			t.Fatalf("парные прибытия: moving %v/%v; want false/false", ents[0].Moving, ents[1].Moving)
		}
		return stops, st.Dump(ents)
	}
	stops, dump1 := run()
	_, dump2 := run()
	if string(dump1) != string(dump2) {
		t.Errorf("два независимых прогона расходятся (порядок эффектов по id недетерминирован)")
	}
	if stops != 2 {
		t.Fatalf("парные прибытия: StopMove %d; want 2", stops)
	}
}

func TestFoldMoveInFirstStepAfterSleepDelta0(t *testing.T) {
	t.Parallel()
	st := newState()
	ents := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
	res := foldM(10, 0, st, ents, emptyGeo, moveLetter(101, syncPos.X+115, syncPos.Y, syncPos.Z))
	e := ents[0]
	if !e.Moving || e.MoveDone != 0 || e.Pos != syncPos {
		t.Fatalf("первый шаг после сна: moving %v done %d pos %v; want true, 0, %v",
			e.Moving, e.MoveDone, e.Pos, syncPos)
	}
	if _, ok := findPush(res, 7, opCharMoveToLocation); !ok {
		t.Errorf("эхо старта отсутствует")
	}
}

func TestFoldValidatePositionSpeedhackTable(t *testing.T) {
	t.Parallel()
	// Дебет бакета = расхождение отчёта с authPos (не пройденный путь):
	// честный клиент с джиттером/латентностью не осушает бакет никогда,
	// скачок ×2–3 флагается после исчерпания CAP+SLACK. Отчёты ~1/с (канон).
	for _, tc := range []struct {
		name     string
		reportFn func(authX, progress int32) int32 // отчёт по позиции и прогрессу сервера
		wantFlag bool
		wantSnap bool
	}{
		{"честный", func(a, p int32) int32 { return a }, false, false},
		{"джиттер ±10%", func(a, p int32) int32 {
			if p%2 == 0 {
				return a + 11 // ±10% скорости за секунду
			}
			return a - 11
		}, false, false},
		{"латентность 300 мс", func(a, p int32) int32 { return a - 34 }, false, false},
		{"×2", func(a, p int32) int32 { return a + p }, true, true},
		{"×3", func(a, p int32) int32 { return a + 2*p }, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newState()
			ents := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
			startMove(ents[0], 2000, 0, 0) // длинный забег
			var res StepResult
			for i := range 30 {
				var envs []transport.Envelope
				if i%10 == 9 { // отчёт раз в секунду
					progress := ents[0].Pos.X - syncPos.X
					rx := tc.reportFn(ents[0].Pos.X, progress)
					envs = append(envs, validateLetter(101, rx, syncPos.Y, syncPos.Z, 0))
				}
				res = foldM(10+Tick(i), 1, st, ents, emptyGeo, envs...)
			}
			if got := st.SpeedFlags > 0; got != tc.wantFlag {
				t.Errorf("SpeedFlags = %d; want flag %v", st.SpeedFlags, tc.wantFlag)
			}
			if got := st.SnapBacks > 0; got != tc.wantSnap {
				t.Errorf("SnapBacks = %d; want snap %v", st.SnapBacks, tc.wantSnap)
			}
			if tc.wantSnap {
				if _, ok := findPush(res, 7, opValidateLocation); !ok {
					t.Errorf("snap-back ValidateLocation отсутствует")
				}
			}
		})
	}
}

func TestFoldValidatePositionDriftSnapBack(t *testing.T) {
	t.Parallel()
	// прыжок 400 юн: расхождение сверх порога канона — флаг класса
	// спидхака + коррекция; позиция сервера не мутирована отчётом
	st := newState()
	ents := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
	res := foldM(10, 1, st, ents, emptyGeo, validateLetter(101, syncPos.X+400, syncPos.Y, syncPos.Z, 0))
	if st.SnapBacks != 1 || st.SpeedFlags != 1 {
		t.Fatalf("прыжок: SnapBacks %d SpeedFlags %d; want 1, 1", st.SnapBacks, st.SpeedFlags)
	}
	if _, ok := findPush(res, 7, opValidateLocation); !ok {
		t.Errorf("ValidateLocation отсутствует")
	}
	if ents[0].Pos.X != syncPos.X {
		t.Errorf("позиция сервера сместилась: %v", ents[0].Pos)
	}
}

func TestFoldValidatePositionBoundaries(t *testing.T) {
	t.Parallel()
	// системная граница флага из полного бакета: CAP+SLACK = 287.5 юн
	// (дрейф сверх неё выжигает бакет ниже −SLACK тем же отчётом)
	for _, tc := range []struct {
		name     string
		dx       int32 // смещение отчёта
		budget   int64 // стартовый бакет
		wantSnap bool
		wantFlag bool
	}{
		{"дрейф 287 — терпимо", 287, speedCAP, false, false},
		{"дрейф 288 — флаг", 288, speedCAP, true, true},
		{"бакет −SLACK ровно", 1, -speedSLACK + 1000, false, false},
		{"бакет за SLACK", 1, -speedSLACK, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newState()
			ents := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
			ents[0].Player.SpeedBudget = tc.budget
			foldM(10, 0, st, ents, emptyGeo, validateLetter(101, syncPos.X+tc.dx, syncPos.Y, syncPos.Z, 0))
			if got := st.SnapBacks > 0; got != tc.wantSnap {
				t.Errorf("SnapBacks = %d; want %v", st.SnapBacks, tc.wantSnap)
			}
			if got := st.SpeedFlags > 0; got != tc.wantFlag {
				t.Errorf("SpeedFlags = %d; want %v", st.SpeedFlags, tc.wantFlag)
			}
		})
	}
}

func TestFoldPendingTeleportBucketReset(t *testing.T) {
	t.Parallel()
	// рождение ставит флаг (P3.7); первый отчёт рядом гасит и наливает бакет
	st := newState()
	ents := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
	ents[0].Player.PendingTeleport = true
	ents[0].Player.SpeedBudget = 0
	foldM(10, 0, st, ents, emptyGeo, validateLetter(101, syncPos.X+200, syncPos.Y, syncPos.Z, 0))
	if ents[0].Player.PendingTeleport || ents[0].Player.SpeedBudget != speedCAP {
		t.Fatalf("гашение: флаг %v бакет %d; want false, CAP", ents[0].Player.PendingTeleport, ents[0].Player.SpeedBudget)
	}
	// далеко — snap-back, флаг держится до близкого отчёта
	st2 := newState()
	ents2 := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
	ents2[0].Player.PendingTeleport = true
	res := foldM(10, 0, st2, ents2, emptyGeo, validateLetter(101, syncPos.X+500, syncPos.Y, syncPos.Z, 0))
	if !ents2[0].Player.PendingTeleport || st2.SnapBacks != 1 {
		t.Fatalf("далёкий отчёт: флаг %v snap %d; want true, 1", ents2[0].Player.PendingTeleport, st2.SnapBacks)
	}
	if _, ok := findPush(res, 7, opValidateLocation); !ok {
		t.Errorf("snap-back отсутствует")
	}
	foldM(11, 0, st2, ents2, emptyGeo, validateLetter(101, syncPos.X+50, syncPos.Y, syncPos.Z, 0))
	if ents2[0].Player.PendingTeleport {
		t.Errorf("флаг не погашен близким отчётом")
	}
}

func TestFoldCannotMoveAnymoreTable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		moving   bool
		dx       int32
		heading  int32
		wantH    int32
		wantNoop bool
	}{
		{"в движении рядом", true, 10, 70000, 4464, false},
		{"в движении дрейф", true, 400, -70000, 61072, false},
		{"не в движении", false, 10, 65536, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newState()
			ents := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
			if tc.moving {
				startMove(ents[0], 500, 0, 0)
			}
			res := foldM(10, 0, st, ents, emptyGeo,
				cannotMoveLetter(101, syncPos.X+tc.dx, syncPos.Y, syncPos.Z, tc.heading))
			e := ents[0]
			if tc.wantNoop {
				if st.CannotMoveNoops != 1 || len(res.Pushes) != 0 {
					t.Fatalf("no-op: счётчик %d пуши %d; want 1, 0", st.CannotMoveNoops, len(res.Pushes))
				}
				return
			}
			if e.Moving || e.Dest != e.Pos {
				t.Fatalf("остановка: moving %v dest %v", e.Moving, e.Dest)
			}
			if e.Heading != tc.wantH {
				t.Errorf("heading = %d; want %d (нормализация маской)", e.Heading, tc.wantH)
			}
			if _, ok := findPush(res, 7, opStopMove); !ok {
				t.Errorf("StopMove отсутствует")
			}
			if tc.dx > 300 {
				if st.SnapBacks != 1 {
					t.Errorf("дрейф упора: SnapBacks %d; want 1", st.SnapBacks)
				}
			}
		})
	}
}

func TestFoldClientFrameEvilInputsTable(t *testing.T) {
	t.Parallel()
	base := struct{ tx, ty int32 }{syncPos.X + 100, syncPos.Y}
	full := func(op byte, n int) []byte {
		b := make([]byte, n)
		b[0] = op
		return b
	}
	trunc := func(op byte, n int) []byte { return full(op, n)[:n-1] }
	cases := []struct {
		name string
		env  transport.Envelope
	}{
		{"обрезанный move", envFrame(101, trunc(0x01, protocol.MoveToLocationSize))},
		{"обрезанный validate", envFrame(101, trunc(0x48, protocol.ValidatePositionSize))},
		{"обрезанный cannot", envFrame(101, trunc(0x36, protocol.CannotMoveAnymoreSize))},
		{"пустой payload", envFrame(101, nil)},
		{"цель вне мира", moveLetter(101, 1<<30, base.ty, syncPos.Z)},
		{"отчёт INT32_MIN", validateLetter(101, -(1 << 31), -(1 << 31), 0, 0)},
		{"упор вне мира", cannotMoveLetter(101, 1<<30, base.ty, 0, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newState()
			ents := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
			startMove(ents[0], 500, 0, 0)
			res := foldM(10, 0, st, ents, emptyGeo, tc.env) // дроп без паники
			if st.DroppedFrames != 1 {
				t.Fatalf("DroppedFrames = %d; want 1", st.DroppedFrames)
			}
			if !ents[0].Moving || len(res.Pushes) != 0 {
				t.Errorf("состояние тронуто злым входом: moving %v pushes %d", ents[0].Moving, len(res.Pushes))
			}
		})
	}
}

// envFrame — кадр клиента сырыми байтами (злые входы).
func envFrame(id transport.EntityID, payload []byte) transport.Envelope {
	return transport.Envelope{
		To: transport.Addr{Entity: id}, FromID: testRules().Gateway,
		Kind: transport.KindClientFrame, Payload: payload,
	}
}

func TestFoldMoveToLocationCapDropsBeyondK(t *testing.T) {
	t.Parallel()
	st := newState()
	var ents []*Entity
	var envs []transport.Envelope
	for i := range 65 {
		id := transport.EntityID(101 + i)
		ents = append(ents, playerEnt(id, syncPos.X, syncPos.Y+int32(10*i), syncPos.Z))
		envs = append(envs, moveLetter(id, syncPos.X+100, syncPos.Y+int32(10*i), syncPos.Z))
	}
	for i := range 5 { // невалидные: в K не входят (F23)
		envs = append(envs, envFrame(transport.EntityID(201+i), []byte{0x01}))
	}
	foldM(10, 0, st, ents, emptyGeo, envs...)
	moving := 0
	for _, e := range ents {
		if e.Moving {
			moving++
		}
	}
	if moving != moveLettersCap {
		t.Errorf("стартовано %d; want %d (кап K)", moving, moveLettersCap)
	}
	if st.DroppedFrames != 6 { // 65-е валидное + 5 обрезанных
		t.Errorf("DroppedFrames = %d; want 6", st.DroppedFrames)
	}
	// отказ сверх капа отвечает ActionFailed (KT4-4: молчание клинит инпут)
	res := foldM(10, 0, st, ents, emptyGeo, envs...)
	if _, ok := findPush(res, uint64(ents[64].Player.ConnID), opActionFailed); !ok {
		t.Errorf("ActionFailed сверх капа не отправлен")
	}
}

func TestFoldEnterWorldRecordOutOfGridDeadLetter(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		x    int
	}{
		{"заворачивается кастом", 1<<32 + int(syncPos.X)},
		{"вне сетки int64", 1_000_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newState()
			rec := mkRec("acc", "hero", 0)
			rec.X, rec.Y, rec.Z = tc.x, int(syncPos.Y), 0
			res := Fold(10, 1, rand.New(rand.NewPCG(1, 10)), st, []*Entity{},
				portion(enterMsg(1, "acc", rec)), nil, testRules(), emptyGeo)
			if len(res.Births) != 0 || st.DeadLetters != 1 {
				t.Fatalf("вход вне сетки: births %d dead %d; want 0, 1", len(res.Births), st.DeadLetters)
			}
		})
	}
}

func TestFoldLogoutResetsMoveSegmentFields(t *testing.T) {
	t.Parallel()
	st := newState()
	ents := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
	startMove(ents[0], 500, 0, 0)
	foldM(10, 0, st, ents, emptyGeo, clientFrame(101, protocol.OpLogout))
	e := ents[0]
	if e.Moving || e.Dest != e.Pos || e.MoveFrom != e.Pos || e.MoveDist != 0 || e.MoveDone != 0 {
		t.Fatalf("logout: поля отрезка не в стоячем виде: %+v", e)
	}
}

func TestFoldLinkDeadExpiryWhileMovingSnapshot(t *testing.T) {
	t.Parallel()
	st := newState()
	ents := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
	startMove(ents[0], 1150, 0, 0)
	ents[0].Heading = 4321
	st.ResolveBirth("acc", 7, 101)                   // тень коннекта (обычно материализует актор)
	foldM(10, 3, st, ents, emptyGeo, linkDeadMsg(7)) // обрыв в движении
	var res StepResult
	for i := range testRules().GraceTicks {
		res = foldM(11+Tick(i), 1, st, ents, emptyGeo)
	}
	e := ents[0]
	if e.Moving || e.MoveDist != 0 {
		t.Fatalf("экспирация: отрезок не погашен: %+v", e)
	}
	q := st.SaveQ["acc"]
	if q == nil {
		t.Fatalf("сохранение не поставлено")
	}
	if q.Char.X != int(e.Pos.X) || q.Char.Heading != 4321 {
		t.Errorf("снимок: X %d heading %d; want %d, 4321 (живые значения)", q.Char.X, q.Char.Heading, int(e.Pos.X))
	}
	for _, p := range res.Pushes {
		if pushOp(p) == opStopMove {
			t.Errorf("StopMove в мёртвый сокет на экспирации")
		}
	}
}

func TestFoldHeadingCalculationTable(t *testing.T) {
	t.Parallel()
	// осевые и диагональ: N=0, E=16384, S=32768, W=49152 (atan2(dx,dy)·65536/2π)
	cases := []struct {
		name   string
		dx, dy int32
		want   int32
	}{
		{"север", 0, 100, 0},
		{"восток", 100, 0, 16384},
		{"юг", 0, -100, 32768},
		{"запад", -100, 0, 49152},
		{"СВ", 100, 100, 8192},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from := syncPos
			to := Position{X: from.X + tc.dx, Y: from.Y + tc.dy, Z: from.Z}
			if got := calcHeading(from, to); got != tc.want {
				t.Errorf("calcHeading(dx=%d, dy=%d) = %d; want %d", tc.dx, tc.dy, got, tc.want)
			}
		})
	}
}

func TestFoldMoveToMaxDistanceNoOverflow(t *testing.T) {
	t.Parallel()
	st := newState()
	// противоположный угол сетки — обе точки InWorld, дистанция ~диагональ
	far := Position{X: int32(geo.GeoToWorldX(31*2048 + 2000)), Y: int32(geo.GeoToWorldY(31*2048 + 2000)), Z: 0}
	ents := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
	foldM(10, 0, st, ents, emptyGeo, moveLetter(101, far.X, far.Y, far.Z))
	e := ents[0]
	if !e.Moving || e.MoveDist <= 0 {
		t.Fatalf("макс-дистанция: moving %v dist %d; want true, >0 (без переполнения)", e.Moving, e.MoveDist)
	}
}

// TestFoldAdvanceSpeedMatchesCharInfoRunSpd — инвариант связки: скорость
// advance (runSpeed) обязана равняться RunSpd/MoveMultiplier из CharInfo —
// на ней держатся гладкость экстраполяции наблюдателей и нулевой дебет
// честного клиента; фаза 4 (баффы) тронет именно её.
func TestFoldAdvanceSpeedMatchesCharInfoRunSpd(t *testing.T) {
	t.Parallel()
	d := charInfoOf(replica.Record{Kind: replica.RecordKindPlayer})
	if want := int32(persist.HumanFighter.RunSpd); d.RunSpd != want || d.MoveMultiplier != 1.0 {
		t.Fatalf("CharInfo RunSpd=%d mult=%v; want %d, 1.0", d.RunSpd, d.MoveMultiplier, want)
	}
	// секунда бега покрывает ровно RunSpd юнитов
	st := newState()
	ents := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
	startMove(ents[0], 300, 0, 0)
	for i := range 10 {
		foldM(10+Tick(i), 1, st, ents, emptyGeo)
	}
	if got := ents[0].Pos.X - syncPos.X; got != 115 {
		t.Errorf("за 1 с пройдено %d юн; want %d (= RunSpd)", got, d.RunSpd)
	}
}

// TestUserInfoSpeedsNonZero — живой клиент KT-4: с нулевой скоростью в
// UserInfo собственный персонаж не двигается (клиент умножает локальную
// симуляцию на RunSpd/MoveMultiplier кадра). Связка — те же константы, что
// CharInfo и advance.
func TestUserInfoSpeedsNonZero(t *testing.T) {
	t.Parallel()
	st := newState()
	rec := mkRecAt("uiz", "Hero", int(syncPos.X), int(syncPos.Y))
	res := Fold(10, 1, rand.New(rand.NewPCG(1, 10)), st, []*Entity{},
		portion(enterMsg(1, "uiz", rec)), nil, testRules(), emptyGeo)
	ents := applyBirth(st, res)
	d := userInfoOf(ents[0].Player)
	if d.RunSpd != int32(persist.HumanFighter.RunSpd) || d.WalkSpd != int32(persist.HumanFighter.WalkSpd) {
		t.Fatalf("UserInfo RunSpd/WalkSpd = %d/%d; want %d/%d (шаблон)",
			d.RunSpd, d.WalkSpd, persist.HumanFighter.RunSpd, persist.HumanFighter.WalkSpd)
	}
	if d.MoveMultiplier != 1.0 || d.AttackSpeedMultiplier != 1.0 || !d.Running {
		t.Fatalf("UserInfo множители/режим = %v/%v/%v; want 1.0/1.0/run", d.MoveMultiplier, d.AttackSpeedMultiplier, d.Running)
	}
}

// TestFoldValidatePositionAdoptsZ — KT3-4: вертикаль рельефа клиента
// принимается стоящему в допуске (планар не трогаем — сервер-авторитетен);
// вне допуска и движущемуся — серверная Z сохраняется.
func TestFoldValidatePositionAdoptsZ(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		dz       int32
		want     int32 // итоговая Z сущности
		moving   bool
		wantSnap bool
	}{
		{name: "в допуске принят", dz: 400, want: 400},
		{name: "граница допуска", dz: -500, want: -500},
		{name: "вне допуска сохранён", dz: 900, want: 0},
		{name: "движущемуся не адаптируется", dz: 400, want: 0, moving: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := newState()
			ents := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, 0)}
			if tc.moving {
				startMove(ents[0], 300, 0, 0)
			}
			res := foldM(10, 1, st, ents, emptyGeo,
				validateLetter(101, syncPos.X, syncPos.Y, tc.dz, 0))
			if got := ents[0].Pos.Z; got != tc.want {
				t.Fatalf("Z = %d; want %d", got, tc.want)
			}
			if !tc.moving {
				if got := ents[0].Pos.X; got != syncPos.X {
					t.Fatalf("X мутирован отчётом: %d; want %d (планар сервер-авторитетен)", got, syncPos.X)
				}
			}
			_ = res
		})
	}
}
