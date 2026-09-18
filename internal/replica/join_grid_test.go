package replica

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/udisondev/l2go/internal/transport"
)

// mustJoin — конструирование join с фабрик-инвариантом (ошибка = тест-провал).
func mustJoin(t *testing.T, g Grid, cfg JoinConfig) *Join {
	t.Helper()
	j, err := NewJoin(g, cfg)
	if err != nil {
		t.Fatalf("NewJoin: %v", err)
	}
	return j
}

// testG — сетка тестов пакета (значение, безопасно между параллельными
// тестами): границы клеток на кратных 8192, начало (−8192, −8192).
var testG = testGrid()

// counts — мультимножество событий (цель, вид) наблюдателя.
func counts(events []Event, obs transport.EntityID) map[[2]uint64]int {
	got := map[[2]uint64]int{}
	for _, ev := range events {
		if ev.Obs.Entity != obs {
			continue
		}
		got[[2]uint64{uint64(ev.Target.Entity), uint64(ev.Kind)}]++
	}
	return got
}

// TestJoinPairAcrossCellBoundaryLifecycle — пара через границу клеток:
// ввод по Enter из соседней клетки, стрим вдоль границы, выход за Exit при
// обеих клетках в окне (ловит буквализм «Remove только в чистке»), выход из
// окна — ровно один Remove.
func TestJoinPairAcrossCellBoundaryLifecycle(t *testing.T) {
	t.Parallel()
	g := testGrid()
	cfg := CanonJoinConfig()
	obs := recAt(1, -100, -100) // клетка (0,0)

	t.Run("ввод из соседней клетки", func(t *testing.T) {
		p := NewPublisher(g)
		j := mustJoin(t, g, cfg)
		events := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 100, -100)})
		if c := counts(events, 1)[[2]uint64{2, uint64(EventIntroduce)}]; c != 1 {
			t.Fatalf("Introduce(2) = %d; want 1 (события %v)", c, events)
		}
	})

	t.Run("стрим вдоль границы", func(t *testing.T) {
		p := NewPublisher(g)
		j := mustJoin(t, g, cfg)
		stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 100, -100)})
		for k := 0; k < 5; k++ { // качание через границу x=0, дистанция ≤ Enter
			x := int32(110 + k*10)
			if k%2 == 1 {
				x = -110 - int32(k)*10
			}
			events := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, x, -100)})
			if c := counts(events, 1); len(c) != 1 || c[[2]uint64{2, uint64(EventUpdate)}] != 1 {
				t.Fatalf("шаг %d вдоль границы: состав %v; want ровно один Update", k, c)
			}
		}
	})

	t.Run("выход за Exit в окне", func(t *testing.T) {
		p := NewPublisher(g)
		j := mustJoin(t, g, cfg)
		o := recAt(1, -2000, -100)
		stepPair(t, p, j, []Observer{obsOf(o)}, []Record{o, recAt(2, 1500, -100)}) // d=3500 — ввод
		// d=4250 > Exit, клетка цели (1,0) остаётся в окне наблюдателя (0,0)
		events := stepPair(t, p, j, []Observer{obsOf(o)}, []Record{o, recAt(2, 2250, -100)})
		if c := counts(events, 1)[[2]uint64{2, uint64(EventRemove)}]; c != 1 {
			t.Fatalf("Remove за Exit внутри окна = %d; want 1 — член повис бы навсегда (события %v)", c, events)
		}
	})

	t.Run("выход из окна", func(t *testing.T) {
		p := NewPublisher(g)
		j := mustJoin(t, g, cfg)
		stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 100, -100)})
		// клетка (2,0): вне окна 3×3 наблюдателя (0,0); d ≈ 8292 > Exit
		events := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 8300, -100)})
		if c := counts(events, 1)[[2]uint64{2, uint64(EventRemove)}]; c != 1 {
			t.Fatalf("Remove из окна = %d; want 1 (события %v)", c, events)
		}
	})
}

// TestJoinHysteresisArcAcrossCellBoundaryZeroChurn — дуга гистерезиса,
// пересекающая границу клетки: нулевой чурн на дуге (в том числе при смене
// клетки целью), ровно по одному событию на переходах, возврат в кольцо —
// молчание. Ловит глушение детекта членства сменой Cell.
func TestJoinHysteresisArcAcrossCellBoundaryZeroChurn(t *testing.T) {
	t.Parallel()
	g := testGrid()
	cfg := CanonJoinConfig()
	obs := recAt(1, -100, -100)
	p := NewPublisher(g)
	j := mustJoin(t, g, cfg)
	stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 3300, -100)}) // d=3400 — ввод
	// дуга в кольце 3500 < d ≤ 4200, качание через границу x=0
	for k := 0; k < 6; k++ {
		x := int32(3900)
		if k%2 == 1 {
			x = -3700
		}
		y := int32(-100 + (k%3)*400)
		events := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, x, y)})
		for _, ev := range events {
			if ev.Kind == EventIntroduce || ev.Kind == EventRemove {
				t.Fatalf("шаг дуги %d: осцилляция %v (чурн должен быть 0; события %v)", k, ev.Kind, events)
			}
		}
	}
	out := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 4300, -100)})
	if c := counts(out, 1)[[2]uint64{2, uint64(EventRemove)}]; c != 1 {
		t.Fatalf("Remove за exit = %d; want 1", c)
	}
	ring := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 4100, -100)})
	if len(ring) != 0 {
		t.Fatalf("возврат в кольцо: события %v; want 0", ring)
	}
	in := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 3000, -100)})
	if c := counts(in, 1)[[2]uint64{2, uint64(EventIntroduce)}]; c != 1 {
		t.Fatalf("Introduce внутрь enter = %d; want 1", c)
	}
}

// TestJoinObserverWindowShiftNoFlicker — наблюдатель пересекает границу
// клетки (окно едет): члены ≤ Exit не мерцают (дальняя кромка окна ≥8193 их
// не задевает), член за пределом нового Exit удаляется, новая кромка
// вводится.
func TestJoinObserverWindowShiftNoFlicker(t *testing.T) {
	t.Parallel()
	g := testGrid()
	cfg := CanonJoinConfig()
	p := NewPublisher(g)
	j := mustJoin(t, g, cfg)
	obs := recAt(1, -100, -100)
	stay := recAt(2, -1000, -400) // d ≈ 1180 — член, остаётся
	fresh := recAt(4, 3500, -100) // d = 3600 > Enter — не введён до сдвига
	// hold вводится в Enter (d=3300), затем отходит в кольцо (d=4100):
	// член по гистерезису — до сдвига наблюдателя
	stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, stay, recAt(3, -3400, -100), fresh})
	stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, stay, recAt(3, -4200, -100), fresh})
	moved := recAt(1, 100, -100) // смена клетки (0,0) → (1,0); hold d=4300 > Exit
	events := stepPair(t, p, j, []Observer{obsOf(moved)}, []Record{moved, stay, recAt(3, -4200, -100), fresh})
	c := counts(events, 1)
	if n := c[[2]uint64{2, uint64(EventUpdate)}] + c[[2]uint64{2, uint64(EventIntroduce)}] +
		c[[2]uint64{2, uint64(EventRemove)}]; n != 0 {
		t.Fatalf("член stay при сдвиге окна мерцает: %v", c)
	}
	if c[[2]uint64{3, uint64(EventRemove)}] != 1 {
		t.Fatalf("hold (d=4300 после сдвига) не удалён: %v", c)
	}
	if c[[2]uint64{4, uint64(EventIntroduce)}] != 1 {
		t.Fatalf("fresh (d=3400 после сдвига) не введён: %v", c)
	}
}

// TestJoinTargetReturnsToWindowReintroduces — возврат цели в окно после
// ухода: повторный ввод после Enter, повторный уход — Remove; фантома и
// невидимости нет.
func TestJoinTargetReturnsToWindowReintroduces(t *testing.T) {
	t.Parallel()
	g := testGrid()
	cfg := CanonJoinConfig()
	obs := recAt(1, -100, -100)
	p := NewPublisher(g)
	j := mustJoin(t, g, cfg)
	stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 100, -100)})
	if c := counts(stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 8300, -100)}), 1)[[2]uint64{2, uint64(EventRemove)}]; c != 1 {
		t.Fatalf("уход из окна: Remove = %d; want 1", c)
	}
	if evs := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 8300, -100)}); len(evs) != 0 {
		t.Fatalf("стационар вне окна: %v; want 0", evs)
	}
	if c := counts(stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 3400, -100)}), 1)[[2]uint64{2, uint64(EventIntroduce)}]; c != 1 {
		t.Fatalf("возврат в окно: Introduce = %d; want 1", c)
	}
	if c := counts(stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 8300, -100)}), 1)[[2]uint64{2, uint64(EventRemove)}]; c != 1 {
		t.Fatalf("повторный уход: Remove = %d; want 1", c)
	}
}

// TestJoinDespawnInWindowSingleRemove — деспавн члена окна при неподвижном
// наблюдателе: сегменты несут только занятые слоты, детект — по сиду слота
// (cellInvalid), ровно один Remove по вечному id (gone-ветки dirty больше
// нет).
func TestJoinDespawnInWindowSingleRemove(t *testing.T) {
	t.Parallel()
	g := testGrid()
	cfg := CanonJoinConfig()
	obs := recAt(1, -100, -100)
	p := NewPublisher(g)
	j := mustJoin(t, g, cfg)
	stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 100, -100)})
	events := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs})
	if c := counts(events, 1)[[2]uint64{2, uint64(EventRemove)}]; c != 1 {
		t.Fatalf("деспавн в окне: Remove = %d; want 1 — вечный фантом (события %v)", c, events)
	}
	if evs := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs}); len(evs) != 0 {
		t.Fatalf("после деспавна: %v; want 0", evs)
	}
}

// TestJoinSlotReuseOutsideWindowSingleRemove — реюз слота жильцом вне окна:
// один источник Remove (чистка хвоста), двойного удаления нет, ввод
// вне-оконного жильца не происходит.
func TestJoinSlotReuseOutsideWindowSingleRemove(t *testing.T) {
	t.Parallel()
	g := testGrid()
	cfg := CanonJoinConfig()
	obs := recAt(1, -100, -100)
	p := NewPublisher(g)
	j := mustJoin(t, g, cfg)
	stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 3300, -100)})
	// X (id 2) деспавнится, Y (id 3) занимает её слот — в клетке вне окна
	events := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(3, 8300, -100)})
	c := counts(events, 1)
	if c[[2]uint64{2, uint64(EventRemove)}] != 1 {
		t.Fatalf("Remove(X) = %d; want 1 — двойной Remove или фантом (события %v)", c[[2]uint64{2, uint64(EventRemove)}], events)
	}
	if len(c) != 1 {
		t.Fatalf("реюз вне окна: лишние события %v", c)
	}
}

// TestJoinSlotReuseInWindowCoveredObserver — реюз слота В окне при
// covered-наблюдателе (двинулся в тот же шаг): Remove старого жильца по
// вечному id + Introduce нового; view держит нового (уход нового за Exit
// эмитит Remove — фантома старого нет). Регресс F11.
func TestJoinSlotReuseInWindowCoveredObserver(t *testing.T) {
	t.Parallel()
	g := testGrid()
	cfg := CanonJoinConfig()
	obs := recAt(1, -100, -100)
	p := NewPublisher(g)
	j := mustJoin(t, g, cfg)
	stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 100, -100)})
	// одним поколением: наблюдатель двинулся (changed ⇒ полный проход),
	// X деспавнулась, N родилась на её слоте в окне
	moved := recAt(1, 100, -100)
	events := stepPair(t, p, j, []Observer{obsOf(moved)}, []Record{moved, recAt(3, 200, -100)})
	c := counts(events, 1)
	if c[[2]uint64{2, uint64(EventRemove)}] != 1 || c[[2]uint64{3, uint64(EventIntroduce)}] != 1 {
		t.Fatalf("реюз в окне при covered: want Remove(2)+Introduce(3); got %v", c)
	}
	if c[[2]uint64{3, uint64(EventRemove)}] != 0 {
		t.Fatalf("новый жилец удалён в тот же шаг: %v", c)
	}
	// view держит нового жильца: уход N за Exit эмитит Remove
	out := stepPair(t, p, j, []Observer{obsOf(moved)}, []Record{moved, recAt(3, 5000, -100)})
	if counts(out, 1)[[2]uint64{3, uint64(EventRemove)}] != 1 {
		t.Fatalf("уход нового жильца за Exit: Remove нет — фантом (события %v)", out)
	}
}

// TestJoinPanicWindowReconcileRunsTailCleanup — паник-окно Apply на шаге
// деспавна члена: примирение следующего шага доезжает чистку хвоста (чистка
// обязана жить на всех путях шага), стационар после — молчание.
func TestJoinPanicWindowReconcileRunsTailCleanup(t *testing.T) {
	t.Parallel()
	g := testGrid()
	cfg := CanonJoinConfig()
	obs := recAt(1, -100, -100)
	p := NewPublisher(g)
	j := mustJoin(t, g, cfg)
	stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 100, -100)})
	j.ForcePanicInApply.Store(true)
	next := p.Build([]Record{obs}) // деспавн члена 2
	j.Step([]Observer{obsOf(obs)}, next)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatalf("шов ForcePanicInApply не сработал")
			}
		}()
		j.Apply()
	}()
	j.ForcePanicInApply.Store(false)
	events := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs})
	if c := counts(events, 1)[[2]uint64{2, uint64(EventRemove)}]; c != 1 {
		t.Fatalf("примирение не доезжает чистку: Remove(2) = %d; want 1 (события %v)", c, events)
	}
	if evs := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs}); len(evs) != 0 {
		t.Fatalf("стационар после примирения: %v; want 0", evs)
	}
}

// refKnown — эталонная известность с состоянием: гистерезис по истории
// (членство при Enter < d ≤ Exit зависит от прошлых шагов, не только позиций).
type refKnown map[transport.EntityID]map[transport.EntityID]struct{}

func (r refKnown) step(recs []Record, cfg JoinConfig) {
	present := map[transport.EntityID]Record{}
	for _, rec := range recs {
		present[rec.Entity] = rec
	}
	// наблюдатели-новички прежде расчёта: их известность стартует пустой
	for id := range present {
		if _, ok := r[id]; !ok {
			r[id] = map[transport.EntityID]struct{}{}
		}
	}
	for obs := range r {
		o, ok := present[obs]
		if !ok {
			delete(r, obs)
			continue
		}
		for m := range r[obs] {
			t, ok := present[m]
			if !ok || beyondExit(&o, &t, cfg) || !Visible(o.Flags, t.Flags) {
				delete(r[obs], m)
			}
		}
		for id, t := range present {
			if id == obs {
				continue
			}
			if _, known := r[obs][id]; known {
				continue
			}
			if inEnter(&o, &t, cfg) {
				r[obs][id] = struct{}{}
			}
		}
	}
}

// TestJoinPropertyGridSetEquality — свойство на сетке: после каждого шага
// view каждого наблюдателя == эталонной известности с гистерезисом; случайные
// расстановки/перемещения/деспавны/рождения в нескольких клетках, генератор
// таргетит границы клеток и кольцо гистерезиса. Ловит рассинхрон чистки и
// вводов на сетке (обобщение вырожденного set-equality).
func TestJoinPropertyGridSetEquality(t *testing.T) {
	t.Parallel()
	g := testGrid()
	cfg := CanonJoinConfig()
	for iter := 0; iter < 40; iter++ {
		rng := rand.New(rand.NewPCG(42, uint64(iter)))
		p := NewPublisher(g)
		j := mustJoin(t, g, cfg)
		ref := refKnown{}
		nextID := transport.EntityID(1)
		recs := make([]Record, 0, 24)
		for range 16 {
			recs = append(recs, recAt(nextID, randCellPos(rng, 0), randCellPos(rng, 0)))
			nextID++
		}
		for step := 0; step < 30; step++ {
			switch rng.IntN(6) {
			case 0, 1, 2: // перемещения
				for i := range recs {
					if rng.IntN(2) == 0 {
						recs[i].X, recs[i].Y = randCellPos(rng, 1), randCellPos(rng, 1)
					}
				}
			case 3: // деспавн трети
				kept := recs[:0]
				for _, rec := range recs {
					if rng.IntN(3) != 0 {
						kept = append(kept, rec)
					}
				}
				recs = kept
			case 4: // рождения
				for range 1 + rng.IntN(3) {
					recs = append(recs, recAt(nextID, randCellPos(rng, 0), randCellPos(rng, 0)))
					nextID++
				}
			case 5: // кольцо гистерезиса относительно первой записи
				if len(recs) > 1 {
					base := recs[0]
					d := []int32{3499, 3500, 3501, 4200, 4201}[rng.IntN(5)]
					recs[1].X, recs[1].Y = base.X+d, base.Y
				}
			}
			ref.step(recs, cfg)
			obs := make([]Observer, 0, len(recs))
			for _, rec := range recs {
				obs = append(obs, obsOf(rec))
			}
			stepPair(t, p, j, obs, recs)
			for _, rec := range recs {
				want := ref[rec.Entity]
				got := map[transport.EntityID]bool{}
				if v := j.views[rec.Entity]; v != nil {
					for _, id := range v.ids {
						if id != 0 {
							got[id] = true
						}
					}
				}
				if len(got) != len(want) {
					t.Fatalf("итер %d шаг %d: наблюдатель %d: got %v want %v", iter, step, rec.Entity, got, want)
				}
				for id := range want {
					if !got[id] {
						t.Fatalf("итер %d шаг %d: наблюдатель %d: цель %d в эталоне, во view нет (got %v want %v)",
							iter, step, rec.Entity, id, got, want)
					}
				}
			}
		}
	}
}

// randCellPos — координата с таргетингом границ клеток: кратные 8192 и ±1
// (кламп в диапазон 4 клеток от начала сетки).
func randCellPos(rng *rand.Rand, mode int) int32 {
	switch mode {
	case 0: // произвольная в [−8192, 4·8192)
		return int32(rng.IntN(5*8192)) - 8192
	default: // смещение от текущей: границы клеток и окрестность
		edge := int32(rng.IntN(5) * 8192)
		return edge + []int32{-1, 0, 1}[rng.IntN(3)] + int32(rng.IntN(6000)-3000)
	}
}

// TestJoinStepStagesIdempotentOnGrid — много-клеточный стадинг: повторный
// Step без Apply идентичен, view нетронут (твин P3.8 на сетке).
func TestJoinStepStagesIdempotentOnGrid(t *testing.T) {
	t.Parallel()
	g := testGrid()
	cfg := CanonJoinConfig()
	obs := recAt(1, -100, -100)
	p := NewPublisher(g)
	j := mustJoin(t, g, cfg)
	recs := []Record{obs, recAt(2, 100, -100), recAt(3, 5000, -100), recAt(4, -100, 5000)}
	blob := p.Build(recs)
	e1 := j.Step([]Observer{obsOf(obs)}, blob)
	if len(e1) == 0 {
		t.Fatalf("первый Step пуст")
	}
	if v := j.views[1]; v != nil {
		t.Fatalf("Step создал view до Apply: %+v", v)
	}
	e2 := j.Step([]Observer{obsOf(obs)}, blob)
	if len(e2) != len(e1) {
		t.Fatalf("повторный Step: %d событий vs %d", len(e2), len(e1))
	}
	for i := range e1 {
		if e1[i] != e2[i] {
			t.Fatalf("событие %d: %v vs %v", i, e1[i], e2[i])
		}
	}
}

// TestJoinBothMovingAcrossCellBoundaryUpdate — наблюдатель и цель оба
// changed и пересекают границу клетки: полный проход covered-наблюдателя
// несёт апдейт пары (твин P3.10-F47 на сетке).
func TestJoinBothMovingAcrossCellBoundaryUpdate(t *testing.T) {
	t.Parallel()
	g := testGrid()
	cfg := CanonJoinConfig()
	p := NewPublisher(g)
	j := mustJoin(t, g, cfg)
	mk := func(ox, tx int32) []Record {
		return []Record{recAt(1, ox, -100), recAt(2, tx, -100)}
	}
	obs := []Observer{obsOf(recAt(1, 0, 0))}
	stepPair(t, p, j, obs, mk(-200, -100))
	events := stepPair(t, p, j, obs, mk(100, 200)) // оба пересекли x=0
	if c := counts(events, 1)[[2]uint64{2, uint64(EventUpdate)}]; c != 1 {
		t.Fatalf("апдейт пары при одновременном пересечении границы = %d; want 1 (события %v)", c, events)
	}
}

// TestJoinWindowImmuneToOutsideCrowd — функциональный пин фальсификатора:
// события наблюдателя не зависят от толпы вне окна и её раскладки по клеткам
// (стоимость фальсифицируется бенчем BenchmarkJoinStepIsolatedFromCrowd).
func TestJoinWindowImmuneToOutsideCrowd(t *testing.T) {
	t.Parallel()
	g := testGrid()
	cfg := CanonJoinConfig()
	scenario := func(crowd int, cells int) []Event {
		p := NewPublisher(g)
		j := mustJoin(t, g, cfg)
		obs := recAt(1, -100, -100)
		recs := []Record{obs, recAt(2, 100, -100)}
		for i := range crowd {
			x := int32(65536 + (i%cells)*8192 + i%97)
			y := int32(65536 + (i/cells)*97%8192)
			recs = append(recs, recAt(transport.EntityID(1000+i), x, y))
		}
		return stepPair(t, p, j, []Observer{obsOf(obs)}, recs)
	}
	base := scenario(0, 1)
	for _, tc := range []struct{ crowd, cells int }{
		{1000, 1},
		{100_000, 1000},
	} {
		got := scenario(tc.crowd, tc.cells)
		if len(got) != len(base) {
			t.Fatalf("толпа %d в %d клетках: событий %d; want %d", tc.crowd, tc.cells, len(got), len(base))
		}
		for i := range base {
			if base[i] != got[i] {
				t.Fatalf("толпа %d/%d: событие %d = %v; want %v", tc.crowd, tc.cells, i, got[i], base[i])
			}
		}
	}
	_ = fmt.Sprint()
}
