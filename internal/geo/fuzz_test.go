package geo

import (
	"os"
	"path/filepath"
	"testing"
)

// cellLayerHeights перечисляет высоты всех слоёв ячейки (оракул инвариантов
// фаззинга).
func cellLayerHeights(c Cell) []int {
	bi := &c.r.blocks[c.b]
	d := c.r.data
	off := int(bi.off)
	switch BlockType(d[off]) {
	case BlockFlat:
		return []int{int(int16(leUint16(d[off+1:])))}
	case BlockComplex:
		return []int{cellHeight(leUint16(d[off+1+2*int(c.c):]))}
	default:
		start, end := c.mlSpan()
		out := make([]int, 0, (end-start)/2)
		for o := start + 1; o < end; o += 2 {
			out = append(out, cellHeight(leUint16(d[o:])))
		}
		return out
	}
}

// FuzzDecodeRegion: декод не паникует ни на каком входе; при успехе лукапы
// держат инварианты портов канона: LowerZ(z) ≤ z ≤ HigherZ(z), Nearest-высота
// — одна из высот слоёв ячейки. Лукапы выборочные: полный свип 65536 блоков
// убил бы throughput фаззера.
func FuzzDecodeRegion(f *testing.F) {
	for _, name := range []string{"golden_flat.l2j", "golden_ml.l2j"} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			f.Fatalf("чтение %s: %v", name, err)
		}
		f.Add(data)
		f.Add(data[:len(data)/2])
		f.Add(data[:64])
	}
	f.Add([]byte{0})
	f.Add([]byte{2, 0})

	f.Fuzz(func(t *testing.T, data []byte) {
		reg, _, err := decodeRegion(16, 10, data)
		if err != nil {
			return
		}
		const baseX, baseY = 16 * regionCells, 10 * regionCells
		for i := 0; i < 64; i++ {
			lx, ly := (i*97)%regionCells, (i*131)%regionCells
			c := reg.CellAt(baseX+lx, baseY+ly)
			c.BlockType()
			for _, z := range []int{-16384, -1, 0, 1, 8, 100, 16000} {
				nz, _ := c.Nearest(z)
				if lz, hz := c.LowerZ(z), c.HigherZ(z); lz > z || hz < z {
					t.Fatalf("cell(%d,%d) z=%d: LowerZ=%d HigherZ=%d; инвариант LowerZ ≤ z ≤ HigherZ нарушен", lx, ly, z, lz, hz)
				}
				found := false
				for _, h := range cellLayerHeights(c) {
					if h == nz {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("cell(%d,%d) z=%d: Nearest=%d не является высотой слоя ячейки %v", lx, ly, z, nz, cellLayerHeights(c))
				}
			}
		}
	})
}
