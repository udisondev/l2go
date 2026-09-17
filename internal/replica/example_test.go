package replica_test

import (
	"fmt"

	"github.com/udisondev/l2go/internal/replica"
	"github.com/udisondev/l2go/internal/transport"
)

// Пример каркаса: публикация населения, событийный ввод наблюдателя и единый
// предикат пары.
func Example() {
	cfg := replica.CanonJoinConfig()
	p := replica.NewPublisher()
	j := replica.NewJoin(cfg)

	obs := replica.Record{Entity: 1, X: 0, Y: 0, Kind: replica.RecordKindPlayer}
	target := replica.Record{Entity: 2, X: 100, Y: 0, Kind: replica.RecordKindPlayer}
	blob := p.Build([]replica.Record{obs, target})
	events := j.Step([]replica.Observer{{Entity: 1, ConnID: 7}}, blob)
	j.Apply()
	p.Commit(blob)
	for _, ev := range events {
		fmt.Println(ev.Obs.ConnID, ev.Kind, ev.Target.Entity, replica.Visible(0, ev.Target.Flags))
	}
	// Output:
	// 7 0 2 true
	_ = transport.EntityID(0)
}
