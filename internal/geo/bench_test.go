package geo

import (
	"crypto/sha256"
	"sync"
	"testing"
)

// Фикстуры бенчей строятся лениво один раз: карта с одним регионом и три
// состава региона — целиком flat, целиком complex, multilayer с 3 слоями в
// каждой ячейке.
var (
	benchOnce   sync.Once
	flatData    []byte
	complexData []byte
	mlData      []byte
	benchMap    *Map
	flatCell    Cell
	complexCell Cell
	mlCell      Cell
	mlInputs    map[string][sha256.Size]byte
)

func buildBenchFixtures() {
	fb := newRegionBuilder()
	for fb.n < regionBlocks {
		fb.addFlat(1000)
	}
	flatData = fb.build()

	cb := newRegionBuilder()
	cells := flatComplexBlock(1000, NSWEAll)
	for cb.n < regionBlocks {
		cb.addComplex(cells)
	}
	complexData = cb.build()

	mb := newRegionBuilder()
	one := [3]uint16{encodeCellWord(-16, East), encodeCellWord(0, West), encodeCellWord(16, North)}
	var mc [blockCells][]uint16
	for i := range mc {
		mc[i] = one[:]
	}
	for mb.n < regionBlocks {
		mb.addMultilayer(mc)
	}
	mlData = mb.build()

	flatReg, _, _ := decodeRegion(16, 10, flatData)
	complexReg, _, _ := decodeRegion(16, 10, complexData)
	mlReg, _, _ := decodeRegion(16, 10, mlData)
	flatCell = flatReg.CellAt(16*regionCells, 10*regionCells)
	complexCell = complexReg.CellAt(16*regionCells, 10*regionCells)
	mlCell = mlReg.CellAt(16*regionCells, 10*regionCells)

	benchMap = &Map{}
	benchMap.regions[16*RegionsY+10] = mlReg
	mlInputs = map[string][sha256.Size]byte{}
}

func benchReady() {
	benchOnce.Do(buildBenchFixtures)
}

func BenchmarkRegionAtInWorld(b *testing.B) {
	benchReady()
	b.ReportAllocs()
	for b.Loop() {
		sinkRegion = benchMap.RegionAt(16*regionCells+3, 10*regionCells+3)
	}
}

func BenchmarkRegionAtOutOfWorld(b *testing.B) {
	benchReady()
	b.ReportAllocs()
	for b.Loop() {
		sinkRegion = benchMap.RegionAt(-1, 0)
	}
}

func BenchmarkCellAtFlat(b *testing.B) {
	benchReady()
	b.ReportAllocs()
	for b.Loop() {
		sinkCell = flatCell.r.CellAt(16*regionCells, 10*regionCells)
	}
}

func BenchmarkCellAtComplex(b *testing.B) {
	benchReady()
	b.ReportAllocs()
	for b.Loop() {
		sinkCell = complexCell.r.CellAt(16*regionCells, 10*regionCells)
	}
}

func BenchmarkCellAtMultilayer(b *testing.B) {
	benchReady()
	b.ReportAllocs()
	for b.Loop() {
		sinkCell = mlCell.r.CellAt(16*regionCells, 10*regionCells)
	}
}

func BenchmarkBlockTypeFlat(b *testing.B) {
	benchReady()
	b.ReportAllocs()
	for b.Loop() {
		sinkType = flatCell.BlockType()
	}
}

func BenchmarkBlockTypeComplex(b *testing.B) {
	benchReady()
	b.ReportAllocs()
	for b.Loop() {
		sinkType = complexCell.BlockType()
	}
}

func BenchmarkBlockTypeMultilayer(b *testing.B) {
	benchReady()
	b.ReportAllocs()
	for b.Loop() {
		sinkType = mlCell.BlockType()
	}
}

func BenchmarkNearestMultilayer(b *testing.B) {
	benchReady()
	b.ReportAllocs()
	for b.Loop() {
		sinkInt, sinkNSWE = mlCell.Nearest(4)
	}
}

func BenchmarkLowerZMultilayer(b *testing.B) {
	benchReady()
	b.ReportAllocs()
	for b.Loop() {
		sinkInt = mlCell.LowerZ(4)
	}
}

func BenchmarkHigherZMultilayer(b *testing.B) {
	benchReady()
	b.ReportAllocs()
	for b.Loop() {
		sinkInt = mlCell.HigherZ(4)
	}
}

// BenchmarkDecodeRegion — цена валидации и построения индексов (SetBytes —
// пропускная способность по байтам региона).
func BenchmarkDecodeRegion(b *testing.B) {
	benchReady()
	b.ReportAllocs()
	b.SetBytes(int64(len(mlData)))
	for b.Loop() {
		_, _, err := decodeRegion(16, 10, mlData)
		if err != nil {
			b.Fatalf("decodeRegion: %v", err)
		}
	}
}

// BenchmarkManifest — цена манифеста: SHA-256 содержимого файла + свёртка
// состава, как в LoadDir (SetBytes — ГБ/с по байтам файла).
func BenchmarkManifest(b *testing.B) {
	benchReady()
	b.ReportAllocs()
	b.SetBytes(int64(len(mlData)))
	for b.Loop() {
		mlInputs["16_10.l2j"] = sha256.Sum256(mlData)
		sinkManifest = manifestOf(mlInputs)
	}
}

var sinkManifest [sha256.Size]byte
