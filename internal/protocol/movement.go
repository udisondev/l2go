// Пакеты движения и позиционирования стационарной фазы. C→GS-писатели —
// отправка тест-клиентом (l2client), C→GS-представления — разбор сервером;
// GS→C — зеркально. Порты L2J Mobius CT_0_Interlude @43ac8878:
// clientpackets/{MoveToLocation,ValidatePosition,CannotMoveAnymore}.java,
// serverpackets/{MoveToLocation,StopMove,TeleportToLocation,ValidateLocation,
// DeleteObject}.java; доп. сверка udisondev/interlude@34fe4c8. Поле objId —
// вечный EntityID транспорта.

package protocol

// Опкоды (связь с каталогом — TestConstantsMatchCatalog).
const (
	moveToLocation     = 0x01 // C→GameServer
	charMoveToLocation = 0x01 // GameServer→C (совпадает с C→GS 0x01 — разные таблицы)
	validatePosition   = 0x48
	cannotMoveAnymore  = 0x36
	stopMove           = 0x47
	teleportToLocation = 0x28
	validateLocation   = 0x61
	deleteObject       = 0x12
)

// Опкоды GS→C-кадров движения — клиентский харнесс (компоновка пишет кадры
// через Write*-писатели, опкоды не читает).
const (
	OpDeleteObject       = deleteObject
	OpCharMoveToLocation = charMoveToLocation
	OpStopMove           = stopMove
	OpValidateLocation   = validateLocation
)

// Размеры кадров с опкодом (конвенция AttackSize).
const (
	MoveToLocationSize     = 29 // опкод + 7×D
	ValidatePositionSize   = 21 // опкод + 5×D
	CannotMoveAnymoreSize  = 17 // опкод + 4×D
	CharMoveToLocationSize = 29 // опкод + 7×D
	StopMoveSize           = 21 // опкод + 5×D
	TeleportToLocationSize = 25 // опкод + 6×D
	ValidateLocationSize   = 21 // опкод + 5×D
	DeleteObjectSize       = 9  // опкод + D + D
)

// Публичные опкоды C→GS стационарной фазы — белый список шлюза:
// неизвестные стационарные опкоды шлюз дропает с метрикой, коннект жив.
const (
	OpCMoveToLocation    = moveToLocation
	OpCValidatePosition  = validatePosition
	OpCCannotMoveAnymore = cannotMoveAnymore

	NameMoveToLocation = "MOVE_TO_LOCATION"
)

// WriteMoveToLocation пишет кадр MoveToLocation (C→GS): цель, точка
// отправления, режим (0 — клавиатура, 1 — мышь). Возвращает размер кадра.
func WriteMoveToLocation(dst []byte, targetX, targetY, targetZ, originX, originY, originZ, movementMode int32) int {
	if len(dst) < MoveToLocationSize {
		panic(shortDst("WriteMoveToLocation", len(dst), MoveToLocationSize))
	}
	dst[0] = byte(moveToLocation)
	WriteD(dst[1:], targetX)
	WriteD(dst[5:], targetY)
	WriteD(dst[9:], targetZ)
	WriteD(dst[13:], originX)
	WriteD(dst[17:], originY)
	WriteD(dst[21:], originZ)
	WriteD(dst[25:], movementMode)
	return MoveToLocationSize
}

// WriteValidatePosition пишет кадр ValidatePosition (C→GS): заявленная
// клиентом позиция, heading и id транспорта (0 — пешком).
func WriteValidatePosition(dst []byte, x, y, z, heading, vehicleID int32) int {
	if len(dst) < ValidatePositionSize {
		panic(shortDst("WriteValidatePosition", len(dst), ValidatePositionSize))
	}
	dst[0] = byte(validatePosition)
	WriteD(dst[1:], x)
	WriteD(dst[5:], y)
	WriteD(dst[9:], z)
	WriteD(dst[13:], heading)
	WriteD(dst[17:], vehicleID)
	return ValidatePositionSize
}

// WriteCannotMoveAnymore пишет кадр CannotMoveAnymore (C→GS): точка, в
// которой клиентский коллайдер упёрся в препятствие.
func WriteCannotMoveAnymore(dst []byte, x, y, z, heading int32) int {
	if len(dst) < CannotMoveAnymoreSize {
		panic(shortDst("WriteCannotMoveAnymore", len(dst), CannotMoveAnymoreSize))
	}
	dst[0] = byte(cannotMoveAnymore)
	WriteD(dst[1:], x)
	WriteD(dst[5:], y)
	WriteD(dst[9:], z)
	WriteD(dst[13:], heading)
	return CannotMoveAnymoreSize
}

// WriteCharMoveToLocation пишет кадр CharMoveToLocation (GS→C): объект,
// точка назначения и текущая позиция (порядок канона: dst раньше cur).
func WriteCharMoveToLocation(dst []byte, objID, dstX, dstY, dstZ, curX, curY, curZ int32) int {
	if len(dst) < CharMoveToLocationSize {
		panic(shortDst("WriteCharMoveToLocation", len(dst), CharMoveToLocationSize))
	}
	dst[0] = byte(charMoveToLocation)
	WriteD(dst[1:], objID)
	WriteD(dst[5:], dstX)
	WriteD(dst[9:], dstY)
	WriteD(dst[13:], dstZ)
	WriteD(dst[17:], curX)
	WriteD(dst[21:], curY)
	WriteD(dst[25:], curZ)
	return CharMoveToLocationSize
}

// WriteStopMove пишет кадр StopMove (GS→C): остановка объекта в точке с
// данным heading.
func WriteStopMove(dst []byte, objID, x, y, z, heading int32) int {
	if len(dst) < StopMoveSize {
		panic(shortDst("WriteStopMove", len(dst), StopMoveSize))
	}
	dst[0] = byte(stopMove)
	WriteD(dst[1:], objID)
	WriteD(dst[5:], x)
	WriteD(dst[9:], y)
	WriteD(dst[13:], z)
	WriteD(dst[17:], heading)
	return StopMoveSize
}

// WriteTeleportToLocation пишет кадр TeleportToLocation (GS→C); флаг fade —
// константа 0 канона (мгновенное исчезновение, без растворения).
func WriteTeleportToLocation(dst []byte, objID, x, y, z, heading int32) int {
	if len(dst) < TeleportToLocationSize {
		panic(shortDst("WriteTeleportToLocation", len(dst), TeleportToLocationSize))
	}
	dst[0] = byte(teleportToLocation)
	WriteD(dst[1:], objID)
	WriteD(dst[5:], x)
	WriteD(dst[9:], y)
	WriteD(dst[13:], z)
	WriteD(dst[17:], 0)
	WriteD(dst[21:], heading)
	return TeleportToLocationSize
}

// WriteValidateLocation пишет кадр ValidateLocation (GS→C): авторитетная
// позиция сервера (snap-back коррекции скорости).
func WriteValidateLocation(dst []byte, objID, x, y, z, heading int32) int {
	if len(dst) < ValidateLocationSize {
		panic(shortDst("WriteValidateLocation", len(dst), ValidateLocationSize))
	}
	dst[0] = byte(validateLocation)
	WriteD(dst[1:], objID)
	WriteD(dst[5:], x)
	WriteD(dst[9:], y)
	WriteD(dst[13:], z)
	WriteD(dst[17:], heading)
	return ValidateLocationSize
}

// WriteDeleteObject пишет кадр DeleteObject (GS→C): удаление объекта из
// известности клиента; хвостовое D 1 — «исчезнуть», а не «спешиться».
func WriteDeleteObject(dst []byte, objID int32) int {
	if len(dst) < DeleteObjectSize {
		panic(shortDst("WriteDeleteObject", len(dst), DeleteObjectSize))
	}
	dst[0] = byte(deleteObject)
	WriteD(dst[1:], objID)
	WriteD(dst[5:], 1)
	return DeleteObjectSize
}
