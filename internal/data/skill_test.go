package data

import (
	"strings"
	"testing"
	"testing/fstest"
)

// skillFS — MapFS с файлом скиллов поверх обязательных каталогов категорий.
func skillFS(name string, xml string) fstest.MapFS {
	return catFS(map[string]*fstest.MapFile{
		"stats/skills/" + name: {Data: []byte(xml)},
	})
}

// loadSkillXML загружает один файл скиллов; фатальная ошибка FS — текет.
func loadSkillXML(t *testing.T, xml string) (*Static, *Report) {
	t.Helper()
	st, rep, err := Load(skillFS("x.xml", xml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return st, rep
}

// TestSkillGoldenSemantics: значения типизированных полей по уровням —
// таблицы, энчант-наследование, override таблицей и строковым литералом,
// isDebuff через таблицу, дефолты канона.
func TestSkillGoldenSemantics(t *testing.T) {
	st, rep := loadSynth(t)
	if rep.HasErrors() {
		t.Fatalf("ошибки в чистой синтетике: %+v", rep.Errors)
	}
	cases := []Skill{
		{Name: "Тренировочный удар", OperateType: "A1", TargetType: "ONE",
			ID: 7001, Level: 1, HitTime: 100, ReuseDelay: 1000, AbnormalTime: 60},
		{Name: "Тренировочный удар", OperateType: "A1", TargetType: "ONE",
			ID: 7001, Level: 4, HitTime: 400, ReuseDelay: 4000, AbnormalTime: 60},
		// Маршрут 1: hitTime наследует последнее базовое значение таблицы,
		// reuseDelay перекрыт таблицей маршрута по подиндексу.
		{Name: "Тренировочный удар", OperateType: "A1", TargetType: "ONE",
			ID: 7001, Level: 101, HitTime: 400, ReuseDelay: 990, AbnormalTime: 60},
		{Name: "Тренировочный удар", OperateType: "A1", TargetType: "ONE",
			ID: 7001, Level: 130, HitTime: 400, ReuseDelay: 700, AbnormalTime: 60},
		// Маршрут 2: строковые литералы перекрывают operateType/isDebuff,
		// reuseDelay наследуется.
		{Name: "Тренировочный удар", OperateType: "A2", TargetType: "ONE",
			ID: 7001, Level: 141, HitTime: 400, ReuseDelay: 4000, AbnormalTime: 60, IsDebuff: true},
		{Name: "Тренировочный удар", OperateType: "A2", TargetType: "ONE",
			ID: 7001, Level: 170, HitTime: 400, ReuseDelay: 4000, AbnormalTime: 60, IsDebuff: true},
		// Дефолты канона: targetType отсутствует → SELF.
		{Name: "Пассивка новичка", OperateType: "P", TargetType: "SELF", ID: 7002, Level: 1},
		// isDebuff через таблицу.
		{Name: "Тренировочный дебафф", OperateType: "A2", TargetType: "ONE",
			ID: 7004, Level: 1, AbnormalTime: 120, IsMagic: 1},
		{Name: "Тренировочный дебафф", OperateType: "A2", TargetType: "ONE",
			ID: 7004, Level: 2, AbnormalTime: 120, IsMagic: 1, IsDebuff: true},
		{Name: "Тренировочный свиток", OperateType: "CA1", TargetType: "GROUND",
			ID: 7005, Level: 1, IsMagic: 3},
	}
	for _, w := range cases {
		got, ok := st.Skill(w.ID, w.Level)
		if !ok {
			t.Fatalf("Skill(%d, %d) не найден", w.ID, w.Level)
		}
		if got != w {
			t.Errorf("Skill(%d, %d) = %+v; want %+v", w.ID, w.Level, got, w)
		}
	}
}

// TestSkillAccess: каждый заявленный уровень достижим, вне диапазона — false
// (база 1..levels, маршруты 101+/141+, интервалы между маршрутами — промах).
func TestSkillAccess(t *testing.T) {
	st, _ := loadSynth(t)
	hit := []struct {
		id SkillID
		lv int32
	}{{7001, 1}, {7001, 4}, {7001, 101}, {7001, 130}, {7001, 141}, {7001, 170},
		{7002, 1}, {7003, 3}, {7004, 2}, {7005, 1}, {7100, 1}}
	for _, c := range hit {
		if _, ok := st.Skill(c.id, c.lv); !ok {
			t.Errorf("Skill(%d, %d) = ok=false; want true", c.id, c.lv)
		}
	}
	miss := []struct {
		id SkillID
		lv int32
	}{{7001, 0}, {7001, 5}, {7001, 100}, {7001, 131}, {7001, 140}, {7001, 171},
		{7002, 2}, {9999, 1}, {7001, -101}}
	for _, c := range miss {
		if _, ok := st.Skill(c.id, c.lv); ok {
			t.Errorf("Skill(%d, %d) = ok=true; want false", c.id, c.lv)
		}
	}
}

// TestSkillDefAccess: исходная форма — таблицы (без #), raw-bag всех прямых
// элементов в файловой форме, overrides, raw-деревья.
func TestSkillDefAccess(t *testing.T) {
	st, _ := loadSynth(t)
	def, ok := st.SkillDef(7001)
	if !ok {
		t.Fatalf("SkillDef(7001) не найден")
	}
	if def.ID != 7001 || def.Name != "Тренировочный удар" || def.Levels != 4 {
		t.Errorf("def шапка = %+v", def)
	}
	if len(def.Enchant) != 2 || def.Enchant[0].Route != 1 || def.Enchant[1].Route != 2 {
		t.Errorf("def.Enchant = %+v; want маршруты [1 2]", def.Enchant)
	}
	tbl, ok := def.Table("hit")
	if !ok || len(tbl) != 4 || tbl[0] != "100" || tbl[3] != "400" {
		t.Errorf("Table(\"hit\") = %v, %v; want 4 значения", tbl, ok)
	}
	if _, ok := def.Table("#hit"); ok {
		t.Errorf("Table(\"#hit\") = ok=true; want конвенцию без #")
	}
	if v, ok := def.Set("hitTime"); !ok || v != "#hit" {
		t.Errorf("Set(\"hitTime\") = %q, %v; want файловая форма \"#hit\"", v, ok)
	}
	if v, ok := def.Set("icon"); !ok || v != "icon.skill7001" {
		t.Errorf("Set(\"icon\") = %q, %v", v, ok)
	}
	if v, ok := def.EnchantValue(1, "reuseDelay"); !ok || v != "#ench1Reuse" {
		t.Errorf("EnchantValue(1, reuseDelay) = %q, %v", v, ok)
	}
	if v, ok := def.EnchantValue(2, "operateType"); !ok || v != "A2" {
		t.Errorf("EnchantValue(2, operateType) = %q, %v", v, ok)
	}
	if _, ok := def.EnchantValue(3, "x"); ok {
		t.Errorf("EnchantValue(3, x) = ok=true; want false")
	}
	raw := def.Raw()
	if len(raw) != 2 || raw[0].Name != "effects" || raw[1].Name != "enchant2effects" {
		t.Fatalf("def.Raw имена = %+v; want [effects enchant2effects]", raw)
	}
	eff := raw[0]
	if len(eff.Children) != 2 || eff.Children[0].Name != "TestDamage" || eff.Children[1].Name != "TestOverTime" {
		t.Fatalf("effects.Children = %+v", eff.Children)
	}
	if p := eff.Children[0].Children; len(p) != 1 || p[0].Name != "power" || p[0].Text != "10" {
		t.Errorf("TestDamage.Children = %+v; want power=10", p)
	}
	if a := eff.Children[1].Attrs; len(a) != 1 || a[0].Name != "tick" || a[0].Value != "2000" {
		t.Errorf("TestOverTime.Attrs = %+v; want tick=2000", a)
	}
	// Условия 7003: атрибуты отсортированы, вложенный player.
	def3, _ := st.SkillDef(7003)
	raw3 := def3.Raw()
	if len(raw3) != 1 || raw3[0].Name != "conditions" {
		t.Fatalf("7003.Raw = %+v; want [conditions]", raw3)
	}
	if a := raw3[0].Attrs; len(a) != 2 || a[0].Name != "addName" || a[1].Name != "msgId" {
		t.Errorf("conditions.Attrs = %+v; want отсортированные [addName msgId]", a)
	}
	if len(raw3[0].Children) != 1 || raw3[0].Children[0].Name != "player" {
		t.Errorf("conditions.Children = %+v", raw3[0].Children)
	}
}

// TestSkillCounters: счётчики категории на синтетике (состав фикстуры
// детерминирован генератором).
func TestSkillCounters(t *testing.T) {
	_, rep := loadSynth(t)
	if rep.Skills != 35 {
		t.Errorf("Skills = %d; want 35", rep.Skills)
	}
	if rep.SkillLevels != 1416 {
		t.Errorf("SkillLevels = %d; want 1416", rep.SkillLevels)
	}
	if rep.EnchantedSkills != 7 {
		t.Errorf("EnchantedSkills = %d; want 7", rep.EnchantedSkills)
	}
	if rep.SkillTables != 39 {
		t.Errorf("SkillTables = %d; want 39", rep.SkillTables)
	}
	if rep.MissingTargetType != 1 {
		t.Errorf("MissingTargetType = %d; want 1 (7002)", rep.MissingTargetType)
	}
	if rep.EmptyValues != 1 {
		t.Errorf("EmptyValues = %d; want 1 (7005 feed)", rep.EmptyValues)
	}
	if rep.OrphanEnchants != 0 || rep.NestedDirect != 0 || rep.DupTables != 0 {
		t.Errorf("OrphanEnchants=%d NestedDirect=%d DupTables=%d; want 0/0/0",
			rep.OrphanEnchants, rep.NestedDirect, rep.DupTables)
	}
}

// TestSkillItemLinks: предметные ссылки 7003 (таблица — свой предмет на
// уровень) и 7005 (литерал) разрешаются общим каркасом.
func TestSkillItemLinks(t *testing.T) {
	st, rep := loadSynth(t)
	_ = st
	if rep.HasErrors() {
		t.Fatalf("ошибки: %+v", rep.Errors)
	}
	xml := `<list>
	<skill id="9600" levels="2" name="Битая ссылка">
		<operateType>A1</operateType><targetType>SELF</targetType>
		<itemConsumeId>9999</itemConsumeId>
	</skill>
</list>`
	_, rep = loadSkillXML(t, xml)
	if !rep.HasErrors() {
		t.Fatal("битая itemConsumeId ссылка не поймана")
	}
	found := false
	for _, e := range rep.Errors {
		if e.Code == CodeLink && e.Category == "skills" && strings.Contains(e.Message, "9999") {
			found = true
		}
	}
	if !found {
		t.Errorf("нет записи link про предмет 9999: %+v", rep.Errors)
	}
}

// TestSkillEvil: каждая злость — отдельная детерминированная запись с местом.
func TestSkillEvil(t *testing.T) {
	base := func(body string) string {
		return `<list>` + body + `</list>`
	}
	skill := func(attrs string, body string) string {
		return `<skill ` + attrs + `>` + body + `</skill>`
	}
	tests := []struct {
		name string
		xml  string
		code string
		id   int64
		msg  string
	}{
		{"дубль id", base(skill(`id="9700" levels="1" name="A"`, `<operateType>P</operateType><targetType>SELF</targetType>`) +
			skill(`id="9700" levels="1" name="B"`, `<operateType>P</operateType><targetType>SELF</targetType>`)), CodeDupID, 9700, ""},
		{"levels 0", base(skill(`id="9701" levels="0" name="A"`, `<operateType>P</operateType>`)), CodeNumber, 9701, ""},
		{"levels минус", base(skill(`id="9702" levels="-1" name="A"`, `<operateType>P</operateType>`)), CodeNumber, 9702, ""},
		{"levels не число", base(skill(`id="9703" levels="много" name="A"`, `<operateType>P</operateType>`)), CodeNumber, 9703, ""},
		{"нет id", base(skill(`levels="1" name="A"`, `<operateType>P</operateType>`)), CodeAttr, 0, ""},
		{"нет name", base(skill(`id="9704" levels="1"`, `<operateType>P</operateType>`)), CodeAttr, 0, ""},
		{"нет levels", base(skill(`id="9705" name="A"`, `<operateType>P</operateType>`)), CodeAttr, 0, ""},
		{"нет операции", base(skill(`id="9706" levels="1" name="A"`, `<targetType>SELF</targetType>`)), CodeAttr, 9706, ""},
		{"таблица без решётки", base(skill(`id="9707" levels="1" name="A"`, `<table name="hit">1</table><operateType>P</operateType><targetType>SELF</targetType>`)), CodeAttr, 9707, ""},
		{"ссылка на отсутствующую таблицу", base(skill(`id="9708" levels="1" name="A"`, `<operateType>P</operateType><targetType>SELF</targetType><hitTime>#nope</hitTime>`)), CodeTable, 9708, "nope"},
		{"таблица короче levels", base(skill(`id="9709" levels="5" name="A"`, `<table name="#hit">1 2 3</table><operateType>A1</operateType><targetType>SELF</targetType><hitTime>#hit</hitTime>`)), CodeTable, 9709, "#hit"},
		{"энчант-таблица короче маршрута", base(skill(`id="9710" levels="1" name="A" enchantGroup1="1"`, `<table name="#e">1 2 3</table><operateType>A1</operateType><targetType>SELF</targetType><enchant1 name="hitTime">#e</enchant1>`)), CodeTable, 9710, "#e"},
		{"hitTime минус", base(skill(`id="9711" levels="1" name="A"`, `<operateType>A1</operateType><targetType>SELF</targetType><hitTime>-1</hitTime>`)), CodeNumber, 9711, ""},
		{"isDebuff не bool", base(skill(`id="9712" levels="1" name="A"`, `<operateType>A2</operateType><targetType>ONE</targetType><isDebuff>да</isDebuff>`)), CodeNumber, 9712, ""},
		{"isMagic не число", base(skill(`id="9713" levels="1" name="A"`, `<operateType>A2</operateType><targetType>ONE</targetType><isMagic>abc</isMagic>`)), CodeNumber, 9713, ""},
		{"enchantGroup9", base(skill(`id="9714" levels="1" name="A" enchantGroup9="1"`, `<operateType>P</operateType><targetType>SELF</targetType>`)), CodeAttr, 9714, ""},
		{"enchantGroup значение 0", base(skill(`id="9715" levels="1" name="A" enchantGroup1="0"`, `<operateType>P</operateType><targetType>SELF</targetType>`)), CodeAttr, 9715, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, rep := loadSkillXML(t, tt.xml)
			found := false
			for _, e := range rep.Errors {
				if e.Code == tt.code && e.ID == tt.id && (tt.msg == "" || strings.Contains(e.Message, tt.msg)) {
					found = true
				}
			}
			if !found {
				t.Errorf("нет записи %s (id=%d, msg~%q); отчёт: %+v", tt.code, tt.id, tt.msg, rep.Errors)
			}
		})
	}
}

// TestSkillCountersGreen: широта данных — счётчики, не ошибки: неизвестные
// enum-значения, отсутствие targetType, orphan-override, атрибуты, вложенный
// прямой элемент, дубль ключа/таблицы, таблица ровно levels (граница).
func TestSkillCountersGreen(t *testing.T) {
	xml := `<list>
	<skill id="9800" levels="1" name="Широта">
		<operateType>XX</operateType>
		<targetType>NOWHERE</targetType>
		<isMagic>9</isMagic>
		<fanRange>0,0,200,180</fanRange>
		<hitTime levelValues="1 2">100</hitTime>
	</skill>
	<skill id="9801" levels="1" name="Без цели">
		<operateType>P</operateType>
	</skill>
	<skill id="9802" levels="1" name="Сирота">
		<operateType>P</operateType><targetType>SELF</targetType>
		<enchant3 name="hitTime">5</enchant3>
	</skill>
	<skill id="9803" levels="1" name="Вложенный">
		<operateType>P</operateType><targetType>SELF</targetType>
		<hitTime><a>7</a></hitTime>
	</skill>
	<skill id="9804" levels="1" name="Дубли">
		<operateType>P</operateType><targetType>SELF</targetType>
		<hitTime>1</hitTime><hitTime>2</hitTime>
		<table name="#t">1</table><table name="#t">2</table>
	</skill>
	<skill id="9805" levels="3" name="Ровно">
		<table name="#hit">10 20 30</table>
		<operateType>A1</operateType><targetType>SELF</targetType>
		<hitTime>#hit</hitTime>
	</skill>
</list>`
	st, rep := loadSkillXML(t, xml)
	if rep.HasErrors() {
		t.Fatalf("счётчики не ошибки, а ошибки: %+v", rep.Errors)
	}
	if got := rep.UnknownTypes["skill.operateType.XX"]; got != 1 {
		t.Errorf("UnknownTypes[skill.operateType.XX] = %d; want 1", got)
	}
	if got := rep.UnknownTypes["skill.targetType.NOWHERE"]; got != 1 {
		t.Errorf("UnknownTypes[skill.targetType.NOWHERE] = %d; want 1", got)
	}
	if got := rep.UnknownTypes["skill.isMagic.9"]; got != 1 {
		t.Errorf("UnknownTypes[skill.isMagic.9] = %d; want 1", got)
	}
	if got := rep.UnknownKeys["skill.attr.levelValues"]; got != 1 {
		t.Errorf("UnknownKeys[skill.attr.levelValues] = %d; want 1", got)
	}
	if rep.MissingTargetType != 1 {
		t.Errorf("MissingTargetType = %d; want 1 (9801)", rep.MissingTargetType)
	}
	if rep.OrphanEnchants != 1 {
		t.Errorf("OrphanEnchants = %d; want 1 (9802)", rep.OrphanEnchants)
	}
	if rep.NestedDirect != 1 {
		t.Errorf("NestedDirect = %d; want 1 (9803)", rep.NestedDirect)
	}
	if rep.DupKeys != 1 || rep.DupTables != 1 {
		t.Errorf("DupKeys=%d DupTables=%d; want 1/1 (9804)", rep.DupKeys, rep.DupTables)
	}
	// Дубли: побеждает последний; вложенный текст конкатенируется.
	if v, _ := st.SkillDef(9804).Set("hitTime"); v != "2" {
		t.Errorf("Set(hitTime) 9804 = %q; want 2 (последний)", v)
	}
	if s, _ := st.Skill(9803, 1); s.HitTime != 7 {
		t.Errorf("HitTime 9803 = %d; want 7 (конкатенация)", s.HitTime)
	}
	if s, _ := st.Skill(9805, 3); s.HitTime != 30 {
		t.Errorf("HitTime 9805 L3 = %d; want 30 (таблица ровно levels)", s.HitTime)
	}
	if s, _ := st.Skill(9800, 1); s.TargetType != "NOWHERE" || s.OperateType != "XX" || s.IsMagic != 9 {
		t.Errorf("9800 = %+v; want значения широты как есть", s)
	}
}

// TestSkillOrderOfFile: применение значений в порядке файла — прямой элемент
// после override затирает его, override после прямого побеждает.
func TestSkillOrderOfFile(t *testing.T) {
	xml := `<list>
	<skill id="9810" levels="2" name="Прямой после" enchantGroup1="1">
		<table name="#e">50 51 52 53 54 55 56 57 58 59 60 61 62 63 64 65 66 67 68 69 70 71 72 73 74 75 76 77 78 79 80</table>
		<enchant1 name="hitTime">#e</enchant1>
		<hitTime>200</hitTime>
		<operateType>A1</operateType><targetType>SELF</targetType>
	</skill>
	<skill id="9811" levels="2" name="Override после" enchantGroup1="1">
		<table name="#e">50 51 52 53 54 55 56 57 58 59 60 61 62 63 64 65 66 67 68 69 70 71 72 73 74 75 76 77 78 79 80</table>
		<hitTime>200</hitTime>
		<enchant1 name="hitTime">#e</enchant1>
		<operateType>A1</operateType><targetType>SELF</targetType>
	</skill>
</list>`
	st, rep := loadSkillXML(t, xml)
	if rep.HasErrors() {
		t.Fatalf("ошибки: %+v", rep.Errors)
	}
	if s, _ := st.Skill(9810, 101); s.HitTime != 200 {
		t.Errorf("9810 L101 HitTime = %d; want 200 (прямой позже — побеждает)", s.HitTime)
	}
	if s, _ := st.Skill(9811, 101); s.HitTime != 50 {
		t.Errorf("9811 L101 HitTime = %d; want 50 (override позже — побеждает)", s.HitTime)
	}
	if s, _ := st.Skill(9811, 130); s.HitTime != 80 {
		t.Errorf("9811 L130 HitTime = %d; want 80 (подиндекс 29)", s.HitTime)
	}
}

// TestSkillItemLinkDedup: таблица одинаковых id не плодит копий ошибки.
func TestSkillItemLinkDedup(t *testing.T) {
	xml := `<list>
	<skill id="9820" levels="80" name="Один и тот же предмет">
		<table name="#itemConsumeId">` + strings.TrimSuffix(strings.Repeat("9998 ", 80), " ") + `</table>
		<operateType>A1</operateType><targetType>SELF</targetType>
		<itemConsumeId>#itemConsumeId</itemConsumeId>
	</skill>
</list>`
	_, rep := loadSkillXML(t, xml)
	n := 0
	for _, e := range rep.Errors {
		if e.Code == CodeLink && strings.Contains(e.Message, "9998") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("записей link про 9998 = %d; want 1 (дедуп по паре скилл-предмет)", n)
	}
}
