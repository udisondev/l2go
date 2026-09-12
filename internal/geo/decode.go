package geo

import "fmt"

// StructError — структурный дефект файла геодаты; Block — индекс блока
// 0..65535, Offset — смещение в байтах от начала файла.
type StructError struct {
	Code    string
	Block   int
	Offset  int
	Message string
}

func (e *StructError) Error() string {
	return fmt.Sprintf("гео %s: блок %d, офсет %d: %s", e.Code, e.Block, e.Offset, e.Message)
}

// DecodeRegion проверяет структуру байтов региона и строит индексы
// представления за один проход. Байты не копируются: Region ссылается на
// data, время жизни слайса обязано покрывать время жизни Region.
func DecodeRegion(rx, ry int, data []byte) (*Region, error) {
	reg, _, err := decodeRegion(rx, ry, data)
	return reg, err
}

// regionStats — счётчики одного региона для отчёта загрузки.
type regionStats struct {
	blocksFlat    int
	blocksComplex int
	blocksMulti   int
	layers        int // сумма слоёв по multilayer-ячейкам
	maxLayers     int
	dupLayerZ     int
	indexBytes    int
}

// decodeRegion — валидация и построение индексов; вся структура региона
// проверяется здесь, поэтому методы Cell могут читать без граничных
// проверок.
func decodeRegion(rx, ry int, data []byte) (*Region, *regionStats, error) {
	if rx < 0 || rx >= RegionsX || ry < 0 || ry >= RegionsY {
		return nil, nil, &StructError{Code: CodeRange,
			Message: fmt.Sprintf("координаты региона (%d, %d) вне сетки %d×%d", rx, ry, RegionsX, RegionsY)}
	}
	if !sizeOK(len(data)) {
		return nil, nil, &StructError{Code: CodeSize,
			Message: fmt.Sprintf("файл %d байт сверх потолка %d", len(data), maxRegionBytes)}
	}

	st := &regionStats{}
	blocks := make([]blockIdx, regionBlocks)
	var cells []uint16
	var dupSeen [64]uint64 // 4096 кодов высоты слоя (12 бит)
	off := 0
	for b := 0; b < regionBlocks; b++ {
		if off >= len(data) {
			return nil, nil, truncErr(b, off, len(data))
		}
		switch BlockType(data[off]) {
		case BlockFlat:
			if off+3 > len(data) {
				return nil, nil, truncErr(b, off, len(data))
			}
			blocks[b] = blockIdx{off: uint32(off), ml: noML}
			st.blocksFlat++
			off += 3
		case BlockComplex:
			if off+1+2*blockCells > len(data) {
				return nil, nil, truncErr(b, off, len(data))
			}
			blocks[b] = blockIdx{off: uint32(off), ml: noML}
			st.blocksComplex++
			off += 1 + 2*blockCells
		case BlockMultilayer:
			mlBase := len(cells)
			blocks[b].off = uint32(off)
			p := off + 1
			for cell := 0; cell < blockCells; cell++ {
				if p >= len(data) {
					return nil, nil, truncErr(b, p, len(data))
				}
				n := int(data[p])
				if n < layersMin || n > layersMax {
					return nil, nil, &StructError{Code: CodeLayers, Block: b, Offset: p,
						Message: fmt.Sprintf("слоёв %d вне %d..%d", n, layersMin, layersMax)}
				}
				if p+1+2*n > len(data) {
					return nil, nil, truncErr(b, p, len(data))
				}
				cells = append(cells, uint16(p-off))
				st.layers += n
				if n > st.maxLayers {
					st.maxLayers = n
				}
				// Дубликаты высот слоёв: код высоты = биты 4–15 слова.
				for o := p + 1; o < p+1+2*n; o += 2 {
					code := leUint16(data[o:]) >> 4
					if dupSeen[code>>6]>>(code&63)&1 == 1 {
						st.dupLayerZ++
					} else {
						dupSeen[code>>6] |= uint64(1) << (code & 63)
					}
				}
				for o := p + 1; o < p+1+2*n; o += 2 {
					code := leUint16(data[o:]) >> 4
					dupSeen[code>>6] &^= uint64(1) << (code & 63)
				}
				p += 1 + 2*n
			}
			blocks[b].ml = uint32(mlBase)
			st.blocksMulti++
			off = p
		default:
			return nil, nil, &StructError{Code: CodeBlockType, Block: b, Offset: off,
				Message: fmt.Sprintf("тип блока %d", data[off])}
		}
	}
	if off != len(data) {
		return nil, nil, &StructError{Code: CodeTail, Block: regionBlocks - 1, Offset: off,
			Message: fmt.Sprintf("%d лишних байт после %d блоков", len(data)-off, regionBlocks)}
	}

	// Финализация до точного размера: append-рост оставляет запас ёмкости,
	// который иначе остался бы в куче навсегда.
	if len(cells) > 0 {
		final := make([]uint16, len(cells))
		copy(final, cells)
		cells = final
	}
	st.indexBytes = len(blocks)*8 + len(cells)*2

	return &Region{rx: rx, ry: ry, data: data, blocks: blocks, cells: cells}, st, nil
}

func truncErr(block, offset, size int) error {
	return &StructError{Code: CodeTrunc, Block: block, Offset: offset,
		Message: fmt.Sprintf("данные кончились: файл %d байт", size)}
}
