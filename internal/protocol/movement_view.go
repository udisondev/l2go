// Представления пакетов движения (обе стороны направления): конструктор
// проверяет минимальную длину кадра один раз, геттеры читают по офсетам без
// копий. Валидация значений — на применении у владельца.

package protocol

import "fmt"

// shortDst — диагностика нарушения программного контракта писателя (короткий
// dst); прецедент паники — attack.go, контракт — doc.go пакета.
func shortDst(fn string, have, want int) string {
	return fmt.Sprintf("protocol: %s: dst длиной %d байт < %d", fn, have, want)
}

// MoveToLocationView — представление кадра MoveToLocation (C→GS).
type MoveToLocationView []byte

// NewMoveToLocationView проверяет минимальную длину и возвращает представление.
func NewMoveToLocationView(b []byte) (MoveToLocationView, bool) {
	if len(b) < MoveToLocationSize {
		return nil, false
	}
	return MoveToLocationView(b), true
}

// TargetX возвращает X точки назначения.
func (v MoveToLocationView) TargetX() int32 { return leD(v, 1) }

// TargetY возвращает Y точки назначения.
func (v MoveToLocationView) TargetY() int32 { return leD(v, 5) }

// TargetZ возвращает Z точки назначения.
func (v MoveToLocationView) TargetZ() int32 { return leD(v, 9) }

// OriginX возвращает X точки отправления.
func (v MoveToLocationView) OriginX() int32 { return leD(v, 13) }

// OriginY возвращает Y точки отправления.
func (v MoveToLocationView) OriginY() int32 { return leD(v, 17) }

// OriginZ возвращает Z точки отправления.
func (v MoveToLocationView) OriginZ() int32 { return leD(v, 21) }

// MovementMode возвращает режим управления (0 — клавиатура, 1 — мышь).
func (v MoveToLocationView) MovementMode() int32 { return leD(v, 25) }

// ValidatePositionView — представление кадра ValidatePosition (C→GS).
type ValidatePositionView []byte

// NewValidatePositionView проверяет минимальную длину и возвращает представление.
func NewValidatePositionView(b []byte) (ValidatePositionView, bool) {
	if len(b) < ValidatePositionSize {
		return nil, false
	}
	return ValidatePositionView(b), true
}

// X возвращает заявленную клиентом X-координату.
func (v ValidatePositionView) X() int32 { return leD(v, 1) }

// Y возвращает заявленную клиентом Y-координату.
func (v ValidatePositionView) Y() int32 { return leD(v, 5) }

// Z возвращает заявленную клиентом Z-координату.
func (v ValidatePositionView) Z() int32 { return leD(v, 9) }

// Heading возвращает заявленный heading.
func (v ValidatePositionView) Heading() int32 { return leD(v, 13) }

// VehicleID возвращает id транспорта (0 — пешком).
func (v ValidatePositionView) VehicleID() int32 { return leD(v, 17) }

// CannotMoveAnymoreView — представление кадра CannotMoveAnymore (C→GS).
type CannotMoveAnymoreView []byte

// NewCannotMoveAnymoreView проверяет минимальную длину и возвращает представление.
func NewCannotMoveAnymoreView(b []byte) (CannotMoveAnymoreView, bool) {
	if len(b) < CannotMoveAnymoreSize {
		return nil, false
	}
	return CannotMoveAnymoreView(b), true
}

// X возвращает X точки упора в препятствие.
func (v CannotMoveAnymoreView) X() int32 { return leD(v, 1) }

// Y возвращает Y точки упора в препятствие.
func (v CannotMoveAnymoreView) Y() int32 { return leD(v, 5) }

// Z возвращает Z точки упора в препятствие.
func (v CannotMoveAnymoreView) Z() int32 { return leD(v, 9) }

// Heading возвращает heading в точке упора.
func (v CannotMoveAnymoreView) Heading() int32 { return leD(v, 13) }

// CharMoveToLocationView — представление кадра CharMoveToLocation (GS→C).
type CharMoveToLocationView []byte

// NewCharMoveToLocationView проверяет минимальную длину и возвращает представление.
func NewCharMoveToLocationView(b []byte) (CharMoveToLocationView, bool) {
	if len(b) < CharMoveToLocationSize {
		return nil, false
	}
	return CharMoveToLocationView(b), true
}

// ObjID возвращает EntityID движущегося объекта.
func (v CharMoveToLocationView) ObjID() int32 { return leD(v, 1) }

// DstX возвращает X точки назначения.
func (v CharMoveToLocationView) DstX() int32 { return leD(v, 5) }

// DstY возвращает Y точки назначения.
func (v CharMoveToLocationView) DstY() int32 { return leD(v, 9) }

// DstZ возвращает Z точки назначения.
func (v CharMoveToLocationView) DstZ() int32 { return leD(v, 13) }

// X возвращает текущую X-координату.
func (v CharMoveToLocationView) X() int32 { return leD(v, 17) }

// Y возвращает текущую Y-координату.
func (v CharMoveToLocationView) Y() int32 { return leD(v, 21) }

// Z возвращает текущую Z-координату.
func (v CharMoveToLocationView) Z() int32 { return leD(v, 25) }

// StopMoveView — представление кадра StopMove (GS→C).
type StopMoveView []byte

// NewStopMoveView проверяет минимальную длину и возвращает представление.
func NewStopMoveView(b []byte) (StopMoveView, bool) {
	if len(b) < StopMoveSize {
		return nil, false
	}
	return StopMoveView(b), true
}

// ObjID возвращает EntityID остановившегося объекта.
func (v StopMoveView) ObjID() int32 { return leD(v, 1) }

// X возвращает X точки остановки.
func (v StopMoveView) X() int32 { return leD(v, 5) }

// Y возвращает Y точки остановки.
func (v StopMoveView) Y() int32 { return leD(v, 9) }

// Z возвращает Z точки остановки.
func (v StopMoveView) Z() int32 { return leD(v, 13) }

// Heading возвращает heading после остановки.
func (v StopMoveView) Heading() int32 { return leD(v, 17) }

// TeleportToLocationView — представление кадра TeleportToLocation (GS→C).
type TeleportToLocationView []byte

// NewTeleportToLocationView проверяет минимальную длину и возвращает представление.
func NewTeleportToLocationView(b []byte) (TeleportToLocationView, bool) {
	if len(b) < TeleportToLocationSize {
		return nil, false
	}
	return TeleportToLocationView(b), true
}

// ObjID возвращает EntityID телепортируемого объекта.
func (v TeleportToLocationView) ObjID() int32 { return leD(v, 1) }

// X возвращает X точки прибытия.
func (v TeleportToLocationView) X() int32 { return leD(v, 5) }

// Y возвращает Y точки прибытия.
func (v TeleportToLocationView) Y() int32 { return leD(v, 9) }

// Z возвращает Z точки прибытия.
func (v TeleportToLocationView) Z() int32 { return leD(v, 13) }

// Flags возвращает флаг fade (0 — мгновенно).
func (v TeleportToLocationView) Flags() int32 { return leD(v, 17) }

// Heading возвращает heading после телепорта.
func (v TeleportToLocationView) Heading() int32 { return leD(v, 21) }

// ValidateLocationView — представление кадра ValidateLocation (GS→C).
type ValidateLocationView []byte

// NewValidateLocationView проверяет минимальную длину и возвращает представление.
func NewValidateLocationView(b []byte) (ValidateLocationView, bool) {
	if len(b) < ValidateLocationSize {
		return nil, false
	}
	return ValidateLocationView(b), true
}

// ObjID возвращает EntityID корректируемого объекта.
func (v ValidateLocationView) ObjID() int32 { return leD(v, 1) }

// X возвращает авторитетную X-координату.
func (v ValidateLocationView) X() int32 { return leD(v, 5) }

// Y возвращает авторитетную Y-координату.
func (v ValidateLocationView) Y() int32 { return leD(v, 9) }

// Z возвращает авторитетную Z-координату.
func (v ValidateLocationView) Z() int32 { return leD(v, 13) }

// Heading возвращает авторитетный heading.
func (v ValidateLocationView) Heading() int32 { return leD(v, 17) }

// DeleteObjectView — представление кадра DeleteObject (GS→C).
type DeleteObjectView []byte

// NewDeleteObjectView проверяет минимальную длину и возвращает представление.
func NewDeleteObjectView(b []byte) (DeleteObjectView, bool) {
	if len(b) < DeleteObjectSize {
		return nil, false
	}
	return DeleteObjectView(b), true
}

// ObjID возвращает EntityID удаляемого объекта.
func (v DeleteObjectView) ObjID() int32 { return leD(v, 1) }
