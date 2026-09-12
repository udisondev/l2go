package geo

// Константы формата L2J-геодаты. Порт констант GeoEngine канона Mobius
// Interlude (GPL): GeoEngine.java, IBlock.java, IRegion.java.

const (
	// RegionsX и RegionsY — сетка регионов мира (GEO_REGIONS_X/Y канона).
	RegionsX = 32
	RegionsY = 32

	cellSize    = 16 // сторона ячейки в юнитах (COORDINATE_SCALE)
	worldMinX   = -655360
	worldMinY   = -589824
	worldCenter = 8 // сдвиг к центру ячейки (COORDINATE_OFFSET)

	regionBlocks = 256 * 256 // блоков в регионе (REGION_BLOCKS)
	regionCells  = 2048      // ячеек региона по оси (REGION_CELLS_X/Y)
	blockCells   = 64        // ячеек в блоке (BLOCK_CELLS)

	layersMin = 1 // лимит слоёв ячейки multilayer-блока (канон: 1..125)
	layersMax = 125

	// maxRegionBytes — потолок файла региона: 1 GiB покрывает максимум
	// легального файла 65536·(1+64·251) = 1 052 835 840 байт.
	maxRegionBytes = 1 << 30
)

// BlockType — тип блока в файле; значения равны байтам формата
// (IBlock.TYPE_* канона).
type BlockType uint8

const (
	BlockFlat       BlockType = 0 // одна высота на блок
	BlockComplex    BlockType = 1 // 64 ячейки: высота + NSWE
	BlockMultilayer BlockType = 2 // 64 ячейки: слои (высота, NSWE)
)

// NSWE — флаги направлений ячейки (Cell.java канона): младшие 4 бита слова
// ячейки/слоя.
type NSWE byte

const (
	East  NSWE = 1 << 0 // восток
	West  NSWE = 1 << 1 // запад
	South NSWE = 1 << 2 // юг
	North NSWE = 1 << 3 // север

	NSWEAll = East | West | South | North
)

// WorldToGeoX переводит мировую координату X в гео-координату ячейки
// (порт GeoEngine.getGeoX).
func WorldToGeoX(worldX int) int { return (worldX - worldMinX) / cellSize }

// WorldToGeoY — то же для оси Y (порт GeoEngine.getGeoY).
func WorldToGeoY(worldY int) int { return (worldY - worldMinY) / cellSize }

// GeoToWorldX возвращает центр ячейки в мировых координатах
// (порт GeoEngine.getWorldX).
func GeoToWorldX(geoX int) int { return geoX*cellSize + worldMinX + worldCenter }

// GeoToWorldY — то же для оси Y (порт GeoEngine.getWorldY).
func GeoToWorldY(geoY int) int { return geoY*cellSize + worldMinY + worldCenter }

// leUint16 читает uint16 little-endian (порядок байтов формата .l2j).
func leUint16(b []byte) uint16 { return uint16(b[0]) | uint16(b[1])<<8 }

// cellHeight — высота из слова complex-ячейки/слоя: биты 4–15 со знаком,
// шаг 8 юнитов, диапазон −16384..16376 (порт ComplexBlock.getCellHeight).
func cellHeight(v uint16) int { return int(int16(v&0xFFF0)) >> 1 }

// cellNSWE — флаги направлений из слова ячейки/слоя.
func cellNSWE(v uint16) NSWE { return NSWE(v & 0x0F) }

// sizeOK — размер файла региона в пределах потолка (выделен отдельно, чтобы
// потолок проверялся тестом без выделения гигабайта).
func sizeOK(n int) bool { return n >= 0 && n <= maxRegionBytes }
