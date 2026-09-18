// UserInfoView — представление кадра UserInfo (GS→C): тест-оракул/l2client
// читает собственный портрет. Конструктор навигирует имя и титул и требует
// полный фиксированный хвост (111 Б); геттеры — по потребителю оракула.
// Навигационные геттеры мерят строковые поля без декода (sFieldLen): отказ
// ReadS невозможен после успешного конструктора, игнор осознан.

package protocol

// Offsets середины между именем и титулом (после конца имени).
const (
	userInfoLevelOff = 3 * 4             // race, female, baseClass → level
	userInfoCurHpOff = 4*4 + 8 + 6*4 + 4 // + exp, статы, maxHp → curHp
	userInfoCurMpOff = userInfoCurHpOff + 4
)

// UserInfoView — представление кадра UserInfo (GS→C).
type UserInfoView []byte

// NewUserInfoView навигирует имя (офсет 21) и титул и проверяет хвост;
// отказ — hex-дамп диспетчера.
func NewUserInfoView(b []byte) (UserInfoView, bool) {
	if len(b) < userInfoHead+2 {
		return nil, false
	}
	n, ok := sFieldLen(b, userInfoHead)
	if !ok {
		return nil, false
	}
	titleOff := userInfoHead + n + userInfoMid
	if titleOff+2 > len(b) {
		return nil, false
	}
	tn, ok := sFieldLen(b, titleOff)
	if !ok || titleOff+tn+userInfoTail > len(b) {
		return nil, false
	}
	return UserInfoView(b), true
}

// nameEnd — офсет конца имени (с терминатором).
func (v UserInfoView) nameEnd() int {
	n, _ := sFieldLen(v, userInfoHead)
	return userInfoHead + n
}

// tailOff — офсет фиксированного хвоста после титула (один проход по имени).
func (v UserInfoView) tailOff() int {
	off := v.nameEnd() + userInfoMid
	tn, _ := sFieldLen(v, off)
	return off + tn
}

// X возвращает X-координату.
func (v UserInfoView) X() int32 { return leD(v, 1) }

// Y возвращает Y-координату.
func (v UserInfoView) Y() int32 { return leD(v, 5) }

// Z возвращает Z-координату.
func (v UserInfoView) Z() int32 { return leD(v, 9) }

// ObjID возвращает EntityID игрока.
func (v UserInfoView) ObjID() int32 { return leD(v, 17) }

// Name возвращает имя игрока.
func (v UserInfoView) Name() (string, bool) {
	s, _, ok := ReadS(v, userInfoHead)
	return s, ok
}

// Level возвращает уровень.
func (v UserInfoView) Level() int32 { return leD(v, v.nameEnd()+userInfoLevelOff) }

// CurHP возвращает текущее HP.
func (v UserInfoView) CurHP() (int32, bool) {
	return ReadD(v, v.nameEnd()+userInfoCurHpOff)
}

// CurMP возвращает текущее MP.
func (v UserInfoView) CurMP() (int32, bool) {
	return ReadD(v, v.nameEnd()+userInfoCurMpOff)
}

// ClassID возвращает класс из хвоста (после титула).
func (v UserInfoView) ClassID() int32 { return leD(v, v.tailOff()+53) }

// Fields возвращает поля трафик-лога: имя, идентификатор, позиция, уровень
// (ядро слитка входа — e2e-ассерты).
func (v UserInfoView) Fields() []Field {
	name, _ := v.Name()
	return []Field{
		{K: "name", V: Quote(name)},
		{K: "objID", V: num32(v.ObjID())},
		{K: "x", V: num32(v.X())},
		{K: "y", V: num32(v.Y())},
		{K: "z", V: num32(v.Z())},
		{K: "level", V: num32(v.Level())},
	}
}
