package data

// Виды ссылок каркаса целостности.
const (
	linkItem byte = 'i' // дроп → предмет
	linkNPC  byte = 'n' // спавн/миньон/ignore → NPC
	linkTerr byte = 't' // спавн → территория
)

// linkRef — отложенная ссылка: копится при разборе, разрешается один раз
// после всех категорий Load. Владелец — категория+файл+строка (+ID записи,
// где он есть; спавн-запись идентифицируется файлом и строкой).
type linkRef struct {
	cat     string
	file    string
	line    int
	ownerID int64  // ID записи-владельца (NPC); 0 — спавн
	kind    byte   // linkItem | linkNPC | linkTerr
	keyID   int64  // числовой ключ (предмет/NPC)
	keyName string // строковый ключ (территория)
}

// resolveLinks разрешает накопленные ссылки против финальных карт категорий.
// Спавн NPC из диапазона fake players 80000–89999 без определения — счётчик,
// не ошибка (порт гейта L2J_Mobius SpawnData.checkTemplate, GPLv3); прочие
// неразрешённые ссылки — записи-ошибки с местом.
func resolveLinks(ctx *loadCtx) {
	_ = ctx
}
