package data

import (
	"encoding/xml"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
)

// ItemID — идентификатор предмета датапака.
type ItemID int32

// Item — предмет датапака. Типизированные поля покрывают минимального
// потребителя игровых фаз (инвентарь, дроп, подбор); значения отсутствующих
// полей берутся из дефолтов датапака. Прочие параметры хранятся в исходном
// виде и доступны через Set. Записи не меняются после загрузки.
// Семантика разбора и дефолты портированы с L2J_Mobius (DocumentItem,
// DocumentBase.parseBeanSet, ItemTemplate.set), GPLv3.
type Item struct {
	ID           ItemID
	Name         string
	Type         string
	Weight       int64
	Price        int64
	Stackable    bool
	CrystalType  string
	CrystalCount int64
	Material     string
	BodyPart     string

	set map[string]string
}

// Set возвращает исходное значение параметра предмета по ключу. Ключами
// служат имена set-элементов датапака как есть и статов блока stats с
// префиксом "stat." (например "stat.pAtk").
func (it Item) Set(key string) (string, bool) {
	v, ok := it.set[key]
	return v, ok
}

// lineOf — номер строки текущей позиции декодера.
func lineOf(dec *xml.Decoder) int {
	l, _ := dec.InputPos()
	return l
}

// itemsDir — каталог категории предметов в корне данных.
const itemsDir = "stats/items"

// statPrefix — префикс ключей блока stats в raw-bag.
const statPrefix = "stat."

// knownItemTypes — известные типы предмета; прочие — широта данных (счётчик).
var knownItemTypes = map[string]struct{}{"Weapon": {}, "Armor": {}, "EtcItem": {}}

// typedSetKeys — set-ключи, отражаемые в типизированные поля Item; прочие
// ключи остаются только в raw-bag и считаются неизвестными.
var typedSetKeys = map[string]struct{}{
	"weight": {}, "price": {}, "is_stackable": {},
	"crystal_type": {}, "crystal_count": {}, "material": {}, "bodypart": {},
}

// loadItems читает категорию предметов: XML верхнего уровня itemsDir;
// подкаталоги (custom с дублями ID и прочие) не читаются, но считаются в
// отчёт — молчаливая потеря состава невозможна.
// Семантика разбора и дефолты: L2J_Mobius DocumentItem, DocumentBase.parseBeanSet,
// ItemTemplate.set (порт, GPLv3).
func loadItems(fsys fs.FS, ctx *loadCtx) {
	loadFlatCategory(fsys, ctx, itemsDir, "items", parseItemsFile)
}

// countXML считает XML-файлы каталога. Ошибка чтения даёт 0 сознательно:
// подкаталог опционален, недоступность видна как ноль пропущенных файлов.
func countXML(fsys fs.FS, dir string) int {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".xml") {
			n++
		}
	}
	return n
}

// parseItemsFile — предметы через общий каркас parseListFile.
func parseItemsFile(path string, data []byte, ctx *loadCtx) {
	parseListFile(path, data, "items", "item", parseItem, ctx)
}

// parseItem разбирает предмет; true — декодер в состоянии ошибки XML, разбор
// файла пора прекратить (запись отчёта уже внесена).
func parseItem(dec *xml.Decoder, start xml.StartElement, path string, ctx *loadCtx) bool {
	line := lineOf(dec)
	var idRaw, typ, name string
	seen := map[string]struct{}{}
	for _, a := range start.Attr {
		if _, dup := seen[a.Name.Local]; dup {
			ctx.entry(Entry{Category: "items", File: path, Line: line,
				Code: CodeAttr, Message: "повтор атрибута " + a.Name.Local})
		}
		seen[a.Name.Local] = struct{}{}
		switch a.Name.Local {
		case "id":
			idRaw = strings.TrimSpace(a.Value)
		case "type":
			typ = strings.TrimSpace(a.Value)
		case "name":
			name = strings.TrimSpace(a.Value)
		}
	}
	if idRaw == "" || typ == "" || name == "" {
		ctx.entry(Entry{Category: "items", File: path, Line: line,
			Code: CodeAttr, Message: "нет обязательного атрибута id/type/name"})
		skipElement(dec, start)
		return false
	}
	id64, err := strconv.ParseInt(idRaw, 10, 32)
	if err != nil || id64 <= 0 {
		ctx.entry(Entry{Category: "items", File: path, Line: line,
			Code: CodeNumber, Message: "id " + idRaw + " вне домена (int32, положительный)"})
		skipElement(dec, start)
		return false
	}
	id := ItemID(id64)
	bag := map[string]string{}
	if !readItemContent(dec, start, path, id, bag, ctx) {
		return true
	}
	if _, dup := ctx.items[id]; dup {
		ctx.entry(Entry{Category: "items", File: path, Line: line, ID: int64(id),
			Code: CodeDupID, Message: "дубликат ID, побеждает первая запись"})
		return false
	}
	if _, ok := knownItemTypes[typ]; !ok {
		ctx.rep.UnknownTypes[typ]++
	}
	ctx.items[id] = buildItem(id, name, typ, bag, path, line, ctx)
	ctx.rep.Items++
	return false
}

// readItemContent читает содержимое item до закрывающего тега; false — обрыв
// или ошибка XML (запись уже внесена). set после закрытого блока stats —
// структурная битость (канон роняет предмет целиком): запись-ошибка, элемент
// пропускается, предмет доразбирается.
func readItemContent(dec *xml.Decoder, start xml.StartElement, path string, id ItemID, bag map[string]string, ctx *loadCtx) bool {
	badXML := func(err error) bool {
		ctx.entry(Entry{Category: "items", File: path, Line: lineOf(dec), ID: int64(id),
			Code: CodeXML, Message: fmt.Sprintf("разбор XML: %v", err)})
		return false
	}
	inStats := false
	wasStats := false
	for {
		tok, err := dec.Token()
		if err != nil {
			return badXML(err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case t.Name.Local == "set" && inStats:
				ctx.entry(Entry{Category: "items", File: path, Line: lineOf(dec), ID: int64(id),
					Code: CodeAttr, Message: "set внутри блока stats"})
				skipElement(dec, t)
			case t.Name.Local == "set" && wasStats:
				ctx.entry(Entry{Category: "items", File: path, Line: lineOf(dec), ID: int64(id),
					Code: CodeAttr, Message: "set после закрытого блока stats"})
				skipElement(dec, t)
			case t.Name.Local == "set":
				if !readSet(dec, t, path, ctx, bag) {
					return false
				}
			case t.Name.Local == "stats" && !wasStats:
				inStats = true
				wasStats = true
			case t.Name.Local == "stat" && inStats:
				if !readStat(dec, t, path, ctx, bag) {
					return false
				}
			default:
				ctx.rep.SkippedElements[t.Name.Local]++
				skipElement(dec, t)
			}
		case xml.EndElement:
			if t.Name.Local == "item" {
				return true
			}
			if t.Name.Local == "stats" {
				inStats = false
			}
		}
	}
}

// readSet переносит семантику set-элемента: значение из атрибута val, при
// отсутствии — текст тега; пустое значение пропускается; дубликат ключа
// перезаписывается (побеждает последний, как в каноне).
func readSet(dec *xml.Decoder, start xml.StartElement, path string, ctx *loadCtx, bag map[string]string) bool {
	name := ""
	val := ""
	haveVal := false
	for _, a := range start.Attr {
		switch a.Name.Local {
		case "name":
			name = strings.TrimSpace(a.Value)
		case "val":
			val = strings.TrimSpace(a.Value)
			haveVal = true
		}
	}
	if name == "" {
		ctx.rep.UnnamedSets++
		skipElement(dec, start)
		return true
	}
	if _, ok := typedSetKeys[name]; !ok {
		ctx.rep.UnknownKeys[name]++
	}
	if !haveVal {
		text, err := readText(dec, start)
		if err != nil {
			ctx.entry(Entry{Category: "items", File: path, Line: lineOf(dec),
				Code: CodeXML, Message: fmt.Sprintf("разбор XML: %v", err)})
			return false
		}
		val = strings.TrimSpace(text)
	} else {
		skipElement(dec, start)
	}
	if val == "" {
		ctx.rep.EmptyValues++
		return true
	}
	if _, dup := bag[name]; dup {
		ctx.rep.DupKeys++
	}
	bag[name] = val
	return true
}

// readStat читает запись блока stats в raw-bag с префиксом stat.
func readStat(dec *xml.Decoder, start xml.StartElement, path string, ctx *loadCtx, bag map[string]string) bool {
	typ := ""
	for _, a := range start.Attr {
		if a.Name.Local == "type" {
			typ = strings.TrimSpace(a.Value)
		}
	}
	if typ == "" {
		ctx.rep.StatNoType++
		skipElement(dec, start)
		return true
	}
	text, err := readText(dec, start)
	if err != nil {
		ctx.entry(Entry{Category: "items", File: path, Line: lineOf(dec),
			Code: CodeXML, Message: fmt.Sprintf("разбор XML: %v", err)})
		return false
	}
	val := strings.TrimSpace(text)
	if val == "" {
		ctx.rep.EmptyValues++
		return true
	}
	key := statPrefix + typ
	if _, dup := bag[key]; dup {
		ctx.rep.DupKeys++
	}
	bag[key] = val
	return true
}

// buildItem отражает raw-bag в типизированные поля; дефолты — семантика
// ItemTemplate.set канона. Значение вне домена типизированного поля — ошибка,
// поле остаётся с дефолтом.
func buildItem(id ItemID, name, typ string, bag map[string]string, path string, line int, ctx *loadCtx) Item {
	it := Item{
		ID: id, Name: name, Type: typ,
		Material: "STEEL", BodyPart: "none", CrystalType: "NONE",
		set: bag,
	}
	bad := func(key, val string) {
		ctx.entry(Entry{Category: "items", File: path, Line: line, ID: int64(id),
			Code: CodeNumber, Message: key + "=" + val + " не разбирается как число"})
	}
	if v, ok := bag["weight"]; ok {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			bad("weight", v)
		} else {
			it.Weight = n
		}
	}
	if v, ok := bag["price"]; ok {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			bad("price", v)
		} else {
			it.Price = n
		}
	}
	if v, ok := bag["crystal_count"]; ok {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			bad("crystal_count", v)
		} else {
			it.CrystalCount = n
		}
	}
	if v, ok := bag["is_stackable"]; ok {
		switch v {
		case "true":
			it.Stackable = true
		case "false":
			it.Stackable = false
		default:
			ctx.entry(Entry{Category: "items", File: path, Line: line, ID: int64(id),
				Code: CodeNumber, Message: "is_stackable=" + v + " не разбирается как bool"})
		}
	}
	if v, ok := bag["material"]; ok {
		it.Material = v
	}
	if v, ok := bag["bodypart"]; ok {
		it.BodyPart = v
	}
	if v, ok := bag["crystal_type"]; ok {
		it.CrystalType = v
	}
	return it
}

// readText читает текстовое содержимое элемента до его закрывающего тега;
// текст вложенных элементов включается (семантика getTextContent канона).
func readText(dec *xml.Decoder, el xml.StartElement) (string, error) {
	var sb strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			sb.Write(t)
		case xml.EndElement:
			if t.Name.Local == el.Name.Local {
				return sb.String(), nil
			}
		}
	}
}

// skipElement потребляет поддерево элемента целиком.
func skipElement(dec *xml.Decoder, start xml.StartElement) {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		}
	}
}
