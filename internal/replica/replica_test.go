package replica

import (
	"testing"

	"github.com/udisondev/l2go/internal/transport"
)

type fakeAdvisory struct{}

func (fakeAdvisory) Snapshot(CellID, transport.EntityID) (Snapshot, bool) {
	return Snapshot{}, false
}

func TestAdvisoryContract(t *testing.T) {
	// compile-time контракт: интерфейс удовлетворяется читателем-заглушкой.
	var _ Advisory = fakeAdvisory{}
	s := Snapshot{entity: 9, epoch: 4}
	if s.Entity() != 9 || s.Epoch() != 4 {
		t.Errorf("Snapshot accessors = (%d, %d); want (9, 4)", s.Entity(), s.Epoch())
	}
}
