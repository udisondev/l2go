// UserInfo (GS→C) — портрет самого игрока при входе в мир. Полный
// канонный лэйаут serverpackets/UserInfo.java @43ac8878, включая канонные
// дубли (RHAND в блоках paperdoll objectId ×17 и displayId ×17, pAtkSpd ×2 в
// боевом ряду, flyRun/flyWalk ×2) и c6-блок шорт-хвостов (14H + D + 12H + D +
// 4H); доп. сверка udisondev/interlude@34fe4c8. Инвентаря нет — paperdoll
// нули; objId — вечный EntityID транспорта.

package protocol

// Опкод (связь с каталогом — TestConstantsMatchCatalog).
const userInfo = 0x04

// OpUserInfo — публичный опкод GS→C UserInfo (лог l2client, e2e-ассерты).
const OpUserInfo = userInfo

// NameUserInfo — имя кадра UserInfo для трафик-лога.
const NameUserInfo = "USER_INFO"

// UserInfoData — варьируемые поля кадра UserInfo; нули и константы канона
// (vehicleId, paperdoll, аугментации, клан, права, кубики, fishing и пр.)
// пишет писатель. Передаётся по значению: событийная частота, стек-копия
// без алиасинга.
type UserInfoData struct {
	X                     int32
	Y                     int32
	Z                     int32
	ObjID                 int32
	Name                  string
	Race                  int32
	Female                bool
	BaseClass             int32
	Level                 int32
	Exp                   int64
	Str                   int32
	Dex                   int32
	Con                   int32
	Int                   int32
	Wit                   int32
	Men                   int32
	MaxHp                 int32
	CurHp                 int32
	MaxMp                 int32
	CurMp                 int32
	Sp                    int32
	CurLoad               int32
	MaxLoad               int32
	HasWeapon             bool
	PAtk                  int32
	PAtkSpd               int32
	PDef                  int32
	Evasion               int32
	Accuracy              int32
	Crit                  int32
	MAtk                  int32
	MAtkSpd               int32
	MDef                  int32
	RunSpd                int32
	WalkSpd               int32
	SwimRunSpd            int32
	SwimWalkSpd           int32
	MoveMultiplier        float64
	AttackSpeedMultiplier float64
	CollisionRadius       float64
	CollisionHeight       float64
	HairStyle             int32
	HairColor             int32
	Face                  int32
	Title                 string
	ClassID               int32
	MaxCp                 int32
	CurCp                 int32
	Running               bool
}

// Фиксированные части кадра (байт): до имени, между именем и титулом, хвост.
const (
	userInfoHead = 1 + 20                          // опкод + x/y/z/vehicleId/objId
	userInfoMid  = 4*4 + 8 + 6*4 + 4*4 + 3*4 + 4 + // race..level, exp, статы, hp/mp, sp/load, weaponFlag
		17*4 + 17*4 + // paperdoll objectId + displayId (RHAND дважды в обоих)
		(14*2 + 4 + 12*2 + 4 + 4*2) + // c6-блок
		10*4 + 2*4 + // боевой ряд (pAtkSpd дважды) + pvpFlag/karma
		8*4 + 4*8 + 3*4 + 4 // скорости, F-блок, внешность, isGM
	userInfoTail = 111 // clan×4 … cursed — см. WriteUserInfo
)

// UserInfoSize — размер кадра UserInfo с опкодом.
func UserInfoSize(d UserInfoData) int {
	return userInfoHead + userInfoMid + userInfoTail + LenS(d.Name) + LenS(d.Title)
}

// WriteUserInfo пишет кадр UserInfo (GS→C). Паника при коротком dst —
// программный контракт (doc.go).
func WriteUserInfo(dst []byte, d UserInfoData) int {
	if len(dst) < UserInfoSize(d) {
		panic(shortDst("WriteUserInfo", len(dst), UserInfoSize(d)))
	}
	dst[0] = byte(userInfo)
	WriteD(dst[1:], d.X)
	WriteD(dst[5:], d.Y)
	WriteD(dst[9:], d.Z)
	WriteD(dst[13:], 0) // vehicleId — транспорта нет
	WriteD(dst[17:], d.ObjID)
	off := userInfoHead + WriteS(dst[userInfoHead:], d.Name)
	writeD := func(v int32) {
		WriteD(dst[off:], v)
		off += 4
	}
	writeD(d.Race)
	if d.Female {
		writeD(1)
	} else {
		writeD(0)
	}
	writeD(d.BaseClass)
	writeD(d.Level)
	WriteQ(dst[off:], d.Exp)
	off += 8
	writeD(d.Str)
	writeD(d.Dex)
	writeD(d.Con)
	writeD(d.Int)
	writeD(d.Wit)
	writeD(d.Men)
	writeD(d.MaxHp)
	writeD(d.CurHp)
	writeD(d.MaxMp)
	writeD(d.CurMp)
	writeD(d.Sp)
	writeD(d.CurLoad)
	writeD(d.MaxLoad)
	if d.HasWeapon { // канон: 40 с оружием, 20 без
		writeD(40)
	} else {
		writeD(20)
	}
	for i := 0; i < 17; i++ { // paperdoll objectId
		writeD(0)
	}
	for i := 0; i < 17; i++ { // paperdoll displayId (RHAND дважды — канон)
		writeD(0)
	}
	writeH0 := func(n int) {
		for i := 0; i < n; i++ {
			WriteH(dst[off:], 0)
			off += 2
		}
	}
	writeH0(14) // c6: 14×H
	writeD(0)   // аугментация RHAND
	writeH0(12)
	writeD(0) // аугментация RHAND (дубль канона)
	writeH0(4)
	writeD(d.PAtk)
	writeD(d.PAtkSpd)
	writeD(d.PDef)
	writeD(d.Evasion)
	writeD(d.Accuracy)
	writeD(d.Crit)
	writeD(d.MAtk)
	writeD(d.MAtkSpd)
	writeD(d.PAtkSpd) // дубль канона в боевом ряду
	writeD(d.MDef)
	writeD(0) // pvpFlag
	writeD(0) // karma
	writeD(d.RunSpd)
	writeD(d.WalkSpd)
	writeD(d.SwimRunSpd)
	writeD(d.SwimWalkSpd)
	writeD(0) // flyRun
	writeD(0) // flyWalk
	writeD(0) // flyRun (дубль канона)
	writeD(0) // flyWalk (дубль канона)
	WriteF(dst[off:], d.MoveMultiplier)
	WriteF(dst[off+8:], d.AttackSpeedMultiplier)
	WriteF(dst[off+16:], d.CollisionRadius)
	WriteF(dst[off+24:], d.CollisionHeight)
	off += 32
	writeD(d.HairStyle)
	writeD(d.HairColor)
	writeD(d.Face)
	writeD(0) // isGM (builder level)
	off += WriteS(dst[off:], d.Title)
	// Хвост после титула (111 Б): клан/права, состояние, умения, CP, цвета.
	writeD(0)    // clanId
	writeD(0)    // clanCrestId
	writeD(0)    // allyId
	writeD(0)    // allyCrestId
	writeD(0)    // relation
	dst[off] = 0 // mountType
	off++
	dst[off] = 0 // privateStoreType
	off++
	dst[off] = 0 // dwarvenCraft
	off++
	writeD(0)            // pkKills
	writeD(0)            // pvpKills
	WriteH(dst[off:], 0) // cubics
	off += 2
	dst[off] = 0 // partyMatchRoom
	off++
	writeD(0)    // abnormalVisualEffects
	dst[off] = 0 // insideZone WATER
	off++
	writeD(0)            // clanPrivileges
	WriteH(dst[off:], 0) // recomLeft
	off += 2
	WriteH(dst[off:], 0) // recomHave
	off += 2
	writeD(0)            // mountNpcId
	WriteH(dst[off:], 0) // inventoryLimit
	off += 2
	writeD(d.ClassID)
	writeD(0) // special effects
	writeD(d.MaxCp)
	writeD(d.CurCp)
	dst[off] = 0 // enchantEffect
	off++
	dst[off] = 0 // team
	off++
	writeD(0)    // clanCrestLargeId
	dst[off] = 0 // noble
	off++
	dst[off] = 0 // hero
	off++
	dst[off] = 0 // fishing
	off++
	writeD(0) // fishX
	writeD(0) // fishY
	writeD(0) // fishZ
	writeD(defaultNameColor)
	if d.Running {
		dst[off] = 1 // running (окно статусов)
	} else {
		dst[off] = 0
	}
	off++
	writeD(0) // pledgeClass
	writeD(0) // pledgeType
	writeD(defaultTitleColor)
	writeD(0) // cursedWeaponLevel
	return off
}
