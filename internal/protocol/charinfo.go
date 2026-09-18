// CharInfo (GS→C) — представление чужого игрока наблюдателю. Полный канонный лэйаут serverpackets/CharInfo.java @43ac8878,
// включая канонные дубли (RHAND в paperdoll-блоке 12×D, pvpFlag/karma ×2,
// flyRun/flyWalk ×2) и c6-блок шорт-хвостов (4H + D + 12H + D + 4H);
// доп. сверка udisondev/interlude@34fe4c8. Инвентаря нет — paperdoll нули;
// objId — вечный EntityID транспорта.

package protocol

// Опкод (связь с каталогом — TestConstantsMatchCatalog).
const charInfo = 0x03

// OpCharInfo — опкод кадра CharInfo (GS→C).
const OpCharInfo = charInfo

// NameCharInfo — имя кадра CharInfo для трафик-лога.
const NameCharInfo = "CHAR_INFO"

// Цвета имени/титула по умолчанию — порт entity/actor/appearance/
// PlayerAppearance.java @43ac8878 (DEFAULT_TITLE_COLOR, _nameColor).
const (
	defaultNameColor  int32 = 0xFFFFFF
	defaultTitleColor int32 = 0xECF9A2
)

// CharInfoData — варьируемые поля кадра CharInfo; нули и константы канона
// (vehicleId, paperdoll, аугментации, карма, клан, кубики, fishing и пр.)
// пишет писатель. Передаётся по значению: событийная частота (join-AoI),
// стек-копия без алиасинга.
type CharInfoData struct {
	X                     int32
	Y                     int32
	Z                     int32
	ObjID                 int32
	Name                  string
	Race                  int32
	Female                bool
	BaseClass             int32
	MAtkSpd               int32
	PAtkSpd               int32
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
	Standing              bool
	Running               bool
	ClassID               int32
	MaxCp                 int32
	CurCp                 int32
	Heading               int32
}

// Фиксированные части кадра (байт): до имени, между именем и титулом, хвост.
const (
	charInfoHead = 1 + 20 // опкод + x/y/z/vehicleId/objId
	charInfoMid  = 3*4 + 12*4 + (4*2 + 4 + 12*2 + 4 + 4*2) + 6*4 + 8*4 + 4*8 + 3*4
	charInfoTail = 94 // clan×4 … cursed — см. WriteCharInfo
)

// CharInfoSize — размер кадра CharInfo с опкодом.
func CharInfoSize(d CharInfoData) int {
	return charInfoHead + charInfoMid + charInfoTail + LenS(d.Name) + LenS(d.Title)
}

// WriteCharInfo пишет кадр CharInfo (GS→C). Паника при коротком dst —
// программный контракт (doc.go); данные пользователя (строки) входят в
// sizing.
func WriteCharInfo(dst []byte, d CharInfoData) int {
	if len(dst) < CharInfoSize(d) {
		panic(shortDst("WriteCharInfo", len(dst), CharInfoSize(d)))
	}
	dst[0] = byte(charInfo)
	WriteD(dst[1:], d.X)
	WriteD(dst[5:], d.Y)
	WriteD(dst[9:], d.Z)
	WriteD(dst[13:], 0) // vehicleId — транспорта нет
	WriteD(dst[17:], d.ObjID)
	off := charInfoHead + WriteS(dst[charInfoHead:], d.Name)
	writeD := func(v int32) {
		WriteD(dst[off:], v)
		off += 4
	}
	writeH0 := func(n int) {
		for i := 0; i < n; i++ {
			WriteH(dst[off:], 0)
			off += 2
		}
	}
	writeD(d.Race)
	if d.Female {
		writeD(1)
	} else {
		writeD(0)
	}
	writeD(d.BaseClass)
	for i := 0; i < 12; i++ { // paperdoll displayId (RHAND дважды — канон)
		writeD(0)
	}
	writeH0(4)
	writeD(0) // аугментация RHAND
	writeH0(12)
	writeD(0) // аугментация RHAND (дубль канона)
	writeH0(4)
	writeD(0) // pvpFlag
	writeD(0) // karma
	writeD(d.MAtkSpd)
	writeD(d.PAtkSpd)
	writeD(0) // pvpFlag (дубль канона)
	writeD(0) // karma (дубль канона)
	writeD(d.RunSpd)
	writeD(d.WalkSpd)
	writeD(d.SwimRunSpd)
	writeD(d.SwimWalkSpd)
	writeD(0) // flyRun
	writeD(0) // flyWalk
	writeD(0) // flyRun (дубль канона)
	writeD(0) // flyWalk (дубль канона)
	// F-блок: moveMultiplier, attackSpeedMultiplier, collisionRadius/Height.
	WriteF(dst[off:], d.MoveMultiplier)
	WriteF(dst[off+8:], d.AttackSpeedMultiplier)
	WriteF(dst[off+16:], d.CollisionRadius)
	WriteF(dst[off+24:], d.CollisionHeight)
	off += 32
	writeD(d.HairStyle)
	writeD(d.HairColor)
	writeD(d.Face)
	off += WriteS(dst[off:], d.Title)
	// Хвост после титула (94 Б): клан/права, состояние, CP, цвета.
	writeD(0) // clanId
	writeD(0) // clanCrestId
	writeD(0) // allyId
	writeD(0) // allyCrestId
	writeD(0) // relation
	b := func(v bool) {
		if v {
			dst[off] = 1
		} else {
			dst[off] = 0
		}
		off++
	}
	b(d.Standing) // standing = !sitting
	b(d.Running)
	dst[off] = 0 // inCombat
	off++
	dst[off] = 0 // alikeDead
	off++
	dst[off] = 0 // invisible
	off++
	dst[off] = 0 // mountType
	off++
	dst[off] = 0 // privateStoreType
	off++
	WriteH(dst[off:], 0) // cubics
	off += 2
	dst[off] = 0 // partyMatchRoom
	off++
	writeD(0)    // abnormalVisualEffects
	dst[off] = 0 // recomLeft
	off++
	WriteH(dst[off:], 0) // recomHave
	off += 2
	writeD(d.ClassID)
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
	writeD(d.Heading)
	writeD(0) // pledgeClass
	writeD(0) // pledgeType
	writeD(defaultTitleColor)
	writeD(0) // cursedWeaponLevel
	return off
}
