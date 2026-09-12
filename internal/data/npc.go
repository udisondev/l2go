package data

import "io/fs"

// NpcID — идентификатор NPC датапака.
type NpcID int32

// MinionRef — ссылка NPC на миньона из блока parameters/minions. Семантика
// Max портирована с L2J_Mobius MinionHolder.getCount (порт, GPLv3): при
// Max > Count число заспавненных миньонов случайно в [Count, Max].
type MinionRef struct {
	NpcID       NpcID
	Count       int32
	Max         int32
	RespawnTime int32 // секунды
	WeightPoint int32
}

// Drop — предмет дроплиста: шанс в процентах, min/max — штуки.
type Drop struct {
	ItemID ItemID
	Min    int32
	Max    int32
	Chance float64
}

// DropGroup — группа дропа: шанс группы и предметы внутри.
type DropGroup struct {
	Chance float64
	Items  []Drop
}

// DropList — дроплист одного типа (drop|spoil); порядок файла сохраняется.
type DropList struct {
	Type   string
	Groups []DropGroup
	Items  []Drop
}

// Npc — NPC датапака. Типизированные поля покрывают потребителя фаз 3–4
// (спавн, таргетинг, аггро, коллизия, репликация NpcInfo) и ссылки каркаса
// целостности; прочее хранится в raw-bag с путевыми ключами (stats.vitals.hp,
// equipment.rhand, …). Дефолты отсутствующих полей — семантика L2J_Mobius
// NpcData (порт, GPLv3): level 85, type «Folk». Записи не меняются после
// загрузки; вложенные слайсы — только чтение.
type Npc struct {
	ID              NpcID
	Name            string
	Title           string
	Level           int32
	Type            string
	Race            string
	AggroRange      int32
	ClanHelpRange   int32
	IsAggressive    bool
	Clans           []string
	IgnoreNpcIDs    []NpcID
	CollisionRadius float64
	CollisionHeight float64
	Minions         []MinionRef
	DropLists       []DropList

	set map[string]string
}

// Set возвращает исходное значение параметра NPC по путевому ключу.
func (n Npc) Set(key string) (string, bool) {
	v, ok := n.set[key]
	return v, ok
}

// npcsDir — каталог категории NPC в корне данных.
const npcsDir = "stats/npcs"

// knownNpcTypes — известные типы NPC канона; прочие — широта данных (счётчик).
var knownNpcTypes = map[string]bool{
	"Adventurer": true, "Artefact": true, "Auctioneer": true, "BabyPet": true,
	"BroadcastingTower": true, "CastleDoorman": true, "Chest": true,
	"ClanHallDoorman": true, "ClanHallManager": true, "ControlTower": true,
	"DawnPriest": true, "Defender": true, "DungeonGatekeeper": true,
	"DuskPriest": true, "EffectPoint": true, "EventMonster": true,
	"FeedableBeast": true, "FestivalGuide": true, "FestivalMonster": true,
	"Fisherman": true, "FlameTower": true, "FlyTerrainObject": true,
	"Folk": true, "FriendlyMob": true, "GrandBoss": true, "Guard": true,
	"Merchant": true, "Monster": true, "OlympiadManager": true, "Pet": true,
	"PetManager": true, "RaceManager": true, "RaidBoss": true,
	"RiftInvader": true, "SchemeBuffer": true, "Servitor": true,
	"SignsPriest": true, "TamedBeast": true, "Teleporter": true,
	"Trainer": true, "VillageMasterDElf": true, "VillageMasterDwarf": true,
	"VillageMasterFighter": true, "VillageMasterMystic": true,
	"VillageMasterOrc": true, "VillageMasterPriest": true, "Warehouse": true,
}

// knownNpcRaces — известные расы канона; прочие — широта данных (счётчик).
var knownNpcRaces = map[string]bool{
	"ANIMAL": true, "BEAST": true, "BUG": true, "CASTLE_GUARD": true,
	"CONSTRUCT": true, "DARK_ELF": true, "DEMONIC": true, "DIVINE": true,
	"DRAGON": true, "DWARF": true, "ELEMENTAL": true, "ELF": true,
	"ETC": true, "FAIRY": true, "GIANT": true, "HUMAN": true,
	"HUMANOID": true, "MERCENARY": true, "ORC": true, "PLANT": true,
	"SIEGE_WEAPON": true, "UNDEAD": true,
}

// loadNpcs читает категорию NPC: плоский XML-уровень npcsDir, подкаталоги
// (custom и прочие) считаются в отчёт и не читаются. Семантика разбора и
// дефолты: L2J_Mobius NpcData.parseDocument (порт, GPLv3).
func loadNpcs(fsys fs.FS, ctx *loadCtx) {
	_ = fsys
	_ = ctx
}
