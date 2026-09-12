package data

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestSpawnGolden(t *testing.T) {
	st, rep := loadSynth(t)
	if rep.Spawns != 6 {
		t.Fatalf("Spawns = %d; want 6", rep.Spawns)
	}
	sp := st.Spawns()
	// Порядок детерминирован: путь файла, затем позиция.
	wantNPC := []NpcID{30080, 20550, 20551, 20550, 29019, 80000}
	for i, id := range wantNPC {
		if sp[i].NpcID != id {
			t.Errorf("spawns[%d].NpcID = %d; want %d (порядок файла)", i, sp[i].NpcID, id)
		}
	}
	// Точечный спавн с полным набором.
	if !sp[0].HasPoint || sp[0].Point != (Point{147456, 22576, -1989, 16384}) || sp[0].RespawnDelay != 60 {
		t.Errorf("spawns[0] = %+v; want точка 147456/22576/-1989/16384, respawn 60", sp[0])
	}
	// Дефолты: heading −1 и respawnDelay 0 у точки без атрибутов.
	if !sp[1].HasPoint || sp[1].Point.Heading != -1 || sp[1].RespawnDelay != 0 || sp[1].Count != 1 {
		t.Errorf("spawns[1] = %+v; want heading -1 (дефолт канона), respawn 0, count 1", sp[1])
	}
	// Территориальный спавн.
	if sp[2].HasPoint || sp[2].Territory != "synth_territory" || sp[2].Count != 3 || sp[2].RespawnDelay != 22 {
		t.Errorf("spawns[2] = %+v; want территория synth_territory, count 3, respawn 22", sp[2])
	}
	// Точка и территория одновременно: данные сохраняют обе.
	if !sp[3].HasPoint || sp[3].Territory != "synth_both" || sp[3].Count != 2 {
		t.Errorf("spawns[3] = %+v; want точка И территория synth_both, count 2", sp[3])
	}
	if v, ok := sp[3].Set("respawnRandom"); !ok || v != "5" {
		t.Errorf("spawns[3].Set(respawnRandom) = (%q,%v); want (5,true)", v, ok)
	}
	if v, ok := sp[3].Set("chaseRange"); !ok || v != "2000" {
		t.Errorf("spawns[3].Set(chaseRange) = (%q,%v); want (2000,true)", v, ok)
	}
	if v, ok := sp[4].Set("periodOfDay"); !ok || v != "day" {
		t.Errorf("spawns[4].Set(periodOfDay) = (%q,%v); want (day,true) — 29019 day-спавн", v, ok)
	}
	// Территории.
	ter, ok := st.Territory("synth_territory")
	if !ok {
		t.Fatal("территория synth_territory отсутствует")
	}
	if ter.MinZ != -3800 || ter.MaxZ != -3400 || len(ter.Nodes) != 3 || ter.Nodes[0] != [2]int32{70780, 125060} {
		t.Errorf("Territory(synth_territory) = minZ=%d maxZ=%d nodes=%v; want -3800/-3400, 3 узла", ter.MinZ, ter.MaxZ, ter.Nodes)
	}
	both, ok := st.Territory("synth_both")
	if !ok {
		t.Fatal("территория synth_both отсутствует")
	}
	if len(both.Banned) != 1 || both.Banned[0].MinZ != -50 || len(both.Banned[0].Nodes) != 2 {
		t.Errorf("synth_both.Banned = %+v; want одна запрещённая территория", both.Banned)
	}
	if _, ok := st.Territory("нет такой"); ok {
		t.Errorf("несуществующая территория найдена")
	}
}

func TestSpawnCounters(t *testing.T) {
	_, rep := loadSynth(t)
	if rep.Territories != 2 || rep.Spawns != 6 {
		t.Errorf("Territories=%d Spawns=%d; want 2/6", rep.Territories, rep.Spawns)
	}
	if rep.NamedBlocks != 2 || rep.TerrOwnName != 1 || rep.DisabledFiles != 1 {
		t.Errorf("NamedBlocks=%d TerrOwnName=%d DisabledFiles=%d; want 2/1/1",
			rep.NamedBlocks, rep.TerrOwnName, rep.DisabledFiles)
	}
	if rep.WithoutHeading != 1 || rep.WithoutRespawnDelay != 1 {
		t.Errorf("WithoutHeading=%d WithoutRespawnDelay=%d; want 1/1", rep.WithoutHeading, rep.WithoutRespawnDelay)
	}
	if rep.FakePlayersSkipped != 1 {
		t.Errorf("FakePlayersSkipped = %d; want 1 (npc 80000 без определения)", rep.FakePlayersSkipped)
	}
	if got := rep.UnknownKeys["spawn.key.periodOfDay"]; got != 1 {
		t.Errorf("UnknownKeys[spawn.key.periodOfDay] = %d; want 1", got)
	}
	if rep.Files != 3 {
		t.Errorf("Files = %d; want 3 (items+npcs+spawns; off.xml отключён)", rep.Files)
	}
}

// TestSpawnEvil: злые спавны — каждая злость отдельной записью с местом.
func TestSpawnEvil(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantCode string
	}{
		{"list без enabled", "<list><spawn name=\"a\"><npc id=\"1\" x=\"1\" y=\"2\" z=\"3\"/></spawn></list>", CodeAttr},
		{"list с кривым enabled", "<list enabled=\"junk\"><spawn/></list>", CodeAttr},
		{"npc без точки и территории", "<list enabled=\"true\"><spawn name=\"a\"><npc id=\"20550\"/></spawn></list>", CodeAttr},
		{"точка без y", "<list enabled=\"true\"><spawn name=\"a\"><npc id=\"20550\" x=\"1\" z=\"3\"/></spawn></list>", CodeAttr},
		{"территория без minZ", "<list enabled=\"true\"><spawn zone=\"z2\"><territory maxZ=\"1\"><node x=\"1\" y=\"1\"/></territory><npc id=\"20550\" count=\"1\"/></spawn></list>", CodeAttr},
		{"узел без x", "<list enabled=\"true\"><spawn zone=\"z3\"><territory minZ=\"0\" maxZ=\"1\"><node y=\"1\"/></territory><npc id=\"20550\" count=\"1\"/></spawn></list>", CodeAttr},
		{"дубликат имени территории", "<list enabled=\"true\"><spawn zone=\"dup\"><territory minZ=\"0\" maxZ=\"1\"><node x=\"1\" y=\"1\"/></territory><npc id=\"20550\" count=\"1\"/></spawn><spawn zone=\"dup\"><territory minZ=\"0\" maxZ=\"1\"><node x=\"2\" y=\"2\"/></territory><npc id=\"20550\" count=\"1\"/></spawn></list>", CodeDupID},
		{"count ноль", "<list enabled=\"true\"><spawn name=\"a\"><npc id=\"20550\" x=\"1\" y=\"2\" z=\"3\" count=\"0\"/></spawn></list>", CodeNumber},
		{"кривой respawnDelay", "<list enabled=\"true\"><spawn name=\"a\"><npc id=\"20550\" x=\"1\" y=\"2\" z=\"3\" respawnDelay=\"скоро\"/></spawn></list>", CodeNumber},
		{"кривой periodOfDay", "<list enabled=\"true\"><spawn name=\"a\"><npc id=\"20550\" x=\"1\" y=\"2\" z=\"3\" periodOfDay=\"утро\"/></spawn></list>", CodeAttr},
		{"обрыв XML", "<list enabled=\"true\"><spawn zone=\"z\"><terri", CodeXML},
		{"чужой корень", "<spawns/>", CodeRoot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := catFS(map[string]*fstest.MapFile{"spawns/x/x.xml": {Data: []byte(tt.content)}})
			st, rep, err := Load(fsys)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if st == nil || rep == nil {
				t.Fatal("Load вернул nil")
			}
			found := false
			for _, e := range rep.Errors {
				if e.Code == tt.wantCode {
					found = true
					if e.File == "" || e.Line <= 0 || e.Category != "spawns" {
						t.Errorf("запись без места или категории: %+v", e)
					}
				}
			}
			if !found {
				t.Errorf("нет записи %q; записи: %+v", tt.wantCode, rep.Errors)
			}
		})
	}
}

// TestSpawnOrderDeterministic: две загрузки дают одинаковую
// последовательность (структуры с map несравнимы напрямую — сверяем поля).
func TestSpawnOrderDeterministic(t *testing.T) {
	st1, _ := loadSynth(t)
	st2, _ := loadSynth(t)
	for i, s := range st1.Spawns() {
		o := st2.Spawns()[i]
		if s.NpcID != o.NpcID || s.HasPoint != o.HasPoint || s.Point != o.Point ||
			s.Territory != o.Territory || s.Count != o.Count || s.RespawnDelay != o.RespawnDelay ||
			dumpSetsForTest(s.set) != dumpSetsForTest(o.set) {
			t.Errorf("spawns[%d] недетерминирован: %+v vs %+v", i, s, o)
		}
	}
}

func dumpSetsForTest(set map[string]string) string {
	var sb strings.Builder
	writeSets(&sb, set)
	return sb.String()
}

// TestDeepNestingNoPanic: вложенность не рекурсивна по стеку — глубокое
// поддерево пропускается со счётчиком, не роняя загрузку.
func TestDeepNestingNoPanic(t *testing.T) {
	deep := "<list><npc id=\"22000\" type=\"Monster\" name=\"n\">" +
		strings.Repeat("<a>", 50000) + strings.Repeat("</a>", 50000) + "</npc></list>"
	fsys := catFS(map[string]*fstest.MapFile{"stats/npcs/deep.xml": {Data: []byte(deep)}})
	st, rep, err := Load(fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if st == nil || rep == nil {
		t.Fatal("Load вернул nil")
	}
	if rep.DeepSkips == 0 {
		t.Errorf("DeepSkips = 0; want >0 (элементы глубже потолка пути)")
	}
}

// TestSpawnAIData: листья AIData — raw-bag записей блока, по порядку
// документа (семантика канона: параметры собираются при обходе, запись
// читает их в момент своего разбора). Территориальные shape/rad — bag.
func TestSpawnAIData(t *testing.T) {
	content := `<list enabled="true">
	<spawn zone="ai_zone">
		<territory minZ="-10" maxZ="10" shape="NPoly" rad="500">
			<node x="5" y="5"/>
			<node x="6" y="6"/>
			<node x="7" y="7"/>
		</territory>
		<npc id="20550" count="1" respawnDelay="15"/>
		<AIData>
			<aggroRange>400</aggroRange>
			<disableRandomWalk>true</disableRandomWalk>
		</AIData>
		<npc id="29019" x="1" y="2" z="3" heading="7" respawnDelay="5"/>
	</spawn>
</list>`
	fsys := catFS(map[string]*fstest.MapFile{
		"stats/npcs/a.xml": {Data: []byte("<list>" + npcBody(20550) + npcBody(29019) + "</list>")},
		"spawns/a.xml":     {Data: []byte(content)},
	})
	st, rep, err := Load(fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep.HasErrors() {
		t.Fatalf("AIData/shape/rad — широта, не ошибки: %+v", rep.Errors)
	}
	sp := st.Spawns()
	if len(sp) != 2 {
		t.Fatalf("Spawns = %d; want 2", len(sp))
	}
	// Запись до AIData — листьев не получает; после — получает.
	if _, ok := sp[0].Set("aidata.disableRandomWalk"); ok {
		t.Errorf("запись до AIData получила листья; порядок документа нарушен")
	}
	for key, want := range map[string]string{"aidata.aggroRange": "400", "aidata.disableRandomWalk": "true"} {
		if v, ok := sp[1].Set(key); !ok || v != want {
			t.Errorf("Set(%q) = (%q, %v); want (%q, true)", key, v, ok, want)
		}
	}
	ter, _ := st.Territory("ai_zone")
	for key, want := range map[string]string{"terr.shape": "NPoly", "terr.rad": "500"} {
		if v, ok := ter.Set(key); !ok || v != want {
			t.Errorf("Territory.Set(%q) = (%q, %v); want (%q, true)", key, v, ok, want)
		}
	}
}

// TestPeriodOfDayCaseInsensitive: канон сравнивает без учёта регистра.
func TestPeriodOfDayCaseInsensitive(t *testing.T) {
	content := `<list enabled="true"><spawn name="a">` +
		`<npc id="20550" x="1" y="2" z="3" respawnDelay="1" periodOfDay="Day"/>` +
		`</spawn></list>`
	fsys := catFS(map[string]*fstest.MapFile{
		"stats/npcs/a.xml": {Data: []byte("<list>" + npcBody(20550) + "</list>")},
		"spawns/a.xml":     {Data: []byte(content)},
	})
	_, rep, err := Load(fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep.HasErrors() {
		t.Fatalf("periodOfDay=Day должен приниматься (equalsIgnoreCase канона): %+v", rep.Errors)
	}
}

// TestDeepNestingSpawnsNoPanic: спавн-ветка разбора тоже итеративна.
func TestDeepNestingSpawnsNoPanic(t *testing.T) {
	deep := `<list enabled="true"><spawn zone="z"><territory minZ="0" maxZ="1">` +
		strings.Repeat("<a>", 50000) + strings.Repeat("</a>", 50000) +
		`</territory></spawn></list>`
	fsys := catFS(map[string]*fstest.MapFile{"spawns/deep.xml": {Data: []byte(deep)}})
	st, rep, err := Load(fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if st == nil || rep == nil {
		t.Fatal("Load вернул nil")
	}
}

// TestXMLTruncationSingleEntry: обрыв XML внутри блока/территории — ровно
// одна запись об ошибке (файловый цикл), усечённая территория не
// регистрируется.
func TestXMLTruncationSingleEntry(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		truncTerr bool // обрыв произошёл внутри территории
	}{
		{"обрыв внутри территории", `<list enabled="true"><spawn zone="z"><territory minZ="0" maxZ="1"><node x="1" y="1"/><node x="2"`, true},
		{"обрыв внутри блока", `<list enabled="true"><spawn zone="z"><territory minZ="0" maxZ="1"><node x="1" y="1"/></territory><npc id="20550" count="1" resp`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := catFS(map[string]*fstest.MapFile{"spawns/x/x.xml": {Data: []byte(tt.content)}})
			st, rep, err := Load(fsys)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			count := 0
			for _, e := range rep.Errors {
				if e.Code == CodeXML {
					count++
				}
			}
			if count != 1 {
				t.Errorf("записей CodeXML = %d; want 1: %+v", count, rep.Errors)
			}
			if tt.truncTerr {
				if _, ok := st.Territory("z"); ok {
					t.Errorf("усечённая территория зарегистрирована")
				}
			}
		})
	}
}
