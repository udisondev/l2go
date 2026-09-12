package geo

import "testing"

// TestLookupZeroAllocs: весь публичный API представления — 0 аллокаций
// (boxing-регрессии интерфейса ловятся таблицей целиком).
func TestLookupZeroAllocs(t *testing.T) {
	newMixed := func(t *testing.T) *Map {
		t.Helper()
		data, _, _, _, _ := buildProbeRegion()
		reg, _, err := decodeRegion(16, 10, data)
		if err != nil {
			t.Fatalf("decodeRegion: %v", err)
		}
		m := &Map{}
		m.regions[16*regionsY+10] = reg
		return m
	}
	m := newMixed(t)
	flatCell := m.RegionAt(16*regionCells, 10*regionCells).CellAt(16*regionCells, 10*regionCells)
	// Блок 2 — complex (blockX 0, blockY 2); зонд — ячейка (0, 0).
	complexCell := m.RegionAt(16*regionCells, 10*regionCells).CellAt(16*regionCells, 10*regionCells+16)
	// Блок 3 — multilayer (blockX 0, blockY 3); ячейка (lx, ly) = (0, 0)
	// этого блока трёхслойная.
	mlCell := m.RegionAt(16*regionCells, 10*regionCells).CellAt(16*regionCells, 10*regionCells+24)

	cases := []struct {
		name string
		fn   func()
	}{
		{"RegionAt в мире", func() { sinkRegion = m.RegionAt(16*regionCells+3, 10*regionCells+3) }},
		{"RegionAt вне мира", func() { sinkRegion = m.RegionAt(-1, 0) }},
		{"CellAt flat", func() { sinkCell = flatCell.r.CellAt(16*regionCells, 10*regionCells) }},
		{"BlockType flat", func() { sinkType = flatCell.BlockType() }},
		{"Nearest flat", func() { sinkInt, sinkNSWE = flatCell.Nearest(100) }},
		{"LowerZ flat", func() { sinkInt = flatCell.LowerZ(100) }},
		{"HigherZ flat", func() { sinkInt = flatCell.HigherZ(100) }},
		{"CellAt complex", func() { sinkCell = complexCell.r.CellAt(16*regionCells, 10*regionCells+16) }},
		{"BlockType complex", func() { sinkType = complexCell.BlockType() }},
		{"Nearest complex", func() { sinkInt, sinkNSWE = complexCell.Nearest(100) }},
		{"LowerZ complex", func() { sinkInt = complexCell.LowerZ(100) }},
		{"HigherZ complex", func() { sinkInt = complexCell.HigherZ(100) }},
		{"CellAt multilayer", func() { sinkCell = mlCell.r.CellAt(16*regionCells, 10*regionCells+24) }},
		{"BlockType multilayer", func() { sinkType = mlCell.BlockType() }},
		{"Nearest multilayer", func() { sinkInt, sinkNSWE = mlCell.Nearest(100) }},
		{"LowerZ multilayer", func() { sinkInt = mlCell.LowerZ(100) }},
		{"HigherZ multilayer", func() { sinkInt = mlCell.HigherZ(100) }},
	}
	for _, tc := range cases {
		if n := testing.AllocsPerRun(200, tc.fn); n != 0 {
			t.Errorf("%s: аллокаций %g; want 0", tc.name, n)
		}
	}
}

var (
	sinkRegion *Region
	sinkCell   Cell
	sinkInt    int
	sinkNSWE   NSWE
	sinkType   BlockType
)
