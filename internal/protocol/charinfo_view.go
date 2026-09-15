// CharInfoView — представление кадра CharInfo (GS→C): тест-оракул/l2client
// читает чужого игрока. Конструктор навигирует имя и титул и требует полный
// фиксированный хвост (94 Б); геттеры — по потребителю оракула, не все поля
// канона. Навигационные геттеры мерят строковые поля без декода (sFieldLen):
// отказ ReadS невозможен после успешного конструктора, игнор осознан.

package protocol

// charInfoRunOff — офсет runSpd внутри середины между именем и титулом:
// race/female/baseClass (12) + paperdoll 12×D (48) + c6-блок (48) +
// pvpFlag/karma/mAtkSpd/pAtkSpd/pvpFlag/karma (24).
const charInfoRunOff = 3*4 + 12*4 + 48 + 6*4

// CharInfoView — представление кадра CharInfo (GS→C).
type CharInfoView []byte

// NewCharInfoView навигирует имя (офсет 21) и титул и проверяет хвост;
// отказ — hex-дамп диспетчера.
func NewCharInfoView(b []byte) (CharInfoView, bool) {
	if len(b) < charInfoHead+2 {
		return nil, false
	}
	n, ok := sFieldLen(b, charInfoHead)
	if !ok {
		return nil, false
	}
	titleOff := charInfoHead + n + charInfoMid
	if titleOff+2 > len(b) {
		return nil, false
	}
	tn, ok := sFieldLen(b, titleOff)
	if !ok || titleOff+tn+charInfoTail > len(b) {
		return nil, false
	}
	return CharInfoView(b), true
}

// tailOff — офсет фиксированного хвоста после титула.
func (v CharInfoView) tailOff() int {
	n, _ := sFieldLen(v, charInfoHead)
	tn, _ := sFieldLen(v, charInfoHead+n+charInfoMid)
	return charInfoHead + n + charInfoMid + tn
}

// X возвращает X-координату.
func (v CharInfoView) X() int32 { return leD(v, 1) }

// Y возвращает Y-координату.
func (v CharInfoView) Y() int32 { return leD(v, 5) }

// Z возвращает Z-координату.
func (v CharInfoView) Z() int32 { return leD(v, 9) }

// ObjID возвращает EntityID игрока.
func (v CharInfoView) ObjID() int32 { return leD(v, 17) }

// Name возвращает видимое имя.
func (v CharInfoView) Name() (string, bool) {
	s, _, ok := ReadS(v, charInfoHead)
	return s, ok
}

// Race возвращает расу — поле идёт сразу за именем.
func (v CharInfoView) Race() int32 {
	n, _ := sFieldLen(v, charInfoHead)
	return leD(v, charInfoHead+n)
}

// RunSpd возвращает скорость бега — поле середины после боевого ряда.
func (v CharInfoView) RunSpd() int32 {
	n, _ := sFieldLen(v, charInfoHead)
	return leD(v, charInfoHead+n+charInfoRunOff)
}

// ClassID возвращает класс из хвоста (после титула).
func (v CharInfoView) ClassID() int32 { return leD(v, v.tailOff()+37) }

// Heading возвращает heading из хвоста.
func (v CharInfoView) Heading() int32 { return leD(v, v.tailOff()+74) }
