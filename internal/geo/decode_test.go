package geo

import (
	"os"
	"path/filepath"
	"testing"
)

// buildProbeRegion строит регион с зоопарком блоков; возвращает байты и
// индексы зондовых блоков.
func buildProbeRegion() (data []byte, flat, negFlat, complexB, ml int) {
	b := newRegionBuilder()
	flat = b.addFlat(12345)
	negFlat = b.addFlat(-2000)

	var cells [blockCells]uint16
	cells[0] = encodeCellWord(0, East)
	cells[8] = encodeCellWord(16376, NSWEAll)
	cells[63] = encodeCellWord(-16384, North|South)
	cells[16] = encodeCellWord(-8, 0)
	complexB = b.addComplex(cells)

	var mlCells [blockCells][]uint16
	fillDefault(&mlCells, encodeCellWord(0, NSWEAll))
	mlCells[0] = []uint16{
		encodeCellWord(-160, East), encodeCellWord(0, West), encodeCellWord(160, North),
	}
	mlCells[8] = []uint16{encodeCellWord(-16, 0), encodeCellWord(16, 0)} // равные |dz| — первый
	ml = b.addMultilayer(mlCells)

	data = b.build()
	return data, flat, negFlat, complexB, ml
}

func TestDecodeGoldenProbes(t *testing.T) {
	data, flat, negFlat, complexB, ml := buildProbeRegion()
	reg, st, err := decodeRegion(16, 10, data)
	if err != nil {
		t.Fatalf("decodeRegion: %v", err)
	}
	if got := st.blocksFlat; got != regionBlocks-2 {
		t.Errorf("blocksFlat = %d; want %d", got, regionBlocks-2)
	}
	if st.blocksComplex != 1 || st.blocksMulti != 1 {
		t.Errorf("blocksComplex=%d blocksMulti=%d; want 1 и 1", st.blocksComplex, st.blocksMulti)
	}
	if got, want := st.layers, 3+2+(blockCells-2); got != want {
		t.Errorf("layers = %d; want %d", got, want)
	}
	if st.maxLayers != 3 {
		t.Errorf("maxLayers = %d; want 3", st.maxLayers)
	}

	// flat: высота полным int16, все направления открыты (порт FlatBlock).
	gx, gy := cellGeo(16, 10, flat, 3, 5)
	z, nswe := reg.CellAt(gx, gy).Nearest(0)
	if z != 12345 || nswe != NSWEAll {
		t.Errorf("flat Nearest = (%d, %v); want (12345, NSWEAll)", z, nswe)
	}
	if got := reg.CellAt(gx, gy).LowerZ(12000); got != 12000 {
		t.Errorf("flat LowerZ(12000) = %d; want 12000", got)
	}
	if got := reg.CellAt(gx, gy).HigherZ(13000); got != 13000 {
		t.Errorf("flat HigherZ(13000) = %d; want 13000", got)
	}

	gx, gy = cellGeo(16, 10, negFlat, 0, 0)
	if z, _ := reg.CellAt(gx, gy).Nearest(0); z != -2000 {
		t.Errorf("flat(−2000) Nearest = %d; want −2000", z)
	}

	// complex: границы высот и все комбинации NSWE зондами.
	var wantNSWE [blockCells]NSWE
	var wantZ [blockCells]int
	wantNSWE[0], wantZ[0] = East, 0
	wantNSWE[8], wantZ[8] = NSWEAll, 16376
	wantNSWE[63], wantZ[63] = North|South, -16384
	wantNSWE[16], wantZ[16] = 0, -8
	for lx := 0; lx < 8; lx++ {
		for ly := 0; ly < 8; ly++ {
			i := lx*8 + ly
			gx, gy = cellGeo(16, 10, complexB, lx, ly)
			gotZ, gotNSWE := reg.CellAt(gx, gy).Nearest(wantZ[i] - 1000)
			if gotZ != wantZ[i] || gotNSWE != wantNSWE[i] {
				t.Errorf("complex(%d,%d) = (%d, %v); want (%d, %v)", lx, ly, gotZ, gotNSWE, wantZ[i], wantNSWE[i])
			}
			if got := reg.CellAt(gx, gy).BlockType(); got != BlockComplex {
				t.Errorf("complex(%d,%d).BlockType = %v; want BlockComplex", lx, ly, got)
			}
		}
	}

	// multilayer: точное совпадение, ближайший, тай-брейк первого.
	c := reg.CellAt(cellGeo(16, 10, ml, 0, 0))
	if z, nswe := c.Nearest(0); z != 0 || nswe != West {
		t.Errorf("ml Nearest(0) = (%d, %v); want (0, West)", z, nswe)
	}
	if z, _ := c.Nearest(100); z != 160 {
		t.Errorf("ml Nearest(100) = %d; want 160", z)
	}
	if z, _ := c.Nearest(-100); z != -160 {
		t.Errorf("ml Nearest(−100) = %d; want −160", z)
	}
	if got := c.LowerZ(0); got != 0 {
		t.Errorf("ml LowerZ(0) = %d; want 0 (точное совпадение — порт канона)", got)
	}
	if got := c.HigherZ(0); got != 0 {
		t.Errorf("ml HigherZ(0) = %d; want 0 (точное совпадение — порт канона)", got)
	}
	if got := c.LowerZ(-100); got != -160 {
		t.Errorf("ml LowerZ(−100) = %d; want −160", got)
	}
	if got := c.HigherZ(100); got != 160 {
		t.Errorf("ml HigherZ(100) = %d; want 160", got)
	}
	if got := c.LowerZ(-500); got != -500 {
		t.Errorf("ml LowerZ(−500) = %d; want −500 (ниже слоёв нет)", got)
	}
	if got := c.HigherZ(500); got != 500 {
		t.Errorf("ml HigherZ(500) = %d; want 500 (выше слоёв нет)", got)
	}
	if got := c.BlockType(); got != BlockMultilayer {
		t.Errorf("ml BlockType = %v; want BlockMultilayer", got)
	}

	tie := reg.CellAt(cellGeo(16, 10, ml, 1, 0))
	if z, _ := tie.Nearest(0); z != -16 {
		t.Errorf("ml тай-брейк Nearest(0) = %d; want −16 (первый слой)", z)
	}
}

func TestDecodeLastBlockAndMaxLayers(t *testing.T) {
	// Последний блок региона и граница 125 слоёв.
	b := newRegionBuilder()
	var top [blockCells][]uint16
	fillDefault(&top, encodeCellWord(0, NSWEAll))
	layers := make([]uint16, layersMax)
	for i := range layers {
		layers[i] = encodeCellWord(-496+8*i, NSWEAll)
	}
	top[63] = layers
	first := b.addMultilayer(top)
	for b.n < regionBlocks-1 {
		b.addFlat(0)
	}
	last := b.addFlat(7)
	data := b.build()

	reg, st, err := decodeRegion(0, 31, data)
	if err != nil {
		t.Fatalf("decodeRegion: %v", err)
	}
	if st.maxLayers != layersMax {
		t.Errorf("maxLayers = %d; want %d", st.maxLayers, layersMax)
	}
	if got, want := st.layers, layersMax+(blockCells-1); got != want {
		t.Errorf("layers = %d; want %d", got, want)
	}
	c := reg.CellAt(cellGeo(0, 31, first, 7, 7))
	if z, nswe := c.Nearest(0); z != 0 || nswe != NSWEAll {
		t.Errorf("125-слойная ячейка Nearest(0) = (%d, %v); want (0, NSWEAll)", z, nswe)
	}
	lc := reg.CellAt(cellGeo(0, 31, last, 7, 7))
	if z, _ := lc.Nearest(0); z != 7 {
		t.Errorf("последний блок Nearest = %d; want 7", z)
	}
}

func TestDecodeDupLayerZ(t *testing.T) {
	b := newRegionBuilder()
	var cells [blockCells][]uint16
	fillDefault(&cells, encodeCellWord(0, NSWEAll))
	// Пара равных высот и тройка равных — обе ячейки считаются по одному разу.
	cells[0] = []uint16{encodeCellWord(0, East), encodeCellWord(0, West)}
	cells[8] = []uint16{encodeCellWord(64, North), encodeCellWord(64, South), encodeCellWord(64, West)}
	b.addMultilayer(cells)
	reg, st, err := decodeRegion(16, 10, b.build())
	if err != nil {
		t.Fatalf("decodeRegion: %v", err)
	}
	if st.dupLayerZ != 2 {
		t.Errorf("dupLayerZ = %d; want 2 (ячейки, а не повторные слои)", st.dupLayerZ)
	}
	// Дубль высоты не мешает точному совпадению: первый слой побеждает.
	if z, nswe := reg.CellAt(cellGeo(16, 10, 0, 0, 0)).Nearest(0); z != 0 || nswe != East {
		t.Errorf("Nearest при дубле = (%d, %v); want (0, East)", z, nswe)
	}
}

// TestComplexAllNSWE — все 16 комбинаций NSWE complex-блока читаются
// битов-в-бит.
func TestComplexAllNSWE(t *testing.T) {
	b := newRegionBuilder()
	var cells [blockCells]uint16
	for lx := 0; lx < 8; lx++ {
		for ly := 0; ly < 8; ly++ {
			i := lx*8 + ly
			cells[i] = encodeCellWord(8*(i/2), NSWE(i%16))
		}
	}
	blk := b.addComplex(cells)
	reg, _, err := decodeRegion(16, 10, b.build())
	if err != nil {
		t.Fatalf("decodeRegion: %v", err)
	}
	for lx := 0; lx < 8; lx++ {
		for ly := 0; ly < 8; ly++ {
			i := lx*8 + ly
			z, nswe := reg.CellAt(cellGeo(16, 10, blk, lx, ly)).Nearest(0)
			if z != 8*(i/2) || nswe != NSWE(i%16) {
				t.Errorf("complex(%d,%d) = (%d, %v); want (%d, %v)", lx, ly, z, nswe, 8*(i/2), NSWE(i%16))
			}
		}
	}
}

func TestDecodeEvilInputs(t *testing.T) {
	flat := make([]byte, 0, regionBlocks*3)
	for i := 0; i < regionBlocks; i++ {
		flat = append(flat, blockFlatByte, 0, 0)
	}
	cases := []struct {
		name   string
		data   []byte
		code   string
		block  int
		offset int
	}{
		{"пустой файл", nil, CodeTrunc, 0, 0},
		{"flat обрезан", []byte{blockFlatByte, 1}, CodeTrunc, 0, 0},
		{"complex обрезан", append([]byte{blockComplexByte}, make([]byte, 127)...), CodeTrunc, 0, 0},
		{"multilayer обрезан на ячейках", []byte{blockMultiByte, 1, 0, 0x10, 1}, CodeTrunc, 0, 4},
		{"тип блока 3", []byte{3}, CodeBlockType, 0, 0},
		{"тип блока 255", []byte{255}, CodeBlockType, 0, 0},
		{"слоёв 0", []byte{blockMultiByte, 0}, CodeLayers, 0, 1},
		{"слоёв 126", append([]byte{blockMultiByte, 126}, make([]byte, 252)...), CodeLayers, 0, 1},
		{"лишний хвост", append(append([]byte{}, flat...), 0), CodeTail, regionBlocks - 1, len(flat)},
	}
	for _, tc := range cases {
		_, _, err := decodeRegion(16, 10, tc.data)
		if err == nil {
			t.Errorf("%s: ошибки нет; want %s", tc.name, tc.code)
			continue
		}
		se, ok := err.(*StructError)
		if !ok {
			t.Errorf("%s: ошибка %T (%v); want *StructError", tc.name, err, err)
			continue
		}
		if se.Code != tc.code || se.Block != tc.block || se.Offset != tc.offset {
			t.Errorf("%s: код %s блок %d офсет %d; want %s блок %d офсет %d",
				tc.name, se.Code, se.Block, se.Offset, tc.code, tc.block, tc.offset)
		}
		if se.Message == "" {
			t.Errorf("%s: пустое сообщение", tc.name)
		}
	}

	_, _, err := decodeRegion(regionsX, 10, flat)
	if err == nil {
		t.Fatalf("регион вне сетки: ошибки нет; want %s", CodeRange)
	}
	if se := err.(*StructError); se.Code != CodeRange {
		t.Errorf("регион вне сетки: код %s; want %s", se.Code, CodeRange)
	}
	if _, _, err := decodeRegion(-1, 10, flat); err == nil {
		t.Error("отрицательная координата региона: ошибки нет")
	}
}

func TestSizeCeiling(t *testing.T) {
	if !sizeOK(maxRegionBytes) {
		t.Errorf("sizeOK(%d) = false; want true (максимум легального файла меньше)", maxRegionBytes)
	}
	if sizeOK(maxRegionBytes + 1) {
		t.Errorf("sizeOK(%d) = true; want false", maxRegionBytes+1)
	}
	if sizeOK(-1) {
		t.Error("sizeOK(−1) = true; want false")
	}
}

func TestCoordinates(t *testing.T) {
	if got := WorldToGeoX(worldMinX); got != 0 {
		t.Errorf("WorldToGeoX(%d) = %d; want 0", worldMinX, got)
	}
	if got := WorldToGeoY(worldMinY); got != 0 {
		t.Errorf("WorldToGeoY(%d) = %d; want 0", worldMinY, got)
	}
	if got := GeoToWorldX(0); got != worldMinX+worldCenter {
		t.Errorf("GeoToWorldX(0) = %d; want %d", got, worldMinX+worldCenter)
	}
	// Тайл 16 начинается с мировой X −131072 → гео 32768 → регион 16.
	if got := WorldToGeoX(-131072); got != 32768 {
		t.Errorf("WorldToGeoX(−131072) = %d; want 32768", got)
	}
}

func TestRegionAtBounds(t *testing.T) {
	reg, _, err := decodeRegion(16, 10, newRegionBuilder().build())
	if err != nil {
		t.Fatalf("decodeRegion: %v", err)
	}
	m := &Map{}
	m.regions[16*regionsY+10] = reg
	if m.RegionAt(16*regionCells, 10*regionCells) != reg {
		t.Error("RegionAt на первой ячейке региона = nil")
	}
	if m.RegionAt(16*regionCells+regionCells-1, 10*regionCells+regionCells-1) != reg {
		t.Error("RegionAt на последней ячейке региона = nil")
	}
	if m.RegionAt(16*regionCells-1, 10*regionCells) != nil {
		t.Error("RegionAt до региона ≠ nil")
	}
	if m.RegionAt(16*regionCells+regionCells, 10*regionCells) != nil {
		t.Error("RegionAt за регионом ≠ nil")
	}
	if m.RegionAt(-1, 0) != nil {
		t.Error("RegionAt(−1) ≠ nil")
	}
	if m.RegionAt(regionsX*regionCells, 0) != nil {
		t.Error("RegionAt вне мира ≠ nil")
	}
}

func TestCellAtContract(t *testing.T) {
	reg, _, err := decodeRegion(16, 10, newRegionBuilder().build())
	if err != nil {
		t.Fatalf("decodeRegion: %v", err)
	}
	for _, tc := range [][2]int{
		{16*regionCells - 1, 10 * regionCells}, // до региона
		{17 * regionCells, 10 * regionCells},   // за регионом
		{16 * regionCells, 10*regionCells - 1}, // до региона по Y
		{16 * regionCells, 11 * regionCells},   // за регионом по Y
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("CellAt(%d, %d) вне региона: паники нет", tc[0], tc[1])
				}
			}()
			reg.CellAt(tc[0], tc[1])
		}()
	}
}

// goldenMLRegion — multilayer-тяжёлая golden-фикстура: граница 125 слоёв,
// трёхслойная ячейка, дубль высоты, complex-блок.
func goldenMLRegion() []byte {
	b := newRegionBuilder()
	var top [blockCells][]uint16
	fillDefault(&top, encodeCellWord(0, NSWEAll))
	layers := make([]uint16, layersMax)
	for i := range layers {
		layers[i] = encodeCellWord(-496+8*i, NSWEAll)
	}
	top[0] = layers
	var mixed [blockCells][]uint16
	fillDefault(&mixed, encodeCellWord(0, NSWEAll))
	mixed[5] = []uint16{encodeCellWord(-160, East), encodeCellWord(0, West), encodeCellWord(160, North)}
	mixed[9] = []uint16{encodeCellWord(8, 0), encodeCellWord(8, 0)} // дубль высоты
	b.addMultilayer(top)
	b.addMultilayer(mixed)
	b.addComplex(flatComplexBlock(1000, South))
	return b.build()
}

func TestGoldenFixtures(t *testing.T) {
	cases := []struct {
		name  string
		build func() []byte
	}{
		{"golden_flat.l2j", func() []byte { d, _, _, _, _ := buildProbeRegion(); return d }},
		{"golden_ml.l2j", goldenMLRegion},
	}
	for _, tc := range cases {
		path := filepath.Join("testdata", tc.name)
		data := tc.build()
		if os.Getenv("GEO_UPDATE_GOLDEN") == "1" {
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatalf("запись %s: %v", path, err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("чтение %s: %v (регенерация: GEO_UPDATE_GOLDEN=1 go test ./internal/geo -run TestGoldenFixtures)", path, err)
		}
		if string(want) != string(data) {
			t.Errorf("%s: байты разошлись с билдером (регенерация: GEO_UPDATE_GOLDEN=1)", tc.name)
		}
	}
}
