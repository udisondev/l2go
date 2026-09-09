package world

import (
	"testing"

	"github.com/udisondev/l2go/internal/transport"
)

func TestPortionHoldsEnvelopes(t *testing.T) {
	p := Portion{
		Region: 3,
		Tick:   100,
		Envs:   []transport.Envelope{{FromID: 1}},
	}
	if len(p.Envs) != 1 || p.Envs[0].FromID != 1 {
		t.Errorf("Envs = %v; want один конверт от 1", p.Envs)
	}
}
