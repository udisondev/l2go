package gateway

import (
	"fmt"

	"github.com/udisondev/l2go/internal/conn"
)

func ExampleInbound() {
	in := Inbound{Conn: conn.ConnID(2), Tick: 7}
	fmt.Println(in.Conn, in.Tick)
	// Output: 2 7
}
