package data

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"math"
	"path/filepath"
	"strconv"
	"strings"
)

// NpcID — идентификатор NPC датапака.
type NpcID int32

// MinionRef — ссылка NPC на миньона из блока parameters/minions. Семантика
// Max портирована с L2J_Mobius MinionHolder.getCount (порт, GPLv3): при
// Max > Count число заспавненных миньонов случайно в [Count, Max].
type MinionRef struct {
	NpcID       NpcID
	Count       int32
	Max         int32
	RespawnTime int32 // секунды
	WeightPoint int32
}

// Drop — предмет дроплиста: шанс в процентах, min/max — штуки.
type Drop struct {
	ItemID ItemID
	Min    int32
	Max    int32
	Chance float64
}

// DropGroup — группа дропа: шанс группы и предметы внутри.
type DropGroup struct {
	Chance float64
	Items  []Drop
}

// DropList — дроплист одного типа (drop|spoil); порядок файла сохраняется.
type DropList struct {
	Type   string
	Groups []DropGroup
	Items  []Drop
}

// Npc — NPC датапака. Типизированные поля покрывают потребителя фаз 3–4
// (спавн, таргетинг, аггро, коллизия, репликация NpcInfo) и ссылки каркаса
// целостности; прочее хранится в raw-bag с путевыми ключами (stats.vitals.hp,
// equipment.rhand, …). Дефолты отсутствующих полей — семантика L2J_Mobius
// NpcData (порт, GPLv3): level 85, type «Folk». Записи не меняются после
// загрузки; вложенные слайсы — только чтение.
type Npc struct {
	ID              NpcID
	Name            string
	Title           string
	Level           int32
	Type            string
	Race            string
	AggroRange      int32
	ClanHelpRange   int32
	IsAggressive    bool
	Clans           []string
	IgnoreNpcIDs    []NpcID
	CollisionRadius float64
	CollisionHeight float64
	Minions         []MinionRef
	DropLists       []DropList

	set map[string]string
}

// Set возвращает исходное значение параметра NPC по путевому ключу.
func (n Npc) Set(key string) (string, bool) {
	v, ok := n.set[key]
	return v, ok
}

// npcsDir — каталог категории NPC в корне данных.
const npcsDir = "stats/npcs"

// maxBagDepth — потолок глубины пути raw-bag: предел против глубоко
// вложенных зло-входов; глубже — пропуск со счётчиком DeepSkips.
const maxBagDepth = 32

// npcTypedBagKeys — bag-ключи, отражаемые в типизированные поля Npc (или
// поглощаемые ими); прочие ключи считаются неизвестными (широта данных).
var npcTypedBagKeys = map[string]bool{
	"npc.id": true, "npc.level": true, "npc.type": true, "npc.name": true,
	"npc.title": true, "race": true,
	"ai.aggroRange": true, "ai.clanHelpRange": true, "ai.isAggressive": true,
	"collision.radius.normal": true, "collision.height.normal": true,
	"minions": true,
}

// bagFrame — элемент стека путей generic-обхода поддерева NPC.
type bagFrame struct {
	name      string
	attrs     int
	childSeen bool
}

// loadNpcs читает категорию NPC: плоский XML-уровень npcsDir, подкаталоги
// (custom и прочие) считаются в отчёт и не читаются. Семантика разбора и
// дефолты: L2J_Mobius NpcData.parseDocument (порт, GPLv3).
func loadNpcs(fsys fs.FS, ctx *loadCtx) {
	entries, err := fs.ReadDir(fsys, npcsDir)
	if err != nil {
		ctx.fatal(fmt.Errorf("data: чтение каталога %s: %w", npcsDir, err))
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			ctx.rep.SkippedDirs[e.Name()] += countXML(fsys, npcsDir+"/"+e.Name())
			continue
		}
		if !strings.EqualFold(filepath.Ext(e.Name()), ".xml") {
			continue
		}
		path := npcsDir + "/" + e.Name()
		data, ok := ctx.readFileCapped(fsys, path, "npcs")
		if !ok {
			continue
		}
		ctx.rep.Files++
		parseNpcsFile(path, data, ctx)
	}
}

func parseNpcsFile(path string, data []byte, ctx *loadCtx) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	sawRoot := false
	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			ctx.entry(Entry{Category: "npcs", File: path, Line: lineOf(dec),
				Code: CodeXML, Message: fmt.Sprintf("разбор XML: %v", err)})
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if !sawRoot {
				if t.Name.Local != "list" {
					ctx.entry(Entry{Category: "npcs", File: path, Line: lineOf(dec),
						Code: CodeRoot, Message: "корневой элемент " + t.Name.Local + ", нужен list"})
					return
				}
				sawRoot = true
				depth++
				continue
			}
			if t.Name.Local == "npc" && depth == 1 {
				if parseNpc(dec, t, path, ctx) {
					return // ошибка XML: декодер повторит её, запись уже внесена
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
		ctx.entry(Entry{Category: "npcs", File: path, Line: 1,
			Code: CodeXML, Message: "пустой файл"})
	}
}

// parseNpc разбирает NPC верхнего уровня; true — декодер в состоянии ошибки
// XML, разбор файла пора прекратить (запись отчёта уже внесена).
func parseNpc(dec *xml.Decoder, start xml.StartElement, path string, ctx *loadCtx) bool {
	line := lineOf(dec)
	n := Npc{Level: 85, Type: "Folk"} // дефолты канона (NpcData, порт)
	bag := map[string]string{}
	var idRaw string
	haveLevel, haveType, haveName, haveTitle := false, false, false, false
	seen := map[string]bool{}
	for _, a := range start.Attr {
		if seen[a.Name.Local] {
			ctx.entry(Entry{Category: "npcs", File: path, Line: line,
				Code: CodeAttr, Message: "повтор атрибута " + a.Name.Local})
		}
		seen[a.Name.Local] = true
		switch a.Name.Local {
		case "id":
			idRaw = strings.TrimSpace(a.Value)
		case "level":
			haveLevel = true
			npcBagSet(ctx, bag, "npc.level", a.Value)
		case "type":
			haveType = true
			npcBagSet(ctx, bag, "npc.type", a.Value)
		case "name":
			haveName = true
			npcBagSet(ctx, bag, "npc.name", a.Value)
		case "title":
			haveTitle = true
			npcBagSet(ctx, bag, "npc.title", a.Value)
		default:
			npcBagSet(ctx, bag, "npc."+a.Name.Local, a.Value)
		}
	}
	if idRaw == "" {
		ctx.entry(Entry{Category: "npcs", File: path, Line: line,
			Code: CodeAttr, Message: "нет обязательного атрибута id"})
		skipElement(dec, start)
		return false
	}
	id64, err := strconv.ParseInt(idRaw, 10, 32)
	if err != nil || id64 <= 0 {
		ctx.entry(Entry{Category: "npcs", File: path, Line: line,
			Code: CodeNumber, Message: "id " + idRaw + " вне домена (int32, положительный)"})
		skipElement(dec, start)
		return false
	}
	n.ID = NpcID(id64)
	if haveLevel {
		v, err := strconv.ParseInt(strings.TrimSpace(bag["npc.level"]), 10, 32)
		if err != nil {
			ctx.entry(Entry{Category: "npcs", File: path, Line: line, ID: int64(n.ID),
				Code: CodeNumber, Message: "level " + bag["npc.level"] + " не разбирается как число"})
		} else {
			n.Level = int32(v)
		}
	} else {
		ctx.rep.MissingLevel++
	}
	if haveType {
		n.Type = bag["npc.type"]
		if !knownNpcTypes[n.Type] {
			ctx.rep.UnknownTypes["npc.type."+n.Type]++
		}
	} else {
		ctx.rep.MissingType++
	}
	if haveName {
		n.Name = bag["npc.name"]
	} else {
		ctx.rep.MissingName++
	}
	if haveTitle {
		n.Title = bag["npc.title"]
	}

	// Ссылки записи копятся локально и попадают в реестр только у победившей
	// записи: проигравший дубликат не оставляет фантомных ссылок.
	var links []linkRef
	raceSeen := false
	var frames []bagFrame
	var leaf []byte
	done := false
	for !done {
		tok, err := dec.Token()
		if err != nil {
			ctx.entry(Entry{Category: "npcs", File: path, Line: lineOf(dec), ID: int64(n.ID),
				Code: CodeXML, Message: fmt.Sprintf("разбор XML: %v", err)})
			return true
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(frames) > 0 {
				frames[len(frames)-1].childSeen = true
				leaf = nil
			}
			p := joinPath(frames, t.Name.Local)
			switch p {
			case "race":
				text, err := readText(dec, t)
				if err != nil {
					ctx.entry(Entry{Category: "npcs", File: path, Line: lineOf(dec), ID: int64(n.ID),
						Code: CodeXML, Message: fmt.Sprintf("разбор XML: %v", err)})
					return true
				}
				raceSeen = true
				if v := strings.TrimSpace(text); v != "" {
					n.Race = v
					if !knownNpcRaces[v] {
						ctx.rep.UnknownTypes["npc.race."+v]++
					}
				} else {
					ctx.rep.EmptyValues++
				}
			case "ai":
				readAi(dec, t, &n, bag, path, &links, ctx)
			case "parameters":
				readParameters(dec, t, &n, bag, path, &links, ctx)
			case "dropLists":
				readDropLists(dec, t, &n, path, &links, ctx)
			case "collision":
				readCollision(dec, t, &n, bag, ctx)
			case "skillList":
				ctx.rep.SkippedElements["skillList"]++
				skipElement(dec, t)
			default:
				if len(frames) >= maxBagDepth {
					ctx.rep.DeepSkips++
					skipElement(dec, t)
					continue
				}
				frames = append(frames, bagFrame{name: t.Name.Local, attrs: len(t.Attr)})
				for _, a := range t.Attr {
					npcBagSet(ctx, bag, p+"."+a.Name.Local, a.Value)
				}
			}
		case xml.CharData:
			if len(frames) > 0 && !frames[len(frames)-1].childSeen {
				leaf = append(leaf, t...)
			}
		case xml.EndElement:
			if t.Name.Local == "npc" {
				done = true
				break
			}
			if len(frames) == 0 {
				continue
			}
			fr := frames[len(frames)-1]
			frames = frames[:len(frames)-1]
			if !fr.childSeen {
				p := joinPath(frames, fr.name)
				if v := strings.TrimSpace(string(leaf)); v != "" {
					npcBagSet(ctx, bag, p, v)
				} else if fr.attrs == 0 {
					ctx.rep.EmptyValues++
				}
			}
			leaf = nil
		}
	}
	if !raceSeen {
		ctx.rep.MissingRace++
	}
	if _, dup := ctx.npcs[n.ID]; dup {
		ctx.entry(Entry{Category: "npcs", File: path, Line: line, ID: int64(n.ID),
			Code: CodeDupID, Message: "дубликат ID, побеждает первая запись"})
		return false
	}
	n.set = bag
	ctx.npcs[n.ID] = n
	ctx.links = append(ctx.links, links...)
	ctx.rep.Npcs++
	return false
}

// joinPath собирает путь элемента из стека кадров и имени.
func joinPath(frames []bagFrame, name string) string {
	if len(frames) == 0 {
		return name
	}
	var sb strings.Builder
	for _, f := range frames {
		sb.WriteString(f.name)
		sb.WriteByte('.')
	}
	sb.WriteString(name)
	return sb.String()
}

// npcBagSet кладёт значение в raw-bag NPC с ключом-путём: интернирование
// ключа, счётчик неизвестных ключей (за вычетом типизированных), пустые
// значения — счётчик, дубликаты — счётчик (побеждает последний).
func npcBagSet(ctx *loadCtx, bag map[string]string, key, val string) {
	key = ctx.internKey(key)
	if !npcTypedBagKeys[key] {
		ctx.rep.UnknownKeys[ctx.internKey("npc.key."+key)]++
	}
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

// readAi разбирает блок ai: аггро-поля типизированы, clanList — clans и
// ссылки ignoreNpcId→NPC; навыки и прочее — счётчики пропуска.
func readAi(dec *xml.Decoder, start xml.StartElement, n *Npc, bag map[string]string, path string, links *[]linkRef, ctx *loadCtx) {
	for _, a := range start.Attr {
		switch a.Name.Local {
		case "aggroRange":
			npcBagSet(ctx, bag, "ai.aggroRange", a.Value)
			if v, err := strconv.ParseInt(strings.TrimSpace(a.Value), 10, 32); err == nil {
				n.AggroRange = int32(v)
			} else {
				ctx.entry(Entry{Category: "npcs", File: path, Line: lineOf(dec), ID: int64(n.ID),
					Code: CodeNumber, Message: "aggroRange " + a.Value + " не разбирается как число"})
			}
		case "clanHelpRange":
			npcBagSet(ctx, bag, "ai.clanHelpRange", a.Value)
			if v, err := strconv.ParseInt(strings.TrimSpace(a.Value), 10, 32); err == nil {
				n.ClanHelpRange = int32(v)
			} else {
				ctx.entry(Entry{Category: "npcs", File: path, Line: lineOf(dec), ID: int64(n.ID),
					Code: CodeNumber, Message: "clanHelpRange " + a.Value + " не разбирается как число"})
			}
		case "isAggressive":
			npcBagSet(ctx, bag, "ai.isAggressive", a.Value)
			switch strings.TrimSpace(a.Value) {
			case "true":
				n.IsAggressive = true
			case "false":
				n.IsAggressive = false
			default:
				ctx.entry(Entry{Category: "npcs", File: path, Line: lineOf(dec), ID: int64(n.ID),
					Code: CodeNumber, Message: "isAggressive " + a.Value + " не разбирается как bool"})
			}
		default:
			npcBagSet(ctx, bag, "ai."+a.Name.Local, a.Value)
		}
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "clanList":
				readClanList(dec, t, n, path, links, ctx)
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

// readClanList разбирает кланы (строки) и ссылки ignoreNpcId→NPC.
func readClanList(dec *xml.Decoder, start xml.StartElement, n *Npc, path string, links *[]linkRef, ctx *loadCtx) {
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "clan":
				text, err := readText(dec, t)
				if err != nil {
					return
				}
				if v := strings.TrimSpace(text); v != "" {
					n.Clans = append(n.Clans, v)
				} else {
					ctx.rep.EmptyValues++
				}
			case "ignoreNpcId":
				text, err := readText(dec, t)
				if err != nil {
					return
				}
				v, err := strconv.ParseInt(strings.TrimSpace(text), 10, 32)
				if err != nil {
					ctx.entry(Entry{Category: "npcs", File: path, Line: lineOf(dec), ID: int64(n.ID),
						Code: CodeNumber, Message: "ignoreNpcId " + strings.TrimSpace(text) + " не разбирается как число"})
					continue
				}
				n.IgnoreNpcIDs = append(n.IgnoreNpcIDs, NpcID(v))
				*links = append(*links, linkRef{cat: "npcs", file: path, line: lineOf(dec),
					ownerID: int64(n.ID), kind: linkNPC, keyID: v, desc: "ignoreNpcId"})
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

// readParameters разбирает параметры: param — raw-bag, minions —
// типизированные ссылки, навыки — счётчик пропуска.
func readParameters(dec *xml.Decoder, start xml.StartElement, n *Npc, bag map[string]string, path string, links *[]linkRef, ctx *loadCtx) {
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "param":
				name, val := "", ""
				haveVal := false
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "name":
						name = strings.TrimSpace(a.Value)
					case "value":
						val = strings.TrimSpace(a.Value)
						haveVal = true
					}
				}
				if !haveVal {
					text, err := readText(dec, t)
					if err != nil {
						return
					}
					val = strings.TrimSpace(text)
				} else {
					skipElement(dec, t)
				}
				if name != "" {
					npcBagSet(ctx, bag, "parameters."+name, val)
				}
			case "minions":
				readMinions(dec, t, n, bag, path, links, ctx)
			case "skill":
				ctx.rep.SkippedElements["skill"]++
				skipElement(dec, t)
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

// readMinions разбирает блок миньонов: имя группы — raw-bag, ссылки —
// типизированные MinionRef.
func readMinions(dec *xml.Decoder, start xml.StartElement, n *Npc, bag map[string]string, path string, links *[]linkRef, ctx *loadCtx) {
	for _, a := range start.Attr {
		if a.Name.Local == "name" {
			npcBagSet(ctx, bag, "minions", a.Value)
		}
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local != "npc" {
				ctx.rep.SkippedElements[t.Name.Local]++
				skipElement(dec, t)
				continue
			}
			line := lineOf(dec)
			var idRaw string
			m := MinionRef{}
			for _, a := range t.Attr {
				switch a.Name.Local {
				case "id":
					idRaw = strings.TrimSpace(a.Value)
				case "count":
					m.Count = atoi32(a.Value)
				case "max":
					m.Max = atoi32(a.Value)
				case "respawnTime":
					m.RespawnTime = atoi32(a.Value)
				case "weightPoint":
					m.WeightPoint = atoi32(a.Value)
				}
			}
			if idRaw == "" {
				ctx.entry(Entry{Category: "npcs", File: path, Line: line, ID: int64(n.ID),
					Code: CodeAttr, Message: "миньон без атрибута id"})
				skipElement(dec, t)
				continue
			}
			id, err := strconv.ParseInt(idRaw, 10, 32)
			if err != nil || id <= 0 {
				ctx.entry(Entry{Category: "npcs", File: path, Line: line, ID: int64(n.ID),
					Code: CodeNumber, Message: "миньон id " + idRaw + " вне домена"})
				skipElement(dec, t)
				continue
			}
			m.NpcID = NpcID(id)
			n.Minions = append(n.Minions, m)
			*links = append(*links, linkRef{cat: "npcs", file: path, line: line,
				ownerID: int64(n.ID), kind: linkNPC, keyID: id, desc: "миньон"})
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				return
			}
		}
	}
}

// atoi32 — разбор int32; неразборчивое значение даёт 0 (домен поля
// проверяет вызывающий код).
func atoi32(v string) int32 {
	n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 32)
	return int32(n)
}

// readCollision разбирает коллизию: normal — типизированные радиус/высота,
// grown — raw-bag.
func readCollision(dec *xml.Decoder, start xml.StartElement, n *Npc, bag map[string]string, ctx *loadCtx) {
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "radius", "height":
				for _, a := range t.Attr {
					npcBagSet(ctx, bag, "collision."+t.Name.Local+"."+a.Name.Local, a.Value)
					if a.Name.Local != "normal" {
						continue
					}
					if f, err := strconv.ParseFloat(strings.TrimSpace(a.Value), 64); err == nil {
						if t.Name.Local == "radius" {
							n.CollisionRadius = f
						} else {
							n.CollisionHeight = f
						}
					}
				}
				skipElement(dec, t)
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

// readDropLists разбирает дроплисты: drop/spoil → группы и прямые предметы;
// порядок файла сохраняется (осознанное отклонение от сортировки канона —
// нормализация остаётся за потребителем).
func readDropLists(dec *xml.Decoder, start xml.StartElement, n *Npc, path string, links *[]linkRef, ctx *loadCtx) {
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local != "drop" && t.Name.Local != "spoil" {
				ctx.rep.SkippedElements[t.Name.Local]++
				skipElement(dec, t)
				continue
			}
			dl := DropList{Type: t.Name.Local}
			readDropSection(dec, t, &dl, n, path, links, ctx)
			n.DropLists = append(n.DropLists, dl)
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				return
			}
		}
	}
}

func readDropSection(dec *xml.Decoder, start xml.StartElement, dl *DropList, n *Npc, path string, links *[]linkRef, ctx *loadCtx) {
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "group":
				g := DropGroup{}
				chanceOK := true
				for _, a := range t.Attr {
					if a.Name.Local == "chance" {
						g.Chance, chanceOK = dropChance(a.Value, "шанс группы", dec, path, n, ctx)
					}
				}
				readDropGroup(dec, t, &g, n, path, links, ctx)
				if chanceOK {
					dl.Groups = append(dl.Groups, g)
				}
			case "item":
				if d, ok := readDropItem(dec, t, n, path, links, ctx); ok {
					dl.Items = append(dl.Items, d)
				}
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

func readDropGroup(dec *xml.Decoder, start xml.StartElement, g *DropGroup, n *Npc, path string, links *[]linkRef, ctx *loadCtx) {
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local != "item" {
				ctx.rep.SkippedElements[t.Name.Local]++
				skipElement(dec, t)
				continue
			}
			if d, ok := readDropItem(dec, t, n, path, links, ctx); ok {
				g.Items = append(g.Items, d)
			}
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				return
			}
		}
	}
}

// readDropItem разбирает предмет дропа: id обязателен, min/max ≥ 0, шанс —
// неотрицательное конечное число; min>max и шанс >100 — счётчики широты.
func readDropItem(dec *xml.Decoder, start xml.StartElement, n *Npc, path string, links *[]linkRef, ctx *loadCtx) (Drop, bool) {
	line := lineOf(dec)
	var idRaw, minRaw, maxRaw, chanceRaw string
	for _, a := range start.Attr {
		switch a.Name.Local {
		case "id":
			idRaw = strings.TrimSpace(a.Value)
		case "min":
			minRaw = strings.TrimSpace(a.Value)
		case "max":
			maxRaw = strings.TrimSpace(a.Value)
		case "chance":
			chanceRaw = strings.TrimSpace(a.Value)
		}
	}
	skipElement(dec, start)
	if idRaw == "" {
		ctx.entry(Entry{Category: "npcs", File: path, Line: line, ID: int64(n.ID),
			Code: CodeAttr, Message: "предмет дропа без атрибута id"})
		return Drop{}, false
	}
	id, err := strconv.ParseInt(idRaw, 10, 32)
	if err != nil || id <= 0 {
		ctx.entry(Entry{Category: "npcs", File: path, Line: line, ID: int64(n.ID),
			Code: CodeNumber, Message: "предмет дропа id " + idRaw + " вне домена"})
		return Drop{}, false
	}
	d := Drop{ItemID: ItemID(id)}
	min, err := strconv.ParseInt(minRaw, 10, 32)
	if err != nil || min < 0 {
		ctx.entry(Entry{Category: "npcs", File: path, Line: line, ID: int64(n.ID),
			Code: CodeNumber, Message: "min " + minRaw + " вне домена (int32, неотрицательный)"})
		return Drop{}, false
	}
	d.Min = int32(min)
	max, err := strconv.ParseInt(maxRaw, 10, 32)
	if err != nil || max < 0 {
		ctx.entry(Entry{Category: "npcs", File: path, Line: line, ID: int64(n.ID),
			Code: CodeNumber, Message: "max " + maxRaw + " вне домена (int32, неотрицательный)"})
		return Drop{}, false
	}
	d.Max = int32(max)
	if d.Min > d.Max {
		ctx.rep.MinOverMax++
	}
	if chanceRaw != "" {
		var ok bool
		if d.Chance, ok = dropChance(chanceRaw, "шанс", dec, path, n, ctx); !ok {
			return Drop{}, false
		}
	}
	ctx.rep.DropItems++
	*links = append(*links, linkRef{cat: "npcs", file: path, line: line,
		ownerID: int64(n.ID), kind: linkItem, keyID: id, desc: "дроп"})
	return d, true
}

// dropChance разбирает шанс: неотрицательное конечное число; >100 —
// счётчик («всегда», семантика канона).
func dropChance(v, what string, dec *xml.Decoder, path string, n *Npc, ctx *loadCtx) (float64, bool) {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		ctx.entry(Entry{Category: "npcs", File: path, Line: lineOf(dec), ID: int64(n.ID),
			Code: CodeNumber, Message: what + " " + v + " вне домена (неотрицательное число)"})
		return 0, false
	}
	if f > 100 {
		ctx.rep.ChanceOver100++
	}
	return f, true
}

// knownNpcTypes — известные типы NPC канона; прочие — широта данных (счётчик).
var knownNpcTypes = map[string]bool{
	"Adventurer": true, "Artefact": true, "Auctioneer": true, "BabyPet": true,
	"BroadcastingTower": true, "CastleDoorman": true, "Chest": true,
	"ClanHallDoorman": true, "ClanHallManager": true, "ControlTower": true,
	"Doorman": true, "DawnPriest": true, "Defender": true, "DungeonGatekeeper": true,
	"DuskPriest": true, "EffectPoint": true, "EventMonster": true,
	"FeedableBeast": true, "FestivalGuide": true, "FestivalMonster": true,
	"Fisherman": true, "FlameTower": true, "FlyTerrainObject": true,
	"Folk": true, "FriendlyMob": true, "GrandBoss": true, "Guard": true,
	"Merchant": true, "Monster": true, "OlympiadManager": true, "Pet": true,
	"PetManager": true, "RaceManager": true, "RaidBoss": true,
	"RiftInvader": true, "SchemeBuffer": true, "Servitor": true,
	"SignsPriest": true, "TamedBeast": true, "Teleporter": true,
	"Trainer": true, "VillageMasterDElf": true, "VillageMasterDwarf": true,
	"VillageMasterFighter": true, "VillageMasterMystic": true,
	"VillageMasterOrc": true, "VillageMasterPriest": true, "Warehouse": true,
}

// knownNpcRaces — известные расы канона; прочие — широта данных (счётчик).
var knownNpcRaces = map[string]bool{
	"ANIMAL": true, "BEAST": true, "BUG": true, "CASTLE_GUARD": true,
	"CONSTRUCT": true, "DARK_ELF": true, "DEMONIC": true, "DIVINE": true,
	"DRAGON": true, "DWARF": true, "ELEMENTAL": true, "ELF": true,
	"ETC": true, "FAIRY": true, "GIANT": true, "HUMAN": true,
	"HUMANOID": true, "MERCENARY": true, "ORC": true, "PLANT": true,
	"SIEGE_WEAPON": true, "UNDEAD": true,
}
