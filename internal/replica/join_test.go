package replica

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/udisondev/l2go/internal/transport"
)

// stepPair — прогон Build→Commit→Step→Apply над населением; возвращает события.
func stepPair(t *testing.T, p *Publisher, j *Join, obs []Observer, recs []Record) []Event {
	t.Helper()
	blob := p.Build(recs)
	events := j.Step(obs, blob)
	j.Apply()
	p.Commit(blob)
	return events
}

func obsOf(rec Record) Observer {
	return Observer{Entity: rec.Entity, ConnID: uint64(rec.Entity) % 1000}
}
func recAt(id transport.EntityID, x, y int32) Record {
	return Record{Entity: id, X: x, Y: y, Z: 0, Kind: RecordKindPlayer, Name: "t"}
}

// TestJoinSetEqualityPopulationIntersectsRadius — свойство join: для свежего
// наблюдателя множество вводов == «население ∩ enter-радиус ∩ Visible»
// (brute-force с той же арифметикой), self-пара отсутствует; корнер 0/1.
func TestJoinSetEqualityPopulationIntersectsRadius(t *testing.T) {
	t.Parallel()
	for iter := 0; iter < 100; iter++ {
		rng := rand.New(rand.NewPCG(1, uint64(iter)))
		n := rng.IntN(12)
		recs := make([]Record, n)
		for i := range recs {
			recs[i] = recAt(transport.EntityID(i+1), int32(rng.IntN(9000)-4500), int32(rng.IntN(9000)-4500))
		}
		obsRec := recAt(transport.EntityID(n+1), int32(rng.IntN(9000)-4500), int32(rng.IntN(9000)-4500))
		cfg := JoinConfig{Enter: 3500, Exit: 4200}
		p := NewPublisher(testG)
		j := mustJoin(t, testG, cfg)
		events := stepPair(t, p, j, []Observer{obsOf(obsRec)}, append(recs, obsRec))
		got := map[transport.EntityID]bool{}
		for _, ev := range events {
			if ev.Kind != EventIntroduce {
				t.Fatalf("iter %d: свежий наблюдатель получил %v", iter, ev.Kind)
			}
			got[ev.Entity] = true
		}
		want := map[transport.EntityID]bool{}
		for _, r := range recs {
			dx, dy := int64(obsRec.X)-int64(r.X), int64(obsRec.Y)-int64(r.Y)
			if dx*dx+dy*dy <= 3500*3500 {
				want[r.Entity] = true
			}
		}
		for id := range want {
			if !got[id] {
				t.Fatalf("iter %d: цель %d в радиусе, ввода нет (got %v want %v)", iter, id, got, want)
			}
		}
		for id := range got {
			if !want[id] {
				t.Fatalf("iter %d: цель %d вне радиуса, ввод есть", iter, id)
			}
		}
		if got[obsRec.Entity] {
			t.Fatalf("iter %d: self-пара введена", iter)
		}
	}
	// корнер: пустое население и население из одного наблюдателя
	p := NewPublisher(testG)
	j := mustJoin(t, testG, JoinConfig{Enter: 100, Exit: 200})
	if evs := stepPair(t, p, j, nil, nil); len(evs) != 0 {
		t.Fatalf("пустое население: события %v", evs)
	}
	p2 := NewPublisher(testG)
	j2 := mustJoin(t, testG, JoinConfig{Enter: 100, Exit: 200})
	solo := recAt(1, 0, 0)
	if evs := stepPair(t, p2, j2, []Observer{obsOf(solo)}, []Record{solo}); len(evs) != 0 {
		t.Fatalf("одиночный наблюдатель: события %v", evs)
	}
}

// TestJoinRadiusBoundariesExact — семантика границ: d==enter входит, член на
// d==exit удерживается, d==exit+1 выводится.
func TestJoinRadiusBoundariesExact(t *testing.T) {
	t.Parallel()
	cfg := JoinConfig{Enter: 3500, Exit: 4200}
	cases := []struct {
		d    int32
		want string // "in", "out", "hold"
	}{
		{3500, "in"}, {3501, "out"}, {4199, "hold"}, {4200, "hold"}, {4201, "gone"},
	}
	obs := recAt(1, 0, 0)
	for _, c := range cases {
		t.Run(fmt.Sprintf("d=%d", c.d), func(t *testing.T) {
			p := NewPublisher(testG)
			j := mustJoin(t, testG, cfg)
			target := recAt(2, c.d, 0)
			events := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, target})
			intro := false
			for _, ev := range events {
				if ev.Kind == EventIntroduce {
					intro = true
				}
			}
			if c.want == "in" && !intro {
				t.Errorf("ввода нет; want ввод")
			}
			if (c.want == "out" || c.want == "hold" || c.want == "gone") && intro {
				t.Errorf("ввод есть; want нет")
			}
			// удержание/выход: шаг изменения дистанции недостижим без Changed —
			// проверяем сменой позиции цели (Changed) на ту же дистанцию
			if c.want == "hold" || c.want == "gone" {
				// цель уже член (введена с d=enter), позиция меняется на c.d
				p2 := NewPublisher(testG)
				j2 := mustJoin(t, testG, cfg)
				stepPair(t, p2, j2, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 3500, 0)})
				moved := recAt(2, c.d, 0)
				events2 := stepPair(t, p2, j2, []Observer{obsOf(obs)}, []Record{obs, moved})
				removed := false
				for _, ev := range events2 {
					if ev.Kind == EventRemove && ev.Entity == 2 {
						removed = true
					}
				}
				if c.want == "gone" && !removed {
					t.Errorf("удержание за exit; want Remove")
				}
				if c.want == "hold" && removed {
					t.Errorf("Remove в кольце гистерезиса")
				}
			}
		})
	}
}

// TestJoinHysteresisArcZeroChurn — бег по дуге края радиуса: нулевая
// осцилляция вводов/удалений; выход/возврат — ровно по одному событию.
func TestJoinHysteresisArcZeroChurn(t *testing.T) {
	t.Parallel()
	cfg := JoinConfig{Enter: 3500, Exit: 4200}
	obs := recAt(1, 0, 0)
	p := NewPublisher(testG)
	j := mustJoin(t, testG, cfg)
	target := recAt(2, 3400, 0)
	stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, target})
	// дуга в кольце 3500<3600<4200: позиция меняется, член удерживается
	for k := int32(0); k < 5; k++ {
		moved := recAt(2, 3600, 100+k)
		events := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, moved})
		for _, ev := range events {
			if ev.Kind == EventIntroduce || ev.Kind == EventRemove {
				t.Fatalf("шаг дуги %d: осцилляция %v (churn должен быть 0)", k, ev.Kind)
			}
		}
	}
	// выход за exit: ровно один Remove
	out := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 4250, 0)})
	removes := 0
	for _, ev := range out {
		if ev.Kind == EventRemove && ev.Entity == 2 {
			removes++
		}
	}
	if removes != 1 {
		t.Fatalf("Remove за exit = %d; want 1", removes)
	}
	// возврат в кольцо: удержание (не член, вне enter — ноль событий)
	ring := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 4100, 0)})
	if len(ring) != 0 {
		t.Fatalf("возврат в кольцо: события %v; want 0", ring)
	}
	// вход внутрь enter: ровно один ввод
	in := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 3000, 0)})
	intros := 0
	for _, ev := range in {
		if ev.Kind == EventIntroduce && ev.Entity == 2 {
			intros++
		}
	}
	if intros != 1 {
		t.Fatalf("Introduce внутрь enter = %d; want 1", intros)
	}
}

// TestJoinPropertyCoordinateInt32Edges — координаты у пределов int32: отсечка
// переполнения (сравнение с big-арифметикой).
func TestJoinPropertyCoordinateInt32Edges(t *testing.T) {
	t.Parallel()
	cfg := JoinConfig{Enter: 3500, Exit: 4200}
	edges := []int32{0, 1, -1, 1 << 30, -(1 << 30), 1<<31 - 1, -(1 << 31), -71338, 258271}
	for _, ox := range edges {
		for _, tx := range edges {
			obs := recAt(1, ox, 0)
			target := recAt(2, tx, 0)
			p := NewPublisher(testG)
			j := mustJoin(t, testG, cfg)
			events := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, target})
			got := len(events) >= 1 && events[0].Kind == EventIntroduce
			dx := int64(ox) - int64(tx)
			if dx < 0 {
				dx = -dx
			}
			want := dx <= 3500
			if got != want {
				t.Errorf("obs.X=%d tgt.X=%d: introduce=%v; want %v (переполнение d²?)", ox, tx, got, want)
			}
		}
	}
}

// TestPublisherBuildPureUntilCommit — Build не мутирует издателя: два Build
// без Commit идентичны; Commit продвигает поколения.
func TestPublisherBuildPureUntilCommit(t *testing.T) {
	t.Parallel()
	p := NewPublisher(testG)
	recs := []Record{recAt(1, 0, 0), recAt(2, 10, 10)}
	b1 := p.Build(recs)
	b2 := p.Build(recs)
	if b1.gen != b2.gen || b1.base != b2.base {
		t.Fatalf("поколения разошлись без Commit: %d/%d vs %d/%d", b1.gen, b1.base, b2.gen, b2.base)
	}
	if len(b1.records) != len(b2.records) || b1.slotOf[1] != b2.slotOf[1] {
		t.Fatalf("слоты разошлись без Commit")
	}
	if p.Committed() != nil {
		t.Fatalf("до Commit закоммиченное не пусто")
	}
	p.Commit(b2)
	if p.Committed() != b2 {
		t.Fatalf("Commit не опубликовал")
	}
	b3 := p.Build(recs)
	if b3.base != b2.gen {
		t.Fatalf("BaseGen после Commit = %d; want %d", b3.base, b2.gen)
	}
}

// TestPublisherCopiesRecordValues — блоб владеет копией значений: мутация
// входного слайса не меняет построенное поколение.
func TestPublisherCopiesRecordValues(t *testing.T) {
	t.Parallel()
	p := NewPublisher(testG)
	recs := []Record{recAt(1, 0, 0)}
	b := p.Build(recs)
	recs[0].X = 99999
	recs[0].Name = "mutated"
	if b.records[b.slotPos[b.slotOf[1]]].X != 0 || b.records[b.slotPos[b.slotOf[1]]].Name != "t" {
		t.Fatalf("блоб алиасит входной слайс: %+v", b.records[b.slotPos[b.slotOf[1]]])
	}
}

// TestPublisherCommittedImmutableUnderReaderRace — иммутабельность
// опубликованного поколения под читателем (держит поколение через 2 шага).
func TestPublisherCommittedImmutableUnderReaderRace(t *testing.T) {
	p := NewPublisher(testG)
	p.Commit(p.Build([]Record{recAt(1, 0, 0), recAt(2, 1, 1)}))
	done := make(chan struct{})
	go func() {
		defer close(done)
		held := p.Committed()
		x0, name0 := held.records[held.slotPos[held.slotOf[1]]].X, held.records[held.slotPos[held.slotOf[1]]].Name
		for i := 0; i < 2000; i++ {
			if _, ok := p.Read(testG.CellOf(0, 0), 1); !ok {
				t.Error("Read(1) потерял запись")
				return
			}
			if held.records[held.slotPos[held.slotOf[1]]].X != x0 || held.records[held.slotPos[held.slotOf[1]]].Name != name0 {
				t.Error("удержанное поколение мутировало")
				return
			}
		}
	}()
	for i := range 50 {
		p.Commit(p.Build([]Record{recAt(1, int32(i), int32(i)), recAt(2, int32(i+1), int32(i+1))}))
	}
	<-done
}

// TestJoinSlotReuseDetectedByEternalID — деспавн+рождение на слоте: манифест
// несёт Gone+Born, события Remove(старый)+Introduce(новый).
func TestJoinSlotReuseDetectedByEternalID(t *testing.T) {
	t.Parallel()
	cfg := JoinConfig{Enter: 1000, Exit: 1500}
	obs := recAt(1, 0, 0)
	p := NewPublisher(testG)
	j := mustJoin(t, testG, cfg)
	first := recAt(2, 100, 0)
	stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, first})
	slot := p.Committed().slotOf[2]
	next := recAt(3, 120, 0) // займёт младший свободный слот 2
	events := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, next})
	var removedOld, introducedNew bool
	for _, ev := range events {
		if ev.Kind == EventRemove && ev.Entity == 2 {
			removedOld = true
		}
		if ev.Kind == EventIntroduce && ev.Entity == 3 {
			introducedNew = true
		}
	}
	if !removedOld || !introducedNew {
		t.Fatalf("реюз слота: removed=%v introduced=%v (события %v)", removedOld, introducedNew, events)
	}
	if s := p.Committed().slotOf[3]; s != slot {
		t.Fatalf("слот нового жильца = %d; want реюз %d", s, slot)
	}
}

// TestJoinFlagsFlipReevaluatesPredicate — смена флага записи: переоценка всем
// наблюдателям; возврат флага — ввод; повторная переоценка без смены — ноль.
func TestJoinFlagsFlipReevaluatesPredicate(t *testing.T) {
	t.Parallel()
	cfg := JoinConfig{Enter: 1000, Exit: 1500}
	obs := recAt(1, 0, 0)
	p := NewPublisher(testG)
	j := mustJoin(t, testG, cfg)
	target := recAt(2, 100, 0)
	stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, target})
	hidden := target
	hidden.Flags = FlagHidden
	events := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, hidden})
	if len(events) != 1 || events[0].Kind != EventRemove || events[0].Entity != 2 {
		t.Fatalf("скрытие: события %v; want Remove(2)", events)
	}
	// повторный шаг без изменения: ноль событий (переоценка члена ⇒ 0)
	if evs := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, hidden}); len(evs) != 0 {
		t.Fatalf("повторная переоценка: %v; want 0", evs)
	}
	shown := target
	events = stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, shown})
	if len(events) != 1 || events[0].Kind != EventIntroduce || events[0].Entity != 2 {
		t.Fatalf("раскрытие: события %v; want Introduce(2)", events)
	}
}

// TestJoinGoneWithoutMarkerRemovesImmediately — исчезновение записи без
// маркера: немедленный Remove; Moving-маркеры не порождаются (фаза 3).
func TestJoinGoneWithoutMarkerRemovesImmediately(t *testing.T) {
	t.Parallel()
	cfg := JoinConfig{Enter: 1000, Exit: 1500}
	obs := recAt(1, 0, 0)
	p := NewPublisher(testG)
	j := mustJoin(t, testG, cfg)
	target := recAt(2, 100, 0)
	stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, target})
	events := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs})
	if len(events) != 1 || events[0].Kind != EventRemove || events[0].Entity != 2 {
		t.Fatalf("исчезновение: события %v; want Remove(2)", events)
	}
	if len(p.Committed().header.Moving) != 0 {
		t.Fatalf("Moving-маркеры порождены в фазе 3")
	}
}

// TestJoinObserverBirthFillsViewDeathDrops — рождение наблюдателя: первичное
// заполнение; уход наблюдателя: дроп view (мёртвый не течёт).
func TestJoinObserverBirthFillsViewDeathDrops(t *testing.T) {
	t.Parallel()
	cfg := JoinConfig{Enter: 1000, Exit: 1500}
	obs := recAt(1, 0, 0)
	target := recAt(2, 100, 0)
	p := NewPublisher(testG)
	j := mustJoin(t, testG, cfg)
	events := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, target})
	if len(events) != 1 || events[0].Obs.Entity != 1 || events[0].Entity != 2 {
		t.Fatalf("рождение наблюдателя: %v; want Introduce(2→1)", events)
	}
	// наблюдатель уходит из населения: его view дропается
	stepPair(t, p, j, nil, []Record{target})
	if _, ok := j.views[1]; ok {
		t.Fatalf("view ушедшего наблюдателя течёт")
	}
}

// TestJoinObserverMoveFullPassDiff — движение наблюдателя: полный проход его
// пар — вводы новых в радиусе, удаления покинувших.
func TestJoinObserverMoveFullPassDiff(t *testing.T) {
	t.Parallel()
	cfg := JoinConfig{Enter: 1000, Exit: 1500}
	obs := recAt(1, 0, 0)
	near := recAt(2, 500, 0)
	far := recAt(3, 5000, 0)
	p := NewPublisher(testG)
	j := mustJoin(t, testG, cfg)
	stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, near, far})
	moved := recAt(1, 5000, 0)
	events := stepPair(t, p, j, []Observer{obsOf(moved)}, []Record{moved, near, far})
	// multiset пар (цель, вид): полный проход может эмитить обе стороны —
	// сверяем состав, а не последнюю запись по цели
	got := map[[2]uint64]int{}
	for _, ev := range events {
		if ev.Obs.Entity != 1 {
			continue
		}
		got[[2]uint64{uint64(ev.Entity), uint64(ev.Kind)}]++
	}
	if got[[2]uint64{2, uint64(EventRemove)}] != 1 {
		t.Fatalf("покинутая цель: счётчик Remove(2) = %d; want 1 (состав %v)", got[[2]uint64{2, uint64(EventRemove)}], got)
	}
	if got[[2]uint64{3, uint64(EventIntroduce)}] != 1 {
		t.Fatalf("новая цель: счётчик Introduce(3) = %d; want 1 (состав %v)", got[[2]uint64{3, uint64(EventIntroduce)}], got)
	}
}

// TestJoinStepStagesWithoutMutatingView — Step не мутирует view: повторный
// вызов с тем же блобом даёт идентичные события (идемпотентность стадинга).
func TestJoinStepStagesWithoutMutatingView(t *testing.T) {
	t.Parallel()
	cfg := JoinConfig{Enter: 1000, Exit: 1500}
	obs := recAt(1, 0, 0)
	target := recAt(2, 100, 0)
	p := NewPublisher(testG)
	j := mustJoin(t, testG, cfg)
	blob := p.Build([]Record{obs, target})
	e1 := j.Step([]Observer{obsOf(obs)}, blob)
	if len(e1) != 1 {
		t.Fatalf("первый Step: %v", e1)
	}
	if v := j.views[1]; v != nil {
		t.Fatalf("Step создал view до Apply (слепящее окно новорождённого): %+v", v)
	}
	e2 := j.Step([]Observer{obsOf(obs)}, blob)
	if len(e2) != 1 || e2[0] != e1[0] {
		t.Fatalf("повторный Step не идемпотентен: %v vs %v", e1, e2)
	}
}

// TestJoinReconciliationOnBaseGenMismatch — примирение поколений: view,
// применённый к незакоммиченному/частично применённому поколению, сходится
// полным проходом; следующий шаг — ноль событий.
func TestJoinReconciliationOnBaseGenMismatch(t *testing.T) {
	t.Parallel()
	cfg := JoinConfig{Enter: 1000, Exit: 1500}
	obs := recAt(1, 0, 0)
	p := NewPublisher(testG)
	j := mustJoin(t, testG, cfg)
	// нормальный первый шаг
	stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 100, 0)})
	// шаг с паникой между Apply и Commit: Step и Apply прошли, Commit — нет
	next := p.Build([]Record{obs, recAt(2, 100, 0), recAt(3, 200, 0)})
	j.Step([]Observer{obsOf(obs)}, next)
	j.Apply()
	// Commit НЕ случился: следующий Build диффуется от старого prev
	rebuilt := p.Build([]Record{obs, recAt(2, 100, 0), recAt(3, 200, 0)})
	if rebuilt.base == next.gen {
		t.Fatalf("BaseGen шага примирения не разошёлся с применённым")
	}
	events := j.Step([]Observer{obsOf(obs)}, rebuilt)
	j.Apply()
	p.Commit(rebuilt)
	introduced := 0
	for _, ev := range events {
		if ev.Kind == EventIntroduce && ev.Obs.Entity == 1 {
			introduced++
		}
	}
	if introduced != 2 {
		t.Fatalf("примирение: вводов наблюдателю = %d; want 2 (полный эмит)", introduced)
	}
	// следующий шаг (базы сошлись): ноль событий
	if evs := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 100, 0), recAt(3, 200, 0)}); len(evs) != 0 {
		t.Fatalf("стационар после примирения: %v; want 0", evs)
	}
}

// TestJoinReconciliationPartialApply — частичная Apply (шов ForcePanicInApply):
// appliedGen уже продвинут ⇒ примирение следующего шага замыкает обе стороны.
func TestJoinReconciliationPartialApply(t *testing.T) {
	t.Parallel()
	cfg := JoinConfig{Enter: 1000, Exit: 1500}
	obs := recAt(1, 0, 0)
	p := NewPublisher(testG)
	j := mustJoin(t, testG, cfg)
	stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 100, 0)})
	// паника внутри Apply на первом же диффе
	j.ForcePanicInApply.Store(true)
	next := p.Build([]Record{obs, recAt(2, 300, 0), recAt(3, 400, 0)})
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
	if j.appliedGen != next.gen {
		t.Fatalf("appliedGen не продвинут первой операцией Apply")
	}
	// Commit не случился: примирение обязано сойтись полным проходом
	events := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 300, 0), recAt(3, 400, 0)})
	seen3 := false
	for _, ev := range events {
		if ev.Kind == EventIntroduce && ev.Entity == 3 {
			seen3 = true
		}
	}
	if !seen3 {
		t.Fatalf("частичная Apply: примирение не ввело цель 3 (события %v)", events)
	}
	if evs := stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, recAt(2, 300, 0), recAt(3, 400, 0)}); len(evs) != 0 {
		t.Fatalf("стационар после частичной Apply: %v", evs)
	}
}

// TestCanonJoinConfigValues — канонная пара радиусов (L2J PlayerKnownList).
func TestCanonJoinConfigValues(t *testing.T) {
	t.Parallel()
	if got := CanonJoinConfig(); got != (JoinConfig{Enter: 3500, Exit: 4200}) {
		t.Fatalf("CanonJoinConfig = %+v; want {3500 4200}", got)
	}
}

// TestVisibleFlagCombinations — тривиальный эталон предиката фазы 3.
func TestVisibleFlagCombinations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		obs, tgt Flags
		want     bool
	}{
		{0, 0, true},
		{0, FlagHidden, false},
		{FlagHidden, 0, true},
		{FlagHidden, FlagHidden, false},
	}
	for _, c := range cases {
		if got := Visible(c.obs, c.tgt); got != c.want {
			t.Errorf("Visible(%d,%d) = %v; want %v", c.obs, c.tgt, got, c.want)
		}
	}
}

// TestJoinSlotReuseInFullPassKeepsNewTenant — реюз слота при полном проходе
// наблюдателя: Remove(старого жильца) не стирает нового из view (сверка
// вечного id в Apply), порядок эмита не важен.
func TestJoinSlotReuseInFullPassKeepsNewTenant(t *testing.T) {
	t.Parallel()
	cfg := JoinConfig{Enter: 1000, Exit: 1500}
	obs := recAt(1, 0, 0)
	old := recAt(2, 100, 0)
	p := NewPublisher(testG)
	j := mustJoin(t, testG, cfg)
	stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, old})
	// одним поколением: old ушла, new заняла её слот (младший свободный),
	// наблюдатель сдвинулся (Changed ⇒ полный проход его пар)
	moved := recAt(1, 50, 50)
	next := recAt(3, 120, 0)
	events := stepPair(t, p, j, []Observer{obsOf(moved)}, []Record{moved, next})
	sawRemoveOld, sawIntroduceNew := false, false
	for _, ev := range events {
		if ev.Kind == EventRemove && ev.Entity == 2 {
			sawRemoveOld = true
		}
		if ev.Kind == EventIntroduce && ev.Entity == 3 {
			sawIntroduceNew = true
		}
	}
	if !sawRemoveOld || !sawIntroduceNew {
		t.Fatalf("реюз в fullPass: Remove(2)=%v Introduce(3)=%v (события %v)", sawRemoveOld, sawIntroduceNew, events)
	}
	// view держит нового жильца слота; уход new за exit эмитит Remove
	v := j.views[1]
	slot := p.Committed().slotOf[3]
	if v.ids[slot] != 3 {
		t.Fatalf("view слота = %d; want 3 (новый жилец стёрт Remove'ом старого)", v.ids[slot])
	}
	far := recAt(3, 5000, 0)
	events = stepPair(t, p, j, []Observer{obsOf(moved)}, []Record{moved, far})
	removed := false
	for _, ev := range events {
		if ev.Kind == EventRemove && ev.Entity == 3 {
			removed = true
		}
	}
	if !removed {
		t.Fatalf("вечный фантом: уход new за exit не эмитил Remove")
	}
}

// Одновременное движение наблюдателя и цели: changed-наблюдатель уходит в
// полный проход, dirty-цикл его скипает — апдейт пары обязан прийти из
// полного прохода (регресс нагрузочного датчика P3.10: стрим пары терялся,
// пока двигались оба).
func TestJoinBothMovingUpdateDelivered(t *testing.T) {
	pub := NewPublisher(testG)
	join := mustJoin(t, testG, CanonJoinConfig())
	mk := func(x int32) []Record {
		return []Record{
			{Entity: 1, X: x, Y: 0, Z: 0, Kind: RecordKindPlayer},
			{Entity: 2, X: x + 100, Y: 0, Z: 0, Kind: RecordKindPlayer},
		}
	}
	obs := []Observer{{Entity: 1, ConnID: 7}}
	b1 := pub.Build(mk(0))
	join.Step(obs, b1)
	join.Apply()
	// оба сдвинулись: наблюдатель 1 и цель 2 changed одновременно
	pub.Commit(b1) // фаза publish шага: без Commit второй Build — то же поколение
	events := join.Step(obs, pub.Build(mk(100)))
	join.Apply()
	var updates int
	for _, ev := range events {
		if ev.Kind == EventUpdate && ev.Obs.Entity == 1 && ev.Entity == 2 {
			updates++
		}
	}
	if updates != 1 {
		t.Fatalf("апдейтов пары (оба движутся) = %d; want 1 (стрим не теряется)", updates)
	}
}
