package geo

import "fmt"

// Map — геодата мира: регионы без файла — nil. Статика: после загрузки не
// мутируется, чтение из любой горутины без синхронизации.
type Map struct {
	regions [RegionsX * RegionsY]*Region
}

// RegionAt возвращает регион глобальных гео-координат или nil, если геодаты
// нет (в том числе для координат вне мира). Порт GeoEngine.getRegion.
func (m *Map) RegionAt(geoX, geoY int) *Region {
	if uint(geoX) >= RegionsX*regionCells || uint(geoY) >= RegionsY*regionCells {
		return nil
	}
	return m.regions[geoX/regionCells*RegionsY+geoY/regionCells]
}

// blockIdx — индексная запись блока: смещение в байтах региона и старт
// внутриблочных смещений ячеек multilayer-блока (noML — блок однослойный).
type blockIdx struct {
	off uint32
	ml  uint32
}

const noML = ^uint32(0)

// Region — один регион геодаты. Байты data не копируются (обычно mmap вне
// кучи); blocks и cells — производные индексы, иммутабельные после загрузки.
type Region struct {
	rx, ry int
	data   []byte
	blocks []blockIdx
	cells  []uint16 // 64 внутриблочных смещения на каждый multilayer-блок
}

// CellAt возвращает ячейку глобальных гео-координат. Координаты обязаны
// принадлежать этому региону (регион определяет Map.RegionAt); передача
// координат чужого региона — нарушение программного контракта вызывающего.
func (r *Region) CellAt(geoX, geoY int) Cell {
	lx, ly := geoX-r.rx*regionCells, geoY-r.ry*regionCells
	if uint(lx) >= regionCells || uint(ly) >= regionCells {
		panic(fmt.Sprintf("geo: CellAt(%d, %d) вне региона (%d, %d)", geoX, geoY, r.rx, r.ry))
	}
	return Cell{r: r, b: uint16(lx>>3)<<8 | uint16(ly>>3), c: uint8(lx&7)<<3 | uint8(ly&7)}
}

// Cell — ячейка представления; все методы без аллокаций.
type Cell struct {
	r *Region
	b uint16
	c uint8
}

// BlockType возвращает тип блока ячейки.
func (c Cell) BlockType() BlockType {
	return BlockType(c.r.data[c.r.blocks[c.b].off])
}

// Nearest возвращает слой, ближайший к z, и его флаги направлений.
// Порт MultilayerBlock.getNearestLayer: точное совпадение — немедленно,
// иначе минимум |dz|, при равенстве — первый слой по порядку. Flat-блок —
// высота блока и NSWEAll (порт FlatBlock: все направления открыты);
// complex — единственный слой.
func (c Cell) Nearest(z int) (int, NSWE) {
	bi := &c.r.blocks[c.b]
	off := int(bi.off)
	switch BlockType(c.r.data[off]) {
	case BlockFlat, BlockComplex:
		return c.soleLayer(off)
	default:
		return c.nearestLayer(off, z)
	}
}

// LowerZ возвращает высоту слоя не выше z; точное совпадение — сразу; слоёв
// ниже нет — сам z. Порт getNextLowerZ канона.
func (c Cell) LowerZ(z int) int {
	off := int(c.r.blocks[c.b].off)
	switch BlockType(c.r.data[off]) {
	case BlockFlat, BlockComplex:
		h, _ := c.soleLayer(off)
		if h <= z {
			return h
		}
		return z
	default:
		return c.scanZ(z, false)
	}
}

// HigherZ возвращает высоту слоя не ниже z; точное совпадение — сразу; слоёв
// выше нет — сам z. Порт getNextHigherZ канона.
func (c Cell) HigherZ(z int) int {
	off := int(c.r.blocks[c.b].off)
	switch BlockType(c.r.data[off]) {
	case BlockFlat, BlockComplex:
		h, _ := c.soleLayer(off)
		if h >= z {
			return h
		}
		return z
	default:
		return c.scanZ(z, true)
	}
}

// soleLayer — единственный слой flat/complex-блока.
func (c Cell) soleLayer(off int) (int, NSWE) {
	d := c.r.data
	if BlockType(d[off]) == BlockFlat {
		return int(int16(leUint16(d[off+1:]))), NSWEAll
	}
	v := leUint16(d[off+1+2*int(c.c):])
	return cellHeight(v), cellNSWE(v)
}

// nearestLayer — порт MultilayerBlock.getNearestLayer.
func (c Cell) nearestLayer(off, z int) (int, NSWE) {
	d := c.r.data
	start, end := c.mlSpan(off)
	bestZ, bestNSWE, bestDz := 0, NSWE(0), 0
	for o := start + 1; o < end; o += 2 {
		v := leUint16(d[o:])
		lz := cellHeight(v)
		if lz == z {
			return lz, cellNSWE(v)
		}
		if dz := abs(lz - z); o == start+1 || dz < bestDz {
			bestDz, bestZ, bestNSWE = dz, lz, cellNSWE(v)
		}
	}
	return bestZ, bestNSWE
}

// scanZ — getNextLower/HigherZ по слоям multilayer-ячейки: точное совпадение
// — z; иначе слой ближайший к z своей стороны; higher=false — снизу.
func (c Cell) scanZ(z int, higher bool) int {
	d := c.r.data
	start, end := c.mlSpan(int(c.r.blocks[c.b].off))
	best, ok := 0, false
	for o := start + 1; o < end; o += 2 {
		lz := cellHeight(leUint16(d[o:]))
		switch {
		case lz == z:
			return z
		case lz < z && !higher && (!ok || lz > best):
			best, ok = lz, true
		case lz > z && higher && (!ok || lz < best):
			best, ok = lz, true
		}
	}
	if ok {
		return best
	}
	return z
}

// mlSpan — границы данных ячейки multilayer-блока: [start, end).
func (c Cell) mlSpan(off int) (start, end int) {
	d := c.r.data
	start = off + int(c.r.cells[int(c.r.blocks[c.b].ml)+int(c.c)])
	end = start + 1 + 2*int(d[start])
	return start, end
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
