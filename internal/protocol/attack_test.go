package protocol

import (
	"testing"

	"github.com/udisondev/l2go/internal/protocol/fixture"
)

// Писатель Attack: golden против внешней фикстуры (байты собраны из формата
// Mobius Attack.java и вектора полей интерлюд-теста — не нашим писателем).
func TestWriteAttackGolden(t *testing.T) {
	fixtures, err := fixture.Load("attack")
	if err != nil {
		t.Fatalf("fixture.Load(attack): %v", err)
	}
	if len(fixtures) != 1 {
		t.Fatalf("фикстур attack: %d; want 1", len(fixtures))
	}
	f := fixtures[0]
	if f.Name != "ATTACK" || f.Dir != fixture.GameServer {
		t.Fatalf("фикстура: %s/%s; want ATTACK/game-server", f.Dir, f.Name)
	}

	var dst [40]byte
	n := WriteAttack(dst[:], 100,
		AttackHit{TargetID: 200, Damage: 1500, Flags: HitFlagCrit},
		10, 20, 30, 40, 50, 60)
	if n != 40 {
		t.Fatalf("WriteAttack = %d байт; want 40", n)
	}
	if dst[0] != byte(attack) {
		t.Errorf("опкод = 0x%02X; want 0x%02X", dst[0], byte(attack))
	}
	if hexStr(dst[1:]) != hexStr(f.Payload) {
		t.Errorf("payload = %s; want (fixture) %s", hexStr(dst[1:]), hexStr(f.Payload))
	}
}

// Стековый писатель — 0 аллокаций (путь реестра ADR-0005 «пакет-писатель»).
func TestWriteAttackZeroAllocs(t *testing.T) {
	var dst [40]byte
	hit := AttackHit{TargetID: 1, Damage: 2, Flags: HitFlagCrit}
	allocs := testing.AllocsPerRun(100, func() {
		_ = WriteAttack(dst[:], 100, hit, 1, 2, 3, 4, 5, 6)
	})
	if allocs != 0 {
		t.Errorf("WriteAttack: %v аллокаций; want 0", allocs)
	}
}
