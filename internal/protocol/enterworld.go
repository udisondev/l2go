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

// Опкоды C→GS стационара (белый список шлюза).
const (
	requestRestart = 0x46
)

// OpCEnterWorld — публичный опкод C→GS EnterWorld (белый список шлюза).
const (
	OpCEnterWorld = enterWorld

	NameEnterWorld = "ENTER_WORLD"
)

// OpCRequestRestart — публичный опкод C→GS RequestRestart (белый список
// шлюза; ответ фазы 3 — RestartResponse(false)+ActionFailed, рестарта нет).
const OpCRequestRestart = requestRestart

// NameRequestRestart — имя кадра RequestRestart для трафик-лога.
const NameRequestRestart = "REQUEST_RESTART"

// RequestRestartSize — размер кадра RequestRestart с опкодом: маркер без полей.
const RequestRestartSize = 1

// WriteRequestRestart пишет кадр RequestRestart (C→GS) — маркер запроса
// возврата к выбору персонажа, тела у пакета нет.
func WriteRequestRestart(dst []byte) int {
	if len(dst) < RequestRestartSize {
		panic(shortDst("WriteRequestRestart", len(dst), RequestRestartSize))
	}
	dst[0] = byte(requestRestart)
	return RequestRestartSize
}

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

// Опкоды слитка входа (связь с каталогом — TestConstantsMatchCatalog).
const (
	sendMacroList   = 0xE7
	hennaInfo       = 0xE4
	questList       = 0x80
	etcStatusUpdate = 0xF3
	exStorageSub    = 0x2E
	friendList      = 0xFA
	skillCoolTime   = 0xC1
	clientSetTime   = 0xEC
	leaveWorld      = 0x7E
	restartResponse = 0x5F
)

// IGDaysPerDay — константа скорости часов клиента (GameTimeTaskManager
// IG_DAYS_PER_DAY = 6: игровые сутки за 4 реальных часа), Mobius
// CT_0_Interlude @43ac8878.
const IGDaysPerDay = int32(6)

// Идентификаторы системных сообщений слитка (Mobius CT_0_Interlude
// SystemMessageId.java @43ac8878: @ClientString id 1260; welcome id 34 —
// константа chat.go с P3.5).
const SystemMessageIDSevenSignsRecruiting SystemMessageID = 1260

// Пустой SendMacroList (GS→C): D(rev) B B B. Порт L2J Mobius CT_0_Interlude
// @43ac8878: serverpackets/SendMacroList.java; ревизия 2 — свежий персонаж
// канона (MacroList.java: ctor _revision=1, sendUpdate инкрементирует).
const EmptySendMacroListSize = 8

// WriteEmptySendMacroList пишет пустой кадр SendMacroList (rev=2, count=0).
func WriteEmptySendMacroList(dst []byte) int {
	if len(dst) < EmptySendMacroListSize {
		panic(shortDst("WriteEmptySendMacroList", len(dst), EmptySendMacroListSize))
	}
	dst[0] = byte(sendMacroList)
	WriteD(dst[1:], 2)
	dst[5] = 0
	dst[6] = 0
	dst[7] = 0
	return EmptySendMacroListSize
}

// HennaInfo без красок (GS→C): 6×B(статы красок=0) D(слоты=3) D(размер=0).
// Порт L2J Mobius CT_0_Interlude @43ac8878: serverpackets/HennaInfo.java
// (константа 3 — канонические слоты красок).
const EmptyHennaInfoSize = 15

// WriteEmptyHennaInfo пишет кадр HennaInfo без красок.
func WriteEmptyHennaInfo(dst []byte) int {
	if len(dst) < EmptyHennaInfoSize {
		panic(shortDst("WriteEmptyHennaInfo", len(dst), EmptyHennaInfoSize))
	}
	dst[0] = byte(hennaInfo)
	for i := 1; i <= 6; i++ {
		dst[i] = 0
	}
	WriteD(dst[7:], 3)
	WriteD(dst[11:], 0)
	return EmptyHennaInfoSize
}

// Пустой QuestList (GS→C): H(количество). Порт L2J Mobius CT_0_Interlude
// @43ac8878: serverpackets/QuestList.java (паддинг 128 Б закомментирован).
const EmptyQuestListSize = 3

// WriteEmptyQuestList пишет пустой кадр QuestList.
func WriteEmptyQuestList(dst []byte) int {
	if len(dst) < EmptyQuestListSize {
		panic(shortDst("WriteEmptyQuestList", len(dst), EmptyQuestListSize))
	}
	dst[0] = byte(questList)
	WriteH(dst[1:], 0)
	return EmptyQuestListSize
}

// EtcStatusUpdate нейтральный (GS→C): 7×D(0). Порт L2J Mobius CT_0_Interlude
// @43ac8878: serverpackets/EtcStatusUpdate.java.
const NeutralEtcStatusSize = 29

// WriteNeutralEtcStatus пишет нейтральный кадр EtcStatusUpdate.
func WriteNeutralEtcStatus(dst []byte) int {
	if len(dst) < NeutralEtcStatusSize {
		panic(shortDst("WriteNeutralEtcStatus", len(dst), NeutralEtcStatusSize))
	}
	dst[0] = byte(etcStatusUpdate)
	for i := 1; i < NeutralEtcStatusSize; i += 4 {
		WriteD(dst[i:], 0)
	}
	return NeutralEtcStatusSize
}

// ExStorageMaxCount (GS→C, Ex sub 0x2E): 8×D — лимиты слотов Human Fighter
// по дефолтам канона (PlayerConfig @43ac8878: инвентарь не-дварфа 80, склад
// 100, клан 150, приватная продажа 3 / покупка 4, рецепты дварфа/общие 50,
// доп. слоты пояса 0). Порт sp_ExStorageMaxCount.java.
const ExStorageMaxCountSize = 35

// WriteExStorageMaxCount пишет кадр ExStorageMaxCount канонных лимитов.
func WriteExStorageMaxCount(dst []byte) int {
	if len(dst) < ExStorageMaxCountSize {
		panic(shortDst("WriteExStorageMaxCount", len(dst), ExStorageMaxCountSize))
	}
	dst[0] = byte(ExGSOpcode)
	WriteH(dst[1:], exStorageSub)
	WriteD(dst[3:], 80)
	WriteD(dst[7:], 100)
	WriteD(dst[11:], 150)
	WriteD(dst[15:], 3)
	WriteD(dst[19:], 4)
	WriteD(dst[23:], 50)
	WriteD(dst[27:], 50)
	WriteD(dst[31:], 0)
	return ExStorageMaxCountSize
}

// Пустой FriendList (GS→C): D(количество). Порт L2J Mobius CT_0_Interlude
// @43ac8878: serverpackets/FriendList.java.
const EmptyFriendListSize = 5

// WriteEmptyFriendList пишет пустой кадр FriendList.
func WriteEmptyFriendList(dst []byte) int {
	if len(dst) < EmptyFriendListSize {
		panic(shortDst("WriteEmptyFriendList", len(dst), EmptyFriendListSize))
	}
	dst[0] = byte(friendList)
	WriteD(dst[1:], 0)
	return EmptyFriendListSize
}

// Пустой SkillCoolTime (GS→C): D(количество). Порт L2J Mobius CT_0_Interlude
// @43ac8878: serverpackets/SkillCoolTime.java.
const EmptySkillCoolTimeSize = 5

// WriteEmptySkillCoolTime пишет пустой кадр SkillCoolTime.
func WriteEmptySkillCoolTime(dst []byte) int {
	if len(dst) < EmptySkillCoolTimeSize {
		panic(shortDst("WriteEmptySkillCoolTime", len(dst), EmptySkillCoolTimeSize))
	}
	dst[0] = byte(skillCoolTime)
	WriteD(dst[1:], 0)
	return EmptySkillCoolTimeSize
}

// ClientSetTime (GS→C): D(игровые минуты суток) D(6 — IG_DAYS_PER_DAY,
// скорость часов клиента). Порты sp_ClientSetTime.java и
// GameTimeTaskManager.java @43ac8878: минуты = (тики % тики_IG_суток) /
// тики_IG_минуты при Гц метронома.
const ClientSetTimeSize = 9

// WriteClientSetTime пишет кадр ClientSetTime (igDays — константа скорости,
// 6 у канона).
func WriteClientSetTime(dst []byte, clientMinutes, igDays int32) int {
	if len(dst) < ClientSetTimeSize {
		panic(shortDst("WriteClientSetTime", len(dst), ClientSetTimeSize))
	}
	dst[0] = byte(clientSetTime)
	WriteD(dst[1:], clientMinutes)
	WriteD(dst[5:], igDays)
	return ClientSetTimeSize
}

// LeaveWorld (GS→C): маркер из одного опкода — финальный кадр логаута
// (клиент возвращается к выбору сервера). Порт L2J Mobius CT_0_Interlude
// @43ac8878: serverpackets/LeaveWorld.java.
const LeaveWorldSize = 1

// WriteLeaveWorld пишет кадр LeaveWorld.
func WriteLeaveWorld(dst []byte) int {
	if len(dst) < LeaveWorldSize {
		panic(shortDst("WriteLeaveWorld", len(dst), LeaveWorldSize))
	}
	dst[0] = byte(leaveWorld)
	return LeaveWorldSize
}

// RestartResponse (GS→C): D(результат). Порт L2J Mobius CT_0_Interlude
// @43ac8878: serverpackets/RestartResponse.java (RequestRestart отвечает
// false + ActionFailed — рестарт фазы 3 недоступен).
const RestartResponseSize = 5

// WriteRestartResponse пишет кадр RestartResponse.
func WriteRestartResponse(dst []byte, ok bool) int {
	if len(dst) < RestartResponseSize {
		panic(shortDst("WriteRestartResponse", len(dst), RestartResponseSize))
	}
	dst[0] = byte(restartResponse)
	if ok {
		WriteD(dst[1:], 1)
	} else {
		WriteD(dst[1:], 0)
	}
	return RestartResponseSize
}
