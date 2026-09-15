package protocol

import "testing"

// Числа кадра синтетические (городской страж-образный NPC): wire-форма
// важнее конкретных значений, живой датапак подключается с населением NPC —
// тогда числа станут каноническими с атрибуцией файла шаблона.
// Группа свёрнута writer↔view (решение реестра F28); проверено живым
// клиентом на КТ-3 — пометка снимается по факту приёмки.
var tNpcInfo = NpcInfoData{
	ObjID:      268435457,
	DisplayID:  30080, // Roien-подобный городской NPC
	Attackable: false,
	X:          -71338, Y: 258271, Z: -3104,
	Heading:               0,
	MAtkSpd:               333,
	PAtkSpd:               300,
	RunSpd:                120,
	WalkSpd:               80,
	SwimRunSpd:            50,
	SwimWalkSpd:           50,
	MoveMultiplier:        1.0,
	AttackSpeedMultiplier: 1.0,
	CollisionRadius:       8.0,
	CollisionHeight:       24.0,
	RHand:                 0, Chest: 0, LHand: 0,
	Running:   false,
	InCombat:  false,
	AlikeDead: false,
	Name:      "Roien",
	Title:     "Guard",
}

// Двойная сверка NpcInfo: писатель ↔ представление (порт serverpackets/
// AbstractNpcInfo$NpcInfo @43ac8878: displayId+1000000, канонные дубли
// flyRun/Walk и collisionR/H, nameAbove=1).
func TestNpcInfoRoundtrip(t *testing.T) {
	if NpcInfoSize(tNpcInfo) != 180+LenS("Roien")+LenS("Guard") {
		t.Errorf("NpcInfoSize = %d; want %d", NpcInfoSize(tNpcInfo), 180+LenS("Roien")+LenS("Guard"))
	}
	dst := make([]byte, NpcInfoSize(tNpcInfo))
	n := WriteNpcInfo(dst, tNpcInfo)
	if n != len(dst) || dst[0] != byte(npcInfo) {
		t.Fatalf("WriteNpcInfo = %d, op 0x%02X; want %d", n, dst[0], len(dst))
	}
	// Канон: по проводу едет displayId + 1 000 000.
	if got := leD(dst, 5); got != 30080+1000000 {
		t.Errorf("wire displayId = %d; want %d", got, 30080+1000000)
	}
	v, ok := NewNpcInfoView(dst)
	if !ok {
		t.Fatal("NewNpcInfoView: ok = false")
	}
	if v.ObjID() != tNpcInfo.ObjID || v.DisplayID() != 30080+1000000 {
		t.Errorf("objID/displayId = %d/%d; want %d/%d", v.ObjID(), v.DisplayID(), tNpcInfo.ObjID, 30080+1000000)
	}
	if v.Attackable() {
		t.Error("Attackable = true; want false")
	}
	if v.X() != tNpcInfo.X || v.Y() != tNpcInfo.Y || v.Z() != tNpcInfo.Z {
		t.Errorf("позиция = %d,%d,%d; want стартовую", v.X(), v.Y(), v.Z())
	}
	if name, ok := v.Name(); !ok || name != "Roien" {
		t.Errorf("Name = %q, %v; want Roien", name, ok)
	}
	if title, ok := v.Title(); !ok || title != "Guard" {
		t.Errorf("Title = %q, %v; want Guard", title, ok)
	}
	// Обрезка хвоста — отказ конструктора.
	for cut := 1; cut <= 58; cut += 3 {
		if _, ok := NewNpcInfoView(dst[:len(dst)-cut]); ok {
			t.Fatalf("хвост короче на %d Б: ok = true; want false", cut)
		}
	}
	// Нетерминированная строка — отказ: заливаем от начала поля имени до
	// конца кадра байтами без нулевых юнитов (нули хвоста иначе дают ложный
	// терминатор — частичный разбор по конвенции doc.go).
	noTerm := append([]byte{}, dst...)
	for i := npcInfoHead; i < len(noTerm); i++ {
		noTerm[i] = 0x41
	}
	if _, ok := NewNpcInfoView(noTerm); ok {
		t.Error("нетерминированное имя: ok = true; want false")
	}
}

// WriteNpcInfo — 0 аллокаций (репликация join-AoI, клеймо реестра ADR-0005).
func TestWriteNpcInfoZeroAllocs(t *testing.T) {
	d := tNpcInfo
	dst := make([]byte, NpcInfoSize(d))
	allocs := testing.AllocsPerRun(100, func() { _ = WriteNpcInfo(dst, d) })
	if allocs != 0 {
		t.Errorf("WriteNpcInfo: %v аллокаций; want 0", allocs)
	}
}
