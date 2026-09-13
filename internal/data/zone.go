package data

import (
	"encoding/xml"
	"io/fs"
	"strings"
)

// ZoneID — явный идентификатор зоны датапака (отсутствует у большинства
// зон: канон генерирует динамические, статика их не порождает).
type ZoneID int32

// Виды геометрии зоны. Канонические имена форм — Cuboid/NPoly/Cylinder;
// неизвестная форма сохраняется строкой в raw-bag, Contains возвращает false.
type ShapeKind byte

const (
	ShapeUnknown ShapeKind = iota
	ShapeCuboid
	ShapeNPoly
	ShapeCylinder
)

// shapeNames — канонические имена форм для дампа.
var shapeNames = map[ShapeKind]string{
	ShapeCuboid:   "Cuboid",
	ShapeNPoly:    "NPoly",
	ShapeCylinder: "Cylinder",
}

// ZoneSpawn — точка возрождения зоны (элемент spawn; Type — атрибут type
// как в файле, "" — без).
type ZoneSpawn struct {
	X    int32
	Y    int32
	Z    int32
	Type string
}

// ZoneRacePoint — точка возрождения расы (элемент race): имя целевого
// restart-региона; категория mapregion в фазе 2 не загружается, ссылка не
// проверяется (канон fallback'ит на дефолтный регион).
type ZoneRacePoint struct {
	Race  string
	Point string
}

// Zone — зона датапака. Горячие поля геометрии впереди: Contains читает
// только их. BBox/ZLo/ZHi прекомпутированы при загрузке (Cuboid/NPoly
// нормализуют z; Cylinder использует сырые MinZ/MaxZ — семантика канона).
// Семантика форм: L2J_Mobius ZoneCuboid/ZoneNPoly/ZoneCylinder и java.awt
// Rectangle.contains/Polygon.contains (порт, GPLv3). Записи не меняются
// после загрузки; вложенные слайсы — только чтение.
type Zone struct {
	ShapeKind ShapeKind
	MinZ      int32 // как в файле (Dump)
	MaxZ      int32 // как в файле (Dump)
	ZLo       int32 // нормализованный ZLo (Cuboid/NPoly)
	ZHi       int32 // нормализованный ZHi (Cuboid/NPoly)
	Rad       int32 // Cylinder; 0 — нет
	MinX      int32 // bbox: прекомпьют
	MaxX      int32
	MinY      int32
	MaxY      int32
	Nodes     [][2]int32
	ID        ZoneID
	HasID     bool
	Name      string
	Type      string
	SpawnPts  []ZoneSpawn
	RacePts   []ZoneRacePoint

	set map[string]string
}

// Set возвращает исходное значение параметра зоны по ключу (zone.stat.<name>,
// zone.shape — исходная строка неизвестной формы).
func (zn Zone) Set(key string) (string, bool) {
	v, ok := zn.set[key]
	return v, ok
}

// Contains сообщает, принадлежит ли точка зоне. Порт семантики форм канона
// (java.awt): полуоткрытые границы прямоугольников и bbox, включённая
// граница окружности, закрытый диапазон z; вся арифметика — int64.
// Неизвестная форма — false.
func (zn *Zone) Contains(x, y, z int32) bool {
	return false // стаб красной фазы
}

// Contains — принадлежность точки территории спавна: NPoly-семантика канона
// (NpcSpawnTerritory → ZoneNPoly) с нормализацией z. Banned не участвует:
// логика случайной точки спавна — потребитель фазы 3.
func (t *Territory) Contains(x, y, z int32) bool {
	return false // стаб красной фазы
}

// zonesDir — корень категории зон.
const zonesDir = "zones"

// knownZoneTypes — словарь типов канона: 29 регистрируются
// ZoneManager.load() плюс NoPvPZone (грузится Class.forName, в данных
// no_pvp.xml); прочие — широта данных (счётчик zone.type.*).
var knownZoneTypes = map[string]struct{}{}

// loadZones читает категорию зон: плоский XML-уровень zonesDir; подкаталоги
// считаются в отчёт и не читаются. Семантика разбора: L2J_Mobius
// ZoneManager.parseDocument (порт, GPLv3).
func loadZones(fsys fs.FS, ctx *loadCtx) {
	loadFlatCategory(fsys, ctx, zonesDir, "zones", parseZonesFile)
}

// parseZonesFile — стаб красной фазы: корень и enabled читаются, зоны не
// разбираются.
func parseZonesFile(path string, data []byte, ctx *loadCtx) {
	_ = parseZoneFile(path, data, ctx)
}

// parseZoneFile — стаб красной фазы.
func parseZoneFile(path string, data []byte, ctx *loadCtx) bool {
	var dec *xml.Decoder
	_ = dec
	return false
}

// npolyContains — стаб красной фазы: общий even-odd кроссинг для Zone и
// Territory (java.awt.Polygon.contains, порт).
func npolyContains(nodes [][2]int32, minX, maxX, minY, maxY, x, y int32) bool {
	return false
}

// dumpZones — стаб красной фазы: канонический текст зон в порядке загрузки.
func dumpZones(sb *strings.Builder, s *Static) {
	_ = sb
	_ = s
}
