package data

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// goldenStatic — минимальная статика с записью каждой категории: золотая
// фиксация лэйаута секции (правка кодека без правки фикстуры роняет тест
// немедленно; смена формата идёт вместе с фикстурой).
func goldenStatic() *Static {
	it := Item{ID: 7, Name: "клинок", Type: "Weapon", Weight: 10, Price: 5,
		Stackable: true, CrystalType: "d", CrystalCount: 3, Material: "fine_steel",
		BodyPart: "rhand",
		set:      map[string]string{"stat.pAtk": "10"}}

	n := Npc{ID: 21, Name: "гоблин", Title: "разбойник", Level: 9, Type: "Monster",
		Race: "Goblin", AggroRange: 300, ClanHelpRange: 150, IsAggressive: true,
		Clans:           []string{"goblin"},
		IgnoreNpcIDs:    []NpcID{23},
		CollisionRadius: 9.5, CollisionHeight: 21.5,
		Minions: []MinionRef{{NpcID: 22, Count: 2, Max: 4, RespawnTime: 30, WeightPoint: 1}},

		DropLists: []DropList{{Type: "drop",
			Groups: []DropGroup{{Chance: 70, Items: []Drop{{ItemID: 7, Min: 1, Max: 3, Chance: 55.5}}}},
			Items:  []Drop{{ItemID: 8, Min: 1, Max: 1, Chance: 100}}}},
		set: map[string]string{"stats.vitals.hp": "95"}}

	t := Territory{Name: "terr_a", MinZ: -100, MaxZ: 200, MinX: -1000, MaxX: 1000,
		MinY: -2000, MaxY: 2000,
		Nodes:  [][2]int32{{-1000, -2000}, {1000, -2000}, {1000, 2000}},
		Banned: []BannedTerritory{{MinZ: 0, MaxZ: 50, Nodes: [][2]int32{{0, 0}, {10, 10}}}},
		set:    map[string]string{"terr.shape": "NPoly"}}

	sp := NpcSpawn{NpcID: 21, HasPoint: true,
		Point: Point{X: 100, Y: 200, Z: -300, Heading: 16000}, Count: 2, RespawnDelay: 60,
		set: map[string]string{"spawn.respawnRandom": "10"}}

	zn := Zone{ShapeKind: ShapeCuboid, MinZ: -50, MaxZ: 50, ZLo: -50, ZHi: 50,
		MinX: -10, MaxX: 10, MinY: -20, MaxY: 20,
		Nodes: [][2]int32{{-10, -20}, {10, 20}}, ID: 5, HasID: true, Name: "town_peace", Type: "Town",
		SpawnPts: []ZoneSpawn{{X: 1, Y: 2, Z: 3, Type: "respawn"}},
		RacePts:  []ZoneRacePoint{{Race: "Human", Point: "talking_island"}},
		set:      map[string]string{"zone.stat.name": "town"}}

	s1 := Skill{Name: "удар", OperateType: "A1", TargetType: "ONE", ID: 99, Level: 1,
		HitTime: 1000, ReuseDelay: 5000}
	s2 := Skill{Name: "удар", OperateType: "A1", TargetType: "ONE", ID: 99, Level: 2,
		HitTime: 1200, ReuseDelay: 6000}
	s101 := Skill{Name: "удар", OperateType: "A1", TargetType: "ONE", ID: 99, Level: 101,
		HitTime: 800, ReuseDelay: 4000}
	def := &SkillDef{ID: 99, Name: "удар", Levels: 2,
		Enchant:       []SkillEnchant{{Route: 1}},
		Base:          []Skill{s1, s2},
		EnchantLevels: [][]Skill{{s101}},
		tables:        map[string][]string{"hit": {"1000", "1200"}},
		set:           map[string]string{"hitTime": "#hit"},
	}
	def.enchantOverrides = map[int8]map[string]string{1: {"isDebuff": "true"}}
	def.raw = []RawNode{{Name: "effects", Children: []RawNode{{Name: "dmg",
		Attrs: []RawAttr{{Name: "power", Value: "40"}}, Text: "текст"}}}}

	return &Static{
		items:       map[ItemID]Item{7: it},
		npcs:        map[NpcID]Npc{21: n},
		territories: map[string]Territory{"terr_a": t},
		spawns:      []NpcSpawn{sp},
		zones:       []Zone{zn},
		skills:      map[SkillID]*SkillDef{99: def},
	}
}

// goldenSectionHex — байты EncodeStatic(goldenStatic()) в hex; золотая
// фиксация лэйаута секции: правка кодека без правки фикстуры роняет тест
// немедленно.
const goldenSectionHex = "010000000100000001000000010000000100000001000000070000000c000000d0bad0bbd0b8d0bdd0bed0ba06000000576561706f6e0a00000000000000050000000000000001010000006403000000000000000a00000066696e655f737465656c050000007268616e640100000009000000737461742e7041746b020000003130150000000c000000d0b3d0bed0b1d0bbd0b8d0bd12000000d180d0b0d0b7d0b1d0bed0b9d0bdd0b8d0ba09000000070000004d6f6e7374657206000000476f626c696e2c0100009600000001000000000000234000000000008035400100000006000000676f626c696e0100000017000000010000001600000002000000040000001e00000001000000010000000400000064726f70010000000000000000805140010000000700000001000000030000000000000000c04b40010000000800000001000000010000000000000000005940010000000f00000073746174732e766974616c732e687002000000393506000000746572725f619cffffffc800000018fcffffe803000030f8ffffd00700000300000018fcffff30f8ffffe803000030f8ffffe8030000d00700000100000000000000320000000200000000000000000000000a0000000a000000010000000a000000746572722e7368617065050000004e506f6c79150000000164000000c8000000d4feffff803e000000000000020000003c0000000100000013000000737061776e2e7265737061776e52616e646f6d02000000313001ceffffff32000000ceffffff3200000000000000f6ffffff0a000000ecffffff1400000002000000f6ffffffecffffff0a0000001400000005000000010a000000746f776e5f706561636504000000546f776e01000000010000000200000003000000070000007265737061776e010000000500000048756d616e0e00000074616c6b696e675f69736c616e64010000000e0000007a6f6e652e737461742e6e616d6504000000746f776e6300000008000000d183d0b4d0b0d1800200000001000000010200000008000000d183d0b4d0b0d180020000004131030000004f4e456300000001000000e80300008813000000000000000000000008000000d183d0b4d0b0d180020000004131030000004f4e456300000002000000b004000070170000000000000000000000010000000100000008000000d183d0b4d0b0d180020000004131030000004f4e45630000006500000020030000a00f000000000000000000000001000000030000006869740200000004000000313030300400000031323030010000000700000068697454696d650400000023686974010000000101000000080000006973446562756666040000007472756501000000070000006566666563747300000000000000000100000003000000646d670100000005000000706f7765720200000034300a000000d182d0b5d0bad181d18200000000"

func TestCodecGoldenBytes(t *testing.T) {
	want, err := hex.DecodeString(goldenSectionHex)
	if err != nil {
		t.Fatalf("hex фикстуры: %v", err)
	}
	got := EncodeStatic(goldenStatic())
	if !bytes.Equal(want, got) {
		t.Fatalf("лэйаут секции дрейфанул: len %d против %d", len(got), len(want))
	}
	st, err := DecodeStatic(want)
	if err != nil {
		t.Fatalf("DecodeStatic(golden): %v", err)
	}
	if a, b := st.Dump(), goldenStatic().Dump(); a != b {
		t.Fatalf("golden не раундтрипится")
	}
}
