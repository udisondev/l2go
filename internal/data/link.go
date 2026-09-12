package data

import "fmt"

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
	desc    string // вид ссылки для сообщения об ошибке
	fakeOK  bool   // ссылка спавна: отсутствие NPC в диапазоне fake players — счётчик
}

// resolveLinks разрешает накопленные ссылки против финальных карт категорий.
// Спавн NPC из диапазона fake players 80000–89999 без определения — счётчик,
// не ошибка (порт гейта L2J_Mobius SpawnData.checkTemplate, GPLv3); прочие
// неразрешённые ссылки — записи-ошибки с местом.
func resolveLinks(ctx *loadCtx) {
	for _, l := range ctx.links {
		var ok bool
		switch l.kind {
		case linkItem:
			_, ok = ctx.items[ItemID(l.keyID)]
		case linkNPC:
			_, ok = ctx.npcs[NpcID(l.keyID)]
		case linkTerr:
			_, ok = ctx.territories[l.keyName]
		}
		if ok {
			continue
		}
		if l.fakeOK && l.kind == linkNPC && fakePlayerRange(l.keyID) {
			ctx.rep.FakePlayersSkipped++
			continue
		}
		key := fmt.Sprintf("%d", l.keyID)
		if l.kind == linkTerr {
			key = "\"" + l.keyName + "\""
		}
		ctx.entry(Entry{Category: l.cat, File: l.file, Line: l.line, ID: l.ownerID,
			Code: CodeLink, Message: l.desc + "→" + map[byte]string{
				linkItem: "предмет", linkNPC: "NPC", linkTerr: "территория",
			}[l.kind] + " " + key + " не существует"})
	}
}
