package replica

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
)

// testGrid — сетка юнит-тестов: начало (−8192, −8192), границы клеток на
// кратных 8192 (0, 8192, 16384, …); сторона 8192 ≥ Exit канона.
func testGrid() Grid { return NewGrid(-8192, -8192, 13) }

// worldGrid — сетка мирового домена (те же константы, что экспортирует geo).
func worldGrid() Grid { return NewGrid(-655360, -589824, 13) }

// decodeCell — распаковка CellID по схеме gx<<16|gy.
func decodeCell(c CellID) (gx, gy int32) {
	return int32(c >> 16), int32(c & 0xFFFF)
}

// TestGridCellOfBoundariesAndClamp — границы клеток, края мира и края int32:
// значение == эталонной int64-арифметике, вне домена — детерминированный
// кламп к краевой клетке, паники нет. Ловит переполнение вычитания в int32
// (мусорный CellID) и знаковый сдвиг.
func TestGridCellOfBoundariesAndClamp(t *testing.T) {
	t.Parallel()
	g := worldGrid()
	edgeX := []int32{math.MinInt32, -655361, -655360, -655359, -73729, -73728, -73727,
		0, 1, -1, 393215, 393216, math.MaxInt32}
	edgeY := []int32{math.MinInt32, -589825, -589824, -589823, 0, 1, -1, 458751, 458752, math.MaxInt32}
	ref := func(v, min int64) int32 {
		q := (v - min) >> 13
		if q < 0 {
			return 0
		}
		if q > maxCellAxis {
			return maxCellAxis
		}
		return int32(q)
	}
	for _, x := range edgeX {
		for _, y := range edgeY {
			c := g.CellOf(x, y)
			wantX, wantY := ref(int64(x), -655360), ref(int64(y), -589824)
			gotX, gotY := decodeCell(c)
			if gotX != wantX || gotY != wantY {
				t.Errorf("CellOf(%d,%d) = клетка (%d,%d); want (%d,%d)", x, y, gotX, gotY, wantX, wantY)
			}
		}
	}
}

// TestGridCellOfPropertyDomainBoundedDeterministic — чистота и домен:
// случайные int32-координаты дают одинаковый результат повторно, распаковка
// всегда в пределах оси домена.
func TestGridCellOfPropertyDomainBoundedDeterministic(t *testing.T) {
	t.Parallel()
	g := worldGrid()
	for iter := 0; iter < 1000; iter++ {
		rng := rand.New(rand.NewPCG(7, uint64(iter)))
		x, y := rng.Int32(), rng.Int32()
		c1, c2 := g.CellOf(x, y), g.CellOf(x, y)
		if c1 != c2 {
			t.Fatalf("iter %d: CellOf недетерминирован: %d vs %d", iter, c1, c2)
		}
		gx, gy := decodeCell(c1)
		if gx < 0 || gx > maxCellAxis || gy < 0 || gy > maxCellAxis {
			t.Fatalf("iter %d: клетка (%d,%d) вне домена оси [0,%d]", iter, gx, gy, maxCellAxis)
		}
	}
}

// TestGridWindowCoversMembershipProperty — геометрия инварианта «окно 3×3
// покрывает членство» в обе стороны: (а) клетки не обе в окне ⟹ любая пара
// позиций дальше Exit (член, покинувший окно, гарантированно удаляется);
// (б) пары позиций в пределах Enter ⟹ клетки соседние (ввод невозможен вне
// окна) — фальсифицирует сужение окна при сохранении exit-порога.
func TestGridWindowCoversMembershipProperty(t *testing.T) {
	t.Parallel()
	const cell, enter, exit = 8192, 3500, 4200
	offsets := []int32{0, 1, cell / 2, cell - 1}
	posIn := func(gx int32, o int32) int64 { return int64(gx)*cell + int64(o) }
	for dx := -2; dx <= 2; dx++ {
		for dy := -2; dy <= 2; dy++ {
			inWindow := absI(int32(dx)) <= 1 && absI(int32(dy)) <= 1
			minD2 := int64(-1)
			for _, ox := range offsets {
				for _, oy := range offsets {
					for _, tx := range offsets {
						for _, ty := range offsets {
							d2x := gapI(posIn(1, ox), posIn(1+int32(dx), tx))
							d2y := gapI(posIn(1, oy), posIn(1+int32(dy), ty))
							m := d2x*d2x + d2y*d2y
							if minD2 < 0 || m < minD2 {
								minD2 = m
							}
							// (б) близкая пара обязана лежать в соседних клетках
							if m <= enter*enter && !inWindow {
								t.Errorf("пара дистанции² %d ≤ Enter² в клетках офсета (%d,%d) вне окна — ввод потерян", m, dx, dy)
							}
						}
					}
				}
			}
			if !inWindow && minD2 <= exit*exit {
				t.Errorf("офсет (%d,%d) вне окна: минимальная дистанция² = %d ≤ Exit² (ввод вне окна возможен)", dx, dy, minD2)
			}
		}
	}
}

func absI(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

func gapI(a, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}

// TestNewGridRejectsCellSizeBelowExit — фабрик-инвариант cellSize ≥ Exit:
// нарушение — ошибка конструирования (защита от тюнинга фазы 6 в
// осцилляцию); равенство допустимо (кольцо вырождается в кромку).
func TestNewJoinRejectsCellSizeBelowExit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		shift uint
		cfg   JoinConfig
		want  bool // want err == nil
	}{
		{13, CanonJoinConfig(), true},                   // 8192 ≥ 4200
		{12, CanonJoinConfig(), false},                  // 4096 < 4200 — кольцо (3500,4200] не в окне
		{12, JoinConfig{Enter: 4096, Exit: 4096}, true}, // равенство допустимо
		{13, JoinConfig{Enter: 3500, Exit: 9000}, false},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("shift=%d/exit=%d", c.shift, c.cfg.Exit), func(t *testing.T) {
			g := NewGrid(-8192, -8192, c.shift)
			_, err := NewJoin(g, c.cfg)
			if c.want && err != nil {
				t.Fatalf("NewJoin: неожиданная ошибка: %v", err)
			}
			if !c.want && err == nil {
				t.Fatalf("NewJoin: cellSize %d < Exit %d принят без ошибки", int64(1)<<c.shift, c.cfg.Exit)
			}
		})
	}
}
