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
	r, err := NewRegion(m, reg, 1, cfg, log, nullPusher{})
	if err != nil {
		t.Fatalf("NewRegion: %v", err)
	}
	if err := r.Wire(901, 900); err != nil {
		t.Fatalf("Wire: %v", err)
	}
	const population = 30
	var ids []transport.EntityID
	for range population {
		id, err := r.Spawn(Entity{Owner: 1, HP: 100})
		if err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		ids = append(ids, id)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var wg sync.WaitGroup
	wg.Go(func() { m.Run(ctx) })
	wg.Go(func() { r.Run(ctx) })

	const senders, perSender = 8, 300
	var sentReliable, sentFAF, sentCtrl atomic.Int64
	var swg sync.WaitGroup
	for s := range senders {
		swg.Go(func() {
			for i := range perSender {
				id := ids[(s+i)%population]
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
		})
	}
	// целевая волна сверх FAF-капа одного ящика (1024): классовый дроп обязателен
	for range 2048 {
		reg.Send(transport.Envelope{To: transport.Addr{Entity: ids[0]}, FromID: 5, Kind: transport.KindClientFrame})
	}
	sentFAF.Add(2048)
	swg.Wait()

	// затишье: все глубины нулевые (drainBudget не исчерпывается при таком потоке);
	// глубины — атомики ящиков, население стабильно (записи закрыты стартом Run)
	deadline := time.After(5 * time.Second)
	for depthTotal(r) != 0 {
		select {
		case <-deadline:
			cancel()
			wg.Wait()
			t.Fatalf("затишье не наступило: глубина %d", depthTotal(r))
		default:
			time.Sleep(5 * time.Millisecond)
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
	// FAF-дропы ящиков жителей: кап FAF пробивается целевой волной (одиночные
	// дропы невозможны — ящики дренятся), метрики собираются после остановки
	for _, res := range r.residents {
		droppedFAF += res.box.Stats().DroppedFAF
	}
	if got, want := appliedFAF+uint64(droppedFAF), uint64(sentFAF.Load()); got != want {
		t.Errorf("FAF применено+дропнуто %d; want %d (применено %d, дропнуто %d)", got, want, appliedFAF, droppedFAF)
	}
	if droppedFAF == 0 && !raceEnabled {
		t.Errorf("классовый FAF-дроп не фальсифицирован: волна сверх капа не дропнулась")
	}
	if st := r.ctrl.Stats(); st.FinalReliable != 0 {
		t.Errorf("финальные дропы reliable = %d (инцидент)", st.FinalReliable)
	}
	if st := r.Stats(); st.Failed != 0 || st.Frozen {
		t.Errorf("регион деградировал: %+v", st)
	}
}

// depthTotal — сумма глубин ящиков региона (метрика затишья; население
// стабильно — записи закрыты стартом Run, глубины — атомики).
func depthTotal(r *Region) int64 {
	total := r.ctrl.Depth()
	for _, res := range r.residents {
		total += res.box.Depth()
	}
	return total
}
