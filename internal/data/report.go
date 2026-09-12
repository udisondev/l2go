package data

import "crypto/sha256"

// Entry — запись отчёта об ошибке загрузки данных.
type Entry struct {
	Category string
	File     string
	Line     int
	ID       int64 // ID записи категории (предмет/NPC); 0 — записи без ID
	Code     string
	Message  string
}

// Коды записей отчёта.
const (
	CodeXML    = "xml"    // малформленный или обрезанный XML
	CodeRoot   = "root"   // чужой корневой элемент
	CodeDupID  = "dup_id" // дубликат ID записи или имени территории
	CodeAttr   = "attr"   // обязательный атрибут отсутствует или задан повторно
	CodeNumber = "number" // число вне домена или неразборчивое значение поля
	CodeLimit  = "limit"  // файл превышает потолок размера
	CodeLink   = "link"   // битая ссылка между категориями
)

// Report — итог загрузки: счётчики, перечень ошибок и манифест входов
// (SHA-256 по отсортированному составу фактически прочитанных файлов).
// Report — единственный источник численности категорий.
type Report struct {
	Files       int
	Items       int
	Npcs        int
	Spawns      int // записи спавнов
	Territories int
	DropItems   int // предметы во всех дроплистах
	Errors      []Entry
	Manifest    [sha256.Size]byte

	UnknownKeys     map[string]int // ключи с квалификацией категории (npc.key.*, spawn.key.*)
	UnknownTypes    map[string]int // типы с квалификацией категории (npc.type.*, npc.race.*)
	SkippedElements map[string]int
	SkippedDirs     map[string]int

	// Счётчики широты данных (не ошибки).
	EmptyValues int
	DupKeys     int
	UnnamedSets int // set/param без имени
	StatNoType  int

	// Счётчики категорий P2.2.
	MissingLevel        int // NPC без level (дефолт 85)
	MissingType         int // NPC без type (дефолт Folk)
	MissingName         int // NPC без name
	MissingRace         int // NPC без race
	ChanceOver100       int // шанс дропа >100 («всегда»; канон: Antharas)
	MinOverMax          int // min > max в дропе (канон: один случай)
	WithoutRespawnDelay int // спавн без respawnDelay (дефолт 0)
	WithoutHeading      int // точечный спавн без heading (дефолт −1)
	FakePlayersSkipped  int // спавны NPC 80000–89999 без определения
	NamedBlocks         int // спавн-блоки с атрибутом name
	DisabledFiles       int // файлы спавнов с enabled="false"
	TerrOwnName         int // территории с собственным name (не читается)
	DeepSkips           int // элементы глубже потолка пути raw-bag
}

// HasErrors сообщает, есть ли в отчёте ошибки целостности.
func (r *Report) HasErrors() bool {
	return len(r.Errors) > 0
}
