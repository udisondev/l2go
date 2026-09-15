package protocol

import "testing"

// Данные слитка входа: новичок Human Fighter, статы — константы канона
// HumanFighter.xml @43ac8878; производные (PDef/MDef/уклонение/точность) —
// плейсхолдеры (неканоническая калькуляция, точные формулы — с потребителем).
// Группа свёрнута writer↔view (решение реестра F28); проверено живым
// клиентом на КТ-3 — пометка снимается по факту приёмки.
var tUserInfo = UserInfoData{
	X: -71338, Y: 258271, Z: -3104,
	ObjID:     268435456,
	Name:      "Vasya",
	Race:      0,
	Female:    false,
	BaseClass: 0,
	Level:     1,
	Exp:       0,
	Str:       40, Dex: 30, Con: 43, Int: 21, Wit: 11, Men: 25,
	MaxHp: 80, CurHp: 80, MaxMp: 30, CurMp: 30,
	Sp:      0,
	CurLoad: 0, MaxLoad: 0,
	HasWeapon: false,
	PAtk:      4, PAtkSpd: 300, PDef: 80, Evasion: 0, Accuracy: 0, Crit: 4,
	MAtk: 6, MAtkSpd: 333, MDef: 41,
	RunSpd: 115, WalkSpd: 80, SwimRunSpd: 50, SwimWalkSpd: 50,
	MoveMultiplier:        1.0,
	AttackSpeedMultiplier: 1.0,
	CollisionRadius:       9.0,
	CollisionHeight:       23.0,
	HairStyle:             0, HairColor: 0, Face: 0,
	Title:   "",
	ClassID: 0,
	MaxCp:   32,
	CurCp:   32,
	Running: true,
}

// Двойная сверка UserInfo: писатель ↔ представление (порт serverpackets/
// UserInfo.java @43ac8878: paperdoll objectId ×17 + displayId ×17 — RHAND
// дважды в обоих, c6-блок 14H+D+12H+D+4H, дубль pAtkSpd в боевом ряду,
// дубль flyRun/Walk).
func TestUserInfoRoundtrip(t *testing.T) {
	t.Parallel()

	if UserInfoSize(tUserInfo) != 544+LenS("Vasya")+LenS("") {
		t.Errorf("UserInfoSize = %d; want %d", UserInfoSize(tUserInfo), 544+LenS("Vasya")+LenS(""))
	}
	dst := make([]byte, UserInfoSize(tUserInfo))
	n := WriteUserInfo(dst, tUserInfo)
	if n != len(dst) || dst[0] != byte(userInfo) {
		t.Fatalf("WriteUserInfo = %d, op 0x%02X; want %d", n, dst[0], len(dst))
	}
	v, ok := NewUserInfoView(dst)
	if !ok {
		t.Fatal("NewUserInfoView: ok = false")
	}
	if v.ObjID() != tUserInfo.ObjID || v.Level() != 1 {
		t.Errorf("objID/level = %d/%d; want %d/1", v.ObjID(), v.Level(), tUserInfo.ObjID)
	}
	if v.X() != tUserInfo.X || v.Y() != tUserInfo.Y || v.Z() != tUserInfo.Z {
		t.Errorf("позиция = %d,%d,%d; want стартовую", v.X(), v.Y(), v.Z())
	}
	if name, ok := v.Name(); !ok || name != "Vasya" {
		t.Errorf("Name = %q, %v; want Vasya", name, ok)
	}
	if hp, ok := v.CurHP(); !ok || hp != 80 {
		t.Errorf("CurHP = %d, %v; want 80", hp, ok)
	}
	if mp, ok := v.CurMP(); !ok || mp != 30 {
		t.Errorf("CurMP = %d, %v; want 30", mp, ok)
	}
	if v.ClassID() != 0 {
		t.Errorf("ClassID = %d; want 0", v.ClassID())
	}
	// Обрезка хвоста — отказ конструктора.
	for cut := 1; cut <= 111; cut += 3 {
		if _, ok := NewUserInfoView(dst[:len(dst)-cut]); ok {
			t.Fatalf("хвост короче на %d Б: ok = true; want false", cut)
		}
	}
}

// WriteUserInfo — 0 аллокаций (слиток входа P3.7, клеймо реестра ADR-0005).
func TestWriteUserInfoZeroAllocs(t *testing.T) {
	d := tUserInfo
	dst := make([]byte, UserInfoSize(d))
	allocs := testing.AllocsPerRun(100, func() { _ = WriteUserInfo(dst, d) })
	if allocs != 0 {
		t.Errorf("WriteUserInfo: %v аллокаций; want 0", allocs)
	}
}
