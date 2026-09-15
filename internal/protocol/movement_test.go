package protocol

import (
	"encoding/hex"
	"testing"

	"github.com/udisondev/l2go/internal/protocol/fixture"
)

// Golden пакета MoveToLocation (C→GS): писатель l2client против независимо
// собранного вектора. Формат — Mobius CT_0_Interlude clientpackets/MoveToLocation.java.
func TestWriteMoveToLocationGolden(t *testing.T) {
	f := movementFixture(t, "MOVE_TO_LOCATION")
	var dst [MoveToLocationSize]byte
	n := WriteMoveToLocation(dst[:], 100, 200, -300, -10, 20, 30, 1)
	if n != len(dst) {
		t.Fatalf("WriteMoveToLocation = %d; want %d", n, len(dst))
	}
	if dst[0] != byte(moveToLocation) {
		t.Errorf("опкод = 0x%02X; want 0x%02X", dst[0], byte(moveToLocation))
	}
	if hex.EncodeToString(dst[1:]) != hex.EncodeToString(f.Payload) {
		t.Errorf("payload = %s; want %s", hex.EncodeToString(dst[1:]), hex.EncodeToString(f.Payload))
	}
}

// Разбор MoveToLocation сервером: представление против того же вектора.
func TestMoveToLocationViewGolden(t *testing.T) {
	v, ok := NewMoveToLocationView(wire(movementFixture(t, "MOVE_TO_LOCATION")))
	if !ok {
		t.Fatal("NewMoveToLocationView: ok = false")
	}
	if v.TargetX() != 100 || v.TargetY() != 200 || v.TargetZ() != -300 {
		t.Errorf("target = %d,%d,%d; want 100,200,-300", v.TargetX(), v.TargetY(), v.TargetZ())
	}
	if v.OriginX() != -10 || v.OriginY() != 20 || v.OriginZ() != 30 {
		t.Errorf("origin = %d,%d,%d; want -10,20,30", v.OriginX(), v.OriginY(), v.OriginZ())
	}
	if v.MovementMode() != 1 {
		t.Errorf("movementMode = %d; want 1", v.MovementMode())
	}
}

// Golden ValidatePosition (C→GS) в обе стороны.
func TestValidatePositionGolden(t *testing.T) {
	f := movementFixture(t, "VALIDATE_POSITION")
	var dst [ValidatePositionSize]byte
	n := WriteValidatePosition(dst[:], -71338, 258271, -3104, 1251, 0)
	if n != len(dst) || dst[0] != byte(validatePosition) {
		t.Fatalf("WriteValidatePosition = %d, op 0x%02X; want %d, 0x%02X", n, dst[0], len(dst), byte(validatePosition))
	}
	if hex.EncodeToString(dst[1:]) != hex.EncodeToString(f.Payload) {
		t.Errorf("payload = %s; want %s", hex.EncodeToString(dst[1:]), hex.EncodeToString(f.Payload))
	}
	v, ok := NewValidatePositionView(wire(f))
	if !ok {
		t.Fatal("NewValidatePositionView: ok = false")
	}
	if v.X() != -71338 || v.Y() != 258271 || v.Z() != -3104 || v.Heading() != 1251 || v.VehicleID() != 0 {
		t.Errorf("поля = %d,%d,%d,%d,%d; want -71338,258271,-3104,1251,0",
			v.X(), v.Y(), v.Z(), v.Heading(), v.VehicleID())
	}
}

// Golden CannotMoveAnymore (C→GS) в обе стороны.
func TestCannotMoveAnymoreGolden(t *testing.T) {
	f := movementFixture(t, "CANNOT_MOVE_ANYMORE")
	var dst [CannotMoveAnymoreSize]byte
	n := WriteCannotMoveAnymore(dst[:], 1, 2, 3, 456)
	if n != len(dst) || dst[0] != byte(cannotMoveAnymore) {
		t.Fatalf("WriteCannotMoveAnymore = %d, op 0x%02X", n, dst[0])
	}
	if hex.EncodeToString(dst[1:]) != hex.EncodeToString(f.Payload) {
		t.Errorf("payload = %s; want %s", hex.EncodeToString(dst[1:]), hex.EncodeToString(f.Payload))
	}
	v, ok := NewCannotMoveAnymoreView(wire(f))
	if !ok {
		t.Fatal("NewCannotMoveAnymoreView: ok = false")
	}
	if v.X() != 1 || v.Y() != 2 || v.Z() != 3 || v.Heading() != 456 {
		t.Errorf("поля = %d,%d,%d,%d; want 1,2,3,456", v.X(), v.Y(), v.Z(), v.Heading())
	}
}

// Golden CharMoveToLocation (GS→C): писатель сервера + представление-оракул
// против независимого вектора. Порядок полей — serverpackets/MoveToLocation.java.
func TestCharMoveToLocationGolden(t *testing.T) {
	f := movementFixture(t, "CHAR_MOVE_TO_LOCATION")
	var dst [CharMoveToLocationSize]byte
	n := WriteCharMoveToLocation(dst[:], 777, 1000, 2000, 3000, 10, 20, 30)
	if n != len(dst) || dst[0] != byte(charMoveToLocation) {
		t.Fatalf("WriteCharMoveToLocation = %d, op 0x%02X", n, dst[0])
	}
	if hex.EncodeToString(dst[1:]) != hex.EncodeToString(f.Payload) {
		t.Errorf("payload = %s; want %s", hex.EncodeToString(dst[1:]), hex.EncodeToString(f.Payload))
	}
	v, ok := NewCharMoveToLocationView(wire(f))
	if !ok {
		t.Fatal("NewCharMoveToLocationView: ok = false")
	}
	if v.ObjID() != 777 || v.DstX() != 1000 || v.DstY() != 2000 || v.DstZ() != 3000 {
		t.Errorf("dst = %d,%d,%d (obj %d); want 1000,2000,3000 (777)", v.DstX(), v.DstY(), v.DstZ(), v.ObjID())
	}
	if v.X() != 10 || v.Y() != 20 || v.Z() != 30 {
		t.Errorf("cur = %d,%d,%d; want 10,20,30", v.X(), v.Y(), v.Z())
	}
}

// Golden StopMove (GS→C) в обе стороны.
func TestStopMoveGolden(t *testing.T) {
	f := movementFixture(t, "STOP_MOVE")
	var dst [StopMoveSize]byte
	n := WriteStopMove(dst[:], 5, 100, 200, 300, 64)
	if n != len(dst) || dst[0] != byte(stopMove) {
		t.Fatalf("WriteStopMove = %d, op 0x%02X", n, dst[0])
	}
	if hex.EncodeToString(dst[1:]) != hex.EncodeToString(f.Payload) {
		t.Errorf("payload = %s; want %s", hex.EncodeToString(dst[1:]), hex.EncodeToString(f.Payload))
	}
	v, ok := NewStopMoveView(wire(f))
	if !ok {
		t.Fatal("NewStopMoveView: ok = false")
	}
	if v.ObjID() != 5 || v.X() != 100 || v.Y() != 200 || v.Z() != 300 || v.Heading() != 64 {
		t.Errorf("поля = obj %d, %d,%d,%d, h %d; want 5, 100,200,300, 64", v.ObjID(), v.X(), v.Y(), v.Z(), v.Heading())
	}
}

// Golden TeleportToLocation (GS→C) в обе стороны; флаг fade — константа 0 канона.
func TestTeleportToLocationGolden(t *testing.T) {
	f := movementFixture(t, "TELEPORT_TO_LOCATION")
	var dst [TeleportToLocationSize]byte
	n := WriteTeleportToLocation(dst[:], 9, 1, 2, 3, 4)
	if n != len(dst) || dst[0] != byte(teleportToLocation) {
		t.Fatalf("WriteTeleportToLocation = %d, op 0x%02X", n, dst[0])
	}
	if hex.EncodeToString(dst[1:]) != hex.EncodeToString(f.Payload) {
		t.Errorf("payload = %s; want %s", hex.EncodeToString(dst[1:]), hex.EncodeToString(f.Payload))
	}
	v, ok := NewTeleportToLocationView(wire(f))
	if !ok {
		t.Fatal("NewTeleportToLocationView: ok = false")
	}
	if v.ObjID() != 9 || v.X() != 1 || v.Y() != 2 || v.Z() != 3 || v.Flags() != 0 || v.Heading() != 4 {
		t.Errorf("поля = obj %d, %d,%d,%d, flags %d, h %d; want 9, 1,2,3, 0, 4",
			v.ObjID(), v.X(), v.Y(), v.Z(), v.Flags(), v.Heading())
	}
}

// Golden ValidateLocation (GS→C, snap-back коррекции P3.9) в обе стороны.
func TestValidateLocationGolden(t *testing.T) {
	f := movementFixture(t, "VALIDATE_LOCATION")
	var dst [ValidateLocationSize]byte
	n := WriteValidateLocation(dst[:], 3, 7, 8, 9, 10)
	if n != len(dst) || dst[0] != byte(validateLocation) {
		t.Fatalf("WriteValidateLocation = %d, op 0x%02X", n, dst[0])
	}
	if hex.EncodeToString(dst[1:]) != hex.EncodeToString(f.Payload) {
		t.Errorf("payload = %s; want %s", hex.EncodeToString(dst[1:]), hex.EncodeToString(f.Payload))
	}
	v, ok := NewValidateLocationView(wire(f))
	if !ok {
		t.Fatal("NewValidateLocationView: ok = false")
	}
	if v.ObjID() != 3 || v.X() != 7 || v.Y() != 8 || v.Z() != 9 || v.Heading() != 10 {
		t.Errorf("поля = obj %d, %d,%d,%d, h %d; want 3, 7,8,9, 10", v.ObjID(), v.X(), v.Y(), v.Z(), v.Heading())
	}
}

// Golden DeleteObject (GS→C): за objId следует D 1 (смонтированные исчезают, а
// не спешиваются) — serverpackets/DeleteObject.java.
func TestDeleteObjectGolden(t *testing.T) {
	f := movementFixture(t, "DELETE_OBJECT")
	var dst [DeleteObjectSize]byte
	n := WriteDeleteObject(dst[:], 42)
	if n != len(dst) || dst[0] != byte(deleteObject) {
		t.Fatalf("WriteDeleteObject = %d, op 0x%02X", n, dst[0])
	}
	if hex.EncodeToString(dst[1:]) != hex.EncodeToString(f.Payload) {
		t.Errorf("payload = %s; want %s", hex.EncodeToString(dst[1:]), hex.EncodeToString(f.Payload))
	}
	v, ok := NewDeleteObjectView(wire(f))
	if !ok {
		t.Fatal("NewDeleteObjectView: ok = false")
	}
	if v.ObjID() != 42 {
		t.Errorf("ObjID = %d; want 42", v.ObjID())
	}
}

// Отрицательная таблица: обрезанный кадр и хвост-мусор каждой фиксированной
// формы — детерминированный отказ/допуск конструктора, не паника; лишний
// хвост терпится (hex-дамп диспетчера), недобор — отказ.
func TestMovementViewsEvil(t *testing.T) {
	fixtures := movementFixtures(t)
	for _, c := range []struct {
		name  string
		full  []byte
		ctor  func([]byte) bool
		minLn int
	}{
		{"MOVE_TO_LOCATION", wire(fixtures["MOVE_TO_LOCATION"]),
			func(b []byte) bool { _, ok := NewMoveToLocationView(b); return ok }, MoveToLocationSize},
		{"VALIDATE_POSITION", wire(fixtures["VALIDATE_POSITION"]),
			func(b []byte) bool { _, ok := NewValidatePositionView(b); return ok }, ValidatePositionSize},
		{"CANNOT_MOVE_ANYMORE", wire(fixtures["CANNOT_MOVE_ANYMORE"]),
			func(b []byte) bool { _, ok := NewCannotMoveAnymoreView(b); return ok }, CannotMoveAnymoreSize},
		{"CHAR_MOVE_TO_LOCATION", wire(fixtures["CHAR_MOVE_TO_LOCATION"]),
			func(b []byte) bool { _, ok := NewCharMoveToLocationView(b); return ok }, CharMoveToLocationSize},
		{"STOP_MOVE", wire(fixtures["STOP_MOVE"]),
			func(b []byte) bool { _, ok := NewStopMoveView(b); return ok }, StopMoveSize},
		{"TELEPORT_TO_LOCATION", wire(fixtures["TELEPORT_TO_LOCATION"]),
			func(b []byte) bool { _, ok := NewTeleportToLocationView(b); return ok }, TeleportToLocationSize},
		{"VALIDATE_LOCATION", wire(fixtures["VALIDATE_LOCATION"]),
			func(b []byte) bool { _, ok := NewValidateLocationView(b); return ok }, ValidateLocationSize},
		{"DELETE_OBJECT", wire(fixtures["DELETE_OBJECT"]),
			func(b []byte) bool { _, ok := NewDeleteObjectView(b); return ok }, DeleteObjectSize},
	} {
		for n := 0; n < c.minLn; n++ {
			if c.ctor(c.full[:n]) {
				t.Errorf("%s: обрезано до %d байт: ok = true; want false", c.name, n)
			}
		}
		if !c.ctor(c.full) {
			t.Errorf("%s: полный кадр: ok = false; want true", c.name)
		}
		tail := append(append([]byte{}, c.full...), 0xAA, 0xBB)
		if !c.ctor(tail) {
			t.Errorf("%s: хвост-мусор: ok = false; want true (терпится до дисппетчера)", c.name)
		}
	}
}

// movementFixture — единичный вектор группы movement.
func movementFixture(t *testing.T, name string) fixture.Fixture {
	t.Helper()
	fixtures := movementFixtures(t)
	f, ok := fixtures[name]
	if !ok {
		t.Fatalf("фикстура movement/%s не найдена", name)
	}
	return f
}

// movementFixtures — все векторы группы movement по имени.
// Байты — ручной hex по канону (независимая деривация); проверено живым клиентом на КТ-3.
func movementFixtures(t *testing.T) map[string]fixture.Fixture {
	t.Helper()
	rows, err := fixture.Load("movement")
	if err != nil {
		t.Fatalf("fixture.Load(movement): %v", err)
	}
	if len(rows) != 8 {
		t.Fatalf("фикстур movement: %d; want 8", len(rows))
	}
	out := make(map[string]fixture.Fixture, len(rows))
	for _, r := range rows {
		out[r.Name] = r
	}
	return out
}
