// Пакеты входа в мир. EnterWorld (C→GS) — маркер-кадр с непотребляемыми
// сервером полями (client hwinfo, tracert); пустые ItemList/SkillList/
// ShortCutInit (GS→C) — клиент ждёт их в слитке входа (пустые: инвентаря и
// умений нет); ActionFailed (GS→C) — маркер прерывания клиентского действия
// (финал слитка входа, отказы движения/чата). Порты L2J Mobius CT_0_Interlude @43ac8878:
// clientpackets/EnterWorld.java, serverpackets/{ItemList,SkillList,
// ShortcutInit,ActionFailed}.java.

package protocol

// Опкоды (связь с каталогом — TestConstantsMatchCatalog).
const (
	enterWorld   = 0x03
	itemList     = 0x1B
	skillList    = 0x58
	shortCutInit = 0x45
	actionFail   = 0x25
)

// OpCEnterWorld — публичный опкод C→GS EnterWorld (белый список шлюза, P3.6).
const (
	OpCEnterWorld = enterWorld

	NameEnterWorld = "ENTER_WORLD"
)

// EnterWorldSize — размер кадра EnterWorld с опкодом: 32 Б hwinfo + 4×D +
// 32 Б hwinfo + D + 20 Б tracert.
const EnterWorldSize = 105

// WriteEnterWorld пишет кадр EnterWorld (C→GS) с нулевыми непотребляемыми
// полями: сервер hwinfo/tracert не читает (представление проверяет только
// длину), l2client-отправка семантики не несёт.
func WriteEnterWorld(dst []byte) int {
	if len(dst) < EnterWorldSize {
		panic(shortDst("WriteEnterWorld", len(dst), EnterWorldSize))
	}
	dst[0] = byte(enterWorld)
	for i := 1; i < EnterWorldSize; i++ {
		dst[i] = 0
	}
	return EnterWorldSize
}

// EmptyItemListSize — размер пустого кадра ItemList с опкодом.
const EmptyItemListSize = 5

// WriteEmptyItemList пишет пустой кадр ItemList (GS→C): showWindow=0 — канон
// входа (EnterWorld.runImpl шлёт ItemList(player, false)), счётчик 0.
func WriteEmptyItemList(dst []byte) int {
	if len(dst) < EmptyItemListSize {
		panic(shortDst("WriteEmptyItemList", len(dst), EmptyItemListSize))
	}
	dst[0] = byte(itemList)
	WriteH(dst[1:], 0)
	WriteH(dst[3:], 0)
	return EmptyItemListSize
}

// EmptySkillListSize — размер пустого кадра SkillList с опкодом.
const EmptySkillListSize = 5

// WriteEmptySkillList пишет пустой кадр SkillList (GS→C): счётчик 0.
func WriteEmptySkillList(dst []byte) int {
	if len(dst) < EmptySkillListSize {
		panic(shortDst("WriteEmptySkillList", len(dst), EmptySkillListSize))
	}
	dst[0] = byte(skillList)
	WriteD(dst[1:], 0)
	return EmptySkillListSize
}

// EmptyShortCutInitSize — размер пустого кадра ShortCutInit с опкодом.
const EmptyShortCutInitSize = 5

// WriteEmptyShortCutInit пишет пустой кадр ShortCutInit (GS→C): счётчик 0.
func WriteEmptyShortCutInit(dst []byte) int {
	if len(dst) < EmptyShortCutInitSize {
		panic(shortDst("WriteEmptyShortCutInit", len(dst), EmptyShortCutInitSize))
	}
	dst[0] = byte(shortCutInit)
	WriteD(dst[1:], 0)
	return EmptyShortCutInitSize
}

// ActionFailedSize — размер кадра ActionFailed: маркер из одного опкода.
const ActionFailedSize = 1

// WriteActionFailed пишет кадр ActionFailed (GS→C) — прерывание клиентского
// действия (пакета-тела нет).
func WriteActionFailed(dst []byte) int {
	if len(dst) < ActionFailedSize {
		panic(shortDst("WriteActionFailed", len(dst), ActionFailedSize))
	}
	dst[0] = byte(actionFail)
	return ActionFailedSize
}
