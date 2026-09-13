package data

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"strconv"
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
// (java.awt): прямоугольники и bbox полуоткрыты [min,max), окружность
// включена (<=), z-диапазон замкнут после нормализации; вся арифметика —
// int64 (координаты мира переполняют int32 в квадратах и произведениях).
// Неизвестная форма — false.
func (zn *Zone) Contains(x, y, z int32) bool {
	switch zn.ShapeKind {
	case ShapeCuboid:
		return bboxHalfOpen(zn.MinX, zn.MaxX, zn.MinY, zn.MaxY, x, y) &&
			z >= zn.ZLo && z <= zn.ZHi
	case ShapeNPoly:
		return z >= zn.ZLo && z <= zn.ZHi &&
			npolyContains(zn.Nodes, zn.MinX, zn.MaxX, zn.MinY, zn.MaxY, x, y)
	case ShapeCylinder:
		dx := int64(x) - int64(zn.Nodes[0][0])
		dy := int64(y) - int64(zn.Nodes[0][1])
		return dx*dx+dy*dy <= int64(zn.Rad)*int64(zn.Rad) &&
			z >= zn.MinZ && z <= zn.MaxZ
	default:
		return false
	}
}

// bboxHalfOpen — предикат bbox с семантикой java.awt Rectangle.contains:
// левая/нижняя границы включены, правая/верхняя полуоткрыты.
func bboxHalfOpen(minX, maxX, minY, maxY, x, y int32) bool {
	return x >= minX && x < maxX && y >= minY && y < maxY
}

// npolyContains — общий even-odd кроссинг для Zone и Territory: порт
// java.awt.Polygon.contains (через L2J_Mobius ZoneNPoly, GPLv3). Ребро
// засчитывается при строгом чередовании (y1>py)!=(y2>py) (горизонтальные
// рёбра пропускаются), хит — точка строго левее пересечения ребра с
// горизонталью точки; сравнение без деления, в int64, знак (y2−y1)
// согласован инвертированием обеих частей. Bbox-предфильтр —
// поведение-сохраняющая оптимизация канона (промах O(1) вместо O(n) рёбер).
func npolyContains(nodes [][2]int32, minX, maxX, minY, maxY, x, y int32) bool {
	if !bboxHalfOpen(minX, maxX, minY, maxY, x, y) {
		return false
	}
	py := int64(y)
	px := int64(x)
	hits := false
	j := len(nodes) - 1
	for i := 0; i < len(nodes); i++ {
		y1 := int64(nodes[j][1])
		y2 := int64(nodes[i][1])
		if (y1 > py) != (y2 > py) {
			x1 := int64(nodes[j][0])
			x2 := int64(nodes[i][0])
			lhs := (px - x1) * (y2 - y1)
			rhs := (py - y1) * (x2 - x1)
			if y2 < y1 {
				lhs, rhs = -lhs, -rhs
			}
			if lhs < rhs {
				hits = !hits
			}
		}
		j = i
	}
	return hits
}

// Contains — принадлежность точки территории спавна: NPoly-семантика канона
// (NpcSpawnTerritory → ZoneNPoly) с нормализацией z. Banned не участвует:
// логика случайной точки спавна — потребитель фазы 3.
func (t *Territory) Contains(x, y, z int32) bool {
	zlo, zhi := t.MinZ, t.MaxZ
	if zlo > zhi {
		zlo, zhi = zhi, zlo
	}
	return z >= zlo && z <= zhi &&
		npolyContains(t.Nodes, t.MinX, t.MaxX, t.MinY, t.MaxY, x, y)
}

// zonesDir — корень категории зон.
const zonesDir = "zones"

// knownZoneTypes — словарь типов канона: 29 регистрируются
// ZoneManager.load() плюс NoPvPZone (грузится Class.forName, в данных
// no_pvp.xml); прочие — широта данных (счётчик zone.type.*).
var knownZoneTypes = map[string]struct{}{
	"ArenaZone": {}, "BossZone": {}, "CastleZone": {}, "ClanHallZone": {},
	"ConditionZone": {}, "DamageZone": {}, "DerbyTrackZone": {}, "EffectZone": {},
	"FishingZone": {}, "FlagPvPZone": {}, "HqZone": {}, "JailZone": {},
	"MotherTreeZone": {}, "NoLandingZone": {}, "NoPvPZone": {}, "NoRestartZone": {},
	"NoStoreZone": {}, "NoSummonFriendZone": {}, "OlympiadStadiumZone": {},
	"PeaceZone": {}, "ResidenceHallTeleportZone": {}, "ResidenceTeleportZone": {},
	"ResidenceZone": {}, "RespawnZone": {}, "ScriptZone": {}, "SiegableHallZone": {},
	"SiegeZone": {}, "SwampZone": {}, "TownZone": {}, "WaterZone": {},
}

// loadZones читает категорию зон: плоский XML-уровень zonesDir; подкаталоги
// считаются в отчёт и не читаются. Семантика разбора: L2J_Mobius
// ZoneManager.parseDocument (порт, GPLv3).
func loadZones(fsys fs.FS, ctx *loadCtx) {
	loadFlatCategory(fsys, ctx, zonesDir, "zones", parseZonesFile)
}

// parseZonesFile — файловый цикл категории. Семантика enabled корня — порт
// канона зон и отличие от спавнов: отсутствие атрибута означает «файл
// грузится» (ZoneManager проверяет только явное false), мусорное значение —
// ошибка (молчаливая потеря невозможна), false — пропуск с подсчётом.
func parseZonesFile(path string, data []byte, ctx *loadCtx) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	sawRoot := false
	enabled := ""
	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			ctx.entry(Entry{Category: "zones", File: path, Line: lineOf(dec),
				Code: CodeXML, Message: fmt.Sprintf("разбор XML: %v", err)})
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if !sawRoot {
				if t.Name.Local != "list" {
					ctx.entry(Entry{Category: "zones", File: path, Line: lineOf(dec),
						Code: CodeRoot, Message: "корневой элемент " + t.Name.Local + ", нужен list"})
					return
				}
				for _, a := range t.Attr {
					if a.Name.Local == "enabled" {
						enabled = strings.ToLower(strings.TrimSpace(a.Value))
					}
				}
				if enabled != "" && enabled != "true" && enabled != "false" {
					ctx.entry(Entry{Category: "zones", File: path, Line: lineOf(dec),
						Code: CodeAttr, Message: "enabled " + enabled + " не разбирается как bool"})
					return
				}
				if enabled == "false" {
					ctx.rep.DisabledFiles++
					return
				}
				sawRoot = true
				depth++
				continue
			}
			if t.Name.Local == "zone" && depth == 1 {
				if parseZone(dec, t, path, ctx) {
					return // ошибка XML: запись уже внесена
				}
				continue
			}
			ctx.rep.SkippedElements[t.Name.Local]++
			skipElement(dec, t)
		case xml.EndElement:
			depth--
		}
	}
	if !sawRoot {
		ctx.entry(Entry{Category: "zones", File: path, Line: 1,
			Code: CodeXML, Message: "пустой файл"})
	}
}

// zoneFields — разобранные атрибуты зоны до чтения детей.
type zoneFields struct {
	typ, shape, name, idRaw, minRaw, maxRaw, radRaw string
}

// parseZone разбирает зону: атрибуты, дети (node/stat/spawn/race), валидация
// геометрии, прекомьют bbox/z; true — декодер в состоянии ошибки XML.
// Ошибка формы или вырожденность — запись-ошибка, зона не входит в статику.
func parseZone(dec *xml.Decoder, start xml.StartElement, path string, ctx *loadCtx) bool {
	line := lineOf(dec)
	var f zoneFields
	seen := map[string]struct{}{}
	for _, a := range start.Attr {
		if _, dup := seen[a.Name.Local]; dup {
			ctx.entry(Entry{Category: "zones", File: path, Line: line,
				Code: CodeAttr, Message: "повтор атрибута " + a.Name.Local})
		}
		seen[a.Name.Local] = struct{}{}
		switch a.Name.Local {
		case "type":
			f.typ = strings.TrimSpace(a.Value)
		case "shape":
			f.shape = strings.TrimSpace(a.Value)
		case "name":
			f.name = strings.TrimSpace(a.Value)
		case "id":
			f.idRaw = strings.TrimSpace(a.Value)
		case "minZ":
			f.minRaw = strings.TrimSpace(a.Value)
		case "maxZ":
			f.maxRaw = strings.TrimSpace(a.Value)
		case "rad":
			f.radRaw = strings.TrimSpace(a.Value)
		default:
			skipAttr(ctx, "zone", a.Name.Local)
		}
	}
	c := readZoneContent(dec, start, path, ctx)
	if c.badXML {
		return true
	}
	zn, ok := buildZone(f, c, path, line, ctx)
	if !ok {
		return false
	}
	if zn.HasID {
		if prev, dup := ctx.zoneIDs[zn.ID]; dup {
			ctx.entry(Entry{Category: "zones", File: path, Line: line, ID: int64(zn.ID),
				Code: CodeDupID, Message: "дубликат id зоны (первая из " + prev + ")"})
			return false
		}
		ctx.zoneIDs[zn.ID] = path
		ctx.rep.ZonesWithID++
	}
	if zn.Name == "" {
		ctx.rep.MissingZoneName++
	} else {
		ctx.zoneNames[zn.Name]++
	}
	if _, known := knownZoneTypes[zn.Type]; !known {
		ctx.rep.UnknownTypes["zone.type."+zn.Type]++
	}
	ctx.zones = append(ctx.zones, zn)
	return false
}

// zoneContent — накопленные дети зоны.
type zoneContent struct {
	nodes  [][2]int32
	spawns []ZoneSpawn
	races  []ZoneRacePoint
	bag    map[string]string
	badXML bool
}

// readZoneContent читает детей зоны до закрывающего тега; ошибки отдельных
// элементов — записи отчёта, разбор продолжается (прецедент readNodes).
func readZoneContent(dec *xml.Decoder, start xml.StartElement, path string, ctx *loadCtx) zoneContent {
	c := zoneContent{bag: map[string]string{}}
	bad := func(err error) {
		ctx.entry(Entry{Category: "zones", File: path, Line: lineOf(dec),
			Code: CodeXML, Message: fmt.Sprintf("разбор XML: %v", err)})
		c.badXML = true
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			bad(err)
			return c
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "node":
				if !readZoneNode(dec, t, path, ctx, &c) {
					return c
				}
			case "stat":
				if !readZoneStat(dec, t, path, ctx, c.bag) {
					return c
				}
			case "spawn":
				if !readZoneSpawn(dec, t, path, ctx, &c) {
					return c
				}
			case "race":
				if !readZoneRace(dec, t, path, ctx, &c) {
					return c
				}
			default:
				ctx.rep.SkippedElements[t.Name.Local]++
				skipElement(dec, t)
			}
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				return c
			}
		}
	}
}

// readZoneNode разбирает узел зоны (атрибуты X/Y); отсутствие или неразбор —
// запись-ошибка, узел пропускается.
func readZoneNode(dec *xml.Decoder, start xml.StartElement, path string, ctx *loadCtx, c *zoneContent) bool {
	var xRaw, yRaw string
	for _, a := range start.Attr {
		switch a.Name.Local {
		case "X":
			xRaw = strings.TrimSpace(a.Value)
		case "Y":
			yRaw = strings.TrimSpace(a.Value)
		default:
			skipAttr(ctx, "node", a.Name.Local)
		}
	}
	x, err1 := strconv.ParseInt(xRaw, 10, 32)
	y, err2 := strconv.ParseInt(yRaw, 10, 32)
	if xRaw == "" || yRaw == "" || err1 != nil || err2 != nil {
		ctx.entry(Entry{Category: "zones", File: path, Line: lineOf(dec),
			Code: CodeAttr, Message: "узел зоны без X/Y или вне домена"})
	} else {
		c.nodes = append(c.nodes, [2]int32{int32(x), int32(y)})
	}
	skipElement(dec, start)
	return true
}

// readZoneStat разбирает параметр зоны в raw-bag (zone.stat.<name>); дубликат
// ключа — последний + счётчик (списочная семантика affectedRace/affectedClassId
// канона не переносится: потребитель фаз 4+, потеря фиксирована реестром F19).
func readZoneStat(dec *xml.Decoder, start xml.StartElement, path string, ctx *loadCtx, bag map[string]string) bool {
	name, val := "", ""
	for _, a := range start.Attr {
		switch a.Name.Local {
		case "name":
			name = strings.TrimSpace(a.Value)
		case "val":
			val = strings.TrimSpace(a.Value)
		}
	}
	if name == "" || val == "" {
		ctx.entry(Entry{Category: "zones", File: path, Line: lineOf(dec),
			Code: CodeAttr, Message: "stat зоны без name/val"})
		skipElement(dec, start)
		return true
	}
	skipElement(dec, start)
	key := ctx.internKey("zone.stat." + name)
	if _, dup := bag[key]; dup {
		ctx.rep.DupKeys++
	}
	bag[key] = val
	return true
}

// readZoneSpawn разбирает точку возрождения зоны; отсутствие координат или
// неразбор — запись-ошибка, точка пропускается.
func readZoneSpawn(dec *xml.Decoder, start xml.StartElement, path string, ctx *loadCtx, c *zoneContent) bool {
	var xRaw, yRaw, zRaw, typ string
	for _, a := range start.Attr {
		switch a.Name.Local {
		case "X":
			xRaw = strings.TrimSpace(a.Value)
		case "Y":
			yRaw = strings.TrimSpace(a.Value)
		case "Z":
			zRaw = strings.TrimSpace(a.Value)
		case "type":
			typ = strings.TrimSpace(a.Value)
		default:
			skipAttr(ctx, "spawn", a.Name.Local)
		}
	}
	x, err1 := strconv.ParseInt(xRaw, 10, 32)
	y, err2 := strconv.ParseInt(yRaw, 10, 32)
	z, err3 := strconv.ParseInt(zRaw, 10, 32)
	switch {
	case xRaw == "" || yRaw == "" || zRaw == "":
		ctx.entry(Entry{Category: "zones", File: path, Line: lineOf(dec),
			Code: CodeAttr, Message: "spawn зоны без X/Y/Z"})
	case err1 != nil || err2 != nil || err3 != nil:
		ctx.entry(Entry{Category: "zones", File: path, Line: lineOf(dec),
			Code: CodeNumber, Message: "координаты spawn зоны вне домена (int32)"})
	default:
		c.spawns = append(c.spawns, ZoneSpawn{X: int32(x), Y: int32(y), Z: int32(z), Type: typ})
		ctx.rep.ZoneSpawns++
	}
	skipElement(dec, start)
	return true
}

// readZoneRace разбирает точку возрождения расы; отсутствие name/point —
// запись-ошибка, элемент пропускается.
func readZoneRace(dec *xml.Decoder, start xml.StartElement, path string, ctx *loadCtx, c *zoneContent) bool {
	race, point := "", ""
	for _, a := range start.Attr {
		switch a.Name.Local {
		case "name":
			race = strings.TrimSpace(a.Value)
		case "point":
			point = strings.TrimSpace(a.Value)
		}
	}
	skipElement(dec, start)
	if race == "" || point == "" {
		ctx.entry(Entry{Category: "zones", File: path, Line: lineOf(dec),
			Code: CodeAttr, Message: "race зоны без name/point"})
		return true
	}
	c.races = append(c.races, ZoneRacePoint{Race: race, Point: point})
	ctx.rep.ZoneRacePoints++
	return true
}

// buildZone валидирует форму и вырожденность, прекомпьютит bbox/z.
func buildZone(f zoneFields, c zoneContent, path string, line int, ctx *loadCtx) (Zone, bool) {
	bad := func(code, msg string) {
		ctx.entry(Entry{Category: "zones", File: path, Line: line, Code: code, Message: msg})
	}
	if f.typ == "" {
		bad(CodeAttr, "зона без обязательного атрибута type")
		return Zone{}, false
	}
	if f.shape == "" {
		bad(CodeAttr, "зона без обязательного атрибута shape")
		return Zone{}, false
	}
	if f.minRaw == "" || f.maxRaw == "" {
		bad(CodeAttr, "зона без minZ/maxZ")
		return Zone{}, false
	}
	minZ, err1 := strconv.ParseInt(f.minRaw, 10, 32)
	maxZ, err2 := strconv.ParseInt(f.maxRaw, 10, 32)
	if err1 != nil || err2 != nil {
		bad(CodeNumber, "minZ/maxZ вне домена (int32)")
		return Zone{}, false
	}
	zn := Zone{
		MinZ: int32(minZ), MaxZ: int32(maxZ),
		Name: f.name, Type: f.typ,
		Nodes: c.nodes, SpawnPts: c.spawns, RacePts: c.races,
		set: c.bag,
	}
	if f.idRaw != "" {
		id, err := strconv.ParseInt(f.idRaw, 10, 32)
		if err != nil || id <= 0 {
			bad(CodeNumber, "id "+f.idRaw+" вне домена (int32, положительный)")
			return Zone{}, false
		}
		zn.ID, zn.HasID = ZoneID(id), true
	}
	if minZ > maxZ {
		ctx.rep.MinZOverMaxZ++
	} else if minZ == maxZ {
		ctx.rep.MinEqMaxZ++
	}
	zn.ZLo, zn.ZHi = int32(minZ), int32(maxZ)
	if zn.ZLo > zn.ZHi {
		zn.ZLo, zn.ZHi = zn.ZHi, zn.ZLo
	}
	who := "зона " + quoteName(zn.Name)
	switch f.shape {
	case "Cuboid":
		zn.ShapeKind = ShapeCuboid
		if len(c.nodes) != 2 {
			bad(CodeNumber, who+": Cuboid требует ровно 2 узла, узлов "+strconv.Itoa(len(c.nodes)))
			return Zone{}, false
		}
		zn.MinX, zn.MaxX = i32min(c.nodes[0][0], c.nodes[1][0]), i32max(c.nodes[0][0], c.nodes[1][0])
		zn.MinY, zn.MaxY = i32min(c.nodes[0][1], c.nodes[1][1]), i32max(c.nodes[0][1], c.nodes[1][1])
		if zn.MinX == zn.MaxX || zn.MinY == zn.MaxY {
			bad(CodeNumber, who+": нулевая площадь (вырожденный кубоид)")
			return Zone{}, false
		}
	case "NPoly":
		zn.ShapeKind = ShapeNPoly
		if len(c.nodes) < 3 {
			bad(CodeNumber, who+": NPoly требует не менее 3 узлов, узлов "+strconv.Itoa(len(c.nodes)))
			return Zone{}, false
		}
		if polyAreaZero(c.nodes) {
			bad(CodeNumber, who+": нулевая площадь (коллинеарные или совпадающие узлы)")
			return Zone{}, false
		}
		zn.MinX, zn.MaxX, zn.MinY, zn.MaxY = polyBounds(c.nodes)
	case "Cylinder":
		zn.ShapeKind = ShapeCylinder
		if len(c.nodes) != 1 {
			bad(CodeNumber, who+": Cylinder требует ровно 1 узел, узлов "+strconv.Itoa(len(c.nodes)))
			return Zone{}, false
		}
		if f.radRaw == "" {
			bad(CodeAttr, who+": Cylinder без rad")
			return Zone{}, false
		}
		rad, err := strconv.ParseInt(f.radRaw, 10, 32)
		if err != nil || rad <= 0 {
			bad(CodeNumber, who+": rad "+f.radRaw+" вне домена (int32, положительный)")
			return Zone{}, false
		}
		zn.Rad = int32(rad)
	default:
		// Неизвестная форма — широта данных (решение 7 фазы): зона входит в
		// статику, Contains возвращает false, исходная строка — в raw-bag.
		zn.ShapeKind = ShapeUnknown
		c.bag[ctx.internKey("zone.shape")] = f.shape
		ctx.rep.UnknownTypes["zone.shape."+f.shape]++
	}
	return zn, true
}

// quoteName — имя записи для сообщений.
func quoteName(name string) string {
	if name == "" {
		return "(без имени)"
	}
	return "\"" + name + "\""
}

// polyBounds — bbox полигона.
func polyBounds(nodes [][2]int32) (minX, maxX, minY, maxY int32) {
	minX, maxX, minY, maxY = nodes[0][0], nodes[0][0], nodes[0][1], nodes[0][1]
	for _, n := range nodes[1:] {
		minX, maxX = i32min(minX, n[0]), i32max(maxX, n[0])
		minY, maxY = i32min(minY, n[1]), i32max(maxY, n[1])
	}
	return
}

// polyAreaZero — нулевая площадь шнурком в int64: покрывает совпадающие и
// коллинеарные узлы одной проверкой O(n).
func polyAreaZero(nodes [][2]int32) bool {
	var area2 int64
	j := len(nodes) - 1
	for i := 0; i < len(nodes); i++ {
		area2 += int64(nodes[j][0])*int64(nodes[i][1]) - int64(nodes[i][0])*int64(nodes[j][1])
		j = i
	}
	return area2 == 0
}

func i32min(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}

func i32max(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}

// dumpZones пишет канонический текст зон в порядке загрузки: исходные
// minZ/maxZ, каноническое имя формы (unknown — исходная строка), сортированный
// стат-набор.
func dumpZones(sb *strings.Builder, s *Static) {
	for _, zn := range s.zones {
		shape := shapeNames[zn.ShapeKind]
		if zn.ShapeKind == ShapeUnknown {
			if v, ok := zn.set["zone.shape"]; ok {
				shape = v
			}
		}
		fmt.Fprintf(sb, "zone name=%q type=%q shape=%q", zn.Name, zn.Type, shape)
		if zn.HasID {
			fmt.Fprintf(sb, " id=%d", zn.ID)
		}
		fmt.Fprintf(sb, " minZ=%d maxZ=%d", zn.MinZ, zn.MaxZ)
		if zn.ShapeKind == ShapeCylinder {
			fmt.Fprintf(sb, " rad=%d", zn.Rad)
		}
		sb.WriteString(" nodes=[")
		for i, n := range zn.Nodes {
			if i > 0 {
				sb.WriteByte(' ')
			}
			fmt.Fprintf(sb, "%d,%d", n[0], n[1])
		}
		sb.WriteString("] spawns=[")
		for i, sp := range zn.SpawnPts {
			if i > 0 {
				sb.WriteByte(' ')
			}
			fmt.Fprintf(sb, "%d,%d,%d", sp.X, sp.Y, sp.Z)
			if sp.Type != "" {
				sb.WriteByte(':')
				sb.WriteString(sp.Type)
			}
		}
		sb.WriteString("] races=[")
		for i, rp := range zn.RacePts {
			if i > 0 {
				sb.WriteByte(' ')
			}
			fmt.Fprintf(sb, "%s>%s", rp.Race, rp.Point)
		}
		sb.WriteString("]")
		writeSets(sb, zn.set)
		sb.WriteByte('\n')
	}
}
