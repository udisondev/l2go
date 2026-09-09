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
