package world

import (
	"fmt"
	"testing"

	"github.com/udisondev/l2go/internal/transport"
)

func ExamplePortion() {
	p := Portion{Region: 3, Tick: 100, Envs: []transport.Envelope{{FromID: 1}}}
	fmt.Println(p.Region, p.Tick, len(p.Envs))
	// Output: 3 100 1
}

func TestSuitcaseCarriesWholeState(t *testing.T) {
	e := &Entity{
		ID:        7,
		Owner:     2,
		Pos:       Position{X: 1, Y: 2, Z: 3},
		Dead:      true,
		Servants:  [4]ServantSlot{{Alive: true}},
		Transfers: []TransferRecord{{ID: 9, Phase: 2}},
	}
	s := Suitcase{Entity: e, Cursor: 55, Attempt: 3}
	if s.Entity != e || s.Cursor != 55 || s.Attempt != 3 {
		t.Errorf("Suitcase = %+v; want состояние по указателю, курсор 55, попытка 3", s)
	}
	if s.Entity.Servants[0].Alive != true || s.Entity.Transfers[0].ID != 9 {
		t.Errorf("состояние в чемодане неполно: %+v", s.Entity)
	}
}

func ExampleEntity() {
	e := Entity{ID: 7, Owner: 2, Pos: Position{X: 1, Y: 2, Z: 3}}
	fmt.Println(e.ID, e.Owner, e.Pos.X, e.Pos.Z)
	// Output: 7 2 1 3
}
