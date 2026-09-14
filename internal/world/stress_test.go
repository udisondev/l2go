package world

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/udisondev/l2go/internal/transport"
)

// Стресс -race: несколько отправителей против тикающего региона. Reliable —
// применены ровно по разу (живому не дропаются); FAF — применены + классово
// дропнуты = отправлено; финальных дропов reliable нет (инцидент-класс).
func TestRegionStressSenders(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Hz = 1000
	reg := transport.NewRegistry(0)
	m, err := NewMetronome(cfg)
	if err != nil {
		t.Fatalf("NewMetronome: %v", err)
	}
	log, err := NewPortionLog(t.TempDir(), 1, m.period, false, 1<<20)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
	}
	r, err := NewRegion(m, reg, 1, cfg, log)
	if err != nil {
		t.Fatalf("NewRegion: %v", err)
	}
	const population = 30
	var ids []transport.EntityID
	for i := 0; i < population; i++ {
		id, err := r.Spawn(Entity{Owner: 1, HP: 100})
		if err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		ids = append(ids, id)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); m.Run(ctx) }()
	go func() { defer wg.Done(); r.Run(ctx) }()

	const senders, perSender = 8, 300
	var sentReliable, sentFAF, sentCtrl atomic.Int64
	var swg sync.WaitGroup
	for s := 0; s < senders; s++ {
		swg.Add(1)
		go func(seed int) {
			defer swg.Done()
			for i := 0; i < perSender; i++ {
				id := ids[(seed+i)%population]
				switch i % 3 {
				case 0:
					reg.Send(transport.Envelope{To: transport.Addr{Entity: id}, FromID: 5, Kind: transport.KindAggro})
					sentReliable.Add(1)
				case 1:
					reg.Send(transport.Envelope{To: transport.Addr{Entity: id}, FromID: 5, Kind: transport.KindClientFrame})
					sentFAF.Add(1)
				default:
					reg.Send(transport.Envelope{To: transport.Addr{Entity: r.ctrlID}, FromID: 5, Kind: transport.KindEnterWorld})
					sentCtrl.Add(1)
				}
			}
		}(s)
	}
	swg.Wait()

	// затишье: все глубины нулевые (drainBudget не исчерпывается при таком потоке);
	// глубины — атомики ящиков, население стабильно (записи закрыты стартом Run)
	deadline := time.After(5 * time.Second)
	for r.depthTotal() != 0 {
		select {
		case <-deadline:
			cancel()
			wg.Wait()
			t.Fatalf("затишье не наступило: глубина %d", r.depthTotal())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	wg.Wait()

	appliedReliable := r.state.KindCounts[transport.KindAggro-1]
	if want := uint64(sentReliable.Load()); appliedReliable != want {
		t.Errorf("reliable применено %d; want %d (ровно по разу)", appliedReliable, want)
	}
	appliedCtrl := r.state.KindCounts[transport.KindEnterWorld-1]
	if want := uint64(sentCtrl.Load()); appliedCtrl != want {
		t.Errorf("контрольные применено %d; want %d", appliedCtrl, want)
	}
	appliedFAF := r.state.KindCounts[transport.KindClientFrame-1]
	droppedFAF := r.ctrl.Stats().DroppedFAF
	if got, want := appliedFAF+uint64(droppedFAF), uint64(sentFAF.Load()); got != want {
		t.Errorf("FAF применено+дропнуто %d; want %d (применено %d, дропнуто %d)", got, want, appliedFAF, droppedFAF)
	}
	if st := r.ctrl.Stats(); st.FinalReliable != 0 {
		t.Errorf("финальные дропы reliable = %d (инцидент)", st.FinalReliable)
	}
	if st := r.Stats(); st.Failed != 0 || st.Frozen {
		t.Errorf("регион деградировал: %+v", st)
	}
}
