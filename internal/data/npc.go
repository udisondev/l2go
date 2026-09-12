package data

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io/fs"
	"math"
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
var npcTypedBagKeys = map[string]struct{}{
	"npc.id": {}, "npc.level": {}, "npc.type": {}, "npc.name": {},
	"npc.title":     {},
	"ai.aggroRange": {}, "ai.clanHelpRange": {}, "ai.isAggressive": {},
	"collision.radius.normal": {}, "collision.height.normal": {},
	"minions": {},
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
	loadFlatCategory(fsys, ctx, npcsDir, "npcs", parseNpcsFile)
}

// parseNpcsFile — NPC через общий каркас parseListFile.
func parseNpcsFile(path string, data []byte, ctx *loadCtx) {
	parseListFile(path, data, "npcs", "npc", parseNpc, ctx)
}

// parseNpc разбирает NPC верхнего уровня; true — декодер в состоянии ошибки
// XML, разбор файла пора прекратить (запись отчёта уже внесена).
func parseNpc(dec *xml.Decoder, start xml.StartElement, path string, ctx *loadCtx) bool {
	line := lineOf(dec)
	n := Npc{Level: 85, Type: "Folk"} // дефолты канона (NpcData, порт)
	bag := map[string]string{}
	var idRaw string
	haveID, haveLevel, haveType, haveName, haveTitle := false, false, false, false, false
	dupAttr := func(name string, seen *bool) {
		if *seen {
			ctx.entry(Entry{Category: "npcs", File: path, Line: line,
				Code: CodeAttr, Message: "повтор атрибута " + name})
		}
		*seen = true
	}
	for _, a := range start.Attr {
		switch a.Name.Local {
		case "id":
			dupAttr("id", &haveID)
			idRaw = strings.TrimSpace(a.Value)
		case "level":
			dupAttr("level", &haveLevel)
			npcBagSet(ctx, bag, "npc.level", a.Value)
		case "type":
			dupAttr("type", &haveType)
			npcBagSet(ctx, bag, "npc.type", a.Value)
		case "name":
			dupAttr("name", &haveName)
			npcBagSet(ctx, bag, "npc.name", a.Value)
		case "title":
			dupAttr("title", &haveTitle)
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
		if _, ok := knownNpcTypes[n.Type]; !ok {
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
				// Канон нормализует расу toUpperCase (NpcData, порт).
				if v := strings.ToUpper(strings.TrimSpace(text)); v != "" {
					n.Race = v
					if _, ok := knownNpcRaces[v]; !ok {
						ctx.rep.UnknownTypes[ctx.internKey("npc.race."+v)]++
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
				readCollision(dec, t, &n, bag, path, ctx)
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
			// Текст копится только пока у верхнего кадра нет детей: текст
			// листа — его значение, текст контейнера — форматирование.
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
				if v := string(bytes.TrimSpace(leaf)); v != "" {
					npcBagSet(ctx, bag, p, v)
				} else if fr.attrs == 0 {
					// Пустой лист без атрибутов — пустое значение; лист с
					// атрибутами уже отдал их в bag на StartElement.
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

// bagSet кладёт значение в raw-bag записи с ключом-путём: интернирование
// ключа, квалифицированный счётчик неизвестных (qual+key, за вычетом typ —
// словаря типизированных ключей категории), пустые значения — счётчик,
// дубликаты — счётчик (побеждает последний). Возвращает bag (возможно,
// свежесозданный).
func bagSet(ctx *loadCtx, bag map[string]string, qual, key, val string, typ map[string]struct{}) map[string]string {
	if bag == nil {
		bag = map[string]string{}
	}
	key = ctx.internKey(key)
	if _, typed := typ[key]; !typed {
		ctx.rep.UnknownKeys[ctx.internKey(qual+key)]++
	}
	val = strings.TrimSpace(val)
	if val == "" {
		ctx.rep.EmptyValues++
		return bag
	}
	if _, dup := bag[key]; dup {
		ctx.rep.DupKeys++
	}
	bag[key] = val
	return bag
}

// npcBagSet — bagSet для NPC (квалификация npc.key., словарь типизированных
// ключей NPC); bag NPC существует всегда.
func npcBagSet(ctx *loadCtx, bag map[string]string, key, val string) {
	bagSet(ctx, bag, "npc.key.", key, val, npcTypedBagKeys)
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
				if err != nil || v <= 0 {
					ctx.entry(Entry{Category: "npcs", File: path, Line: lineOf(dec), ID: int64(n.ID),
						Code: CodeNumber, Message: "ignoreNpcId " + strings.TrimSpace(text) + " вне домена (int32, положительный)"})
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
				} else {
					ctx.rep.UnnamedSets++ // param без имени — потеря состава, считаем
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
			numeric := true // домен всех чисел миньона: int32, неотрицательный
			num := func(name, v string) int32 {
				p, err := strconv.ParseInt(strings.TrimSpace(v), 10, 32)
				if err != nil || p < 0 {
					ctx.entry(Entry{Category: "npcs", File: path, Line: line, ID: int64(n.ID),
						Code: CodeNumber, Message: "миньон " + name + "=" + v + " вне домена (int32, неотрицательный)"})
					numeric = false
					return 0
				}
				return int32(p)
			}
			for _, a := range t.Attr {
				switch a.Name.Local {
				case "id":
					idRaw = strings.TrimSpace(a.Value)
				case "count":
					m.Count = num("count", a.Value)
				case "max":
					m.Max = num("max", a.Value)
				case "respawnTime":
					m.RespawnTime = num("respawnTime", a.Value)
				case "weightPoint":
					m.WeightPoint = num("weightPoint", a.Value)
				default:
					skipAttr(ctx, "minion", a.Name.Local)
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
			if !numeric {
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

// skipAttr считает посторонний атрибут типизированного листа: потеря
// состава невозможна, интерпретация — по потребителю.
func skipAttr(ctx *loadCtx, tag, name string) {
	ctx.rep.UnknownKeys[ctx.internKey("skip.attr."+tag+"."+name)]++
}

// readCollision разбирает коллизию: normal — типизированные радиус/высота,
// grown — raw-bag.
func readCollision(dec *xml.Decoder, start xml.StartElement, n *Npc, bag map[string]string, path string, ctx *loadCtx) {
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
					// Все атрибуты коллизии — в raw-bag; normal/grown сверх
					// того отражаются в типизированные поля.
					npcBagSet(ctx, bag, "collision."+t.Name.Local+"."+a.Name.Local, a.Value)
					if a.Name.Local != "normal" {
						continue
					}
					f, err := strconv.ParseFloat(strings.TrimSpace(a.Value), 64)
					if err != nil {
						ctx.entry(Entry{Category: "npcs", File: path, Line: lineOf(dec), ID: int64(n.ID),
							Code: CodeNumber, Message: "коллизия " + t.Name.Local + "." + a.Name.Local + "=" + a.Value + " не разбирается как число"})
						continue
					}
					if t.Name.Local == "radius" {
						n.CollisionRadius = f
					} else {
						n.CollisionHeight = f
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
				chanceOK := false
				hasChance := false
				for _, a := range t.Attr {
					if a.Name.Local == "chance" {
						hasChance = true
						g.Chance, chanceOK = dropChance(a.Value, "шанс группы", dec, path, n, ctx)
					} else {
						skipAttr(ctx, "group", a.Name.Local)
					}
				}
				if !hasChance {
					ctx.entry(Entry{Category: "npcs", File: path, Line: lineOf(dec), ID: int64(n.ID),
						Code: CodeAttr, Message: "группа дропа без атрибута chance"})
				}
				// Шанс группы обязателен (канон: parseDouble без дефолта);
				// ссылки и счётчики группы — только если группа вошла.
				var groupLinks []linkRef
				readDropGroup(dec, t, &g, n, path, &groupLinks, ctx)
				if chanceOK {
					dl.Groups = append(dl.Groups, g)
					*links = append(*links, groupLinks...)
					ctx.rep.DropItems += len(g.Items)
				}
			case "item":
				if d, ok := readDropItem(dec, t, n, path, links, ctx); ok {
					dl.Items = append(dl.Items, d)
					ctx.rep.DropItems++
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

// readDropItem разбирает предмет дропа: id и chance обязательны (канон:
// parseDouble/parseInteger без дефолтов), min/max ≥ 0, шанс — неотрицательное
// конечное число; min>max и шанс >100 — счётчики широты. Ссылку регистрирует
// в переданный буфер — коммит только вместе с вошедшей секцией.
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
		default:
			skipAttr(ctx, "drop", a.Name.Local)
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
	if chanceRaw == "" {
		ctx.entry(Entry{Category: "npcs", File: path, Line: line, ID: int64(n.ID),
			Code: CodeAttr, Message: "предмет дропа без атрибута chance"})
		return Drop{}, false
	}
	var ok bool
	if d.Chance, ok = dropChance(chanceRaw, "шанс", dec, path, n, ctx); !ok {
		return Drop{}, false
	}
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
var knownNpcTypes = map[string]struct{}{
	"Adventurer": {}, "Artefact": {}, "Auctioneer": {}, "BabyPet": {},
	"BroadcastingTower": {}, "CastleDoorman": {}, "Chest": {},
	"ClanHallDoorman": {}, "ClanHallManager": {}, "ControlTower": {},
	"Doorman": {}, "DawnPriest": {}, "Defender": {}, "DungeonGatekeeper": {},
	"DuskPriest": {}, "EffectPoint": {}, "EventMonster": {},
	"FeedableBeast": {}, "FestivalGuide": {}, "FestivalMonster": {},
	"Fisherman": {}, "FlameTower": {}, "FlyTerrainObject": {},
	"Folk": {}, "FriendlyMob": {}, "GrandBoss": {}, "Guard": {},
	"Merchant": {}, "Monster": {}, "OlympiadManager": {}, "Pet": {},
	"PetManager": {}, "RaceManager": {}, "RaidBoss": {},
	"RiftInvader": {}, "SchemeBuffer": {}, "Servitor": {},
	"SignsPriest": {}, "TamedBeast": {}, "Teleporter": {},
	"Trainer": {}, "VillageMasterDElf": {}, "VillageMasterDwarf": {},
	"VillageMasterFighter": {}, "VillageMasterMystic": {},
	"VillageMasterOrc": {}, "VillageMasterPriest": {}, "Warehouse": {},
}

// knownNpcRaces — известные расы канона; прочие — широта данных (счётчик).
var knownNpcRaces = map[string]struct{}{
	"ANIMAL": {}, "BEAST": {}, "BUG": {}, "CASTLE_GUARD": {},
	"CONSTRUCT": {}, "DARK_ELF": {}, "DEMONIC": {}, "DIVINE": {},
	"DRAGON": {}, "DWARF": {}, "ELEMENTAL": {}, "ELF": {},
	"ETC": {}, "FAIRY": {}, "GIANT": {}, "HUMAN": {},
	"HUMANOID": {}, "MERCENARY": {}, "ORC": {}, "PLANT": {},
	"SIEGE_WEAPON": {}, "UNDEAD": {},
}
