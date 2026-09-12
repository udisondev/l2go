package data

import "io/fs"

// BannedTerritory — территория-исключение спавна: узлы и диапазон высот.
type BannedTerritory struct {
	MinZ  int32
	MaxZ  int32
	Nodes [][2]int32
}

// Territory — территория спавна с инлайн-геометрией. Геометрия хранится как
// данные; принадлежность точки и вырожденность — фаза 2 P2.5. Shape/Rad —
// raw-bag до появления потребителя форм.
type Territory struct {
	Name   string
	MinZ   int32
	MaxZ   int32
	Nodes  [][2]int32
	Banned []BannedTerritory

	set map[string]string
}

// Set возвращает исходное значение параметра территории по ключу
// (terr.shape, terr.rad, banned.shape, banned.rad).
func (t Territory) Set(key string) (string, bool) {
	v, ok := t.set[key]
	return v, ok
}

// Point — точка спавна; Heading −1 — «нет» (семантика канона).
type Point struct {
	X       int32
	Y       int32
	Z       int32
	Heading int32
}

// NpcSpawn — запись спавна: точка и/или территория (канон при обеих спавнит
// в случайную точку территории, а точку и heading игнорирует — L2J_Mobius
// Spawn.initializeNpc, порт, GPLv3; выбор остаётся за потребителем).
// RespawnDelay — секунды; Count — число экземпляров. Прочие атрибуты
// (respawnRandom, chaseRange, periodOfDay, листья AIData) — raw-bag.
type NpcSpawn struct {
	NpcID        NpcID
	HasPoint     bool
	Point        Point
	Territory    string // "" — точечный спавн
	Count        int32
	RespawnDelay int32 // секунды
	set          map[string]string
}

// Set возвращает исходное значение параметра спавна по ключу.
func (s NpcSpawn) Set(key string) (string, bool) {
	v, ok := s.set[key]
	return v, ok
}

// spawnsDir — корень категории спавнов (обход рекурсивный).
const spawnsDir = "spawns"

// loadSpawns читает категорию спавнов: рекурсивный обход XML-файлов spawnsDir;
// файлы с list enabled="false" пропускаются и считаются. Семантика разбора и
// дефолты: L2J_Mobius SpawnData.parseDocument (порт, GPLv3).
func loadSpawns(fsys fs.FS, ctx *loadCtx) {
	_ = fsys
	_ = ctx
}
