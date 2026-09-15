// Представления пакетов входа в мир (обе стороны направления): EnterWorld —
// только длина (поля не потребляются), пустые списки — заголовок и счётчики,
// ActionFailed — маркер.

package protocol

import "encoding/binary"

// EnterWorldView — представление кадра EnterWorld (C→GS); поля hwinfo/tracert
// сервером не потребляются — геттеров нет, конструктор проверяет длину.
type EnterWorldView []byte

// NewEnterWorldView проверяет длину кадра (фиксированный формат) и возвращает
// представление.
func NewEnterWorldView(b []byte) (EnterWorldView, bool) {
	if len(b) < EnterWorldSize {
		return nil, false
	}
	return EnterWorldView(b), true
}

// ItemListView — представление кадра ItemList (GS→C).
type ItemListView []byte

// NewItemListView проверяет заголовок (showWindow + счётчик) и возвращает
// представление; тела записей нет.
func NewItemListView(b []byte) (ItemListView, bool) {
	if len(b) < EmptyItemListSize {
		return nil, false
	}
	return ItemListView(b), true
}

// ShowWindow возвращает флаг окна (0 — фоновая отправка слитка входа).
func (v ItemListView) ShowWindow() int16 {
	return int16(binary.LittleEndian.Uint16(v[1:]))
}

// Count возвращает число предметов.
func (v ItemListView) Count() int16 {
	return int16(binary.LittleEndian.Uint16(v[3:]))
}

// SkillListView — представление кадра SkillList (GS→C).
type SkillListView []byte

// NewSkillListView проверяет заголовок (счётчик) и возвращает представление.
func NewSkillListView(b []byte) (SkillListView, bool) {
	if len(b) < EmptySkillListSize {
		return nil, false
	}
	return SkillListView(b), true
}

// Count возвращает число умений.
func (v SkillListView) Count() int32 { return leD(v, 1) }

// ShortCutInitView — представление кадра ShortCutInit (GS→C).
type ShortCutInitView []byte

// NewShortCutInitView проверяет заголовок (счётчик) и возвращает представление.
func NewShortCutInitView(b []byte) (ShortCutInitView, bool) {
	if len(b) < EmptyShortCutInitSize {
		return nil, false
	}
	return ShortCutInitView(b), true
}

// Count возвращает число ячеек панели быстрого доступа.
func (v ShortCutInitView) Count() int32 { return leD(v, 1) }

// ActionFailedView — представление маркера ActionFailed (GS→C).
type ActionFailedView []byte

// NewActionFailedView проверяет наличие опкода.
func NewActionFailedView(b []byte) (ActionFailedView, bool) {
	if len(b) < ActionFailedSize {
		return nil, false
	}
	return ActionFailedView(b), true
}
