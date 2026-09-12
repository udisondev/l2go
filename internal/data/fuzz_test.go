package data

import (
	"os"
	"reflect"
	"testing"
	"testing/fstest"
)

// FuzzLoadItems: любые байты как файл категории — без паник; Load возвращает
// отчёт (или фатальную FS-ошибку) и детерминирован по всему отчёту.
func FuzzLoadItems(f *testing.F) {
	seed, err := os.ReadFile("testdata/synth/stats/items/items.xml")
	if err != nil {
		f.Fatalf("чтение seed: %v", err)
	}
	f.Add(seed)
	f.Add([]byte("<list><item id=\"1\" type=\"We"))
	f.Add([]byte("<list><item id=\"-3\" type=\"\" name=\"\"><set val=\"\""))
	f.Add([]byte{0x00, 0xff, 0xfe})

	f.Fuzz(func(t *testing.T, data []byte) {
		fsys := catFS(map[string]*fstest.MapFile{"stats/items/x.xml": {Data: data}})
		_, rep1, err1 := Load(fsys)
		_, rep2, err2 := Load(fsys)
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("недетерминированная фатальная ошибка: %v vs %v", err1, err2)
		}
		if err1 != nil {
			return
		}
		if rep1 == nil || rep2 == nil {
			t.Fatal("отчёт nil на произвольном входе")
		}
		if !reflect.DeepEqual(rep1, rep2) {
			t.Fatalf("недетерминированный отчёт:\n%+v\n---\n%+v", rep1, rep2)
		}
	})
}

// FuzzLoadNpcSpawns: любые байты как файлы категорий NPC и спавнов — без
// паник, отчёт детерминирован.
func FuzzLoadNpcSpawns(f *testing.F) {
	npcSeed, err := os.ReadFile("testdata/synth/stats/npcs/npcs.xml")
	if err != nil {
		f.Fatalf("чтение npc seed: %v", err)
	}
	spawnSeed, err := os.ReadFile("testdata/synth/spawns/Synth/spawns.xml")
	if err != nil {
		f.Fatalf("чтение spawn seed: %v", err)
	}
	itemSeed, err := os.ReadFile("testdata/synth/stats/items/items.xml")
	if err != nil {
		f.Fatalf("чтение item seed: %v", err)
	}
	f.Add(npcSeed, spawnSeed)
	f.Add([]byte("<list><npc id=\"1\" type=\"Mon"), spawnSeed)
	f.Add(npcSeed, []byte("<list enabled=\"true\"><spawn zone=\""))
	f.Add([]byte("<list><npc id=\"20550\" type=\"Monster\" name=\"n\"><dropLists><drop><group chance=\"NaN\"><item id=\"9001\" min=\"1\" max=\"2\" chance=\"∞\"/></group></drop></dropLists></npc></list>"), spawnSeed)
	f.Add([]byte{0x00, 0xfe, 0xff}, []byte{0xff})

	f.Fuzz(func(t *testing.T, npcData, spawnData []byte) {
		fsys := catFS(map[string]*fstest.MapFile{
			"stats/items/i.xml": {Data: itemSeed},
			"stats/npcs/x.xml":  {Data: npcData},
			"spawns/x.xml":      {Data: spawnData},
		})
		_, rep1, err1 := Load(fsys)
		_, rep2, err2 := Load(fsys)
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("недетерминированная фатальная ошибка: %v vs %v", err1, err2)
		}
		if err1 != nil {
			return
		}
		if rep1 == nil || rep2 == nil {
			t.Fatal("отчёт nil на произвольном входе")
		}
		if !reflect.DeepEqual(rep1, rep2) {
			t.Fatalf("недетерминированный отчёт:\n%+v\n---\n%+v", rep1, rep2)
		}
	})
}
