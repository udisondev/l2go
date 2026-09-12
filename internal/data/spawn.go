package data

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
)

// BannedTerritory — территория-исключение спавна: узлы и диапазон высот.
type BannedTerritory struct {
	MinZ  int32
	MaxZ  int32
	Nodes [][2]int32
}

// Territory — территория спавна с инлайн-геометрией. Геометрия хранится как
// данные; принадлежность точки и вырожденность — P2.5. Shape/Rad — raw-bag
// до появления потребителя форм.
type Territory struct {
	Name   string
	MinZ   int32
	MaxZ   int32
	Nodes  [][2]int32
	Banned []BannedTerritory

	set map[string]string
}

// Set возвращает исходное значение параметра территории по ключу
// (terr.shape, terr.rad, banned.shape, banned.rad).
func (t Territory) Set(key string) (string, bool) {
	v, ok := t.set[key]
	return v, ok
}

// Point — точка спавна; Heading −1 — «нет» (семантика канона).
type Point struct {
	X       int32
	Y       int32
	Z       int32
	Heading int32
}

// NpcSpawn — запись спавна: точка и/или территория (канон при обеих спавнит
// в случайную точку территории, а точку и heading игнорирует — L2J_Mobius
// Spawn.initializeNpc, порт, GPLv3; выбор остаётся за потребителем).
// RespawnDelay — секунды; Count — число экземпляров. Прочие атрибуты
// (respawnRandom, chaseRange, periodOfDay, листья AIData) — raw-bag.
type NpcSpawn struct {
	NpcID        NpcID
	HasPoint     bool
	Point        Point
	Territory    string // "" — без территории
	Count        int32
	RespawnDelay int32 // секунды

	set map[string]string
}

// Set возвращает исходное значение параметра спавна по ключу.
func (s NpcSpawn) Set(key string) (string, bool) {
	v, ok := s.set[key]
	return v, ok
}

// spawnsDir — корень категории спавнов (обход рекурсивный).
const spawnsDir = "spawns"

// fakePlayerRange — диапазон fake players канона: спавн NPC из диапазона
// без определения — счётчик, не ошибка (порт SpawnData.checkTemplate).
func fakePlayerRange(id int64) bool {
	return id >= 80000 && id <= 89999
}

// loadSpawns читает категорию спавнов: рекурсивный обход XML-файлов
// spawnsDir (лексикографический порядок WalkDir — детерминирован); файлы с
// list enabled="false" считаются и пропускаются. Семантика разбора и
// дефолты: L2J_Mobius SpawnData.parseDocument (порт, GPLv3).
func loadSpawns(fsys fs.FS, ctx *loadCtx) {
	if _, err := fs.ReadDir(fsys, spawnsDir); err != nil {
		ctx.fatal(fmt.Errorf("data: чтение каталога %s: %w", spawnsDir, err))
		return
	}
	err := fs.WalkDir(fsys, spawnsDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("data: обход %s: %w", p, err)
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".xml") {
			return nil
		}
		data, ok := ctx.readFileCapped(fsys, p, "spawns")
		if !ok {
			return nil
		}
		parseSpawnFile(p, data, ctx)
		return nil
	})
	if err != nil {
		ctx.fatal(err)
	}
}

func parseSpawnFile(path string, data []byte, ctx *loadCtx) {
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
			ctx.entry(Entry{Category: "spawns", File: path, Line: lineOf(dec),
				Code: CodeXML, Message: fmt.Sprintf("разбор XML: %v", err)})
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if !sawRoot {
				if t.Name.Local != "list" {
					ctx.entry(Entry{Category: "spawns", File: path, Line: lineOf(dec),
						Code: CodeRoot, Message: "корневой элемент " + t.Name.Local + ", нужен list"})
					return
				}
				for _, a := range t.Attr {
					if a.Name.Local == "enabled" {
						enabled = strings.ToLower(strings.TrimSpace(a.Value))
					}
				}
				if enabled == "" {
					// Семантика канона: NPE на отсутствующем enabled — файл не
					// читается; у нас — запись-ошибка и пропуск файла.
					ctx.entry(Entry{Category: "spawns", File: path, Line: lineOf(dec),
						Code: CodeAttr, Message: "list без обязательного атрибута enabled"})
					return
				}
				if enabled != "true" && enabled != "false" {
					ctx.entry(Entry{Category: "spawns", File: path, Line: lineOf(dec),
						Code: CodeAttr, Message: "enabled " + enabled + " не разбирается как bool"})
					return
				}
				if enabled == "false" {
					ctx.rep.DisabledFiles++
					return
				}
				sawRoot = true
				ctx.rep.Files++
				depth++
				continue
			}
			if t.Name.Local == "spawn" && depth == 1 {
				parseSpawnBlock(dec, t, path, ctx)
				continue
			}
			ctx.rep.SkippedElements[t.Name.Local]++
			skipElement(dec, t)
		case xml.EndElement:
			depth--
		}
	}
	if !sawRoot {
		ctx.entry(Entry{Category: "spawns", File: path, Line: 1,
			Code: CodeXML, Message: "пустой файл"})
	}
}

// parseSpawnBlock разбирает блок спавна: точечные и территориальные записи.
func parseSpawnBlock(dec *xml.Decoder, start xml.StartElement, path string, ctx *loadCtx) {
	terrName := ""
	for _, a := range start.Attr {
		switch a.Name.Local {
		case "name":
			ctx.rep.NamedBlocks++
		case "zone":
			terrName = strings.TrimSpace(a.Value)
		case "zones":
			ctx.rep.SkippedElements["zones"]++ // ноль вхождений в каноне Interlude
		}
	}
	var blockBag map[string]string // листья AIData, применяются к записям блока по порядку документа
	for {
		tok, err := dec.Token()
		if err != nil {
			ctx.entry(Entry{Category: "spawns", File: path, Line: lineOf(dec),
				Code: CodeXML, Message: fmt.Sprintf("разбор XML: %v", err)})
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "territory":
				parseTerritory(dec, t, terrName, path, ctx)
			case "banned_territory":
				parseBanned(dec, t, terrName, path, ctx)
			case "npc":
				parseSpawnNpc(dec, t, terrName, blockBag, path, ctx)
			case "AIData":
				if blockBag == nil {
					blockBag = map[string]string{}
				}
				readSpawnAIData(dec, t, blockBag, ctx)
			default:
				ctx.rep.SkippedElements[t.Name.Local]++
				skipElement(dec, t)
			}
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				return
			}
		}
	}
}

// parseTerritory разбирает инлайн-геометрию и регистрирует её под именем
// блока zone (собственный name-атрибут не читается — счётчик).
func parseTerritory(dec *xml.Decoder, start xml.StartElement, terrName, path string, ctx *loadCtx) {
	line := lineOf(dec)
	if terrName == "" {
		ctx.entry(Entry{Category: "spawns", File: path, Line: line,
			Code: CodeAttr, Message: "территория вне блока с zone"})
		skipElement(dec, start)
		return
	}
	t := Territory{Name: terrName, set: map[string]string{}}
	var minRaw, maxRaw string
	for _, a := range start.Attr {
		switch a.Name.Local {
		case "minZ":
			minRaw = strings.TrimSpace(a.Value)
		case "maxZ":
			maxRaw = strings.TrimSpace(a.Value)
		case "name":
			ctx.rep.TerrOwnName++
		case "shape":
			t.set["terr.shape"] = strings.TrimSpace(a.Value)
		case "rad":
			t.set["terr.rad"] = strings.TrimSpace(a.Value)
		}
	}
	if minRaw == "" || maxRaw == "" {
		ctx.entry(Entry{Category: "spawns", File: path, Line: line,
			Code: CodeAttr, Message: "территория " + terrName + " без minZ/maxZ"})
		skipElement(dec, start)
		return
	}
	minZ, err1 := strconv.ParseInt(minRaw, 10, 32)
	maxZ, err2 := strconv.ParseInt(maxRaw, 10, 32)
	if err1 != nil || err2 != nil {
		ctx.entry(Entry{Category: "spawns", File: path, Line: line,
			Code: CodeNumber, Message: "территория " + terrName + ": minZ/maxZ вне домена"})
		skipElement(dec, start)
		return
	}
	t.MinZ, t.MaxZ = int32(minZ), int32(maxZ)
	nodesOK := true
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch tt := tok.(type) {
		case xml.StartElement:
			if tt.Name.Local != "node" {
				ctx.rep.SkippedElements[tt.Name.Local]++
				skipElement(dec, tt)
				continue
			}
			var xRaw, yRaw string
			for _, a := range tt.Attr {
				switch a.Name.Local {
				case "x":
					xRaw = strings.TrimSpace(a.Value)
				case "y":
					yRaw = strings.TrimSpace(a.Value)
				}
			}
			x, err1 := strconv.ParseInt(xRaw, 10, 32)
			y, err2 := strconv.ParseInt(yRaw, 10, 32)
			if xRaw == "" || yRaw == "" || err1 != nil || err2 != nil {
				ctx.entry(Entry{Category: "spawns", File: path, Line: lineOf(dec),
					Code: CodeAttr, Message: "узел территории " + terrName + " без x/y"})
				nodesOK = false
			} else {
				t.Nodes = append(t.Nodes, [2]int32{int32(x), int32(y)})
			}
			skipElement(dec, tt)
		case xml.EndElement:
			if tt.Name.Local == start.Name.Local {
				if !nodesOK || len(t.Nodes) == 0 {
					return // ошибки узлов уже внесены; вырожденность геометрии — P2.5
				}
				if _, dup := ctx.territories[t.Name]; dup {
					ctx.entry(Entry{Category: "spawns", File: path, Line: line,
						Code: CodeDupID, Message: "дубликат имени территории " + t.Name + ", побеждает первая"})
					return
				}
				ctx.territories[t.Name] = t
				ctx.rep.Territories++
				return
			}
		}
	}
}

// parseBanned разбирает территорию-исключение и прикрепляет её к территории
// блока (блок обязан определить территорию раньше — семантика канона).
func parseBanned(dec *xml.Decoder, start xml.StartElement, terrName, path string, ctx *loadCtx) {
	line := lineOf(dec)
	if terrName == "" {
		ctx.entry(Entry{Category: "spawns", File: path, Line: line,
			Code: CodeAttr, Message: "banned_territory вне блока с zone"})
		skipElement(dec, start)
		return
	}
	b := BannedTerritory{}
	var minRaw, maxRaw string
	for _, a := range start.Attr {
		switch a.Name.Local {
		case "minZ":
			minRaw = strings.TrimSpace(a.Value)
		case "maxZ":
			maxRaw = strings.TrimSpace(a.Value)
		}
	}
	minZ, err1 := strconv.ParseInt(minRaw, 10, 32)
	maxZ, err2 := strconv.ParseInt(maxRaw, 10, 32)
	if minRaw == "" || maxRaw == "" || err1 != nil || err2 != nil {
		ctx.entry(Entry{Category: "spawns", File: path, Line: line,
			Code: CodeAttr, Message: "banned_territory без minZ/maxZ"})
		skipElement(dec, start)
		return
	}
	b.MinZ, b.MaxZ = int32(minZ), int32(maxZ)
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch tt := tok.(type) {
		case xml.StartElement:
			if tt.Name.Local != "node" {
				ctx.rep.SkippedElements[tt.Name.Local]++
				skipElement(dec, tt)
				continue
			}
			var xRaw, yRaw string
			for _, a := range tt.Attr {
				switch a.Name.Local {
				case "x":
					xRaw = strings.TrimSpace(a.Value)
				case "y":
					yRaw = strings.TrimSpace(a.Value)
				}
			}
			x, err1 := strconv.ParseInt(xRaw, 10, 32)
			y, err2 := strconv.ParseInt(yRaw, 10, 32)
			if xRaw == "" || yRaw == "" || err1 != nil || err2 != nil {
				ctx.entry(Entry{Category: "spawns", File: path, Line: lineOf(dec),
					Code: CodeAttr, Message: "узел banned_territory без x/y"})
			} else {
				b.Nodes = append(b.Nodes, [2]int32{int32(x), int32(y)})
			}
			skipElement(dec, tt)
		case xml.EndElement:
			if tt.Name.Local == start.Name.Local {
				if ter, ok := ctx.territories[terrName]; ok {
					ter.Banned = append(ter.Banned, b)
					ctx.territories[terrName] = ter
				} else {
					ctx.entry(Entry{Category: "spawns", File: path, Line: line,
						Code: CodeAttr, Message: "banned_territory до определения территории " + terrName})
				}
				return
			}
		}
	}
}

// parseSpawnNpc разбирает запись спавна: точка и/или территория блока.
func parseSpawnNpc(dec *xml.Decoder, start xml.StartElement, terrName string, blockBag map[string]string, path string, ctx *loadCtx) {
	line := lineOf(dec)
	var idRaw string
	var xRaw, yRaw, zRaw, headRaw string
	hasX, hasY, hasZ, hasHead := false, false, false, false
	count := int32(1)
	respawn := int32(0)
	hasRespawn := false
	bag := map[string]string{}
	for k, v := range blockBag {
		bag[k] = v
	}
	for _, a := range start.Attr {
		v := strings.TrimSpace(a.Value)
		switch a.Name.Local {
		case "id":
			idRaw = v
		case "x":
			xRaw, hasX = v, true
		case "y":
			yRaw, hasY = v, true
		case "z":
			zRaw, hasZ = v, true
		case "heading":
			headRaw, hasHead = v, true
		case "count":
			n, err := strconv.ParseInt(v, 10, 32)
			if err != nil || n <= 0 {
				ctx.entry(Entry{Category: "spawns", File: path, Line: line,
					Code: CodeNumber, Message: "count " + v + " вне домена (int32, положительный)"})
				skipElement(dec, start)
				return
			}
			count = int32(n)
		case "respawnDelay":
			n, err := strconv.ParseInt(v, 10, 32)
			if err != nil || n < 0 {
				ctx.entry(Entry{Category: "spawns", File: path, Line: line,
					Code: CodeNumber, Message: "respawnDelay " + v + " вне домена (секунды, int32)"})
				skipElement(dec, start)
				return
			}
			respawn, hasRespawn = int32(n), true
		case "periodOfDay":
			if v != "day" && v != "night" {
				ctx.entry(Entry{Category: "spawns", File: path, Line: line,
					Code: CodeAttr, Message: "periodOfDay " + v + " вне домена (day|night)"})
				skipElement(dec, start)
				return
			}
			spawnBagSet(ctx, bag, "periodOfDay", v)
		default:
			spawnBagSet(ctx, bag, a.Name.Local, v)
		}
	}
	if idRaw == "" {
		ctx.entry(Entry{Category: "spawns", File: path, Line: line,
			Code: CodeAttr, Message: "запись спавна без атрибута id"})
		skipElement(dec, start)
		return
	}
	id, err := strconv.ParseInt(idRaw, 10, 32)
	if err != nil || id <= 0 {
		ctx.entry(Entry{Category: "spawns", File: path, Line: line,
			Code: CodeNumber, Message: "npc id " + idRaw + " вне домена"})
		skipElement(dec, start)
		return
	}
	sp := NpcSpawn{NpcID: NpcID(id), Count: count, RespawnDelay: respawn, Territory: terrName, set: bag}
	if hasX || hasY || hasZ {
		if !hasX || !hasY || !hasZ {
			ctx.entry(Entry{Category: "spawns", File: path, Line: line,
				Code: CodeAttr, Message: "точка спавна задана не полностью (нужны x, y и z)"})
			skipElement(dec, start)
			return
		}
		x, err1 := strconv.ParseInt(xRaw, 10, 32)
		y, err2 := strconv.ParseInt(yRaw, 10, 32)
		z, err3 := strconv.ParseInt(zRaw, 10, 32)
		if err1 != nil || err2 != nil || err3 != nil {
			ctx.entry(Entry{Category: "spawns", File: path, Line: line,
				Code: CodeNumber, Message: "координаты точки вне домена (int32)"})
			skipElement(dec, start)
			return
		}
		sp.HasPoint = true
		sp.Point = Point{X: int32(x), Y: int32(y), Z: int32(z), Heading: -1}
		if hasHead {
			h, err := strconv.ParseInt(headRaw, 10, 32)
			if err != nil {
				ctx.entry(Entry{Category: "spawns", File: path, Line: line,
					Code: CodeNumber, Message: "heading " + headRaw + " не разбирается как число (записан дефолт)"})
			} else {
				sp.Point.Heading = int32(h)
			}
		} else {
			ctx.rep.WithoutHeading++
		}
	} else if terrName == "" {
		ctx.entry(Entry{Category: "spawns", File: path, Line: line,
			Code: CodeAttr, Message: "спавн без точки и территории"})
		skipElement(dec, start)
		return
	}
	if !hasRespawn {
		ctx.rep.WithoutRespawnDelay++
	}
	ctx.spawns = append(ctx.spawns, sp)
	ctx.rep.Spawns++
	ctx.links = append(ctx.links, linkRef{cat: "spawns", file: path, line: line,
		kind: linkNPC, keyID: id, desc: "спавн", fakeOK: true})
	if terrName != "" {
		ctx.links = append(ctx.links, linkRef{cat: "spawns", file: path, line: line,
			kind: linkTerr, keyName: terrName, desc: "спавн-территория"})
	}
}

// readSpawnAIData разбирает листья AIData в raw-bag блока (aidata.*).
func readSpawnAIData(dec *xml.Decoder, start xml.StartElement, bag map[string]string, ctx *loadCtx) {
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			text, err := readText(dec, t)
			if err != nil {
				return
			}
			spawnBagSet(ctx, bag, "aidata."+t.Name.Local, text)
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				return
			}
		}
	}
}

// spawnBagSet кладёт значение в raw-bag спавна: интернирование ключей,
// квалифицированный счётчик неизвестных (spawn.key.*), пустые — счётчик.
func spawnBagSet(ctx *loadCtx, bag map[string]string, key, val string) {
	key = ctx.internKey(key)
	ctx.rep.UnknownKeys[ctx.internKey("spawn.key."+key)]++
	val = strings.TrimSpace(val)
	if val == "" {
		ctx.rep.EmptyValues++
		return
	}
	if _, dup := bag[key]; dup {
		ctx.rep.DupKeys++
	}
	bag[key] = val
}
