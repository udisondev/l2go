// Композиция слитка входа (EnterWorld): 16 кадров канонного порядка
// (Mobius CT_0_Interlude EnterWorld.java @43ac8878). Вызывается регионом;
// кадры уходят в Stage как стационарные (crypt) — марку ставит мир.
package encode

import (
	"github.com/udisondev/l2go/internal/protocol"
)

// ObjectIDBase — каноническая база объектных ID клиента (L2J IdManagerConfig
// FirstObjectId = 268435456, Mobius CT_0_Interlude @43ac8878): видимый клиенту
// ObjectID = база + EntityID транспорта (биекция в канонный диапазон).
const ObjectIDBase = 268435456

// EnterWorldData — аргумент композиции слитка: EntityID игрока (ObjectID
// выводится), поля UserInfo, курс и игровое время (канон ClientSetTime:
// D(игровые минуты суток) D(IG_DAYS_PER_DAY=6); минуты — из тика метронома
// функцией GameTimeMinutes).
type EnterWorldData struct {
	Entity          uint64
	User            protocol.UserInfoData
	Heading         int32
	GameTimeMinutes int32
}

// GameTimeMinutes — игровые минуты суток из тика метронома (канон
// GameTimeTaskManager @43ac8878: игровые сутки = 4 реальных часа при 10 Гц;
// формула выражена через Гц — хардкод периода запрещён инвариантом 7).
func GameTimeMinutes(tick uint64, hz int) int32 {
	perDay := uint64(14400 * hz) // секунд в IG-сутках × Гц
	perMin := uint64(10 * hz)    // тиков в IG-минуте
	return int32((tick % perDay) / perMin)
}

// ComposeEnterWorld собирает 16 кадров слитка входа; ObjID сущности в
// UserInfo/ValidateLocation = ObjectIDBase + Entity.
func ComposeEnterWorld(d EnterWorldData) [][]byte {
	objID := int32(ObjectIDBase + d.Entity)
	d.User.ObjID = objID

	frames := make([][]byte, 0, 16)
	put := func(n int, w func(dst []byte) int) {
		dst := make([]byte, n)
		w(dst)
		frames = append(frames, dst)
	}

	put(protocol.UserInfoSize(d.User), func(dst []byte) int { return protocol.WriteUserInfo(dst, d.User) })
	put(protocol.EmptySendMacroListSize, protocol.WriteEmptySendMacroList)
	put(protocol.EmptyItemListSize, protocol.WriteEmptyItemList)
	put(protocol.EmptyShortCutInitSize, protocol.WriteEmptyShortCutInit)
	put(protocol.EmptyHennaInfoSize, protocol.WriteEmptyHennaInfo)
	put(protocol.EmptyQuestListSize, protocol.WriteEmptyQuestList)
	put(protocol.NeutralEtcStatusSize, protocol.WriteNeutralEtcStatus)
	put(protocol.ExStorageMaxCountSize, protocol.WriteExStorageMaxCount)
	put(protocol.EmptyFriendListSize, protocol.WriteEmptyFriendList)
	put(protocol.SystemMessageSize, func(dst []byte) int {
		return protocol.WriteSystemMessage(dst, protocol.SystemMessageWelcomeToTheWorldOfLineageII)
	})
	put(protocol.SystemMessageSize, func(dst []byte) int {
		return protocol.WriteSystemMessage(dst, protocol.SystemMessageIDSevenSignsRecruiting)
	})
	put(protocol.EmptySkillCoolTimeSize, protocol.WriteEmptySkillCoolTime)
	put(protocol.EmptySkillListSize, protocol.WriteEmptySkillList)
	put(protocol.ValidateLocationSize, func(dst []byte) int {
		return protocol.WriteValidateLocation(dst, objID, d.User.X, d.User.Y, d.User.Z, d.Heading)
	})
	put(protocol.ActionFailedSize, protocol.WriteActionFailed)
	put(protocol.ClientSetTimeSize, func(dst []byte) int {
		return protocol.WriteClientSetTime(dst, d.GameTimeMinutes, protocol.IGDaysPerDay)
	})
	return frames
}
