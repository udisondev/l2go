package protocol

import "testing"

// Тестовые данные видимости «второго клиента»: числа новичка Human Fighter —
// константы канона HumanFighter.xml @43ac8878 (коллизии — male 9.0/23.0).
// Группа свёрнута writer↔view (решение реестра F28); проверено живым
// клиентом на КТ-3 — пометка снимается по факту приёмки.
var tCharInfo = CharInfoData{
	X: -71338, Y: 258271, Z: -3104,
	ObjID:                 268435456,
	Name:                  "Vasya",
	Race:                  0,
	Female:                false,
	BaseClass:             0,
	MAtkSpd:               333,
	PAtkSpd:               300,
	RunSpd:                115,
	WalkSpd:               80,
	SwimRunSpd:            50,
	SwimWalkSpd:           50,
	MoveMultiplier:        1.0,
	AttackSpeedMultiplier: 1.0,
	CollisionRadius:       9.0,
	CollisionHeight:       23.0,
	HairStyle:             0, HairColor: 0, Face: 0,
	Title:    "",
	Standing: true,
	Running:  true,
	ClassID:  0,
	MaxCp:    32,
	CurCp:    32,
	Heading:  8191,
}

// Двойная сверка CharInfo: писатель ↔ представление на каноническом лэйауте
// (порт serverpackets/CharInfo.java @43ac8878: 12×D paperdoll с канонным
// дублем RHAND, c6-блок 4H+D+12H+D+4H, дубль pvpFlag/karma и flyRun/Walk).
func TestCharInfoRoundtrip(t *testing.T) {
	if CharInfoSize(tCharInfo) != 323+LenS("Vasya")+LenS("") {
		t.Errorf("CharInfoSize = %d; want %d", CharInfoSize(tCharInfo), 323+LenS("Vasya")+LenS(""))
	}
	dst := make([]byte, CharInfoSize(tCharInfo))
	n := WriteCharInfo(dst, tCharInfo)
	if n != len(dst) || dst[0] != byte(charInfo) {
		t.Fatalf("WriteCharInfo = %d, op 0x%02X; want %d", n, dst[0], len(dst))
	}
	v, ok := NewCharInfoView(dst)
	if !ok {
		t.Fatal("NewCharInfoView: ok = false")
	}
	if v.ObjID() != tCharInfo.ObjID {
		t.Errorf("ObjID = %d; want %d", v.ObjID(), tCharInfo.ObjID)
	}
	if v.X() != tCharInfo.X || v.Y() != tCharInfo.Y || v.Z() != tCharInfo.Z {
		t.Errorf("позиция = %d,%d,%d; want %d,%d,%d", v.X(), v.Y(), v.Z(), tCharInfo.X, tCharInfo.Y, tCharInfo.Z)
	}
	if name, ok := v.Name(); !ok || name != "Vasya" {
		t.Errorf("Name = %q, %v; want Vasya", name, ok)
	}
	if v.Race() != 0 || v.RunSpd() != 115 {
		t.Errorf("race/runSpd = %d/%d; want 0/115 (гонялся баг фиксированного офсета Race)", v.Race(), v.RunSpd())
	}
	if v.ClassID() != 0 || v.Heading() != 8191 {
		t.Errorf("classID/heading = %d/%d; want 0/8191", v.ClassID(), v.Heading())
	}
	// Обрезка хвоста на каждый байт — отказ конструктора.
	for cut := 1; cut <= 94; cut++ {
		if _, ok := NewCharInfoView(dst[:len(dst)-cut]); ok {
			t.Fatalf("хвост короче на %d Б: ok = true; want false", cut)
		}
	}
}

// CharInfo с непустым титулом: смещение хвоста сдвигается, разбор остаётся
// корректным; нетерминированное имя — отказ.
func TestCharInfoTitleAndEvil(t *testing.T) {
	d := tCharInfo
	d.Title = "Нубопроводчик"
	dst := make([]byte, CharInfoSize(d))
	if WriteCharInfo(dst, d) != len(dst) {
		t.Fatalf("WriteCharInfo с титулом: размер")
	}
	v, ok := NewCharInfoView(dst)
	if !ok {
		t.Fatal("NewCharInfoView: ok = false")
	}
	if v.ClassID() != 0 || v.Heading() != d.Heading {
		t.Errorf("хвост при длинном титуле сместился неверно: classID/heading = %d/%d", v.ClassID(), v.Heading())
	}
	noTerm := append([]byte{}, dst...)
	for i := 22; i+1 < len(noTerm); i += 2 {
		if noTerm[i] == 0 && noTerm[i+1] == 0 {
			noTerm[i] = 0x42
		}
	}
	if _, ok := NewCharInfoView(noTerm); ok {
		t.Error("нетерминированное имя: ok = true; want false")
	}
}

// WriteCharInfo — 0 аллокаций (горячий путь репликации, клеймо реестра
// ADR-0005; прецедент TestWriteAttackZeroAllocs).
func TestWriteCharInfoZeroAllocs(t *testing.T) {
	d := tCharInfo
	dst := make([]byte, CharInfoSize(d))
	allocs := testing.AllocsPerRun(100, func() { _ = WriteCharInfo(dst, d) })
	if allocs != 0 {
		t.Errorf("WriteCharInfo: %v аллокаций; want 0", allocs)
	}
}
