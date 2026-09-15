// Представления пакетов флоу создания персонажа (обе стороны направления).
// CharacterCreateView — переменно-структурное: конструктор навигирует имя и
// проверяет хвост 12×D; геттеры после строки возвращают значения (строка
// валидирована конструктором, офсеты хвоста фиксированы).

package protocol

// CharacterCreateView — представление кадра CharacterCreate (C→GS).
type CharacterCreateView []byte

// NewCharacterCreateView навигирует имя (UTF-16LE с терминатором) и требует
// после него полный хвост 12×D; отказ — hex-дамп диспетчера.
func NewCharacterCreateView(b []byte) (CharacterCreateView, bool) {
	n, ok := sFieldLen(b, 1)
	if !ok || len(b) < n+1+12*4 {
		return nil, false
	}
	return CharacterCreateView(b), true
}

// Name возвращает имя создаваемого персонажа.
func (v CharacterCreateView) Name() (string, bool) {
	s, _, ok := ReadS(v, 1)
	return s, ok
}

func (v CharacterCreateView) tail(off int) int32 {
	n, _ := sFieldLen(v, 1)
	return leD(v, n+off)
}

// Race возвращает расу.
func (v CharacterCreateView) Race() int32 { return v.tail(1) }

// Sex возвращает пол (0 — мужчина, 1 — женщина).
func (v CharacterCreateView) Sex() int32 { return v.tail(5) }

// ClassID возвращает класс.
func (v CharacterCreateView) ClassID() int32 { return v.tail(9) }

// Int возвращает заявленный INT.
func (v CharacterCreateView) Int() int32 { return v.tail(13) }

// Str возвращает заявленный STR.
func (v CharacterCreateView) Str() int32 { return v.tail(17) }

// Con возвращает заявленный CON.
func (v CharacterCreateView) Con() int32 { return v.tail(21) }

// Men возвращает заявленный MEN.
func (v CharacterCreateView) Men() int32 { return v.tail(25) }

// Dex возвращает заявленный DEX.
func (v CharacterCreateView) Dex() int32 { return v.tail(29) }

// Wit возвращает заявленный WIT.
func (v CharacterCreateView) Wit() int32 { return v.tail(33) }

// HairStyle возвращает причёску.
func (v CharacterCreateView) HairStyle() int32 { return v.tail(37) }

// HairColor возвращает цвет волос.
func (v CharacterCreateView) HairColor() int32 { return v.tail(41) }

// Face возвращает тип лица.
func (v CharacterCreateView) Face() int32 { return v.tail(45) }

// CharacterDeleteView — представление кадра CharacterDelete (C→GS).
type CharacterDeleteView []byte

// NewCharacterDeleteView проверяет минимальную длину и возвращает представление.
func NewCharacterDeleteView(b []byte) (CharacterDeleteView, bool) {
	if len(b) < CharacterDeleteSize {
		return nil, false
	}
	return CharacterDeleteView(b), true
}

// CharSlot возвращает слот удаляемого персонажа.
func (v CharacterDeleteView) CharSlot() int32 { return leD(v, 1) }

// NewCharacterView — представление маркера NewCharacter (C→GS).
type NewCharacterView []byte

// NewNewCharacterView проверяет наличие опкода.
func NewNewCharacterView(b []byte) (NewCharacterView, bool) {
	if len(b) < NewCharacterSize {
		return nil, false
	}
	return NewCharacterView(b), true
}

// CharTemplatesView — представление кадра CharTemplates (GS→C).
type CharTemplatesView []byte

// NewCharTemplatesView проверяет заголовок (счётчик) и полный состав записей.
func NewCharTemplatesView(b []byte) (CharTemplatesView, bool) {
	if len(b) < 5 {
		return nil, false
	}
	count := int(leD(b, 1))
	if count < 0 || len(b) < 5+count*charTemplateWire {
		return nil, false
	}
	return CharTemplatesView(b), true
}

// Count возвращает число шаблонов.
func (v CharTemplatesView) Count() int { return int(leD(v, 1)) }

// Template возвращает шаблон по индексу; выход за границы — отказ.
func (v CharTemplatesView) Template(i int) (CharTemplate, bool) {
	if i < 0 || i >= v.Count() {
		return CharTemplate{}, false
	}
	off := 5 + i*charTemplateWire
	stat := func(j int) int32 { return leD(v, off+12+j*12) }
	return CharTemplate{
		Race:    leD(v, off),
		ClassID: leD(v, off+4),
		Str:     stat(0),
		Dex:     stat(1),
		Con:     stat(2),
		Int:     stat(3),
		Wit:     stat(4),
		Men:     stat(5),
	}, true
}

// CharCreateOkView — представление кадра CharCreateOk (GS→C).
type CharCreateOkView []byte

// NewCharCreateOkView проверяет минимальную длину и возвращает представление.
func NewCharCreateOkView(b []byte) (CharCreateOkView, bool) {
	if len(b) < CharCreateOkSize {
		return nil, false
	}
	return CharCreateOkView(b), true
}

// Ok возвращает поле-признак успеха (канон — константа 1).
func (v CharCreateOkView) Ok() int32 { return leD(v, 1) }

// CharCreateFailView — представление кадра CharCreateFail (GS→C).
type CharCreateFailView []byte

// NewCharCreateFailView проверяет минимальную длину и возвращает представление.
func NewCharCreateFailView(b []byte) (CharCreateFailView, bool) {
	if len(b) < CharCreateFailSize {
		return nil, false
	}
	return CharCreateFailView(b), true
}

// Reason возвращает причину отказа создания.
func (v CharCreateFailView) Reason() CharCreateFailReason { return CharCreateFailReason(leD(v, 1)) }

// CharDeleteFailView — представление кадра CharDeleteFail (GS→C).
type CharDeleteFailView []byte

// NewCharDeleteFailView проверяет минимальную длину и возвращает представление.
func NewCharDeleteFailView(b []byte) (CharDeleteFailView, bool) {
	if len(b) < CharDeleteFailSize {
		return nil, false
	}
	return CharDeleteFailView(b), true
}

// Reason возвращает причину отказа удаления.
func (v CharDeleteFailView) Reason() CharDeleteFailReason {
	return CharDeleteFailReason(leD(v, 1))
}
