// Представления пакетов чата. Say2View — переменно-структурное: конструктор
// валидирует заголовок (строка + тип), адресат whisper навигируется геттером;
// CreatureSayView валидирует полный формат минимального режима; валидация
// значений (тип канала, длина, UTF-16-домены) — на применении у владельца.

package protocol

// Say2View — представление кадра Say2 (C→GS). Навигационные геттеры мерят
// строковые поля без декода (sFieldLen): отказ ReadS невозможен после
// успешного конструктора, игнор осознан.
type Say2View []byte

// NewSay2View проверяет заголовок: терминированный текст и полный D типа.
func NewSay2View(b []byte) (Say2View, bool) {
	n, ok := sFieldLen(b, 1)
	if !ok || len(b) < n+1+4 {
		return nil, false
	}
	return Say2View(b), true
}

// Text возвращает текст реплики.
func (v Say2View) Text() (string, bool) {
	s, _, ok := ReadS(v, 1)
	return s, ok
}

// Type возвращает тип канала (ChatType).
func (v Say2View) Type() ChatType {
	n, _ := sFieldLen(v, 1)
	return ChatType(leD(v, n+1))
}

// Target возвращает адресата whisper; для прочих каналов и отсутствующей
// строки — отказ.
func (v Say2View) Target() (string, bool) {
	n, _ := sFieldLen(v, 1)
	s, _, ok := ReadS(v, n+5)
	return s, ok
}

// CreatureSayView — представление кадра CreatureSay (GS→C, режим имя+текст).
type CreatureSayView []byte

// NewCreatureSayView навигирует имя и требует терминированный текст до конца
// буфера (терминальное поле минимального режима).
func NewCreatureSayView(b []byte) (CreatureSayView, bool) {
	if len(b) < 9 {
		return nil, false
	}
	n, ok := sFieldLen(b, 9)
	if !ok {
		return nil, false
	}
	if _, ok := sFieldLen(b, 9+n); !ok {
		return nil, false
	}
	return CreatureSayView(b), true
}

// SenderObjID возвращает EntityID говорящего.
func (v CreatureSayView) SenderObjID() int32 { return leD(v, 1) }

// Type возвращает канал реплики (ChatType).
func (v CreatureSayView) Type() ChatType { return ChatType(leD(v, 5)) }

// SenderName возвращает имя говорящего.
func (v CreatureSayView) SenderName() (string, bool) {
	s, _, ok := ReadS(v, 9)
	return s, ok
}

// Text возвращает текст реплики.
func (v CreatureSayView) Text() (string, bool) {
	n, _ := sFieldLen(v, 9)
	s, _, ok := ReadS(v, 9+n)
	return s, ok
}

// SystemMessageView — представление кадра SystemMessage (GS→C).
type SystemMessageView []byte

// NewSystemMessageView проверяет минимальную длину и возвращает представление.
func NewSystemMessageView(b []byte) (SystemMessageView, bool) {
	if len(b) < SystemMessageSize {
		return nil, false
	}
	return SystemMessageView(b), true
}

// ID возвращает идентификатор сообщения (SystemMessageID).
func (v SystemMessageView) ID() SystemMessageID { return SystemMessageID(leD(v, 1)) }

// ParamCount возвращает счётчик параметров (без параметров — всегда 0).
func (v SystemMessageView) ParamCount() int32 { return leD(v, 5) }
