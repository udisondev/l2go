package protocol

import "testing"

func TestWriterConventionSample(t *testing.T) {
	var dst [64]byte
	if n := WriteAttack(dst[:], 1, 2); n != 0 {
		t.Errorf("WriteAttack = %d; want 0 (стаб до фазы 1)", n)
	}
	if v := (AttackView)(nil).TargetID(); v != 0 {
		t.Errorf("TargetID = %d; want 0 (стаб до фазы 1)", v)
	}
}
