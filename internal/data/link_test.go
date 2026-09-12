package data

import (
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
)

// brokenBase: один предмет, один NPC с битыми ссылками всех видов, спавны с
// битыми ссылками. Каждая битая ссылка — отдельная запись CodeLink.
func brokenBaseFS() fstest.MapFS {
	npcXML := `<list>
	<npc id="20550" level="10" type="Monster" name="владелец">
		<race>HUMANOID</race>
		<ai><clanList><ignoreNpcId>8888</ignoreNpcId></clanList></ai>
		<parameters><minions name="P"><npc id="7777" count="1"/></minions></parameters>
		<dropLists>
			<drop><group chance="100"><item id="9999" min="1" max="1" chance="100"/></group></drop>
		</dropLists>
	</npc>
</list>`
	spawnXML := `<list enabled="true">
	<spawn name="p"><npc id="6666" x="1" y="2" z="3"/></spawn>
	<spawn zone="nope">
		<npc id="20550" count="1"/>
	</spawn>
</list>`
	return catFS(map[string]*fstest.MapFile{
		"stats/items/a.xml": {Data: []byte(itemXML(9001))},
		"stats/npcs/a.xml":  {Data: []byte(npcXML)},
		"spawns/a.xml":      {Data: []byte(spawnXML)},
	})
}

func TestBrokenLinks(t *testing.T) {
	_, rep, err := Load(brokenBaseFS())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var links []Entry
	for _, e := range rep.Errors {
		if e.Code == CodeLink {
			links = append(links, e)
			if e.File == "" || e.Line <= 0 {
				t.Errorf("ссылка без места: %+v", e)
			}
		}
	}
	// 9999 (дроп→предмет), 7777 (миньон→NPC), 8888 (ignore→NPC),
	// 6666 (спавн→NPC), "nope" (спавн→территория).
	if len(links) != 5 {
		t.Fatalf("битых ссылок %d; want 5: %+v", len(links), links)
	}
	seen := map[string]bool{}
	for _, e := range links {
		seen[fmt.Sprintf("%s:%d", e.File, e.Line)] = true
	}
	if len(seen) != 5 {
		t.Errorf("ссылки слиплись в одну запись: %v", seen)
	}
	for _, want := range []string{"9999", "7777", "8888", "6666", "nope"} {
		found := false
		for _, e := range links {
			if strings.Contains(e.Message, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("нет записи о ссылке %q: %+v", want, links)
		}
	}
}

// TestFakePlayersGate: спавн NPC из диапазона 80000–89999 без определения —
// счётчик, не ошибка (порт гейта канона).
func TestFakePlayersGate(t *testing.T) {
	spawnXML := `<list enabled="true">
	<spawn name="fp"><npc id="80000" x="1" y="2" z="3"/></spawn>
	<spawn name="real"><npc id="20550" x="4" y="5" z="6"/></spawn>
</list>`
	fsys := catFS(map[string]*fstest.MapFile{
		"stats/npcs/a.xml": {Data: []byte("<list>" + npcBody(20550) + "</list>")},
		"spawns/a.xml":     {Data: []byte(spawnXML)},
	})
	st, rep, err := Load(fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep.HasErrors() {
		t.Fatalf("fake player — счётчик, не ошибка: %+v", rep.Errors)
	}
	if rep.FakePlayersSkipped != 1 {
		t.Errorf("FakePlayersSkipped = %d; want 1", rep.FakePlayersSkipped)
	}
	if len(st.Spawns()) != 2 {
		t.Errorf("Spawns = %d; want 2 (запись 80000 сохраняется как данные)", len(st.Spawns()))
	}
	// За пределами диапазона отсутствие NPC — ошибка.
	spawnOut := `<list enabled="true"><spawn name="x"><npc id="79999" x="1" y="2" z="3"/></spawn></list>`
	fsys2 := catFS(map[string]*fstest.MapFile{"spawns/a.xml": {Data: []byte(spawnOut)}})
	_, rep2, err := Load(fsys2)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := false
	for _, e := range rep2.Errors {
		if e.Code == CodeLink && strings.Contains(e.Message, "79999") {
			found = true
		}
	}
	if !found {
		t.Errorf("NPC 79999 вне fake-диапазона должен давать ошибку-ссылку: %+v", rep2.Errors)
	}
}

// TestPhantomLinks: проигравший дубликат не оставляет фантомных ссылок.
func TestPhantomLinks(t *testing.T) {
	npcXML := `<list>
	<npc id="20550" level="10" type="Monster" name="первый"/>
	<npc id="20550" level="10" type="Monster" name="второй">
		<parameters><minions name="P"><npc id="7777" count="1"/></minions></parameters>
	</npc>
</list>`
	fsys := catFS(map[string]*fstest.MapFile{"stats/npcs/a.xml": {Data: []byte(npcXML)}})
	st, rep, err := Load(fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !rep.HasErrors() {
		t.Fatal("дубликат ID должен давать ошибку")
	}
	for _, e := range rep.Errors {
		if e.Code == CodeLink {
			t.Errorf("фантомная ссылка от отброшенного дубликата: %+v", e)
		}
	}
	if n, _ := st.Npc(20550); n.Name != "первый" {
		t.Errorf("победил %q; want первый", n.Name)
	}
}

// TestPhantomTerritory: территория отбрасывается (дубль имени) — но ссылка
// спавна на имя всё равно закрывается первой записью.
func TestTerritoryFirstWins(t *testing.T) {
	spawnXML := `<list enabled="true">
	<spawn zone="dup">
		<territory minZ="0" maxZ="1"><node x="1" y="1"/></territory>
		<npc id="20550" count="1"/>
	</spawn>
	<spawn zone="dup">
		<territory minZ="2" maxZ="3"><node x="2" y="2"/></territory>
		<npc id="20550" count="1"/>
	</spawn>
</list>`
	fsys := catFS(map[string]*fstest.MapFile{
		"stats/npcs/a.xml": {Data: []byte("<list>" + npcBody(20550) + "</list>")},
		"spawns/a.xml":     {Data: []byte(spawnXML)},
	})
	st, rep, err := Load(fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	dup := false
	for _, e := range rep.Errors {
		if e.Code == CodeDupID {
			dup = true
		}
		if e.Code == CodeLink {
			t.Errorf("ссылка на территорию не должна ломаться от дубля: %+v", e)
		}
	}
	if !dup {
		t.Errorf("дубликат имени территории должен давать ошибку: %+v", rep.Errors)
	}
	ter, ok := st.Territory("dup")
	if !ok || ter.MinZ != 0 {
		t.Errorf("победила %+v; want первая запись (minZ=0)", ter)
	}
}
