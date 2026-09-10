package protocol

import "testing"

// attackViewBytes — пакет Attack из вектора интерлюд-теста (см. attack_test.go).
func attackViewBytes(extra ...byte) []byte {
	b := []byte{0x05,
		100, 0, 0, 0, // attackerID
		0xC8, 0, 0, 0, // targetID 200
		0xDC, 0x05, 0, 0, // damage 1500
		0x20,                                  // flags HitFlagCrit
		10, 0, 0, 0, 20, 0, 0, 0, 30, 0, 0, 0, // atkXYZ
		0, 0, // extraHits = 0
		40, 0, 0, 0, 50, 0, 0, 0, 60, 0, 0, 0, // tgtXYZ
	}
	return append(b, extra...)
}

func TestNewAttackView(t *testing.T) {
	full := attackViewBytes()
	if _, ok := NewAttackView(full[:len(full)-1]); ok {
		t.Error("NewAttackView(39 байт): ok=true; want false")
	}
	v, ok := NewAttackView(full)
	if !ok {
		t.Fatal("NewAttackView(40 байт): ok=false")
	}
	if v.AttackerID() != 100 || v.TargetID() != 200 || v.Damage() != 1500 {
		t.Errorf("геттеры ID/урона: %d/%d/%d; want 100/200/1500",
			v.AttackerID(), v.TargetID(), v.Damage())
	}
	if v.Flags() != HitFlagCrit {
		t.Errorf("Flags = 0x%02X; want 0x20", v.Flags())
	}
	if v.AttackX() != 10 || v.AttackY() != 20 || v.AttackZ() != 30 {
		t.Errorf("atkXYZ = %d/%d/%d; want 10/20/30", v.AttackX(), v.AttackY(), v.AttackZ())
	}
	if v.ExtraHits() != 0 {
		t.Errorf("ExtraHits = %d; want 0", v.ExtraHits())
	}
	if v.TargetX() != 40 || v.TargetY() != 50 || v.TargetZ() != 60 {
		t.Errorf("tgtXYZ = %d/%d/%d; want 40/50/60", v.TargetX(), v.TargetY(), v.TargetZ())
	}
}

// Длинный пакет (extraHits > 0) принимается; ExtraHits читается.
func TestAttackViewExtraHits(t *testing.T) {
	// первый hit + один extra {targetID, damage, flags}
	extra := append([]byte{},
		0x2C, 0x01, 0, 0, // targetID 300
		0x10, 0x27, 0, 0, // damage 10000
		0x00, // flags
	)
	b := attackViewBytes()
	b[26] = 1                                                     // extraHits (H на офсете 26, LE)
	b = append(b[:len(b)-12], append(extra, b[len(b)-12:]...)...) // extra перед tgtXYZ
	v, ok := NewAttackView(b)
	if !ok {
		t.Fatal("NewAttackView(многоударный): ok=false")
	}
	if v.ExtraHits() != 1 {
		t.Errorf("ExtraHits = %d; want 1", v.ExtraHits())
	}
}

// Нулевое значение представления недействительно — конструктор обязателен.
func TestAttackViewZeroInvalid(t *testing.T) {
	var v AttackView
	if _, ok := NewAttackView(nil); ok {
		t.Error("NewAttackView(nil): ok=true")
	}
	_ = v // доступ к полям нулевого представления запрещён контрактом
}
