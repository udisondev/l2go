package data

import (
	"encoding/xml"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

// SkillID — идентификатор скилла датапака.
type SkillID int32

// knownOperateTypes — словарь SkillOperateType канона (порт, GPLv3):
// A1 мгновенный, A2 непрерывный+мгновенный, A3 мгновенный+непрерывный,
// A4 event-herb, CA1/CA5 channeling, DA1/DA2 dance, P пассивный, T тумблер.
var knownOperateTypes = map[string]struct{}{
	"A1": {}, "A2": {}, "A3": {}, "A4": {}, "CA1": {}, "CA5": {},
	"DA1": {}, "DA2": {}, "P": {}, "T": {},
}

// knownTargetTypes — словарь TargetType канона, 38 значений (порт, GPLv3).
var knownTargetTypes = map[string]struct{}{
	"AREA":            {},
	"AREA_CORPSE_MOB": {},
	"AREA_FRIENDLY":   {},
	"AREA_SUMMON":     {},

	"AREA_UNDEAD":     {},
	"AURA":            {},
	"AURA_CORPSE_MOB": {},
	"AURA_FRIENDLY":   {},

	"BEHIND_AREA": {},
	"BEHIND_AURA": {},
	"CLAN":        {},
	"CLAN_MEMBER": {},

	"COMMAND_CHANNEL": {},
	"CORPSE":          {},
	"CORPSE_CLAN":     {},
	"CORPSE_MOB":      {},

	"ENEMY_SUMMON": {},
	"FLAGPOLE":     {},
	"FRONT_AREA":   {},
	"FRONT_AURA":   {},

	"GROUND":    {},
	"HOLY":      {},
	"NONE":      {},
	"ONE":       {},
	"OWNER_PET": {},

	"PARTY":        {},
	"PARTY_CLAN":   {},
	"PARTY_MEMBER": {},
	"PARTY_NOTME":  {},

	"PARTY_OTHER": {},
	"PC_BODY":     {},
	"PET":         {},
	"SELF":        {},
	"SERVITOR":    {},

	"SUMMON":       {},
	"TARGET_PARTY": {},
	"UNDEAD":       {},
	"UNLOCKABLE":   {},
}

// maxSkillLevels — потолок атрибута levels (максимум дистрибутива 80, запас
// три порядка): связанная аллокация уровней ограничена до разбора таблиц.
const maxSkillLevels = 65535

// Skill — типизированная запись уровня скилла. Значения полей — результат
// резолва по уровню (таблицы, наследование энчантов, override); отсутствующие
// в данных поля — дефолты Skill.ctor канона (порт, GPLv3): тайминги 0,
// IsDebuff false, TargetType SELF. Запись не меняется после загрузки.
type Skill struct {
	Name         string
	OperateType  string
	TargetType   string
	ID           SkillID
	Level        int32
	HitTime      int32 // мс
	ReuseDelay   int32 // мс
	AbnormalTime int32 // сек
	IsMagic      int32 // 0 физический, 1 магический, 2 статический, 3 танец
	IsDebuff     bool
}

// SkillEnchant — энчант-маршрут определения скилла; размер маршрута — длина
// соответствующего слайса EnchantLevels.
type SkillEnchant struct {
	Route int8
}

// RawAttr — атрибут узла raw-дерева.
type RawAttr struct {
	Name  string
	Value string
}

// RawNode — узел иммутабельного минимального дерева эффектов и условий:
// атрибуты отсортированы по имени при загрузке, текст нормализован
// схлопыванием пробельных серий, комментарии отбрасываются. Резолв
// параметров — за потребителем (фазы 4/6); слайсы — только чтение.
type RawNode struct {
	Name     string
	Attrs    []RawAttr
	Text     string
	Children []RawNode
}

// SkillDef — определение скилла: исходная форма (таблицы, прямые элементы,
// override энчантов, деревья эффектов/условий) и материализованные уровни.
// Методы возвращают данные только для чтения; мапы и слайсы записи —
// контракт «только чтение» пакета. Семантика генерации уровней и резолва —
// порт L2J_Mobius DocumentSkill/DocumentBase (GPLv3).
type SkillDef struct {
	ID            SkillID
	Name          string
	Levels        int32
	Enchant       []SkillEnchant
	Base          []Skill
	EnchantLevels [][]Skill // индекс совпадает с Enchant (маршрут — поле Route)

	tables           map[string][]string
	set              map[string]string
	enchantOverrides map[int8]map[string]string
	raw              []RawNode
}

// Table возвращает таблицу значений по имени без решётки (как хранится:
// "#hit" в файле — Table("hit")). Слайс — только чтение.
func (d SkillDef) Table(name string) ([]string, bool) {
	t, ok := d.tables[name]
	return t, ok
}

// Set возвращает исходное файловое значение прямого элемента (включая
// типизированные ключи, как у предметов P2.1): значение без резолва,
// "#"-ссылки — как в файле. Материализованное значение уровня — метод
// Static.Skill.
func (d SkillDef) Set(key string) (string, bool) {
	v, ok := d.set[key]
	return v, ok
}

// EnchantValue возвращает исходное значение override энчант-маршрута.
func (d SkillDef) EnchantValue(route int8, key string) (string, bool) {
	m, ok := d.enchantOverrides[route]
	if !ok {
		return "", false
	}
	v, ok := m[key]
	return v, ok
}

// Raw возвращает контейнеры эффектов и условий в порядке файла. Слайс —
// только чтение.
func (d SkillDef) Raw() []RawNode {
	return d.raw
}

// skillsDir — каталог категории скиллов в корне данных.
const skillsDir = "stats/skills"

// skillSrc — значение ключа в порядке файла: прямой элемент (route 0) или
// override энчант-маршрута. Для одного ключа и контекста уровня побеждает
// последний по файлу (семантика StatSet.set канона).
type skillSrc struct {
	key   string
	route int8 // 0 — прямой элемент
	val   string
	line  int
}

// loadSkills читает категорию скиллов: плоский XML верхнего уровня skillsDir;
// подкаталоги (custom) считаются в отчёт и не читаются.
// Семантика разбора: L2J_Mobius DocumentSkill (порт, GPLv3).
func loadSkills(fsys fs.FS, ctx *loadCtx) {
	loadFlatCategory(fsys, ctx, skillsDir, "skills", parseSkillsFile)
}

func parseSkillsFile(path string, data []byte, ctx *loadCtx) {
	parseListFile(path, data, "skills", "skill", parseSkill, ctx)
}

// parseSkill разбирает определение скилла; true — ошибка XML, разбор файла
// пора прекратить (запись отчёта уже внесена).
func parseSkill(dec *xml.Decoder, start xml.StartElement, path string, ctx *loadCtx) bool {
	line := lineOf(dec)
	var idRaw, name, levelsRaw string
	hasName := false
	routes := map[int8]int{}
	seen := map[string]struct{}{}
	for _, a := range start.Attr {
		if _, dup := seen[a.Name.Local]; dup {
			ctx.entry(Entry{Category: "skills", File: path, Line: line,
				Code: CodeAttr, Message: "повтор атрибута " + a.Name.Local})
		}
		seen[a.Name.Local] = struct{}{}
		switch {
		case a.Name.Local == "id":
			idRaw = strings.TrimSpace(a.Value)
		case a.Name.Local == "name":
			name = strings.TrimSpace(a.Value)
			hasName = true
		case a.Name.Local == "levels":
			levelsRaw = strings.TrimSpace(a.Value)
		case strings.HasPrefix(a.Name.Local, "enchantGroup"):
			r, g, ok := parseEnchantGroupAttr(a.Name.Local, a.Value)
			if !ok {
				// id ещё не разобран надёжно — запись без ID, как у прочих
				// атрибутных ошибок категорий.
				ctx.entry(Entry{Category: "skills", File: path, Line: line,
					Code: CodeAttr, Message: a.Name.Local + "=" + a.Value + " вне домена (маршрут 1–8, значение ≥ 1)"})
				continue
			}
			routes[r] = enchantGroupSize(g)
		default:
			ctx.rep.UnknownKeys[ctx.internKey("skill.attr."+a.Name.Local)]++
		}
	}
	// Пустое имя легально (name="" встречается в дистрибутиве: канон хранит
	// пустую строку); отсутствие атрибута — ошибка.
	if idRaw == "" || !hasName || levelsRaw == "" {
		ctx.entry(Entry{Category: "skills", File: path, Line: line,
			Code: CodeAttr, Message: "нет обязательного атрибута id/name/levels"})
		skipElement(dec, start)
		return false
	}
	id64, err := strconv.ParseInt(idRaw, 10, 32)
	if err != nil || id64 <= 0 {
		ctx.entry(Entry{Category: "skills", File: path, Line: line,
			Code: CodeNumber, Message: "id " + idRaw + " вне домена (int32, положительный)"})
		skipElement(dec, start)
		return false
	}
	levels, err := strconv.ParseInt(levelsRaw, 10, 32)
	if err != nil || levels < 1 || levels > maxSkillLevels {
		ctx.entry(Entry{Category: "skills", File: path, Line: line, ID: id64,
			Code: CodeNumber, Message: "levels " + levelsRaw + " вне домена [1, 65535]"})
		skipElement(dec, start)
		return false
	}
	def := &SkillDef{
		ID: SkillID(id64), Name: name, Levels: int32(levels),
		tables: map[string][]string{}, set: map[string]string{},
	}
	var srcs []skillSrc
	if !readSkillContent(dec, start, path, def, &srcs, routes, ctx) {
		return true
	}
	if _, dup := ctx.skills[def.ID]; dup {
		ctx.entry(Entry{Category: "skills", File: path, Line: line, ID: int64(def.ID),
			Code: CodeDupID, Message: "дубликат ID, побеждает первая запись"})
		return false
	}
	buildSkillLevels(def, srcs, routes, path, line, ctx)
	ctx.skills[def.ID] = def
	ctx.rep.Skills++
	ctx.rep.SkillLevels += len(def.Base)
	for _, lv := range def.EnchantLevels {
		ctx.rep.SkillLevels += len(lv)
	}
	if len(def.Enchant) > 0 {
		ctx.rep.EnchantedSkills++
	}
	ctx.rep.SkillTables += len(def.tables)
	return false
}

// parseEnchantGroupAttr разбирает атрибут enchantGroupR: маршрут — суффикс
// имени, group — числовое значение.
func parseEnchantGroupAttr(name, value string) (route int8, group int, ok bool) {
	r, err := strconv.Atoi(strings.TrimPrefix(name, "enchantGroup"))
	if err != nil || r < 1 || r > 8 {
		return 0, 0, false
	}
	g, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || g < 1 {
		return 0, 0, false
	}
	return int8(r), g, true
}

// enchantGroupSize — размер маршрута по значению группы (порт
// DocumentSkill.getEnchantGroupSize, CT2.4-совместимость, GPLv3).
func enchantGroupSize(group int) int {
	if group < 3 {
		return 30
	}
	return 15
}

// Суффиксы семейств контейнеров канона: effects/selfEffects/…/enchantNeffects
// и conditions/enchantNconditions — обрезанные суффиксы матчат всё семейство
// одной проверкой (DocumentSkill читает их единообразно).
const (
	containerEffectsSuffix = "ffects"
	containerCondSuffix    = "onditions"
)

// readSkillContent читает детей skill до закрывающего тега: таблицы,
// override энчантов, контейнеры эффектов/условий, прямые элементы.
// false — обрыв или ошибка XML (запись уже внесена).
func readSkillContent(dec *xml.Decoder, start xml.StartElement, path string, def *SkillDef,
	srcs *[]skillSrc, routes map[int8]int, ctx *loadCtx) bool {
	badXML := func(err error) bool {
		ctx.entry(Entry{Category: "skills", File: path, Line: lineOf(dec), ID: int64(def.ID),
			Code: CodeXML, Message: fmt.Sprintf("разбор XML: %v", err)})
		return false
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return badXML(err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := t.Name.Local
			switch {
			case name == "table":
				if !readSkillTable(dec, t, path, def, ctx) {
					return false
				}
			case isEnchantOverride(name):
				if !readSkillOverride(dec, t, path, def, srcs, routes, ctx) {
					return false
				}
			case strings.HasSuffix(name, containerEffectsSuffix) || strings.HasSuffix(name, containerCondSuffix):
				node, err := readRawNode(dec, t, ctx)
				if err != nil {
					return badXML(err)
				}
				def.raw = append(def.raw, node)
			case name == "set":
				if !readSkillSetElem(dec, t, path, def, srcs, ctx) {
					return false
				}
			default:
				if !readSkillDirect(dec, t, path, def, srcs, ctx) {
					return false
				}
			}
		case xml.EndElement:
			if t.Name.Local == "skill" {
				return true
			}
		}
	}
}

// isEnchantOverride: enchantR без суффиксов контейнеров эффектов/условий.
func isEnchantOverride(name string) bool {
	if !strings.HasPrefix(name, "enchant") {
		return false
	}
	return !strings.HasSuffix(name, containerEffectsSuffix) && !strings.HasSuffix(name, containerCondSuffix)
}

// readSkillTable переносит семантику table-элемента (порт
// DocumentBase.parseTable, GPLv3): имя обязано начинаться с #, значения —
// пробельное разделение; дубликат имени — побеждает последняя, счётчик.
func readSkillTable(dec *xml.Decoder, start xml.StartElement, path string, def *SkillDef, ctx *loadCtx) bool {
	name := ""
	for _, a := range start.Attr {
		if a.Name.Local == "name" {
			name = strings.TrimSpace(a.Value)
		}
	}
	text, err := readText(dec, start)
	if err != nil {
		ctx.entry(Entry{Category: "skills", File: path, Line: lineOf(dec), ID: int64(def.ID),
			Code: CodeXML, Message: fmt.Sprintf("разбор XML: %v", err)})
		return false
	}
	if name == "" || !strings.HasPrefix(name, "#") {
		ctx.entry(Entry{Category: "skills", File: path, Line: lineOf(dec), ID: int64(def.ID),
			Code: CodeAttr, Message: "имя таблицы \"" + name + "\" без #"})
		return true
	}
	key := ctx.internKey(strings.TrimPrefix(name, "#"))
	if _, dup := def.tables[key]; dup {
		ctx.rep.DupTables++
	}
	def.tables[key] = strings.Fields(text)
	return true
}

// readSkillOverride читает enchantR-элемент: ключ из атрибута name, значение
// из атрибута val или текста (порт DocumentBase.parseBeanSet — симметрия
// с легаси set). Мёртвый override (нет enchantGroupR) — счётчик
// OrphanEnchants, значение не сохраняется (канон игнорирует молча).
func readSkillOverride(dec *xml.Decoder, start xml.StartElement, path string, def *SkillDef,
	srcs *[]skillSrc, routes map[int8]int, ctx *loadCtx) bool {
	line := lineOf(dec)
	r, err := strconv.Atoi(strings.TrimPrefix(start.Name.Local, "enchant"))
	if err != nil || r < 1 || r > 8 {
		ctx.rep.OrphanEnchants++
		skipElement(dec, start)
		return true
	}
	route := int8(r)
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
		default:
			ctx.rep.UnknownKeys[ctx.internKey("skill.attr."+a.Name.Local)]++
		}
	}
	if !haveVal {
		text, err := readText(dec, start)
		if err != nil {
			ctx.entry(Entry{Category: "skills", File: path, Line: line, ID: int64(def.ID),
				Code: CodeXML, Message: fmt.Sprintf("разбор XML: %v", err)})
			return false
		}
		val = strings.TrimSpace(text)
	} else {
		skipElement(dec, start)
	}
	if _, has := routes[route]; !has {
		ctx.rep.OrphanEnchants++
		return true
	}
	if name == "" {
		ctx.rep.UnnamedSets++
		return true
	}
	if val == "" {
		ctx.rep.EmptyValues++
		return true
	}
	key := ctx.internKey(name)
	if def.enchantOverrides == nil {
		def.enchantOverrides = map[int8]map[string]string{}
	}
	m, ok := def.enchantOverrides[route]
	if !ok {
		m = map[string]string{}
		def.enchantOverrides[route] = m
	}
	if _, dup := m[key]; dup {
		ctx.rep.DupKeys++
	}
	m[key] = val
	*srcs = append(*srcs, skillSrc{key: key, route: route, val: val, line: line})
	return true
}

// readSkillDirect читает прямой элемент: значение = текст (конкатенация при
// вложенности — семантика getTextContent канона); пустой текст — пропуск со
// счётчиком; вложенные дети — счётчик NestedDirect. Ключ попадает в raw-bag
// и в источники материализации.
func readSkillDirect(dec *xml.Decoder, start xml.StartElement, path string, def *SkillDef,
	srcs *[]skillSrc, ctx *loadCtx) bool {
	line := lineOf(dec)
	hasChild := false
	for _, a := range start.Attr {
		ctx.rep.UnknownKeys[ctx.internKey("skill.attr."+a.Name.Local)]++
	}
	text, err := readTextNested(dec, start, &hasChild)
	if err != nil {
		ctx.entry(Entry{Category: "skills", File: path, Line: line, ID: int64(def.ID),
			Code: CodeXML, Message: fmt.Sprintf("разбор XML: %v", err)})
		return false
	}
	if hasChild {
		ctx.rep.NestedDirect++
	}
	key := ctx.internKey(start.Name.Local)
	val := strings.TrimSpace(text)
	if val == "" {
		ctx.rep.EmptyValues++
		return true
	}
	if _, dup := def.set[key]; dup {
		ctx.rep.DupKeys++
	}
	def.set[key] = val
	*srcs = append(*srcs, skillSrc{key: key, route: 0, val: val, line: line})
	return true
}

// readSkillSetElem читает легаси set-элемент (порт
// DocumentBase.parseBeanSet, GPLv3): ключ из name, значение из val или текста;
// в дистрибутиве не встречается, но потеря данных недопустима.
func readSkillSetElem(dec *xml.Decoder, start xml.StartElement, path string, def *SkillDef,
	srcs *[]skillSrc, ctx *loadCtx) bool {
	line := lineOf(dec)
	name, val := "", ""
	haveVal := false
	for _, a := range start.Attr {
		switch a.Name.Local {
		case "name":
			name = strings.TrimSpace(a.Value)
		case "val":
			val = strings.TrimSpace(a.Value)
			haveVal = true
		default:
			ctx.rep.UnknownKeys[ctx.internKey("skill.attr."+a.Name.Local)]++
		}
	}
	if !haveVal {
		text, err := readText(dec, start)
		if err != nil {
			ctx.entry(Entry{Category: "skills", File: path, Line: line, ID: int64(def.ID),
				Code: CodeXML, Message: fmt.Sprintf("разбор XML: %v", err)})
			return false
		}
		val = strings.TrimSpace(text)
	} else {
		skipElement(dec, start)
	}
	if name == "" {
		ctx.rep.UnnamedSets++
		return true
	}
	if val == "" {
		ctx.rep.EmptyValues++
		return true
	}
	key := ctx.internKey(name)
	if _, dup := def.set[key]; dup {
		ctx.rep.DupKeys++
	}
	def.set[key] = val
	*srcs = append(*srcs, skillSrc{key: key, route: 0, val: val, line: line})
	return true
}

// readTextNested читает текстовое содержимое элемента с признаком
// вложенных элементов; текст всех потомков конкатенируется (семантика
// getTextContent канона). Отличается от readText (предметы) подсчётом по
// глубине, а не по имени закрывающего тега: одноимённые вложенные элементы
// не завершают чтение раньше времени; флаг hasChild — для счётчика
// NestedDirect.
func readTextNested(dec *xml.Decoder, el xml.StartElement, hasChild *bool) (string, error) {
	var sb strings.Builder
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			*hasChild = true
			depth++
		case xml.CharData:
			sb.Write(t)
		case xml.EndElement:
			depth--
		}
	}
	return sb.String(), nil
}

// readRawNode читает поддерево эффектов/условий в иммутабельное дерево:
// атрибуты сортируются, текст нормализуется, комментарии опускаются. Глубина
// ограничена maxRawDepth (симметрично декодеру секции статики: парс-зелёное
// всегда декодируется). Ошибка декодера возвращается вызывающему с позицией.
func readRawNode(dec *xml.Decoder, start xml.StartElement, ctx *loadCtx) (RawNode, error) {
	return readRawNodeDepth(dec, start, ctx, 1)
}

func readRawNodeDepth(dec *xml.Decoder, start xml.StartElement, ctx *loadCtx, depth int) (RawNode, error) {
	if depth > maxRawDepth {
		return RawNode{}, fmt.Errorf("raw-дерево глубже %d: <%s>", maxRawDepth, start.Name.Local)
	}
	node := RawNode{Name: ctx.internKey(start.Name.Local)}
	for _, a := range start.Attr {
		node.Attrs = append(node.Attrs, RawAttr{Name: ctx.internKey(a.Name.Local), Value: a.Value})
	}
	sort.Slice(node.Attrs, func(i, j int) bool { return node.Attrs[i].Name < node.Attrs[j].Name })
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return RawNode{}, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			child, err := readRawNodeDepth(dec, t, ctx, depth+1)
			if err != nil {
				return RawNode{}, err
			}
			node.Children = append(node.Children, child)
		case xml.CharData:
			text.Write(t)
		case xml.Comment:
			// комментарии отбрасываются
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				node.Text = normalizeSpace(text.String())
				return node, nil
			}
		}
	}
}

// normalizeSpace схлопывает пробельные серии и обрезает края.
func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// типизированные ключи материализации; прочие прямые элементы — только
// raw-bag (потребители фаз 4/6).
var skillTypedKeys = map[string]struct{}{
	"operateType": {}, "targetType": {}, "hitTime": {}, "reuseDelay": {},
	"abnormalTime": {}, "isMagic": {}, "isDebuff": {}, "itemConsumeId": {},
}

// buildSkillLevels материализует записи уровней: базовые 1..levels и
// энчант-маршруты (номер = 101+40·(r−1)+i). Значения ключей применяются в
// порядке файла (порт DocumentSkill: прямой элемент на базовом уровне L —
// таблица [L−1], на энчант-уровне наследование [levels−1]; override маршрута
// — подиндекс [i]). Разрыв цепочки — таблица отсутствует или короче
// требуемой длины (прямой — levels, override — размер маршрута): запись
// table, поле остаётся с дефолтом, уровень материализуется (сборка красная).
func buildSkillLevels(def *SkillDef, srcs []skillSrc, routes map[int8]int, path string, line int, ctx *loadCtx) {
	// Требуемые длины таблиц всех "#"-ссылок (типизированных и raw: разрыв
	// цепочки — свойство уровня, а не поля) — одна запись на разрыв.
	for _, s := range srcs {
		if !strings.HasPrefix(s.val, "#") {
			continue
		}
		need := int(def.Levels)
		scope := fmt.Sprintf("уровни 1..%d", def.Levels)
		if s.route != 0 {
			need = routes[s.route]
			scope = fmt.Sprintf("маршрут %d, уровни %d..%d", s.route, 101+40*(int32(s.route)-1), 100+40*(int32(s.route)-1)+int32(need))
		}
		tbl, ok := def.tables[strings.TrimPrefix(s.val, "#")]
		if !ok {
			ctx.entry(Entry{Category: "skills", File: path, Line: s.line, ID: int64(def.ID),
				Code: CodeTable, Message: "таблица " + s.val + " не существует (" + scope + ")"})
			continue
		}
		if len(tbl) < need {
			ctx.entry(Entry{Category: "skills", File: path, Line: s.line, ID: int64(def.ID),
				Code: CodeTable, Message: fmt.Sprintf("таблица %s короче требуемой длины %d (есть %d; %s)", s.val, need, len(tbl), scope)})
		}
	}
	// Операция обязательна (в дистрибутиве есть всегда; канон при отсутствии
	// молча теряет запись уровня — здесь ошибка на весь скилл).
	hasOperate := false
	hasTarget := false
	for _, s := range srcs {
		if s.route != 0 {
			continue
		}
		if s.key == "operateType" {
			hasOperate = true
		}
		if s.key == "targetType" {
			hasTarget = true
		}
	}
	if !hasOperate {
		ctx.entry(Entry{Category: "skills", File: path, Line: line, ID: int64(def.ID),
			Code: CodeAttr, Message: "нет обязательного поля operateType"})
	}
	if !hasTarget {
		ctx.rep.MissingTargetType++
	}

	def.Base = make([]Skill, def.Levels)
	state := &skillMatState{checked: map[string]string{}, seenItems: map[ItemID]struct{}{}}
	for i := range def.Base {
		def.Base[i] = materializeSkill(def, srcs, 0, int32(i+1), 0, path, ctx, state)
	}
	routesSorted := make([]int8, 0, len(routes))
	for r := range routes {
		routesSorted = append(routesSorted, r)
	}
	sort.Slice(routesSorted, func(i, j int) bool { return routesSorted[i] < routesSorted[j] })
	for _, r := range routesSorted {
		def.Enchant = append(def.Enchant, SkillEnchant{Route: r})
		lvls := make([]Skill, routes[r])
		for i := range lvls {
			lvls[i] = materializeSkill(def, srcs, r, 101+40*int32(r-1)+int32(i), int32(i), path, ctx, state)
		}
		def.EnchantLevels = append(def.EnchantLevels, lvls)
	}
}

// skillMatState — сквозное состояние материализации одного def: проверенные
// значения (литерал не перепроверивается на каждом уровне), последнее
// ошибочное значение itemConsumeId и дедупликация предметных ссылок по
// паре (скилл, предмет).
type skillMatState struct {
	checked        map[string]string
	seenItems      map[ItemID]struct{}
	itemConsumeErr string
}

// materializeSkill собирает запись уровня: шапка, значения типизированных
// ключей (последний применимый источник по файлу), домены и дефолты
// Skill.ctor канона (порт, GPLv3). Присваивание — на каждом уровне;
// валидация и счётчики широты — однократно на новое значение ключа
// (литерал на всех уровнях — одно вхождение данных). Предметные ссылки
// регистрируются с дедупликацией по паре (скилл, предмет).
func materializeSkill(def *SkillDef, srcs []skillSrc, route int8, level, sub int32, path string, ctx *loadCtx, state *skillMatState) Skill {
	sk := Skill{ID: def.ID, Level: level, Name: def.Name, TargetType: "SELF"}
	itemConsume := ""
	itemLine := 0
	for _, s := range srcs {
		if _, typed := skillTypedKeys[s.key]; !typed {
			continue
		}
		if s.route != 0 && s.route != route {
			continue
		}
		val := s.val
		if strings.HasPrefix(val, "#") {
			idx := level - 1 // базовый уровень L → [L−1]
			if route != 0 {
				idx = def.Levels - 1 // наследование последнего базового значения
				if s.route != 0 {
					idx = sub // подиндекс маршрута
				}
			}
			if tbl, ok := def.tables[strings.TrimPrefix(val, "#")]; ok && int(idx) < len(tbl) {
				val = tbl[idx]
			} else {
				continue // разрыв уже внесён записью table
			}
		}
		// Словарные значения интернируются: уровней ~30 тыс., словарь —
		// десятки строк (решение F4).
		switch s.key {
		case "operateType", "targetType":
			val = ctx.internKey(val)
		}
		assignSkillField(&sk, s.key, val)
		if s.key == "itemConsumeId" {
			itemConsume = val
			itemLine = s.line
		}
		if prev, done := state.checked[s.key]; !done || prev != val {
			state.checked[s.key] = val
			validateSkillField(def.ID, s.key, val, level, path, ctx)
		}
	}
	if id := parseItemConsume(itemConsume, def, level, path, ctx, state); id != 0 {
		if _, dup := state.seenItems[id]; !dup {
			state.seenItems[id] = struct{}{}
			ctx.links = append(ctx.links, linkRef{
				cat: "skills", file: path, line: itemLine, keyID: int64(id),
				kind: linkItem, ownerID: int64(def.ID),
				desc: fmt.Sprintf("потребление уровня %d", level),
			})
		}
	}
	return sk
}

// assignSkillField присваивает значение типизированному полю записи; вне
// домена — поле остаётся с дефолтом (ошибка вносится валидацией).
func assignSkillField(sk *Skill, key, val string) {
	switch key {
	case "operateType":
		sk.OperateType = val
	case "targetType":
		sk.TargetType = val
	case "hitTime":
		if n, ok := atoi32(val); ok && n >= 0 {
			sk.HitTime = n
		}
	case "reuseDelay":
		if n, ok := atoi32(val); ok && n >= 0 {
			sk.ReuseDelay = n
		}
	case "abnormalTime":
		if n, ok := atoi32(val); ok && n >= 0 {
			sk.AbnormalTime = n
		}
	case "isMagic":
		if n, ok := atoi32(val); ok {
			sk.IsMagic = n
		}
	case "isDebuff":
		switch val {
		case "true":
			sk.IsDebuff = true
		case "false":
			sk.IsDebuff = false
		}
	}
}

// validateSkillField проверяет домен значения: ошибки — записи отчёта с
// уровнем, широта данных (неизвестные словарные значения) — счётчики.
func validateSkillField(id SkillID, key, val string, level int32, path string, ctx *loadCtx) {
	badNumber := func(msg string) {
		ctx.entry(Entry{Category: "skills", File: path, ID: int64(id),
			Code: CodeNumber, Message: fmt.Sprintf("уровень %d: %s", level, msg)})
	}
	switch key {
	case "hitTime", "reuseDelay", "abnormalTime":
		n, ok := atoi32(val)
		if !ok {
			badNumber(key + "=" + val + " не разбирается как число")
		} else if n < 0 {
			badNumber(key + "=" + val + " отрицательный")
		}
	case "isMagic":
		n, ok := atoi32(val)
		if !ok {
			badNumber("isMagic=" + val + " не разбирается как число")
		} else if n < 0 || n > 3 {
			ctx.rep.UnknownTypes[ctx.internKey("skill.isMagic."+val)]++
		}
	case "isDebuff":
		if val != "true" && val != "false" {
			badNumber("isDebuff=" + val + " не разбирается как bool")
		}
	case "operateType":
		if _, ok := knownOperateTypes[val]; !ok {
			ctx.rep.UnknownTypes[ctx.internKey("skill.operateType."+val)]++
		}
	case "targetType":
		if _, ok := knownTargetTypes[val]; !ok {
			ctx.rep.UnknownTypes[ctx.internKey("skill.targetType."+val)]++
		}
	}
}

// atoi32 — мягкий разбор int32.
func atoi32(val string) (int32, bool) {
	n, err := strconv.ParseInt(val, 10, 32)
	if err != nil {
		return 0, false
	}
	return int32(n), true
}

// parseItemConsume возвращает id предмета потребления уровня (0 — нет).
// Ошибка домена выносится однократно на новое значение (литерал на всех
// уровнях — одна запись, как у прочих полей).
func parseItemConsume(val string, def *SkillDef, level int32, path string, ctx *loadCtx, state *skillMatState) ItemID {
	if val == "" {
		return 0
	}
	n, err := strconv.ParseInt(val, 10, 32)
	if err != nil || n < 0 {
		if state.itemConsumeErr != val {
			state.itemConsumeErr = val
			ctx.entry(Entry{Category: "skills", File: path, ID: int64(def.ID),
				Code: CodeNumber, Message: fmt.Sprintf("уровень %d: itemConsumeId=%s вне домена (int32, неотрицательный)", level, val)})
		}
		return 0
	}
	if n == 0 {
		return 0
	}
	return ItemID(n)
}

// dumpSkills пишет канонический текст скиллов: определения по возрастанию
// ID (атрибуты, маршруты, таблицы, raw-bag, overrides, raw-деревья), затем
// уровни по возрастанию (id, level).
func dumpSkills(sb *strings.Builder, s *Static) {
	ids := make([]int64, 0, len(s.skills))
	for id := range s.skills {
		ids = append(ids, int64(id))
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, idv := range ids {
		d := s.skills[SkillID(idv)]
		fmt.Fprintf(sb, "skill id=%d name=%q levels=%d ench=[", d.ID, d.Name, d.Levels)
		for i, r := range d.Enchant {
			if i > 0 {
				sb.WriteByte(' ')
			}
			fmt.Fprintf(sb, "%d:%d", r.Route, len(d.EnchantLevels[i]))
		}
		sb.WriteByte(']')
		sb.WriteString(" tables=[")
		names := make([]string, 0, len(d.tables))
		for n := range d.tables {
			names = append(names, n)
		}
		sort.Strings(names)
		for i, n := range names {
			if i > 0 {
				sb.WriteByte(' ')
			}
			fmt.Fprintf(sb, "#%s=%s", n, strings.Join(d.tables[n], " "))
		}
		sb.WriteByte(']')
		writeSets(sb, d.set)
		dumpSkillOverrides(sb, d)
		sb.WriteString(" raw=[")
		for i, n := range d.raw {
			if i > 0 {
				sb.WriteByte(' ')
			}
			dumpRawNode(sb, n)
		}
		sb.WriteString("]\n")
	}
	for _, idv := range ids {
		d := s.skills[SkillID(idv)]
		dumpSkillLevels(sb, d.Base)
		for _, lvls := range d.EnchantLevels {
			dumpSkillLevels(sb, lvls)
		}
	}
}

func dumpSkillLevels(sb *strings.Builder, lvls []Skill) {
	for _, sk := range lvls {
		fmt.Fprintf(sb, "skilllvl id=%d level=%d name=%q op=%q target=%q hit=%d reuse=%d abtime=%d ismagic=%d isdebuff=%t\n",
			sk.ID, sk.Level, sk.Name, sk.OperateType, sk.TargetType, sk.HitTime,
			sk.ReuseDelay, sk.AbnormalTime, sk.IsMagic, sk.IsDebuff)
	}
}

// dumpSkillOverrides пишет overrides энчант-маршрутов: маршруты по
// возрастанию, ключи отсортированы.
func dumpSkillOverrides(sb *strings.Builder, d *SkillDef) {
	routes := make([]int8, 0, len(d.enchantOverrides))
	for r := range d.enchantOverrides {
		routes = append(routes, r)
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i] < routes[j] })
	for _, r := range routes {
		m := d.enchantOverrides[r]
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(sb, " ench%d=[", r)
		for i, k := range keys {
			if i > 0 {
				sb.WriteByte(' ')
			}
			sb.WriteString(k)
			sb.WriteByte('=')
			sb.WriteString(m[k])
		}
		sb.WriteByte(']')
	}
}

// dumpRawNode сериализует узел дерева канонически: имя, атрибуты по имени
// (уже отсортированы при загрузке), текст, дети.
func dumpRawNode(sb *strings.Builder, n RawNode) {
	sb.WriteByte('<')
	sb.WriteString(n.Name)
	for _, a := range n.Attrs {
		sb.WriteByte(' ')
		sb.WriteString(a.Name)
		sb.WriteString(`="`)
		sb.WriteString(dumpEscape(a.Value))
		sb.WriteByte('"')
	}
	sb.WriteByte('>')
	sb.WriteString(dumpEscape(n.Text))
	for _, c := range n.Children {
		dumpRawNode(sb, c)
	}
	sb.WriteString("</")
	sb.WriteString(n.Name)
	sb.WriteByte('>')
}

// dumpEscape — минимальное экранирование для канонического дампа.
func dumpEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
