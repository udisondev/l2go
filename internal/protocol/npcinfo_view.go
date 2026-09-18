// NpcInfoView — представление кадра NpcInfo (GS→C): тест-оракул/l2client
// читает NPC. Конструктор навигирует имя и титул и требует полный
// фиксированный хвост (58 Б); геттеры — по потребителю оракула. Навигация
// мерит строковые поля без декода (sFieldLen): отказ ReadS невозможен после
// успешного конструктора, игнор осознан.

package protocol

// NpcInfoView — представление кадра NpcInfo (GS→C).
type NpcInfoView []byte

// NewNpcInfoView навигирует имя (офсет 122) и титул и проверяет хвост;
// отказ — hex-дамп диспетчера.
func NewNpcInfoView(b []byte) (NpcInfoView, bool) {
	if len(b) < npcInfoHead+2 {
		return nil, false
	}
	n, ok := sFieldLen(b, npcInfoHead)
	if !ok {
		return nil, false
	}
	titleOff := npcInfoHead + n
	if titleOff+2 > len(b) {
		return nil, false
	}
	tn, ok := sFieldLen(b, titleOff)
	if !ok || titleOff+tn+npcInfoTail > len(b) {
		return nil, false
	}
	return NpcInfoView(b), true
}

// ObjID возвращает EntityID NPC.
func (v NpcInfoView) ObjID() int32 { return leD(v, 1) }

// DisplayID возвращает проводной идентификатор шаблона (displayId +
// 1 000 000 — константа канона; потребитель вычитает offset).
func (v NpcInfoView) DisplayID() int32 { return leD(v, 5) }

// Attackable возвращает флаг враждебности.
func (v NpcInfoView) Attackable() bool { return leD(v, 9) != 0 }

// X возвращает X-координату.
func (v NpcInfoView) X() int32 { return leD(v, 13) }

// Y возвращает Y-координату.
func (v NpcInfoView) Y() int32 { return leD(v, 17) }

// Z возвращает Z-координату.
func (v NpcInfoView) Z() int32 { return leD(v, 21) }

// Heading возвращает heading.
func (v NpcInfoView) Heading() int32 { return leD(v, 25) }

// Name возвращает имя NPC.
func (v NpcInfoView) Name() (string, bool) {
	s, _, ok := ReadS(v, npcInfoHead)
	return s, ok
}

// Title возвращает титул NPC.
func (v NpcInfoView) Title() (string, bool) {
	n, _ := sFieldLen(v, npcInfoHead)
	s, _, ok := ReadS(v, npcInfoHead+n)
	return s, ok
}

// Running возвращает режим бега NPC (канон: не бежит при спавне).
func (v NpcInfoView) Running() bool { return v[118] != 0 }

// Fields — строка трафик-лога l2client (поля живого интереса P3.10).
func (v NpcInfoView) Fields() []Field {
	name, _ := v.Name()
	return []Field{
		{K: "name", V: Quote(name)},
		{K: "objID", V: num32(v.ObjID())},
		{K: "displayID", V: num32(v.DisplayID())},
		{K: "attackable", V: num32(leD(v, 9))},
		{K: "x", V: num32(v.X())},
		{K: "y", V: num32(v.Y())},
	}
}
