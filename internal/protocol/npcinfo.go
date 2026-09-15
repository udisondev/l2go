// NpcInfo (GS→C) — NPC в известности наблюдателя. Полный
// канонный лэйаут serverpackets/AbstractNpcInfo$NpcInfo @43ac8878: по проводу
// едет displayId + 1 000 000 (npcstype id), канонные дубли flyRun/Walk и
// collisionRadius/Height, флаг nameAbove=1; оружия нет — нули.
// objId — вечный EntityID транспорта.

package protocol

// Опкод (связь с каталогом — TestConstantsMatchCatalog).
const npcInfo = 0x16

// npcDisplayOffset — константа канона: wire displayId = шаблонный + 1 000 000.
const npcDisplayOffset int32 = 1000000

// NpcInfoData — варьируемые поля кадра NpcInfo; нули и константы канона
// (экипировка, клан, AVE, enchant, flying) пишет писатель. Передаётся по
// значению: событийная частота, стек-копия без алиасинга.
type NpcInfoData struct {
	ObjID                 int32
	DisplayID             int32
	Attackable            bool
	X                     int32
	Y                     int32
	Z                     int32
	Heading               int32
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
	RHand                 int32
	Chest                 int32
	LHand                 int32
	Running               bool
	InCombat              bool
	AlikeDead             bool
	Name                  string
	Title                 string
}

// Фиксированные части кадра (байт): до имени, хвост после титула.
const (
	npcInfoHead = 1 + 121 // опкод + блок до имени включительно
	npcInfoTail = 58      // titleColor … flying — см. WriteNpcInfo
)

// NpcInfoSize — размер кадра NpcInfo с опкодом.
func NpcInfoSize(d NpcInfoData) int {
	return npcInfoHead + npcInfoTail + LenS(d.Name) + LenS(d.Title)
}

// WriteNpcInfo пишет кадр NpcInfo (GS→C). Паника при коротком dst —
// программный контракт (doc.go).
func WriteNpcInfo(dst []byte, d NpcInfoData) int {
	if len(dst) < NpcInfoSize(d) {
		panic(shortDst("WriteNpcInfo", len(dst), NpcInfoSize(d)))
	}
	dst[0] = byte(npcInfo)
	WriteD(dst[1:], d.ObjID)
	WriteD(dst[5:], d.DisplayID+npcDisplayOffset)
	if d.Attackable {
		WriteD(dst[9:], 1)
	} else {
		WriteD(dst[9:], 0)
	}
	WriteD(dst[13:], d.X)
	WriteD(dst[17:], d.Y)
	WriteD(dst[21:], d.Z)
	WriteD(dst[25:], d.Heading)
	WriteD(dst[29:], 0) // unknown D канона
	WriteD(dst[33:], d.MAtkSpd)
	WriteD(dst[37:], d.PAtkSpd)
	WriteD(dst[41:], d.RunSpd)
	WriteD(dst[45:], d.WalkSpd)
	WriteD(dst[49:], d.SwimRunSpd)
	WriteD(dst[53:], d.SwimWalkSpd)
	WriteD(dst[57:], 0) // flyRun
	WriteD(dst[61:], 0) // flyWalk
	WriteD(dst[65:], 0) // flyRun (дубль канона)
	WriteD(dst[69:], 0) // flyWalk (дубль канона)
	WriteF(dst[73:], d.MoveMultiplier)
	WriteF(dst[81:], d.AttackSpeedMultiplier)
	WriteF(dst[89:], d.CollisionRadius)
	WriteF(dst[97:], d.CollisionHeight)
	WriteD(dst[105:], d.RHand)
	WriteD(dst[109:], d.Chest)
	WriteD(dst[113:], d.LHand)
	dst[117] = 1 // name above char — константа канона
	b := func(off int, v bool) {
		if v {
			dst[off] = 1
		} else {
			dst[off] = 0
		}
	}
	b(118, d.Running)
	b(119, d.InCombat)
	b(120, d.AlikeDead)
	dst[121] = 0 // summoned (2 = призыв-анимация; здесь всегда 0)
	off := npcInfoHead + WriteS(dst[npcInfoHead:], d.Name)
	off += WriteS(dst[off:], d.Title)
	writeD := func(v int32) {
		WriteD(dst[off:], v)
		off += 4
	}
	// Хвост после титула (58 Б).
	writeD(0)    // title color (0 = клиентский дефолт)
	writeD(0)    // pvp flag
	writeD(0)    // karma
	writeD(0)    // abnormal visual effects
	writeD(0)    // clan id
	writeD(0)    // crest id
	writeD(0)    // ally id
	writeD(0)    // ally crest
	dst[off] = 0 // C2: вода/полёт
	off++
	dst[off] = 0 // team
	off++
	WriteF(dst[off:], d.CollisionRadius) // дубль канона
	WriteF(dst[off+8:], d.CollisionHeight)
	off += 16
	writeD(0) // enchant effect (C4)
	writeD(0) // flying (C6)
	return off
}
