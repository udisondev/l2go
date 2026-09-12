package geo

// regionBuilder — детерминированный конструктор байтов региона для тестов и
// golden-фикстур. Байты собираются конструктором, а не берутся из дистрибутива
// (лицензионная позиция: фикстуры тестов — наши).

// regionBuilder собирает блоки последовательно; build дополняет регион
// flat-блоками нулевой высоты до полных 256×256.
type regionBuilder struct {
	buf []byte
	n   int
}

func newRegionBuilder() *regionBuilder {
	return &regionBuilder{buf: make([]byte, 0, regionBlocks*3)}
}

// addFlat добавляет flat-блок высоты h (полный int16, без упаковки) и
// возвращает индекс блока.
func (b *regionBuilder) addFlat(h int) int {
	b.buf = append(b.buf, blockFlatByte, byte(h), byte(uint16(h)>>8))
	idx := b.n
	b.n++
	return idx
}

// addComplex добавляет complex-блок из 64 слов ячеек и возвращает индекс.
func (b *regionBuilder) addComplex(cells [blockCells]uint16) int {
	if b.n >= regionBlocks {
		panic("regionBuilder: блоков больше 256×256")
	}
	b.buf = append(b.buf, blockComplexByte)
	for _, w := range cells {
		b.buf = append(b.buf, byte(w), byte(w>>8))
	}
	idx := b.n
	b.n++
	return idx
}

// addMultilayer добавляет multilayer-блок; у каждой ячейки — свой набор слоёв
// (слова). Ячейка без слоёв недопустима формату.
func (b *regionBuilder) addMultilayer(cells [blockCells][]uint16) int {
	if b.n >= regionBlocks {
		panic("regionBuilder: блоков больше 256×256")
	}
	b.buf = append(b.buf, blockMultiByte)
	for _, layers := range cells {
		if len(layers) < 1 || len(layers) > layersMax {
			panic("regionBuilder: слоёв вне 1..125")
		}
		b.buf = append(b.buf, byte(len(layers)))
		for _, w := range layers {
			b.buf = append(b.buf, byte(w), byte(w>>8))
		}
	}
	idx := b.n
	b.n++
	return idx
}

// build возвращает байты региона, дополняя недостающие блоки flat-нулями.
func (b *regionBuilder) build() []byte {
	for b.n < regionBlocks {
		b.addFlat(0)
	}
	return b.buf
}

// Байты типа блока в файле (см. BlockType).
const (
	blockFlatByte    = 0
	blockComplexByte = 1
	blockMultiByte   = 2
)

// encodeCellWord кодирует слово complex-ячейки/слоя: биты 4–15 — код высоты
// (h/8 со знаком), биты 0–3 — NSWE. Высота ячейки кратна 8 и лежит в
// −16384..16376 (следствие кодирования формата).
func encodeCellWord(h int, nswe NSWE) uint16 {
	if h%8 != 0 {
		panic("encodeCellWord: высота не кратна 8")
	}
	c := h / 8
	if c < -2048 || c > 2047 {
		panic("encodeCellWord: код высоты вне 12 бит")
	}
	return uint16(c)<<4 | uint16(nswe)
}

// flatComplexBlock — complex-блок из одного повторённого слова (все ячейки
// одинаковы).
func flatComplexBlock(h int, nswe NSWE) [blockCells]uint16 {
	var cells [blockCells]uint16
	for i := range cells {
		cells[i] = encodeCellWord(h, nswe)
	}
	return cells
}

// cellGeo переводит локальные координаты блока в глобальные гео-координаты.
func cellGeo(rx, ry, block, lx, ly int) (int, int) {
	return rx*regionCells + block/256*8 + lx, ry*regionCells + block%256*8 + ly
}
