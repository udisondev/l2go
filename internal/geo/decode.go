package geo

import (
	"encoding/binary"
	"fmt"
)

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

// sizeErr — ошибка потолка размера файла региона (единая для decode и
// отображения файла).
func sizeErr(size int64) error {
	return &StructError{Code: CodeSize,
		Message: fmt.Sprintf("файл %d байт сверх потолка %d", size, maxRegionBytes)}
}

// DecodeRegion проверяет структуру байтов региона и строит индексы
// представления за один проход. Байты не копируются и не мутируются после
// передачи: Region читает слайс без повторной валидации, время жизни слайса
// обязано покрывать время жизни Region. Раскладка — порт Region.load и
// конструкторов блоков канона Mobius GeoEngine (GPL).
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
	dupLayerZ     int // ячейки со слоями равной высоты
	indexBytes    int
}

// decodeRegion — валидация и построение индексов; вся структура региона
// проверяется здесь, поэтому методы Cell могут читать без граничных
// проверок.
func decodeRegion(rx, ry int, data []byte) (*Region, *regionStats, error) {
	if rx < 0 || rx >= regionsX || ry < 0 || ry >= regionsY {
		return nil, nil, &StructError{Code: CodeRange,
			Message: fmt.Sprintf("координаты региона (%d, %d) вне сетки %d×%d", rx, ry, regionsX, regionsY)}
	}
	if !sizeOK(len(data)) {
		return nil, nil, sizeErr(int64(len(data)))
	}

	st := &regionStats{}
	blocks := make([]blockIdx, regionBlocks)
	var cells []uint16
	var dupSeen [64]uint64 // 4096 кодов высоты слоя; чистится после каждой ячейки
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
			next, err := decodeMultiBlock(data, b, off, &cells, st, &dupSeen)
			if err != nil {
				return nil, nil, err
			}
			blocks[b] = blockIdx{off: uint32(off), ml: uint32(mlBase)}
			off = next
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

// decodeMultiBlock разбирает multilayer-блок (порт конструктора
// MultilayerBlock канона: лимит слоёв 1..125) и дописывает 64
// внутриблочных смещения ячеек в cells. Возвращает офсет следующего блока.
func decodeMultiBlock(data []byte, block, off int, cells *[]uint16, st *regionStats, dupSeen *[64]uint64) (int, error) {
	p := off + 1
	for cell := 0; cell < blockCells; cell++ {
		if p >= len(data) {
			return 0, truncErr(block, p, len(data))
		}
		n := int(data[p])
		if n < layersMin || n > layersMax {
			return 0, &StructError{Code: CodeLayers, Block: block, Offset: p,
				Message: fmt.Sprintf("слоёв %d вне %d..%d", n, layersMin, layersMax)}
		}
		if p+1+2*n > len(data) {
			return 0, truncErr(block, p, len(data))
		}
		*cells = append(*cells, uint16(p-off))
		st.layers += n
		if n > st.maxLayers {
			st.maxLayers = n
		}
		if countDupLayers(data, p, n, dupSeen) {
			st.dupLayerZ++
		}
		p += 1 + 2*n
	}
	st.blocksMulti++
	return p, nil
}

// countDupLayers сообщает, есть ли в ячейке два слоя равной высоты; код
// высоты — биты 4–15 слова (12 бит). Буфер seen чистится по установленным
// битам — общая стоимость линейна по числу слоёв.
func countDupLayers(data []byte, p, n int, seen *[64]uint64) bool {
	dup := false
	for o := p + 1; o < p+1+2*n; o += 2 {
		code := binary.LittleEndian.Uint16(data[o:]) >> 4
		if seen[code>>6]>>(code&63)&1 == 1 {
			dup = true
		} else {
			seen[code>>6] |= uint64(1) << (code & 63)
		}
	}
	for o := p + 1; o < p+1+2*n; o += 2 {
		code := binary.LittleEndian.Uint16(data[o:]) >> 4
		seen[code>>6] &^= uint64(1) << (code & 63)
	}
	return dup
}

func truncErr(block, offset, size int) error {
	return &StructError{Code: CodeTrunc, Block: block, Offset: offset,
		Message: fmt.Sprintf("данные кончились: файл %d байт", size)}
}
