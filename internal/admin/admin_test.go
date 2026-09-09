package admin

import "testing"

func TestStatusZeroValue(t *testing.T) {
	var s Status
	if s.Ticks != 0 || s.PlayersOnline != 0 {
		t.Errorf("нулевое значение Status не пригодно: %+v", s)
	}
}
