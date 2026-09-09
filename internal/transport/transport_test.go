package transport

import "testing"

func TestEnvelopeServantAddr(t *testing.T) {
	env := Envelope{
		To:      Addr{Entity: 42, Slot: 1},
		FromID:  7,
		Class:   ClassReliable,
		Attrs:   AttrBound,
		Payload: []byte{0x01},
	}
	if env.To.Entity != 42 || env.To.Slot != 1 {
		t.Errorf("To = (%d, %d); want (42, 1)", env.To.Entity, env.To.Slot)
	}
	if env.Class != ClassReliable {
		t.Errorf("Class = %d; want %d", env.Class, ClassReliable)
	}
}
