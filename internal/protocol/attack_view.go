// Живой образец конвенции представлений: тип-надстройка над буфером
// соединения с конструктором-валидатором и геттерами по офсетам (нулевая
// копия). Форма определяется ролью потребителя, не направлением пакета:
// представление разбирает входящее — здесь клиент (или тест-оракул) читает
// S→C-пакет Attack, в любом направлении разбор выглядит так.

package protocol

import "encoding/binary"

// AttackView — представление пакета Attack (GameServer→C) над буфером
// соединения. Нулевое значение недействительно: доступ к полям — только
// после успешного NewAttackView (осознанное отступление от правила
// полезного нулевого значения: буфер чужой, гарантий нет).
type AttackView []byte

// NewAttackView проверяет минимальную длину (одиночный удар) и возвращает
// представление. Отказ трактуется диспетчером как показ пакета hex-дампом;
// валидация значений полей — на применении у владельца, не здесь.
// Представление раскрывает первый удар; доп-удары (ExtraHits > 0) —
// hex-дампом диспетчера.
func NewAttackView(b []byte) (AttackView, bool) {
	if len(b) < AttackSize {
		return nil, false
	}
	return AttackView(b), true
}

func (v AttackView) d(off int) int32 { return int32(binary.LittleEndian.Uint32(v[off:])) }

// AttackerID возвращает ID атакующего.
func (v AttackView) AttackerID() int32 { return v.d(1) }

// TargetID возвращает ID цели первого удара.
func (v AttackView) TargetID() int32 { return v.d(5) }

// Damage возвращает урон первого удара.
func (v AttackView) Damage() int32 { return v.d(9) }

// Flags возвращает флаги первого удара (HitFlag*).
func (v AttackView) Flags() byte { return v[13] }

// AttackX возвращает X-координату атакующего.
func (v AttackView) AttackX() int32 { return v.d(14) }

// AttackY возвращает Y-координату атакующего.
func (v AttackView) AttackY() int32 { return v.d(18) }

// AttackZ возвращает Z-координату атакующего.
func (v AttackView) AttackZ() int32 { return v.d(22) }

// ExtraHits возвращает число ударов сверх первого.
func (v AttackView) ExtraHits() int16 {
	return int16(binary.LittleEndian.Uint16(v[26:]))
}

// Координаты цели стоят в конце пакета после доп-ударов (по 9 байт каждый),
// поэтому читаются от хвоста — валидны при любом числе ударов.

// TargetX возвращает X-координату цели.
func (v AttackView) TargetX() int32 { return v.d(len(v) - 12) }

// TargetY возвращает Y-координату цели.
func (v AttackView) TargetY() int32 { return v.d(len(v) - 8) }

// TargetZ возвращает Z-координату цели.
func (v AttackView) TargetZ() int32 { return v.d(len(v) - 4) }
