package gateway

import (
	"fmt"
	"testing"

	"github.com/udisondev/l2go/internal/conn"
)

func TestInboundFields(t *testing.T) {
	in := Inbound{Conn: conn.ConnID(2), Tick: 7}
	if in.Conn != 2 || in.Tick != 7 {
		t.Errorf("Inbound = %+v; want Conn=2 Tick=7", in)
	}
}

func ExampleInbound() {
	in := Inbound{Conn: conn.ConnID(2), Tick: 7}
	fmt.Println(in.Conn, in.Tick)
	// Output: 2 7
}
