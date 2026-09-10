// Живой образец конвенции представлений: тип-надстройка над буфером
// соединения с конструктором-валидатором и геттерами по офсетам (нулевая
// копия). Форма определяется ролью потребителя, не направлением пакета:
// представление разбирает входящее — здесь клиент (или тест-оракул) читает
// S→C-пакет Attack, в любом направлении разбор выглядит так.

package protocol

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

// AttackerID возвращает ID атакующего.
func (v AttackView) AttackerID() int32 { d, _ := ReadD(v, 1); return d }

// TargetID возвращает ID цели первого удара.
func (v AttackView) TargetID() int32 { d, _ := ReadD(v, 5); return d }

// Damage возвращает урон первого удара.
func (v AttackView) Damage() int32 { d, _ := ReadD(v, 9); return d }

// Flags возвращает флаги первого удара (HitFlag*).
func (v AttackView) Flags() byte { return v[13] }

// AttackX возвращает X-координату атакующего.
func (v AttackView) AttackX() int32 { d, _ := ReadD(v, 14); return d }

// AttackY возвращает Y-координату атакующего.
func (v AttackView) AttackY() int32 { d, _ := ReadD(v, 18); return d }

// AttackZ возвращает Z-координату атакующего.
func (v AttackView) AttackZ() int32 { d, _ := ReadD(v, 22); return d }

// ExtraHits возвращает число ударов сверх первого.
func (v AttackView) ExtraHits() int16 { h, _ := ReadH(v, 26); return h }

// TargetX возвращает X-координату цели.
func (v AttackView) TargetX() int32 { d, _ := ReadD(v, 28); return d }

// TargetY возвращает Y-координату цели.
func (v AttackView) TargetY() int32 { d, _ := ReadD(v, 32); return d }

// TargetZ возвращает Z-координату цели.
func (v AttackView) TargetZ() int32 { d, _ := ReadD(v, 36); return d }
