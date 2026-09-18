package replica

import (
	"reflect"
	"testing"

	"github.com/udisondev/l2go/internal/transport"
)

// TestBlobSegmentsPerCellLayout — плотная укладка: сегменты сортированы по
// CellID и не пусты, слоты глобальные и возрастают внутри сегмента, свободный
// слот несёт сентинел cellInvalid (нулевая клетка валидна), повторный Build
// идентичен (реплей/дампы стабильны).
func TestBlobSegmentsPerCellLayout(t *testing.T) {
	t.Parallel()
	g := testGrid()
	p := NewPublisher(g)
	recs := []Record{
		recAt(1, -100, -100),  // клетка (0,0)
		recAt(2, 5000, -100),  // клетка (1,0)
		recAt(3, -100, 5000),  // клетка (0,1)
		recAt(4, 9000, -100),  // клетка (2,0)
		recAt(5, -7000, -100), // клетка (0,0)
	}
	b := p.Build(recs)
	var cells []CellID
	for _, s := range b.segs {
		cells = append(cells, s.cell)
		if s.slotLen == 0 {
			t.Fatalf("пустой сегмент клетки %d", s.cell)
		}
		last := -1
		for k := s.slotOff; k < s.slotOff+s.slotLen; k++ {
			if b.slots[k] <= last {
				t.Fatalf("слоты сегмента %d не возрастают: %d после %d", s.cell, b.slots[k], last)
			}
			last = b.slots[k]
			if got := b.seatBySlot[b.slots[k]]; got.cell != s.cell || got.ent != b.records[k].Entity {
				t.Fatalf("слот %d: сид {клетка %d, жилец %d} ≠ сегменту/записи", b.slots[k], got.cell, got.ent)
			}
		}
	}
	if !sortCells(cells) {
		t.Fatalf("сегменты не сортированы по CellID: %v", cells)
	}
	// глобальность слотов: сиды покрывают занятые, дырок в населении нет
	for slot, st := range b.seatBySlot {
		occupied := slot < len(b.slotPos) && b.slotPos[slot] >= 0
		if occupied && (st.cell == cellInvalid || st.ent == 0) {
			t.Fatalf("занятый слот %d без жильца", slot)
		}
		if !occupied && st.cell != cellInvalid {
			t.Fatalf("свободный слот %d с клеткой %d; want сентинел cellInvalid (нулевая клетка валидна)", slot, st.cell)
		}
	}
	// детерминизм раскладки: повторный Build того же населения идентичен
	b2 := p.Build(recs)
	if !reflect.DeepEqual(b.segs, b2.segs) || !reflect.DeepEqual(b.slots, b2.slots) {
		t.Fatalf("раскладка недетерминирована: %v/%v vs %v/%v", b.segs, b.slots, b2.segs, b2.slots)
	}
}

func sortCells(cs []CellID) bool {
	for i := 1; i < len(cs); i++ {
		if cs[i-1] >= cs[i] {
			return false
		}
	}
	return true
}

// TestBlobSingleCellDegenerateLayout — вырожденные 0/1/N-в-одной-клетке:
// пустые segs не паникуют в Step/Apply, одиночная запись — один сегмент,
// плотная клетка — слоты без дыр (закон оси 6: вырожденная конфигурация
// валидна).
func TestBlobSingleCellDegenerateLayout(t *testing.T) {
	t.Parallel()
	g := testGrid()
	p := NewPublisher(g)
	j, err := NewJoin(g, CanonJoinConfig())
	if err != nil {
		t.Fatalf("NewJoin: %v", err)
	}
	if evs := stepPair(t, p, j, nil, nil); len(evs) != 0 {
		t.Fatalf("пустое население: события %v", evs)
	}
	if len(p.Committed().segs) != 0 {
		t.Fatalf("пустое население: сегменты %v", p.Committed().segs)
	}
	p2 := NewPublisher(g)
	b := p2.Build([]Record{recAt(1, -100, -100)})
	if len(b.segs) != 1 || b.segs[0].slotLen != 1 {
		t.Fatalf("одиночная запись: сегменты %+v", b.segs)
	}
	recs := make([]Record, 50)
	for i := range recs {
		recs[i] = recAt(transport.EntityID(i+1), -100-int32(i)*10, -100)
	}
	b = p2.Build(recs)
	if len(b.segs) != 1 || b.segs[0].slotLen != len(recs) {
		t.Fatalf("одна клетка: сегменты %+v (want 1 сегмент на %d записей)", b.segs, len(recs))
	}
}

// TestPublisherCellChangeRegroupsSlotLives — смена ячейки целью:
// перегруппировка сегментов при живом слоте, changed-бит от смены Cell,
// born/gone не взводятся, маркеры переезда не порождаются (переезд между
// ячейками — не хэндофф), наблюдателю ровно один EventUpdate.
func TestPublisherCellChangeRegroupsSlotLives(t *testing.T) {
	t.Parallel()
	g := testGrid()
	cfg := CanonJoinConfig()
	p := NewPublisher(g)
	j, err := NewJoin(g, cfg)
	if err != nil {
		t.Fatalf("NewJoin: %v", err)
	}
	obs := recAt(1, -500, -500)
	target := recAt(2, -100, -100) // клетка (0,0)
	stepPair(t, p, j, []Observer{obsOf(obs)}, []Record{obs, target})
	slot := p.Committed().slotOf[2]
	blob := p.Build([]Record{obs, recAt(2, 100, -100)}) // клетка (1,0), d ≈ 721 ≤ Enter
	if got := blob.slotOf[2]; got != slot {
		t.Fatalf("смена клетки: слот цели = %d; want живой %d", got, slot)
	}
	if !bitHas(blob.changed, slot) || bitHas(blob.born, slot) || bitHas(blob.gone, slot) {
		t.Fatalf("манифест слота при смене клетки: changed=%v born=%v gone=%v; want changed только",
			bitHas(blob.changed, slot), bitHas(blob.born, slot), bitHas(blob.gone, slot))
	}
	events := j.Step([]Observer{obsOf(obs)}, blob)
	j.Apply()
	p.Commit(blob)
	updates, others := 0, 0
	for _, ev := range events {
		switch {
		case ev.Kind == EventUpdate && ev.Target.Entity == 2:
			updates++
		case ev.Obs.Entity == 1:
			others++
		}
	}
	if updates != 1 || others != 0 {
		t.Fatalf("смена клетки целью: update=%d прочие=%d; want 1/0 (события %v)", updates, others, events)
	}
	if len(blob.header.Moving) != 0 {
		t.Fatalf("смена клетки породила маркеры переезда: %v", blob.header.Moving)
	}
	// сегмент слота теперь клетка (1,0)
	if got := blob.seatBySlot[slot].cell; got != g.CellOf(100, -100) {
		t.Fatalf("слот остался в старом сегменте: клетка сида %d", got)
	}
}

// TestBuildAllocsIndependentOfCellCount — плотная укладка: счёт аллокаций
// Build+Commit не зависит от числа клеток (per-сегментные слайсы дали бы
// ~2×#ячеек). Абсолют фиксируется комментарием (OQ-3/S6: ~8–9 с битмапами).
// Без Parallel: AllocsPerRun зовёт runtime.GC — глобальная точка.
func TestBuildAllocsIndependentOfCellCount(t *testing.T) {
	g := testGrid()
	build := func(recs []Record) float64 {
		p := NewPublisher(g)
		p.Commit(p.Build(recs)) // база prev: стационарный dirty-путь
		return testing.AllocsPerRun(100, func() {
			p.Commit(p.Build(recs))
		})
	}
	one := make([]Record, 1000)
	for i := range one {
		one[i] = recAt(transport.EntityID(i+1), -100-int32(i%50)*10, -100-int32(i/50)*10)
	}
	multi := make([]Record, 1000)
	for i := range multi {
		multi[i] = recAt(transport.EntityID(i+1), int32(i%10)*8192+100, int32(i/10)*8192+100)
	}
	a, b := build(one), build(multi)
	if a != b {
		t.Fatalf("аллокации зависят от числа клеток: 1 клетка = %.0f, 100 клеток = %.0f; want равны", a, b)
	}
	// измеренный абсолют стационарного Build+Commit: карты/present + порядок +
	// слоты + записи + сиды + позиции + сегменты + буфер битмап (бюджет Idle-шага
	// мира — TestRegionStepIdleAllocBudget; ср. benchmarks/P4.1-*-baseline.txt)
	t.Logf("Build+Commit на 1000 записей: %.0f аллокаций (независимо от клеток)", a)
}

// TestPublisherReadRoutesByCellSegment — advisory Read идёт по сегменту
// клетки: попадание с верными полями, промах по чужой клетке, переезд цели
// меняет маршрут (сигнатура Read не меняется).
func TestPublisherReadRoutesByCellSegment(t *testing.T) {
	t.Parallel()
	g := testGrid()
	p := NewPublisher(g)
	p.Commit(p.Build([]Record{recAt(1, -100, -100), recAt(2, 5000, -100)}))
	cellA, cellB := g.CellOf(-100, -100), g.CellOf(5000, -100)
	snap, ok := p.Read(cellB, 2)
	if !ok || snap.Entity() != 2 {
		t.Fatalf("Read(клетка цели) промахнулся: ok=%v snap=%v", ok, snap)
	}
	if x, _, _ := snap.Pos(); x != 5000 {
		t.Fatalf("Read вернул чужие поля: x=%d; want 5000", x)
	}
	if _, ok := p.Read(cellA, 2); ok {
		t.Fatalf("Read по чужой клетке дал hit (поиск игнорирует клетку)")
	}
	p.Commit(p.Build([]Record{recAt(1, -100, -100), recAt(2, 9000, -100)}))
	cellC := g.CellOf(9000, -100)
	if _, ok := p.Read(cellB, 2); ok {
		t.Fatalf("Read по старой клетке переезда дал hit")
	}
	if _, ok := p.Read(cellC, 2); !ok {
		t.Fatalf("Read по новой клетке переезда промахнулся")
	}
}
