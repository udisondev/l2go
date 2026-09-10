// Живой образец конвенции писателей: функция пишет пакет с опкодом в буфер
// вызывающего и возвращает число записанных байт; малые пакеты — стек
// вызывающего, крупные — пул буферов (pkg/bufpool), sizing строковых полей —
// LenS. Порт udisondev/interlude@34fe4c8 pkg/packet/server/attack.go; формат
// сверен с L2J Mobius master CT_0_Interlude (43ac8878) Attack.java.

package protocol

// gameServerOp — опкод направления GameServer→C.
type gameServerOp byte

// attack — опкод пакета Attack (каталог GameServer→C).
const attack gameServerOp = 0x05

// Флаги удара пакета Attack.
const (
	HitFlagMiss byte = 0x80
	HitFlagCrit byte = 0x20
	HitFlagSS   byte = 0x10
	HitFlagShld byte = 0x40
)

// AttackSize — размер пакета Attack с одним ударом.
const AttackSize = 40

// AttackHit — результат одного удара в пакете Attack.
type AttackHit struct {
	TargetID int32
	Damage   int32
	Flags    byte
}

// WriteAttack пишет пакет Attack (одиночный удар) в dst: opcode + attackerID
// + {targetID, damage, flags} + координаты атакующего + счётчик доп-ударов
// (0) + координаты цели. Возвращает число записанных байт (AttackSize).
func WriteAttack(dst []byte, attackerObjID int32, hit AttackHit,
	atkX, atkY, atkZ, tgtX, tgtY, tgtZ int32) int {

	if len(dst) < AttackSize {
		panic("protocol: WriteAttack: dst короче AttackSize=40")
	}
	dst[0] = byte(attack)
	WriteD(dst[1:], attackerObjID)
	WriteD(dst[5:], hit.TargetID)
	WriteD(dst[9:], hit.Damage)
	dst[13] = hit.Flags
	WriteD(dst[14:], atkX)
	WriteD(dst[18:], atkY)
	WriteD(dst[22:], atkZ)
	WriteH(dst[26:], 0) // extraHits
	WriteD(dst[28:], tgtX)
	WriteD(dst[32:], tgtY)
	WriteD(dst[36:], tgtZ)
	return AttackSize
}
