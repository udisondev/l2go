// Представления пакетов чата. Say2View — переменно-структурное: конструктор
// валидирует заголовок (строка + тип), адресат whisper навигируется геттером;
// CreatureSayView валидирует полный формат минимального режима; валидация
// значений (тип канала, длина, UTF-16-домены) — на применении у владельца.

package protocol

import "strings"

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

// TextMeasure — длина текста в UTF-16 code units (без терминатора) и признак
// чистоты декода. Лимит длины канона (Say2.java @43ac8878: >105) мерится
// сырыми юнитами поля, не рунами: астральная руна занимает два юнита.
// clean=false — декод содержал U+FFFD (битый суррогат); литеральный U+FFFD
// легитимного текста от декод-отказа неотличим и отбраковывается вместе с
// ним (over-drop одного кодпоинта).
func (v Say2View) TextMeasure() (units int, clean bool) {
	n, ok := sFieldLen(v, 1)
	if !ok {
		return 0, false
	}
	units = n/2 - 1
	s, _, _ := ReadS(v, 1)
	return units, !strings.ContainsRune(s, 0xFFFD)
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

// Fields возвращает поля трафик-лога: говорящий, канал, имя, текст
// (e2e-ассерты чата P3.11).
func (v CreatureSayView) Fields() []Field {
	name, _ := v.SenderName()
	text, _ := v.Text()
	return []Field{
		{K: "objID", V: num32(v.SenderObjID())},
		{K: "type", V: num32(int32(v.Type()))},
		{K: "name", V: quoted(name)},
		{K: "text", V: quoted(text)},
	}
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
