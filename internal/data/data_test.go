package data

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// catFS собирает FS поверх файлов с обязательными каталогами категорий
// (stats/npcs, spawns — пустые), чтобы MapFS-фикстуры отдельных категорий
// не падали фатально на чужом отсутствующем каталоге.
func catFS(files map[string]*fstest.MapFile) fstest.MapFS {
	out := fstest.MapFS{
		"stats/npcs/.keep": &fstest.MapFile{},
		"spawns/.keep":     &fstest.MapFile{},
	}
	for k, v := range files {
		out[k] = v
	}
	return out
}

func loadSynth(t *testing.T) (*Static, *Report) {
	t.Helper()
	st, rep, err := Load(os.DirFS("testdata/synth"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep == nil || st == nil {
		t.Fatalf("Load вернул nil: st=%v rep=%v", st, rep)
	}
	if rep.HasErrors() {
		t.Fatalf("ошибки в чистой синтетике: %+v", rep.Errors)
	}
	return st, rep
}

func TestLoadGolden(t *testing.T) {
	st, rep := loadSynth(t)
	if rep.Items != 5 {
		t.Errorf("Items = %d; want 5", rep.Items)
	}
	want := map[ItemID]Item{
		9001: {ID: 9001, Name: "Тренировочный клинок", Type: "Weapon",
			Weight: 100, Price: 10, Stackable: false,
			CrystalType: "NONE", CrystalCount: 0, Material: "WOOD", BodyPart: "rhand"},
		9002: {ID: 9002, Name: "Учебная куртка", Type: "Armor",
			Weight: 250, Price: 0, Stackable: false,
			CrystalType: "D", CrystalCount: 15, Material: "STEEL", BodyPart: "chest"},
		9003: {ID: 9003, Name: "Горсть пыли", Type: "EtcItem",
			Weight: 2, Price: 3, Stackable: true,
			CrystalType: "NONE", CrystalCount: 0, Material: "STEEL", BodyPart: "none"},
		9004: {ID: 9004, Name: "Ржавая стрела", Type: "EtcItem",
			Weight: 0, Price: 0, Stackable: true,
			CrystalType: "NONE", CrystalCount: 0, Material: "STEEL", BodyPart: "lrhand"},
		9005: {ID: 9005, Name: "Клинок без значка", Type: "Weapon",
			Weight: 120, Price: 0, Stackable: false,
			CrystalType: "NONE", CrystalCount: 0, Material: "STEEL", BodyPart: "none"},
	}
	for id, w := range want {
		got, ok := st.Item(id)
		if !ok {
			t.Fatalf("предмет %d отсутствует", id)
		}
		if got.ID != w.ID || got.Name != w.Name || got.Type != w.Type ||
			got.Weight != w.Weight || got.Price != w.Price || got.Stackable != w.Stackable ||
			got.CrystalType != w.CrystalType || got.CrystalCount != w.CrystalCount ||
			got.Material != w.Material || got.BodyPart != w.BodyPart {
			t.Errorf("item %d = %+v; want %+v", id, got, w)
		}
	}
	// Дефолты — семантика канона: 9002 без material → STEEL, 9004 без weight → 0.
	if got, _ := st.Item(9002); got.Material != "STEEL" {
		t.Errorf("Material(9002) = %q; want STEEL (дефолт канона)", got.Material)
	}
	if got, _ := st.Item(9004); got.Weight != 0 {
		t.Errorf("Weight(9004) = %d; want 0 (дефолт канона)", got.Weight)
	}
}

func TestRawBag(t *testing.T) {
	st, _ := loadSynth(t)
	it, _ := st.Item(9001)
	for key, want := range map[string]string{
		"weapon_type": "SWORD", // ключ вне типизированного словаря — в bag как есть
		"weight":      "100",   // типизированный ключ тоже остаётся в bag
		"stat.pAtk":   "8",     // статы блока stats — с префиксом stat.
		"stat.mAtk":   "6",
	} {
		if v, ok := it.Set(key); !ok || v != want {
			t.Errorf("Set(%q) = (%q, %v); want (%q, true)", key, v, ok, want)
		}
	}
	if _, ok := it.Set("stat.pDef"); ok {
		t.Errorf("Set(stat.pDef) лишний: стата нет в данных")
	}
}

func TestLoadCounters(t *testing.T) {
	_, rep, err := Load(os.DirFS("testdata/synth"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for key, want := range map[string]int{"weapon_type": 1, "soulshots": 1, "armor_type": 1, "mystery_flag": 1, "icon": 1} {
		if got := rep.UnknownKeys[key]; got != want {
			t.Errorf("UnknownKeys[%q] = %d; want %d", key, got, want)
		}
	}
	if rep.EmptyValues != 1 {
		t.Errorf("EmptyValues = %d; want 1 (пустой icon у 9005)", rep.EmptyValues)
	}
	if rep.DupKeys != 1 {
		t.Errorf("DupKeys = %d; want 1 (вес 9005 задан дважды, победил последний)", rep.DupKeys)
	}
}

func TestTrimSemantics(t *testing.T) {
	content := "<list><item id=\"9600\" type=\"EtcItem\" name=\"С пробелами\">" +
		"<set name=\"weight\" val=\" 100 \"/>" +
		"<set name=\"price\">\n\t200\n</set>" +
		"<stats><stat type=\"pAtk\"> 8 </stat></stats>" +
		"</item></list>"
	fsys := catFS(map[string]*fstest.MapFile{"stats/items/x.xml": {Data: []byte(content)}})
	st, rep, err := Load(fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep.HasErrors() {
		t.Fatalf("trim — семантика канона, не ошибка: %+v", rep.Errors)
	}
	it, _ := st.Item(9600)
	if it.Weight != 100 || it.Price != 200 {
		t.Errorf("после trim: weight=%d price=%d; want 100, 200", it.Weight, it.Price)
	}
	if v, _ := it.Set("stat.pAtk"); v != "8" {
		t.Errorf("stat.pAtk = %q; want 8 (trim)", v)
	}
}

func TestDumpDeterministic(t *testing.T) {
	st1, _ := loadSynth(t)
	st2, _ := loadSynth(t)
	if st1.Dump() != st2.Dump() {
		t.Errorf("Dump недетерминирован:\n%s\n---\n%s", st1.Dump(), st2.Dump())
	}
	if !strings.Contains(st1.Dump(), "id=9001") {
		t.Errorf("Dump не содержит id=9001:\n%s", st1.Dump())
	}
	if i, j := strings.Index(st1.Dump(), "id=9001"), strings.Index(st1.Dump(), "id=9002"); i > j {
		t.Errorf("Dump не отсортирован по ID: 9001 (инд %d) после 9002 (инд %d)", i, j)
	}
}

// TestDumpGoldenItems фиксирует форму канонического дампа предметов: смена
// формата — сознательная правка эталона, тихий дрейф исключён.
func TestDumpGoldenItems(t *testing.T) {
	st, _ := loadSynth(t)
	got := st.Dump()
	// Вырезаем секцию предметов (до первой строки npc/terr/spawn).
	end := strings.Index(got, "npc id=")
	if end < 0 {
		end = len(got)
	}
	items := got[:end]
	want := `id=9001 name="Тренировочный клинок" type="Weapon" weight=100 price=10 stackable=false crystal_type="NONE" crystal_count=0 material="WOOD" bodypart="rhand" sets=[bodypart=rhand material=WOOD price=10 soulshots=1 stat.mAtk=6 stat.pAtk=8 weapon_type=SWORD weight=100]
id=9002 name="Учебная куртка" type="Armor" weight=250 price=0 stackable=false crystal_type="D" crystal_count=15 material="STEEL" bodypart="chest" sets=[armor_type=LIGHT bodypart=chest crystal_count=15 crystal_type=D weight=250]
id=9003 name="Горсть пыли" type="EtcItem" weight=2 price=3 stackable=true crystal_type="NONE" crystal_count=0 material="STEEL" bodypart="none" sets=[is_stackable=true mystery_flag=7 price=3 weight=2]
id=9004 name="Ржавая стрела" type="EtcItem" weight=0 price=0 stackable=true crystal_type="NONE" crystal_count=0 material="STEEL" bodypart="lrhand" sets=[bodypart=lrhand is_stackable=true]
id=9005 name="Клинок без значка" type="Weapon" weight=120 price=0 stackable=false crystal_type="NONE" crystal_count=0 material="STEEL" bodypart="none" sets=[weight=120]
`
	if items != want {
		t.Errorf("секция предметов отлична от эталона:\n---got---\n%s---want---\n%s", items, want)
	}
}

// TestDumpContainsCategories: канонический дамп покрывает все категории —
// NPC (с дроплистами и миньонами), территории, спавны.
func TestDumpContainsCategories(t *testing.T) {
	st, _ := loadSynth(t)
	dump := st.Dump()
	for _, want := range []string{
		"npc id=20550", "minions=[", "drops=[", "terr name=\"synth_territory\"", "spawn npc=",
	} {
		if !strings.Contains(dump, want) {
			t.Errorf("Dump не содержит %q:\n%s", want, dump)
		}
	}
}

func TestManifestDeterministicAndSensitive(t *testing.T) {
	fsys := catFS(map[string]*fstest.MapFile{
		"stats/items/a.xml": {Data: []byte(itemXML(9100))},
		"stats/items/b.xml": {Data: []byte(itemXML(9101))},
	})
	_, r1, err := Load(fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, r2, err := Load(fsys)
	if err != nil {
		t.Fatalf("Load (второй прогон): %v", err)
	}
	if r1.Manifest != r2.Manifest {
		t.Errorf("манифест недетерминирован: %x != %x", r1.Manifest, r2.Manifest)
	}

	fsysMod := catFS(map[string]*fstest.MapFile{
		"stats/items/a.xml": {Data: []byte(itemXML(9100))},
		"stats/items/b.xml": {Data: []byte(itemXML(9102))}, // изменён состав
	})
	_, r3, err := Load(fsysMod)
	if err != nil {
		t.Fatalf("Load (изменённый состав): %v", err)
	}
	if r1.Manifest == r3.Manifest {
		t.Errorf("манифест нечувствителен к изменению содержимого файла")
	}
}

func TestCustomDirSkipped(t *testing.T) {
	fsys := catFS(map[string]*fstest.MapFile{
		"stats/items/a.xml":             {Data: []byte(itemXML(9200))},
		"stats/items/custom/c.xml":      {Data: []byte(itemXML(9201))},
		"stats/items/custom/d.xml":      {Data: []byte(itemXML(9202))},
		"stats/items/documentation.txt": {Data: []byte("справка")},
	})
	st, rep, err := Load(fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep == nil || st == nil {
		t.Fatalf("Load вернул nil: st=%v rep=%v", st, rep)
	}
	if rep.HasErrors() {
		t.Fatalf("ошибки при пропуске custom: %+v", rep.Errors)
	}
	if rep.Items != 1 {
		t.Errorf("Items = %d; want 1 (только верхний уровень)", rep.Items)
	}
	if got := rep.SkippedDirs["custom"]; got != 2 {
		t.Errorf("SkippedDirs[custom] = %d; want 2", got)
	}
	if rep.Files != 1 {
		t.Errorf("Files = %d; want 1 (documentation.txt не XML, custom не читан)", rep.Files)
	}
	// Манифест покрывает только фактически прочитанное: custom не входит.
	base := catFS(map[string]*fstest.MapFile{"stats/items/a.xml": {Data: []byte(itemXML(9200))}})
	_, rb, err := Load(base)
	if err != nil {
		t.Fatalf("Load (без custom): %v", err)
	}
	if rep.Manifest != rb.Manifest {
		t.Errorf("манифест чувствителен к custom, который не читается")
	}
}

func TestFatalFS(t *testing.T) {
	st, rep, err := Load(fstest.MapFS{})
	if err == nil {
		t.Fatal("отсутствие stats/items — фатальная FS-ошибка")
	}
	if st != nil || rep != nil {
		t.Errorf("при фатальной ошибке st=%v rep=%v; want nil, nil", st, rep)
	}
}

func itemXML(id int) string {
	return "<list><item id=\"" + strconv.Itoa(id) + "\" type=\"EtcItem\" name=\"n\"><set name=\"weight\" val=\"1\"/></item></list>"
}
