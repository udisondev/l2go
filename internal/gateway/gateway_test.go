package gateway

import (
	"testing"

	"github.com/udisondev/l2go/internal/net"
)

type fakeGateway struct{}

func (fakeGateway) Assign(Inbound) {}

func TestGatewayContract(t *testing.T) {
	var _ Gateway = fakeGateway{}
	in := Inbound{Conn: net.ConnID(2), Tick: 7}
	if in.Conn != 2 || in.Tick != 7 {
		t.Errorf("Inbound = %+v; want Conn=2 Tick=7", in)
	}
}
