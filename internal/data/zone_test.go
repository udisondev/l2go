package data

import (
	"strings"
	"testing"
	"testing/fstest"
)

// zoneByName — первая зона с данным именем в порядке загрузки (имя — не
// ключ, дубликаты допустимы).
func zoneByName(t *testing.T, st *Static, name string) *Zone {
	t.Helper()
	for i := range st.Zones() {
		if st.Zones()[i].Name == name {
			zn := st.Zones()[i]
			return &zn
		}
	}
	t.Fatalf("зона %q отсутствует", name)
	return nil
}

func TestZoneGolden(t *testing.T) {
	st, rep := loadSynth(t)
	if rep.Zones != 7 {
		t.Fatalf("Zones = %d; want 7 (6 в zones.xml + 1 в second.xml)", rep.Zones)
	}
	if rep.ZoneSpawns != 2 || rep.ZoneRacePoints != 1 {
		t.Errorf("ZoneSpawns = %d, ZoneRacePoints = %d; want 2, 1", rep.ZoneSpawns, rep.ZoneRacePoints)
	}
	if rep.ZonesWithID != 1 {
		t.Errorf("ZonesWithID = %d; want 1", rep.ZonesWithID)
	}
	if rep.DupZoneNames != 1 {
		t.Errorf("DupZoneNames = %d; want 1 (имя synth_npoly дважды)", rep.DupZoneNames)
	}
	if rep.MinZOverMaxZ != 1 {
		t.Errorf("MinZOverMaxZ = %d; want 1 (synth_npoly 500>100)", rep.MinZOverMaxZ)
	}
	if rep.MinEqMaxZ != 1 {
		t.Errorf("MinEqMaxZ = %d; want 1 (synth_respawn 50==50)", rep.MinEqMaxZ)
	}
	if rep.DupKeys < 1 {
		t.Errorf("DupKeys = %d; want >= 1 (повтор stat NoLanding)", rep.DupKeys)
	}
	if rep.UnknownTypes["zone.type.MysteryZone"] != 1 {
		t.Errorf("UnknownTypes[zone.type.MysteryZone] = %d; want 1", rep.UnknownTypes["zone.type.MysteryZone"])
	}
	if rep.UnknownTypes["zone.shape.Octagon"] != 1 {
		t.Errorf("UnknownTypes[zone.shape.Octagon] = %d; want 1", rep.UnknownTypes["zone.shape.Octagon"])
	}

	// Порядок загрузки: файлы лексикографически, позиции в файле.
	var names []string
	for _, zn := range st.Zones() {
		names = append(names, zn.Name)
	}
	wantOrder := "synth_cuboid,synth_npoly,synth_cylinder,synth_npoly,synth_respawn,synth_unknown,synth_noenabled"
	if got := strings.Join(names, ","); got != wantOrder {
		t.Errorf("порядок зон = %q; want %q", got, wantOrder)
	}

	cu := zoneByName(t, st, "synth_cuboid")
	if !cu.HasID || cu.ID != 70001 || cu.Type != "PeaceZone" || cu.ShapeKind != ShapeCuboid {
		t.Errorf("synth_cuboid = %+v; want id 70001, PeaceZone, Cuboid", cu)
	}
	if cu.MinZ != -1000 || cu.MaxZ != -500 {
		t.Errorf("synth_cuboid minZ/maxZ = %d/%d; want исходные -1000/-500", cu.MinZ, cu.MaxZ)
	}
	if v, ok := cu.Set("zone.stat.NoBookmark"); !ok || v != "true" {
		t.Errorf("Set(zone.stat.NoBookmark) = %q,%t; want \"true\",true", v, ok)
	}
	np := zoneByName(t, st, "synth_npoly")
	if np.ZLo != 100 || np.ZHi != 500 {
		t.Errorf("synth_npoly нормализация z = %d..%d; want 100..500 (minZ>maxZ в файле)", np.ZLo, np.ZHi)
	}
	if v, ok := np.Set("zone.stat.NoLanding"); !ok || v != "false" {
		t.Errorf("Set(zone.stat.NoLanding) = %q,%t; want \"false\" (последний побеждает)", v, ok)
	}
	if np.MinX != 95000 || np.MaxX != 115000 || np.MinY != 100000 || np.MaxY != 120000 {
		t.Errorf("synth_npoly bbox = %d..%d x %d..%d; want 95000..115000 x 100000..120000",
			np.MinX, np.MaxX, np.MinY, np.MaxY)
	}
	cy := zoneByName(t, st, "synth_cylinder")
	if cy.Rad != 1500 || cy.ShapeKind != ShapeCylinder {
		t.Errorf("synth_cylinder = rad %d kind %d; want 1500 Cylinder", cy.Rad, cy.ShapeKind)
	}
	rz := zoneByName(t, st, "synth_respawn")
	if len(rz.SpawnPts) != 2 || rz.SpawnPts[1].Type != "other" || rz.SpawnPts[0].Type != "" {
		t.Errorf("SpawnPts = %+v; want 2, второй с type=other, первый без type", rz.SpawnPts)
	}
	if len(rz.RacePts) != 1 || rz.RacePts[0].Race != "HUMAN" || rz.RacePts[0].Point != "talking_island_town" {
		t.Errorf("RacePts = %+v; want HUMAN → talking_island_town", rz.RacePts)
	}
	un := zoneByName(t, st, "synth_unknown")
	if un.ShapeKind != ShapeUnknown {
		t.Errorf("synth_unknown ShapeKind = %d; want ShapeUnknown", un.ShapeKind)
	}
	if v, ok := un.Set("zone.shape"); !ok || v != "Octagon" {
		t.Errorf("Set(zone.shape) = %q,%t; want \"Octagon\" (исходная строка сохранена)", v, ok)
	}
	if un.Contains(15, 15, 5) {
		t.Error("Contains неизвестной формы = true; want false")
	}
	// Файл без enabled грузится (семантика канона зон).
	if zoneByName(t, st, "synth_noenabled") == nil {
		t.Fatal("зона из файла без enabled не загружена")
	}
}

// TestZoneContains — таблица принадлежности точки зоне. Оживания выведены из
// строгой целочисленной int64-семантики порта java.awt (L2J_Mobius
// ZoneCuboid/ZoneNPoly/ZoneCylinder): прямоугольники и bbox полуоткрыты
// [min,max), окружность включена (<=), z-диапазон замкнут после нормализации.
func TestZoneContains(t *testing.T) {
	st, _ := loadSynth(t)
	cu := zoneByName(t, st, "synth_cuboid")
	// Кубоид x∈[100000,110000), y∈[100000,110000), z∈[-1000,-500].
	cases := []struct {
		zn      *Zone
		x, y, z int32
		want    bool
		what    string
	}{
		{cu, 105000, 105000, -750, true, "кубоид: центр"},
		{cu, 100000, 105000, -750, true, "кубоид: левая граница включена"},
		{cu, 105000, 100000, -750, true, "кубоид: нижняя граница включена"},
		{cu, 110000, 105000, -750, false, "кубоид: правая граница полуоткрыта"},
		{cu, 105000, 110000, -750, false, "кубоид: верхняя граница полуоткрыта"},
		{cu, 105000, 105000, -1000, true, "кубоид: z-минимум включён"},
		{cu, 105000, 105000, -500, true, "кубоид: z-максимум включён"},
		{cu, 105000, 105000, -499, false, "кубоид: z выше максимума"},
		{cu, 105000, 105000, -1001, false, "кубоид: z ниже минимума"},
		// Полигон synth_npoly: восьмиугольник x∈[95000,115000),
		// y∈[100000,120000), z нормализован в [100,500].
		{zoneByName(t, st, "synth_npoly"), 105000, 110000, 300, true, "полигон: центр"},
		{zoneByName(t, st, "synth_npoly"), 105000, 100000, 300, true, "полигон: нижнее ребро включено"},
		{zoneByName(t, st, "synth_npoly"), 95000, 110000, 300, true, "полигон: левое ребро включено"},
		{zoneByName(t, st, "synth_npoly"), 115000, 110000, 300, false, "полигон: правая грань bbox вне (полуоткрыта)"},
		{zoneByName(t, st, "synth_npoly"), 105000, 120000, 300, false, "полигон: верхняя грань bbox вне (полуоткрыта)"},
		{zoneByName(t, st, "synth_npoly"), 105000, 110000, 100, true, "полигон: zLo включён (нормализация)"},
		{zoneByName(t, st, "synth_npoly"), 105000, 110000, 99, false, "полигон: z ниже"},
		// Цилиндр: центр (0,0), rad 1500, z∈[-200,200].
		{zoneByName(t, st, "synth_cylinder"), 0, 0, 0, true, "цилиндр: центр"},
		{zoneByName(t, st, "synth_cylinder"), 1500, 0, 0, true, "цилиндр: точка на окружности включена"},
		{zoneByName(t, st, "synth_cylinder"), 1060, 1060, 0, true, "цилиндр: 1060²+1060² <= 1500²"},
		{zoneByName(t, st, "synth_cylinder"), 1061, 1060, 0, false, "цилиндр: за окружностью"},
		{zoneByName(t, st, "synth_cylinder"), 0, 0, 200, true, "цилиндр: z-максимум включён"},
		{zoneByName(t, st, "synth_cylinder"), 0, 0, 201, false, "цилиндр: z выше"},
		// int32-переполнение: |Δ| > 46341 — ложное «внутри» запрещено.
		{zoneByName(t, st, "synth_cylinder"), 300000, 0, 0, false, "цилиндр: dx=300000 — вне (int64)"},
		{zoneByName(t, st, "synth_cuboid"), -300000, 105000, -750, false, "кубоид: dx=-405000 — вне (int64)"},
		{zoneByName(t, st, "synth_npoly"), 405000, 110000, 300, false, "полигон: dx=310000 — вне (int64)"},
	}
	for _, tt := range cases {
		if got := tt.zn.Contains(tt.x, tt.y, tt.z); got != tt.want {
			t.Errorf("Contains(%d,%d,%d) [%s] = %t; want %t", tt.x, tt.y, tt.z, tt.what, got, tt.want)
		}
	}

	// Территория спавна: треугольник (70780,125060)-(71852,124640)-(72660,125432),
	// z∈[-3800,-3400]; центроид — внутри.
	ter, ok := st.Territory("synth_territory")
	if !ok {
		t.Fatal("территория synth_territory отсутствует")
	}
	if !ter.Contains(71764, 125044, -3600) {
		t.Error("Territory.Contains(центроид) = false; want true")
	}
	if ter.Contains(71764, 125044, -3300) {
		t.Error("Territory.Contains: z выше maxZ = true; want false")
	}
	if ter.Contains(60000, 130000, -3600) {
		t.Error("Territory.Contains: точка вне треугольника = true; want false")
	}
}

// TestZoneContainsNoAlloc — Contains на всех формах и территории — 0 аллокаций.
func TestZoneContainsNoAlloc(t *testing.T) {
	st, _ := loadSynth(t)
	zc := zoneByName(t, st, "synth_cuboid")
	zn := zoneByName(t, st, "synth_npoly")
	zy := zoneByName(t, st, "synth_cylinder")
	ter, _ := st.Territory("synth_territory")
	for _, f := range []func(){
		func() { zc.Contains(105000, 105000, -750) },
		func() { zn.Contains(105000, 110000, 300) },
		func() { zy.Contains(1060, 1060, 0) },
		func() { ter.Contains(71764, 125044, -3600) },
	} {
		if n := testing.AllocsPerRun(100, f); n != 0 {
			t.Errorf("Contains: %v аллокаций на вызов; want 0", n)
		}
	}
}

func zoneFile(content string) fstest.MapFS {
	return catFS(map[string]*fstest.MapFile{"zones/x.xml": {Data: []byte(content)}})
}

func TestZoneEvilInputs(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantCode string
	}{
		{"обрезанный XML", "<list enabled=\"true\"><zone name=\"a\" ty", CodeXML},
		{"чужой корневой элемент", "<zones/>", CodeRoot},
		{"мусорный enabled", "<list enabled=\"yes\"><zone name=\"a\" type=\"PeaceZone\" shape=\"Cuboid\" minZ=\"0\" maxZ=\"1\"><node X=\"0\" Y=\"0\"/><node X=\"1\" Y=\"1\"/></zone></list>", CodeAttr},
		{"нет type", "<list enabled=\"true\"><zone name=\"a\" shape=\"Cuboid\" minZ=\"0\" maxZ=\"1\"><node X=\"0\" Y=\"0\"/><node X=\"1\" Y=\"1\"/></zone></list>", CodeAttr},
		{"нет shape", "<list enabled=\"true\"><zone name=\"a\" type=\"PeaceZone\" minZ=\"0\" maxZ=\"1\"><node X=\"0\" Y=\"0\"/><node X=\"1\" Y=\"1\"/></zone></list>", CodeAttr},
		{"нет minZ", "<list enabled=\"true\"><zone name=\"a\" type=\"PeaceZone\" shape=\"Cuboid\" maxZ=\"1\"><node X=\"0\" Y=\"0\"/><node X=\"1\" Y=\"1\"/></zone></list>", CodeAttr},
		{"minZ не число", "<list enabled=\"true\"><zone name=\"a\" type=\"PeaceZone\" shape=\"Cuboid\" minZ=\"x\" maxZ=\"1\"><node X=\"0\" Y=\"0\"/><node X=\"1\" Y=\"1\"/></zone></list>", CodeNumber},
		{"Cuboid с 3 узлами", "<list enabled=\"true\"><zone name=\"a\" type=\"PeaceZone\" shape=\"Cuboid\" minZ=\"0\" maxZ=\"1\"><node X=\"0\" Y=\"0\"/><node X=\"1\" Y=\"1\"/><node X=\"2\" Y=\"2\"/></zone></list>", CodeNumber},
		{"Cuboid с 1 узлом", "<list enabled=\"true\"><zone name=\"a\" type=\"PeaceZone\" shape=\"Cuboid\" minZ=\"0\" maxZ=\"1\"><node X=\"0\" Y=\"0\"/></zone></list>", CodeNumber},
		{"Cuboid линия x1==x2", "<list enabled=\"true\"><zone name=\"a\" type=\"PeaceZone\" shape=\"Cuboid\" minZ=\"0\" maxZ=\"1\"><node X=\"5\" Y=\"0\"/><node X=\"5\" Y=\"9\"/></zone></list>", CodeNumber},
		{"Cuboid все узлы в точке", "<list enabled=\"true\"><zone name=\"a\" type=\"PeaceZone\" shape=\"Cuboid\" minZ=\"0\" maxZ=\"1\"><node X=\"5\" Y=\"5\"/><node X=\"5\" Y=\"5\"/></zone></list>", CodeNumber},
		{"NPoly с 2 узлами", "<list enabled=\"true\"><zone name=\"a\" type=\"PeaceZone\" shape=\"NPoly\" minZ=\"0\" maxZ=\"1\"><node X=\"0\" Y=\"0\"/><node X=\"1\" Y=\"1\"/></zone></list>", CodeNumber},
		{"NPoly коллинеарный", "<list enabled=\"true\"><zone name=\"a\" type=\"PeaceZone\" shape=\"NPoly\" minZ=\"0\" maxZ=\"1\"><node X=\"0\" Y=\"0\"/><node X=\"1\" Y=\"1\"/><node X=\"2\" Y=\"2\"/></zone></list>", CodeNumber},
		{"NPoly все узлы в точке", "<list enabled=\"true\"><zone name=\"a\" type=\"PeaceZone\" shape=\"NPoly\" minZ=\"0\" maxZ=\"1\"><node X=\"5\" Y=\"5\"/><node X=\"5\" Y=\"5\"/><node X=\"5\" Y=\"5\"/></zone></list>", CodeNumber},
		{"Cylinder с 2 узлами", "<list enabled=\"true\"><zone name=\"a\" type=\"EffectZone\" shape=\"Cylinder\" minZ=\"0\" maxZ=\"1\" rad=\"10\"><node X=\"0\" Y=\"0\"/><node X=\"1\" Y=\"1\"/></zone></list>", CodeNumber},
		{"Cylinder без rad", "<list enabled=\"true\"><zone name=\"a\" type=\"EffectZone\" shape=\"Cylinder\" minZ=\"0\" maxZ=\"1\"><node X=\"0\" Y=\"0\"/></zone></list>", CodeAttr},
		{"Cylinder rad 0", "<list enabled=\"true\"><zone name=\"a\" type=\"EffectZone\" shape=\"Cylinder\" minZ=\"0\" maxZ=\"1\" rad=\"0\"><node X=\"0\" Y=\"0\"/></zone></list>", CodeNumber},
		{"Cylinder rad не число", "<list enabled=\"true\"><zone name=\"a\" type=\"EffectZone\" shape=\"Cylinder\" minZ=\"0\" maxZ=\"1\" rad=\"big\"><node X=\"0\" Y=\"0\"/></zone></list>", CodeNumber},
		{"дубликат id", "<list enabled=\"true\">" +
			"<zone name=\"a\" id=\"70001\" type=\"PeaceZone\" shape=\"Cuboid\" minZ=\"0\" maxZ=\"1\"><node X=\"0\" Y=\"0\"/><node X=\"1\" Y=\"1\"/></zone>" +
			"<zone name=\"b\" id=\"70001\" type=\"PeaceZone\" shape=\"Cuboid\" minZ=\"0\" maxZ=\"1\"><node X=\"0\" Y=\"0\"/><node X=\"1\" Y=\"1\"/></zone>" +
			"</list>", CodeDupID},
		{"id не число", "<list enabled=\"true\"><zone name=\"a\" id=\"abc\" type=\"PeaceZone\" shape=\"Cuboid\" minZ=\"0\" maxZ=\"1\"><node X=\"0\" Y=\"0\"/><node X=\"1\" Y=\"1\"/></zone></list>", CodeNumber},
		{"id отрицательный", "<list enabled=\"true\"><zone name=\"a\" id=\"-1\" type=\"PeaceZone\" shape=\"Cuboid\" minZ=\"0\" maxZ=\"1\"><node X=\"0\" Y=\"0\"/><node X=\"1\" Y=\"1\"/></zone></list>", CodeNumber},
		{"stat без name", "<list enabled=\"true\"><zone name=\"a\" type=\"PeaceZone\" shape=\"Cuboid\" minZ=\"0\" maxZ=\"1\"><node X=\"0\" Y=\"0\"/><node X=\"1\" Y=\"1\"/><stat val=\"1\"/></zone></list>", CodeAttr},
		{"spawn без X", "<list enabled=\"true\"><zone name=\"a\" type=\"RespawnZone\" shape=\"Cuboid\" minZ=\"0\" maxZ=\"1\"><node X=\"0\" Y=\"0\"/><node X=\"1\" Y=\"1\"/><spawn Y=\"1\" Z=\"2\"/></zone></list>", CodeAttr},
		{"spawn Z не число", "<list enabled=\"true\"><zone name=\"a\" type=\"RespawnZone\" shape=\"Cuboid\" minZ=\"0\" maxZ=\"1\"><node X=\"0\" Y=\"0\"/><node X=\"1\" Y=\"1\"/><spawn X=\"1\" Y=\"2\" Z=\"z\"/></zone></list>", CodeNumber},
		{"race без point", "<list enabled=\"true\"><zone name=\"a\" type=\"RespawnZone\" shape=\"Cuboid\" minZ=\"0\" maxZ=\"1\"><node X=\"0\" Y=\"0\"/><node X=\"1\" Y=\"1\"/><race name=\"HUMAN\"/></zone></list>", CodeAttr},
		{"узел с X не числом", "<list enabled=\"true\"><zone name=\"a\" type=\"PeaceZone\" shape=\"Cuboid\" minZ=\"0\" maxZ=\"1\"><node X=\"abc\" Y=\"0\"/><node X=\"1\" Y=\"1\"/></zone></list>", CodeAttr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, rep, err := Load(zoneFile(tt.content))
			if err != nil {
				t.Fatalf("Load вернул фатальную ошибку на данных: %v", err)
			}
			if !rep.HasErrors() {
				t.Fatalf("злой вход не отмечен ошибкой; отчёт: %+v", rep)
			}
			found := false
			for _, e := range rep.Errors {
				if e.Code == tt.wantCode {
					found = true
					if e.File == "" || e.Line <= 0 {
						t.Errorf("запись без места: %+v", e)
					}
				}
			}
			if !found {
				t.Errorf("нет записи с кодом %q; записи: %+v", tt.wantCode, rep.Errors)
			}
		})
	}
}

// TestZoneBreadthGreen — широта данных: неизвестное — счётчики, сборка зелёная.
func TestZoneBreadthGreen(t *testing.T) {
	content := `<list enabled="true">
		<zone type="PeaceZone" shape="Cuboid" minZ="0" maxZ="1"><node X="0" Y="0"/><node X="1" Y="1"/></zone>
		<zone name="" type="PeaceZone" shape="Cuboid" minZ="0" maxZ="1"><node X="0" Y="0"/><node X="1" Y="1"/></zone>
		<zone name="b" type="FutureZone" shape="Cuboid" minZ="0" maxZ="1"><node X="0" Y="0"/><node X="1" Y="1"/></zone>
		<zone name="c" type="PeaceZone" shape="Dodecahedron" minZ="0" maxZ="1"><node X="0" Y="0"/><node X="1" Y="1"/></zone>
	</list>`
	st, rep, err := Load(zoneFile(content))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep.HasErrors() {
		t.Fatalf("широта не ошибка; записи: %+v", rep.Errors)
	}
	if rep.Zones != 4 {
		t.Fatalf("Zones = %d; want 4 (все зоны в статике)", rep.Zones)
	}
	if rep.MissingZoneName != 2 {
		t.Errorf("MissingZoneName = %d; want 2 (отсутствие и пустое имя)", rep.MissingZoneName)
	}
	if rep.UnknownTypes["zone.type.FutureZone"] != 1 || rep.UnknownTypes["zone.shape.Dodecahedron"] != 1 {
		t.Errorf("UnknownTypes = %v; want zone.type.FutureZone и zone.shape.Dodecahedron", rep.UnknownTypes)
	}
	if st.Zones()[3].Contains(0, 0, 0) {
		t.Error("Contains неизвестной формы = true; want false")
	}
}

// TestZoneDisabledFile — enabled="false": файл пропущен и посчитан.
func TestZoneDisabledFile(t *testing.T) {
	fsys := catFS(map[string]*fstest.MapFile{
		"zones/off.xml": {Data: []byte(`<list enabled="false"><zone name="a" type="PeaceZone" shape="Cuboid" minZ="0" maxZ="1"><node X="0" Y="0"/><node X="1" Y="1"/></zone></list>`)},
	})
	st, rep, err := Load(fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep.HasErrors() {
		t.Fatalf("отключённый файл не ошибка: %+v", rep.Errors)
	}
	if rep.DisabledFiles != 1 || rep.Zones != 0 || len(st.Zones()) != 0 {
		t.Errorf("DisabledFiles=%d Zones=%d; want 1, 0", rep.DisabledFiles, rep.Zones)
	}
}

// TestTerritoryRetroEvil — ретро-валидация геометрии территорий P2.5:
// вырожденная территория и banned — ошибки с местом.
func TestTerritoryRetroEvil(t *testing.T) {
	content := `<list enabled="true">
		<spawn zone="bad_terr">
			<territory minZ="0" maxZ="100">
				<node x="1" y="1" />
				<node x="2" y="2" />
			</territory>
			<npc id="20550" count="1" respawnDelay="30" />
		</spawn>
	</list>`
	_, rep, err := Load(zoneTerrFS(content))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := false
	for _, e := range rep.Errors {
		if e.Code == CodeNumber && strings.Contains(e.Message, "bad_terr") {
			found = true
			if e.File == "" || e.Line <= 0 {
				t.Errorf("запись без места: %+v", e)
			}
		}
	}
	if !found {
		t.Errorf("вырожденная территория (2 узла) не отмечена; записи: %+v", rep.Errors)
	}
	// Дополнительная запись CodeLink от спавна → несуществующая территория
	// допустима: территория не вошла в статику.
}

// TestTerritoryShapeGuard — terr.shape ≠ NPoly: счётчик, узловая валидация
// не применяется (формат SpawnData поддерживает формы, в Interlude их нет).
func TestTerritoryShapeGuard(t *testing.T) {
	content := `<list enabled="true">
		<spawn zone="cyl_terr">
			<territory shape="Cylinder" rad="50" minZ="0" maxZ="100">
				<node x="10" y="10" />
			</territory>
			<npc id="20550" count="1" respawnDelay="30" />
		</spawn>
	</list>`
	_, rep, err := Load(zoneTerrFS(content))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep.HasErrors() {
		t.Fatalf("форма территории — широта, не ошибка: %+v", rep.Errors)
	}
	if rep.UnknownTypes["terr.shape.Cylinder"] != 1 {
		t.Errorf("UnknownTypes[terr.shape.Cylinder] = %d; want 1", rep.UnknownTypes["terr.shape.Cylinder"])
	}
}

// zoneTerrFS — спавн-файл поверх обязательных каталогов категорий.
func zoneTerrFS(content string) fstest.MapFS {
	return catFS(map[string]*fstest.MapFile{"spawns/x.xml": {Data: []byte(content)}})
}
