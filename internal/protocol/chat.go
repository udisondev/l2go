// Пакеты чата. Say2 (C→GS) — речь клиента: текст, тип канала и адресат
// whisper; CreatureSay (GS→C) — реплика существа в минимальном режиме
// «имя+текст» (режимы charId/messageId/параметров канона — с первым
// потребителем); SystemMessage (GS→C) — системное сообщение без параметров.
// Поле senderObjID — вечный EntityID транспорта.
// Порты L2J Mobius CT_0_Interlude @43ac8878: clientpackets/Say2.java,
// serverpackets/{CreatureSay,SystemMessage}.java, network/SystemMessageId.java,
// network/enums/ChatType.java.

package protocol

// Опкоды (связь с каталогом — TestConstantsMatchCatalog).
const (
	say2          = 0x38
	creatureSay   = 0x4A
	systemMessage = 0x64
)

// ChatType — тип канала речи; порт network/enums/ChatType.java @43ac8878
// (полная таблица канона по прецеденту полных таблиц login.go; имена
// NPC_GENERAL/NPC_SHOUT — серверные псевдонимы тех же значений 0/1, в
// таблицу не входят). ChatGeneral — канал ALL в терминологии интерлюда.
type ChatType int32

// Каналы речи; значения — порт ChatType.java @43ac8878.
const (
	ChatGeneral            ChatType = 0
	ChatShout              ChatType = 1
	ChatWhisper            ChatType = 2
	ChatParty              ChatType = 3
	ChatClan               ChatType = 4
	ChatGM                 ChatType = 5
	ChatPetitionPlayer     ChatType = 6
	ChatPetitionGM         ChatType = 7
	ChatTrade              ChatType = 8
	ChatAlliance           ChatType = 9
	ChatAnnouncement       ChatType = 10
	ChatBoat               ChatType = 11
	ChatFriend             ChatType = 12
	ChatMSNChat            ChatType = 13
	ChatPartyMatchRoom     ChatType = 14
	ChatPartyRoomCommander ChatType = 15
	ChatPartyRoomAll       ChatType = 16
	ChatHeroVoice          ChatType = 17
	ChatCriticalAnnounce   ChatType = 18
	ChatScreenAnnounce     ChatType = 19
	ChatBattlefield        ChatType = 20
	ChatMPCCRoom           ChatType = 21
)

// SystemMessageID — идентификатор системного сообщения; значения — порт
// network/SystemMessageId.java @43ac8878 (@ClientString id). Минимальный
// реестр текущих потребителей; «ошибки создания» канонически отвечаются
// причинами CharCreateFail, SystemMessage-Id для них не существует.
type SystemMessageID int32

// Идентификаторы системных сообщений текущих потребителей.
const (
	SystemMessageWelcomeToTheWorldOfLineageII  SystemMessageID = 34
	SystemMessageChattingIsCurrentlyProhibited SystemMessageID = 147 // канонный отказ Say2
	SystemMessageChatDisabled                  SystemMessageID = 346
)

// OpCSay2 — публичный опкод C→GS Say2 (белый список шлюза).
const OpCSay2 = say2

// Say2Size — размер кадра Say2 с опкодом; адресат пишется только при whisper
// (читающая сторона канона разбирает его условно).
func Say2Size(text string, chatType ChatType, target string) int {
	n := 1 + LenS(text) + 4
	if chatType == ChatWhisper {
		n += LenS(target)
	}
	return n
}

// WriteSay2 пишет кадр Say2 (C→GS) для отправки тест-клиентом.
func WriteSay2(dst []byte, text string, chatType ChatType, target string) int {
	if len(dst) < Say2Size(text, chatType, target) {
		panic(shortDst("WriteSay2", len(dst), Say2Size(text, chatType, target)))
	}
	dst[0] = byte(say2)
	off := 1 + WriteS(dst[1:], text)
	WriteD(dst[off:], int32(chatType))
	off += 4
	if chatType == ChatWhisper {
		off += WriteS(dst[off:], target)
	}
	return off
}

// CreatureSaySize — размер кадра CreatureSay (режим имя+текст) с опкодом.
func CreatureSaySize(senderName, text string) int {
	return 1 + 4 + 4 + LenS(senderName) + LenS(text)
}

// WriteCreatureSay пишет кадр CreatureSay (GS→C): говорящий (senderObjID —
// вечный EntityID), канал, имя и текст реплики.
func WriteCreatureSay(dst []byte, senderObjID int32, chatType ChatType, senderName, text string) int {
	if len(dst) < CreatureSaySize(senderName, text) {
		panic(shortDst("WriteCreatureSay", len(dst), CreatureSaySize(senderName, text)))
	}
	dst[0] = byte(creatureSay)
	WriteD(dst[1:], senderObjID)
	WriteD(dst[5:], int32(chatType))
	off := 9 + WriteS(dst[9:], senderName)
	off += WriteS(dst[off:], text)
	return off
}

// SystemMessageSize — размер кадра SystemMessage без параметров, с опкодом.
const SystemMessageSize = 9

// WriteSystemMessage пишет кадр SystemMessage (GS→C) без параметров; счётчик
// параметров — константа 0.
func WriteSystemMessage(dst []byte, id SystemMessageID) int {
	if len(dst) < SystemMessageSize {
		panic(shortDst("WriteSystemMessage", len(dst), SystemMessageSize))
	}
	dst[0] = byte(systemMessage)
	WriteD(dst[1:], int32(id))
	WriteD(dst[5:], 0)
	return SystemMessageSize
}
