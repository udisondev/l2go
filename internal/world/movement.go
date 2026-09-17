// Движение и анти-чит свёртки региона (P3.9, r1: сервер авторитетен над
// позицией — клиент шлёт намерение, сервер двигает по гео-валидированному
// пути и бродкастит свою позицию). Вся кинематика — здесь, в горутине
// владельца; гео — глобально-иммутабельная статика.

package world

import (
	"log/slog"
	"math"

	"github.com/udisondev/l2go/internal/encode"
	"github.com/udisondev/l2go/internal/geo"
	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/transport"
)

// runSpeed — скорость бега шаблона HumanFighter (канон Interlude: 115 юн/с;
// persist.HumanFighter.RunSpd). В милли-юнитах: runSpeed мЮ/мс = runSpeed·1000
// мЮ/с — все четыре точки таблицы единиц (advance/refill/CAP/дебет) сходятся
// на этом тождестве. Per-entity скорость (баффы, режимы) — фаза 4.
var runSpeed = int64(persist.HumanFighter.RunSpd)

var (
	// speedCAP — предел бакета: 2 с хода (r1 «~1–2 с»; верх диапазона).
	speedCAP = runSpeed * 2000
	// speedSLACK — порог флага спидхака: 0.5 с хода (r1 «порог»; тюнинг —
	// фаза 4 по ботам).
	speedSLACK = runSpeed * 500
	// driftMilli — порог дрейфа отчёта: 300 юн (интерлюд-референс
	// loop_movement.go: distSq > 90000).
	driftMilli = int64(300 * 1000)
	// zAdoptMilli — допуск адаптации Z отчёта: 500 юн (канон L2J
	// ValidatePosition: вертикаль клиента принимается в допуске). Сверх —
	// серверная Z сохраняется (вертикальный чит не прокрашивается).
	zAdoptMilli = int64(500 * 1000)
	// moveLettersCap — обработанных MoveToLocation-писем на шаг (r1 «лимит K
	// на тик»; 64 line-walk'а ≤ ~54 мкс по baseline P2.4). Сверх капа — дроп
	// с метрикой: defer-очередь задумана для NPC-ре-пасов осады (фаза 4), а
	// в свёртке дала бы FIFO-инверсию с немув-письмами той же пачки.
	moveLettersCap = 64
	// cellMilli — размер гео-ячейки в мЮ: кламп ближе ячейки = «уже стоим».
	cellMilli = int64(16 * 1000)
	// headingScale — 65536/2π (якорь L2J MathUtil.calculateHeading).
	headingScale = 10430.378350470453
)

// movement — параметры движения шага: гео-карта (аргумент свёртки — чистота,
// глобал запрещён) и остаток бюджета капа K.
type movement struct {
	gm     *geo.Map
	budget int
}

// foldMoveToLocation — интент движения: кламп гео от авторитетной позиции,
// старт отрезка или немедленный StopMove у стены. Эхо себе — из приватного
// состояния владельцем; наблюдатели получают кадры через AoI-запись.
func foldMoveToLocation(st *State, ent *Entity, env *transport.Envelope, mov *movement, res *StepResult) {
	v, ok := protocol.NewMoveToLocationView(env.Payload)
	if !ok {
		st.DroppedFrames++
		return
	}
	if !geo.InWorld(int(v.TargetX()), int(v.TargetY())) || !geo.InWorld(int(ent.Pos.X), int(ent.Pos.Y)) {
		st.DroppedFrames++
		return
	}
	if mov.budget <= 0 {
		// кап считают только валидные к обработке письма (F23)
		st.DroppedFrames++
		pushActionFailed(res, ent)
		return
	}
	mov.budget--
	// признак ok игнорируем осознанно: кламп — желаемая цель отрезка
	// (отказ = ближайшая достижимая точка того же пути)
	dest, _ := mov.gm.ValidLocation(
		geo.Loc{X: int(ent.Pos.X), Y: int(ent.Pos.Y), Z: int(ent.Pos.Z)},
		geo.Loc{X: int(v.TargetX()), Y: int(v.TargetY()), Z: int(v.TargetZ())})
	target := Position{X: int32(dest.X), Y: int32(dest.Y), Z: int32(dest.Z)}
	if distMilli(ent.Pos, target) < cellMilli {
		// кламп вернул текущую точку: остановка без старта, только себе —
		// наблюдатели о движении не знали (F29); ActionFailed — разблокировка
		// инпута клиента (канон MoveToLocation.java отвечает на каждый отказ)
		stopSegment(ent)
		pushStopMove(res, ent)
		pushActionFailed(res, ent)
		return
	}
	ent.Moving = true
	ent.Dest = target
	ent.MoveFrom = ent.Pos
	ent.MoveDist = distMilli(ent.MoveFrom, ent.Dest)
	ent.MoveDone = 0
	ent.Heading = calcHeading(ent.MoveFrom, ent.Dest)
	pushCharMoveToLocation(res, ent)
}

// foldAdvance — фаза A: продвижение всех движущихся по валидированным
// отрезкам со скоростью × dt и refill бакета игроков (r1: дёшево, без
// гео-трейса — тяжёлый line-walk делается один раз на интент).
func foldAdvance(delta uint64, rules Rules, ents []*Entity, res *StepResult) {
	if delta == 0 {
		return
	}
	step := runSpeed * int64(delta) * rules.PeriodNS / 1e6 // мЮ этого шага
	for _, e := range ents {
		if e.Player != nil {
			e.Player.SpeedBudget = min(e.Player.SpeedBudget+step, speedCAP)
		}
		if !e.Moving {
			continue
		}
		e.MoveDone += step
		if e.MoveDone >= e.MoveDist {
			e.Pos = e.Dest
			stopSegment(e)
			//self-кадр — только игроку (движущийся не-игрок фаз 4+: NPC);
			// наблюдатели получают StopMove через AoI-запись
			if e.Player != nil {
				pushStopMove(res, e)
			}
			continue
		}
		advancePos(e)
	}
}

// advancePos — позиция по отрезку MoveFrom→Dest долей MoveDone/MoveDist.
func advancePos(e *Entity) {
	dx := int64(e.Dest.X - e.MoveFrom.X)
	dy := int64(e.Dest.Y - e.MoveFrom.Y)
	dz := int64(e.Dest.Z - e.MoveFrom.Z)
	e.Pos.X = e.MoveFrom.X + int32(dx*e.MoveDone/e.MoveDist)
	e.Pos.Y = e.MoveFrom.Y + int32(dy*e.MoveDone/e.MoveDist)
	e.Pos.Z = e.MoveFrom.Z + int32(dz*e.MoveDone/e.MoveDist)
}

// stopSegment — любая остановка (прибытие, упор, выход, экспирация, ретаргет):
// поля отрезка — в стоячий вид, Dest следует позиции.
func stopSegment(e *Entity) {
	e.Moving = false
	e.Dest = e.Pos
	e.MoveFrom = e.Pos
	e.MoveDist = 0
	e.MoveDone = 0
}

// foldValidatePosition — сверка отчёта (~1/с): pendingTeleport гасит бакет;
// токен-бакет скорости (дебет = расхождение с authPos, не пройденный путь);
// дрейф сверх порога — snap-back. Планар отчёта позицию сервера не мутирует;
// вертикаль адаптируется в допуске (канон L2J: Z рельефа клиента в точке
// точнее шаблона датапака — закрытие KT3-4 «ноги в земле»).
func foldValidatePosition(st *State, ent *Entity, env *transport.Envelope, res *StepResult) {
	v, ok := protocol.NewValidatePositionView(env.Payload)
	if !ok {
		st.DroppedFrames++
		return
	}
	if !geo.InWorld(int(v.X()), int(v.Y())) {
		st.DroppedFrames++
		return
	}
	d := distMilli(ent.Pos, Position{X: v.X(), Y: v.Y(), Z: v.Z()})
	// Стоящему принимаем Z отчёта в допуске: клиент стоит на своём рельефе,
	// серверный — оценка гео-сетки; движущемуся не трогаем (Z ведёт отрезок).
	if !ent.Moving {
		if dz := (int64(v.Z()) - int64(ent.Pos.Z)) * 1000; dz <= zAdoptMilli && dz >= -zAdoptMilli {
			ent.Pos.Z = v.Z()
		}
	}
	p := ent.Player
	if p.PendingTeleport {
		if d <= driftMilli {
			p.PendingTeleport = false
			p.SpeedBudget = speedCAP
		} else {
			st.SnapBacks++
			pushValidateLocation(res, ent)
		}
		return
	}
	// дрейф и бакет — независимые механизмы (дрейф = коррекция позиции,
	// бакет = накопительное свидетельство скорости); SnapBacks считает кадры
	// коррекции — один на отчёт
	snap := d > driftMilli
	p.SpeedBudget = max(p.SpeedBudget-d, -speedCAP) // пол — переполнение снизу исключено классово
	if p.SpeedBudget < -speedSLACK {
		st.SpeedFlags++
		snap = true
		if !p.SpeedFlagged { // алерт однократен на эпизод: лог-DoS валидными кадрами исключён
			p.SpeedFlagged = true
			slog.Error("world: спидхак — флаг и коррекция",
				"entity", ent.ID, "account", p.Rec.Account, "driftMilli", d, "budget", p.SpeedBudget)
		}
	} else {
		p.SpeedFlagged = false
	}
	if snap {
		st.SnapBacks++
		pushValidateLocation(res, ent)
	}
}

// foldCannotMoveAnymore — клиент упёрся: авторитетная остановка здесь, heading
// письма (нормализация маской — Go-% знаконосен); расхождение сверх порога —
// snap-back. Вне движения — валидный no-op (счётчик, не дроп).
func foldCannotMoveAnymore(st *State, ent *Entity, env *transport.Envelope, res *StepResult) {
	v, ok := protocol.NewCannotMoveAnymoreView(env.Payload)
	if !ok {
		st.DroppedFrames++
		return
	}
	if !geo.InWorld(int(v.X()), int(v.Y())) {
		st.DroppedFrames++
		return
	}
	if !ent.Moving {
		// Стоячий поворот: клиент сообщает финальный heading каналом
		// CannotMoveAnymore — канон stopMove(loc) ставит heading из пакета и
		// бродкастит StopMove всем (наблюдатели — через dirty-запись →
		// EventUpdate → composeStopFrame). Молчаливый no-op оставлял чужую
		// запись со старым heading — поворот не синхронизировался (KT4-5).
		st.CannotMoveNoops++
		ent.Heading = v.Heading() & 0xFFFF
		pushStopMove(res, ent)
		return
	}
	d := distMilli(ent.Pos, Position{X: v.X(), Y: v.Y(), Z: v.Z()})
	ent.Heading = v.Heading() & 0xFFFF
	stopSegment(ent)
	pushStopMove(res, ent)
	if d > driftMilli {
		st.SnapBacks++
		pushValidateLocation(res, ent)
	}
}

// calcHeading — heading отрезка From→Dest; порт L2J MathUtil.calculateHeading:
// atan2(dx,dy)·65536/2π, отрицательное +65536, домен [0,65536). Бит-в-бит
// реплей — режимом «тот же бинарарь/та же мажорная версия Go» (atan2 не
// correctly-rounded; прецедент PCG в Noise).
func calcHeading(from, to Position) int32 {
	h := int32(math.Atan2(float64(to.X-from.X), float64(to.Y-from.Y)) * headingScale)
	if h < 0 {
		h += 65536
	}
	return h
}

// distMilli — 2D-дистанция позиций в мЮ. После InWorld-валидации обоих концов
// dx²+dy² ≤ 2.2e12: int64-запас и точная конвертация в float64; math.Sqrt
// правильно округлён (IEEE-754) — бит-в-бит реплей (D6), NaN исключён
// структурно (неотрицательный аргумент).
func distMilli(a, b Position) int64 {
	dx := int64(a.X) - int64(b.X)
	dy := int64(a.Y) - int64(b.Y)
	return int64(math.Sqrt(float64(dx*dx+dy*dy)) * 1000)
}

// pushCharMoveToLocation — эхо CharMoveToLocation себе: авторитетная
// текущая и клампнутая цель.
func pushCharMoveToLocation(res *StepResult, ent *Entity) {
	objID := int32(encode.ObjectIDBase + uint64(ent.ID))
	pushFrame(res, ent.Player.ConnID, protocol.CharMoveToLocationSize, func(dst []byte) int {
		return protocol.WriteCharMoveToLocation(dst, objID,
			ent.Dest.X, ent.Dest.Y, ent.Dest.Z, ent.Pos.X, ent.Pos.Y, ent.Pos.Z)
	})
}

// pushStopMove — StopMove себе (наблюдателям — через AoI-запись).
func pushStopMove(res *StepResult, ent *Entity) {
	objID := int32(encode.ObjectIDBase + uint64(ent.ID))
	pushFrame(res, ent.Player.ConnID, protocol.StopMoveSize, func(dst []byte) int {
		return protocol.WriteStopMove(dst, objID, ent.Pos.X, ent.Pos.Y, ent.Pos.Z, ent.Heading)
	})
}

// pushValidateLocation — коррекция себе: сервер-авторитетная позиция (snap-back).
func pushValidateLocation(res *StepResult, ent *Entity) {
	objID := int32(encode.ObjectIDBase + uint64(ent.ID))
	pushFrame(res, ent.Player.ConnID, protocol.ValidateLocationSize, func(dst []byte) int {
		return protocol.WriteValidateLocation(dst, objID, ent.Pos.X, ent.Pos.Y, ent.Pos.Z, ent.Heading)
	})
}

// pushActionFailed — разблокировка инпута: канон отвечает ActionFailed на
// каждый отказ движения (MoveToLocation.java, все ветки), молчание клинит
// контроллер живого клиента — клики перестают срабатывать (KT4-4).
func pushActionFailed(res *StepResult, ent *Entity) {
	pushFrame(res, ent.Player.ConnID, protocol.ActionFailedSize, protocol.WriteActionFailed)
}
