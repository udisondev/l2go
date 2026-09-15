package world

import (
	"fmt"

	"github.com/udisondev/l2go/internal/transport"
)

func ExamplePortion() {
	p := Portion{Region: 3, Tick: 100, Envs: []transport.Envelope{{FromID: 1}}}
	fmt.Println(p.Region, p.Tick, len(p.Envs))
	// Output: 3 100 1
}

func ExampleEntity() {
	e := Entity{ID: 7, Owner: 2, Pos: Position{X: 1, Y: 2, Z: 3}}
	fmt.Println(e.ID, e.Owner, e.Pos.X, e.Pos.Z)
	// Output: 7 2 1 3
}
