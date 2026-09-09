package replica

import (
	"fmt"
	"testing"

	"github.com/udisondev/l2go/internal/transport"
)

type fakeAdvisory struct{}

func (fakeAdvisory) Snapshot(CellID, transport.EntityID) (Snapshot, bool) {
	return Snapshot{}, false
}

// compile-time контракт предписанного шва.
var _ Advisory = fakeAdvisory{}

func TestSnapshotAccessors(t *testing.T) {
	s := Snapshot{entity: 9, epoch: 4}
	if s.Entity() != 9 || s.Epoch() != 4 {
		t.Errorf("Snapshot accessors = (%d, %d); want (9, 4)", s.Entity(), s.Epoch())
	}
}

func ExampleSnapshot() {
	s := Snapshot{entity: 9, epoch: 4}
	fmt.Println(s.Entity(), s.Epoch())
	// Output: 9 4
}

func TestMembershipHeader(t *testing.T) {
	h := MembershipHeader{Generation: 6, Moving: []transport.EntityID{1, 2}}
	if h.Generation != 6 || len(h.Moving) != 2 {
		t.Errorf("MembershipHeader = %+v; want генерация 6, 2 переезжающих", h)
	}
}

func ExampleGroundItem() {
	g := GroundItem{ID: 5, X: 10, Y: 20, Z: 30, TemplateID: 1060, Count: 1}
	fmt.Println(g.ID, g.TemplateID, g.Count)
	// Output: 5 1060 1
}
