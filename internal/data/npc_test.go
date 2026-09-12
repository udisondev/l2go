package data

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

func mustNpc(t *testing.T, st *Static, id NpcID) Npc {
	t.Helper()
	n, ok := st.Npc(id)
	if !ok {
		t.Fatalf("NPC %d отсутствует", id)
	}
	return n
}

func TestNpcGolden(t *testing.T) {
	st, rep := loadSynth(t)
	if rep.Npcs != 5 {
		t.Fatalf("Npcs = %d; want 5", rep.Npcs)
	}
	n := mustNpc(t, st, 20550)
	if n.Name != "Учебный орк" || n.Title != "Разбойник" || n.Level != 27 ||
		n.Type != "Monster" || n.Race != "HUMANOID" {
		t.Errorf("20550 = name=%q title=%q level=%d type=%q race=%q", n.Name, n.Title, n.Level, n.Type, n.Race)
	}
	if n.AggroRange != 500 || n.ClanHelpRange != 300 || !n.IsAggressive {
		t.Errorf("20550 аггро: aggro=%d clanHelp=%d aggressive=%t", n.AggroRange, n.ClanHelpRange, n.IsAggressive)
	}
	if !reflect.DeepEqual(n.Clans, []string{"ORC", "GUARD"}) {
		t.Errorf("Clans = %v; want [ORC GUARD] (порядок файла)", n.Clans)
	}
	if !reflect.DeepEqual(n.IgnoreNpcIDs, []NpcID{20551}) {
		t.Errorf("IgnoreNpcIDs = %v; want [20551]", n.IgnoreNpcIDs)
	}
	if n.CollisionRadius != 13 || n.CollisionHeight != 22.5 {
		t.Errorf("collision = %v/%v; want 13/22.5", n.CollisionRadius, n.CollisionHeight)
	}
	if !reflect.DeepEqual(n.Minions, []MinionRef{{NpcID: 20551, Count: 2, Max: 4, RespawnTime: 120, WeightPoint: 1}}) {
		t.Errorf("Minions = %+v; want 20551 2/4/120/1 (включая Max)", n.Minions)
	}
	wantDrops := []DropList{
		{Type: "drop", Groups: []DropGroup{
			{Chance: 70, Items: []Drop{{9001, 1, 1, 25}, {9003, 2, 4, 50}}},
			{Chance: 0.5, Items: []Drop{{9002, 1, 1, 100}}},
		}, Items: []Drop{{9005, 1, 2, 8.5}}},
		{Type: "spoil", Items: []Drop{{9004, 1, 1, 100}}},
	}
	if !reflect.DeepEqual(n.DropLists, wantDrops) {
		t.Errorf("DropLists = %+v; want %+v (порядок файла)", n.DropLists, wantDrops)
	}
	// Дефолты канона: level 85, type Folk; аггро-поля отсутствуют → нули.
	if m := mustNpc(t, st, 30080); m.Level != 85 || m.Type != "Merchant" {
		t.Errorf("30080 = level=%d type=%q; want 85 (дефолт канона), Merchant", m.Level, m.Type)
	}
	if o := mustNpc(t, st, 20551); o.AggroRange != 0 || o.IsAggressive {
		t.Errorf("20551 аггро = %d/%t; want 0/false (явные значения)", o.AggroRange, o.IsAggressive)
	}
	if b := mustNpc(t, st, 29019); b.Race != "DRAGON" {
		t.Errorf("29019 race = %q; want DRAGON", b.Race)
	}
	if _, ok := st.Npc(99999); ok {
		t.Errorf("NPC 99999 не должен существовать")
	}
}

func TestNpcBag(t *testing.T) {
	st, _ := loadSynth(t)
	n := mustNpc(t, st, 20550)
	for key, want := range map[string]string{
		"npc.level":                   "27", // типизированный атрибут тоже в bag
		"npc.title":                   "Разбойник",
		"sex":                         "MALE",
		"stats.str":                   "40",
		"stats.vitals.hp":             "613.5",
		"stats.attack.attackSpeed":    "253",
		"stats.speed.run.ground":      "140",
		"stats.hitTime":               "390",
		"shots.soul":                  "2",
		"corpseTime":                  "86400",
		"exCrtEffect":                 "false",
		"ai.type":                     "AGGRESSIVE",
		"ai.aggroRange":               "500",
		"collision.radius.normal":     "13",
		"collision.radius.grown":      "15",
		"collision.height.grown":      "25",
		"equipment.rhand":             "127",
		"acquire.exp":                 "500",
		"parameters.MoveAroundSocial": "80",
		"minions":                     "Privates",
	} {
		if v, ok := n.Set(key); !ok || v != want {
			t.Errorf("Set(%q) = (%q, %v); want (%q, true)", key, v, ok, want)
		}
	}
	if _, ok := n.Set("skillList"); ok {
		t.Errorf("Set(skillList) лишний: поддерево без потребителя пропускается")
	}
}

func TestNpcCounters(t *testing.T) {
	_, rep := loadSynth(t)
	if rep.MissingLevel != 1 || rep.MissingName != 1 || rep.MissingType != 0 || rep.MissingRace != 2 {
		t.Errorf("missing: level=%d name=%d type=%d race=%d; want 1/1/0/2",
			rep.MissingLevel, rep.MissingName, rep.MissingType, rep.MissingRace)
	}
	if got := rep.UnknownTypes["npc.type.CustomThing"]; got != 1 {
		t.Errorf("UnknownTypes[npc.type.CustomThing] = %d; want 1", got)
	}
	for key, want := range map[string]int{
		"npc.key.sex": 2, "npc.key.stats.vitals.hp": 2, "npc.key.corpseTime": 1,
		"npc.key.ai.type": 1, "npc.key.equipment.rhand": 1, "npc.key.collision.radius.grown": 1,
	} {
		if got := rep.UnknownKeys[key]; got != want {
			t.Errorf("UnknownKeys[%q] = %d; want %d", key, got, want)
		}
	}
	if got := rep.SkippedElements["skillList"]; got != 1 {
		t.Errorf("SkippedElements[skillList] = %d; want 1", got)
	}
	if rep.DropItems != 8 {
		t.Errorf("DropItems = %d; want 8", rep.DropItems)
	}
	if rep.ChanceOver100 != 2 || rep.MinOverMax != 1 {
		t.Errorf("ChanceOver100=%d MinOverMax=%d; want 2/1 (широта, не ошибки)", rep.ChanceOver100, rep.MinOverMax)
	}
}

// TestNpcEvil: домены и структура NPC — каждая злость отдельной записью с местом.
func TestNpcEvil(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantCode string
	}{
		{"дубликат NPC", "<list>" + npcBody(21000) + npcBody(21000) + "</list>", CodeDupID},
		{"npc без id", "<list><npc level=\"10\" type=\"Monster\" name=\"n\"/></list>", CodeAttr},
		{"npc без name и level и type", "<list><npc id=\"21001\"/></list>", CodeAttr},
		{"id вне int32", "<list><npc id=\"9999999999\" type=\"Monster\" name=\"n\"/></list>", CodeNumber},
		{"кривой aggroRange", "<list>" + strings.Replace(npcBody(21002), "###", "<ai aggroRange=\"далеко\"/>", 1) + "</list>", CodeNumber},
		{"кривой isAggressive", "<list>" + strings.Replace(npcBody(21003), "###", "<ai isAggressive=\"зло\"/>", 1) + "</list>", CodeNumber},
		{"дроп без id", "<list>" + strings.Replace(npcBody(21004), "###", "<dropLists><drop><item min=\"1\" max=\"1\" chance=\"5\"/></drop></dropLists>", 1) + "</list>", CodeAttr},
		{"шанс-минус", "<list>" + dropBody(21005, "<item id=\"9001\" min=\"1\" max=\"1\" chance=\"-5\"/>") + "</list>", CodeNumber},
		{"шанс NaN", "<list>" + dropBody(21006, "<item id=\"9001\" min=\"1\" max=\"1\" chance=\"NaN\"/>") + "</list>", CodeNumber},
		{"min отрицательный", "<list>" + dropBody(21007, "<item id=\"9001\" min=\"-2\" max=\"1\" chance=\"5\"/>") + "</list>", CodeNumber},
		{"кривой minion id", "<list>" + strings.Replace(npcBody(21008), "###", "<parameters><minions name=\"P\"><npc id=\"абырвалг\" count=\"1\"/></minions></parameters>", 1) + "</list>", CodeNumber},
		{"обрыв XML", "<list><npc id=\"21009\" type=\"Monster\" name=\"n\"><ai ag", CodeXML},
		{"чужой корень", "<npcs><npc id=\"1\"/></npcs>", CodeRoot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, rep, err := Load(npcFS(t, tt.content))
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
					if e.File == "" || e.Line <= 0 || e.Category != "npcs" {
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

func npcFS(t *testing.T, npcXML string) fstest.MapFS {
	t.Helper()
	return catFS(map[string]*fstest.MapFile{"stats/npcs/x.xml": {Data: []byte(npcXML)}})
}

// npcBody — минимальный NPC; «###» замещается вложенными элементами.
func npcBody(id int) string {
	return "<npc id=\"" + itoa(id) + "\" level=\"10\" type=\"Monster\" name=\"n\">###</npc>"
}

// dropBody — NPC с дроп-секцией (обёртка dropLists/drop вокруг items).
func dropBody(id int, items string) string {
	body := "<dropLists><drop>" + items + "</drop></dropLists>"
	return strings.Replace(npcBody(id), "###", body, 1)
}

func itoa(n int) string { return strconv.Itoa(n) }
