package svc

import "testing"

func TestServiceKinds(t *testing.T) {
	s := Service{Kind: KindParty, ID: 100}
	if s.Kind != KindParty || s.ID != 100 {
		t.Errorf("Service = %+v; want {KindParty, 100}", s)
	}
}
