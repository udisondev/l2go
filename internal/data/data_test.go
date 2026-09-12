package data

import (
	"os"
	"testing"
	"testing/fstest"
)

func TestLoadGolden(t *testing.T) {
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
	if rep.Files != 1 {
		t.Errorf("rep.Files = %d; want 1", rep.Files)
	}
	if rep.Items != 5 {
		t.Errorf("rep.Items = %d; want 5", rep.Items)
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
	if len(st.Items) != len(want) {
		t.Fatalf("len(st.Items) = %d; want %d", len(st.Items), len(want))
	}
	for id, w := range want {
		got := st.Items[id]
		if got.ID != w.ID || got.Name != w.Name || got.Type != w.Type ||
			got.Weight != w.Weight || got.Price != w.Price || got.Stackable != w.Stackable ||
			got.CrystalType != w.CrystalType || got.CrystalCount != w.CrystalCount ||
			got.Material != w.Material || got.BodyPart != w.BodyPart {
			t.Errorf("item %d = %+v; want %+v", id, got, w)
		}
	}
	// Дефолты — это семантика канона: 9002 без material → STEEL, 9004 без weight → 0.
	if got := st.Items[9002].Material; got != "STEEL" {
		t.Errorf("Material(9002) = %q; want STEEL (дефолт канона)", got)
	}
	if got := st.Items[9004].Weight; got != 0 {
		t.Errorf("Weight(9004) = %d; want 0 (дефолт канона)", got)
	}
}

func TestRawBag(t *testing.T) {
	st, _, err := Load(os.DirFS("testdata/synth"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if st == nil {
		t.Fatal("Static nil")
	}
	it := st.Items[9001]
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

func TestDumpDeterministic(t *testing.T) {
	st1, _, err := Load(os.DirFS("testdata/synth"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	st2, _, err := Load(os.DirFS("testdata/synth"))
	if err != nil {
		t.Fatalf("Load (второй прогон): %v", err)
	}
	if st1.Dump() != st2.Dump() {
		t.Errorf("Dump недетерминирован:\n%s\n---\n%s", st1.Dump(), st2.Dump())
	}
	if want := "id=9001"; !contains(st1.Dump(), want) {
		t.Errorf("Dump не содержит %q:\n%s", want, st1.Dump())
	}
	if i, j := indexOf(st1.Dump(), "id=9001"), indexOf(st1.Dump(), "id=9002"); i > j {
		t.Errorf("Dump не отсортирован по ID: 9001 (инд %d) после 9002 (инд %d)", i, j)
	}
}

func contains(s, sub string) bool { return indexOf(s, sub) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestManifestDeterministicAndSensitive(t *testing.T) {
	fsys := fstest.MapFS{
		"stats/items/a.xml": &fstest.MapFile{Data: []byte(itemXML(9100))},
		"stats/items/b.xml": &fstest.MapFile{Data: []byte(itemXML(9101))},
	}
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

	fsysMod := fstest.MapFS{
		"stats/items/a.xml": &fstest.MapFile{Data: []byte(itemXML(9100))},
		"stats/items/b.xml": &fstest.MapFile{Data: []byte(itemXML(9102))}, // изменён состав
	}
	_, r3, err := Load(fsysMod)
	if err != nil {
		t.Fatalf("Load (изменённый состав): %v", err)
	}
	if r1.Manifest == r3.Manifest {
		t.Errorf("манифест нечувствителен к изменению содержимого файла")
	}
}

func TestCustomDirSkipped(t *testing.T) {
	fsys := fstest.MapFS{
		"stats/items/a.xml":             &fstest.MapFile{Data: []byte(itemXML(9200))},
		"stats/items/custom/c.xml":      &fstest.MapFile{Data: []byte(itemXML(9201))},
		"stats/items/custom/d.xml":      &fstest.MapFile{Data: []byte(itemXML(9202))},
		"stats/items/documentation.txt": &fstest.MapFile{Data: []byte("справка")},
	}
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
	if len(st.Items) != 1 {
		t.Errorf("len(st.Items) = %d; want 1 (только верхний уровень)", len(st.Items))
	}
	if rep.SkippedCustomDir != 2 {
		t.Errorf("SkippedCustomDir = %d; want 2", rep.SkippedCustomDir)
	}
	if rep.Files != 1 {
		t.Errorf("Files = %d; want 1 (documentation.txt не XML, custom не читан)", rep.Files)
	}
	// Манифест покрывает только фактически прочитанное: custom не входит.
	base := fstest.MapFS{"stats/items/a.xml": &fstest.MapFile{Data: []byte(itemXML(9200))}}
	_, rb, err := Load(base)
	if err != nil {
		t.Fatalf("Load (без custom): %v", err)
	}
	if rep.Manifest != rb.Manifest {
		t.Errorf("манифест чувствителен к custom, который не читается")
	}
}

func itemXML(id int) string {
	return "<list><item id=\"" + itoa(id) + "\" type=\"EtcItem\" name=\"n\"><set name=\"weight\" val=\"1\"/></item></list>"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
