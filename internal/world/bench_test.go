package world

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/udisondev/l2go/internal/transport"
)

// newBenchRegion — регион с населением и логом во временном каталоге; шаги
// гоняются напрямую (без горутины: single-writer — тест-горутина).
func newBenchRegion(b *testing.B, cfg Config, population int) *Region {
	b.Helper()
	m, err := NewMetronome(cfg)
	if err != nil {
		b.Fatalf("NewMetronome: %v", err)
	}
	reg := transport.NewRegistry(0)
	log, err := NewPortionLog(b.TempDir(), 1, m.period, cfg.LogPayloads, 1<<20)
	if err != nil {
		b.Fatalf("NewPortionLog: %v", err)
	}
	r, err := NewRegion(m, reg, 1, cfg, log)
	if err != nil {
		b.Fatalf("NewRegion: %v", err)
	}
	b.Cleanup(func() { _ = log.Close() })
	for i := 0; i < population; i++ {
		if _, err := r.Spawn(Entity{Owner: 1, HP: 100}); err != nil {
			b.Fatalf("Spawn: %v", err)
		}
	}
	return r
}

// deliverLoad — доставка per писем каждому жителю плюс контрольная смесь
// (каждое 16-е письмо — контрольное в ящик региона; приоритетная ветвь K и
// общий список в цене) — по критерию F7.
func deliverLoad(r *Region, per int) {
	n := 0
	for _, res := range r.residents {
		for j := 0; j < per; j++ {
			if n%16 == 0 {
				r.reg.Send(transport.Envelope{To: transport.Addr{Entity: r.ctrlID}, FromID: 5, Kind: transport.KindEnterWorld})
			} else {
				r.reg.Send(transport.Envelope{To: transport.Addr{Entity: res.ent.ID}, FromID: 5, Kind: transport.KindAggro})
			}
			n++
		}
	}
}

// BenchmarkRegionTick — тик региона целиком (шаг без метронома): под-кейсы
// населения 100/1000/10000 × нагрузки Idle/Load1/Load10 (писем/сущность за
// шаг, доставка входит — реальная стоимость дрена) и оба режима payloads на
// Load1. Реестр «Тик региона / SoA-проход».
func BenchmarkRegionTick(b *testing.B) {
	base := DefaultConfig()
	for _, population := range []int{100, 1000, 10000} {
		for _, load := range []int{0, 1, 10} {
			payloadCases := []bool{false}
			if load == 1 {
				payloadCases = append(payloadCases, true)
			}
			for _, payloads := range payloadCases {
				cfg := base
				cfg.LogPayloads = payloads
				name := fmt.Sprintf("pop=%d/load=%d/payloads=%v", population, load, payloads)
				b.Run(name, func(b *testing.B) {
					r := newBenchRegion(b, cfg, population)
					b.ReportAllocs()
					for b.Loop() {
						if load > 0 {
							deliverLoad(r, load)
						}
						r.metro.tick.Add(1)
						r.step()
					}
				})
			}
		}
	}
}

func ExampleFold() {
	st := &State{}
	ents := []*Entity{{ID: 1, Owner: 1}}
	rng := rand.New(rand.NewPCG(1, 100))
	res := Fold(100, 2, rng, st, ents,
		[]Portion{{Region: 1, Tick: 100, Envs: []transport.Envelope{{Kind: transport.KindXP}}}}, nil)
	fmt.Println(len(res.Out), st.Steps, st.Letters, st.LastDelta, ents[0].Beat)
	// Output: 0 1 1 2 100
}

// Аллокационный бюджет Idle-шага: пустые ящики, дрен и лог-энкод — 0 аллокаций;
// допустимые — RNG шага (rand.New над PCG) и публикация снапшота (escape).
// Бюджет механически фиксирует случайный regress (perf.md: аллокации — первый
// враг тика).
func TestRegionStepIdleAllocBudget(t *testing.T) {
	cfg := DefaultConfig()
	m, err := NewMetronome(cfg)
	if err != nil {
		t.Fatalf("NewMetronome: %v", err)
	}
	reg := transport.NewRegistry(0)
	log, err := NewPortionLog(t.TempDir(), 1, m.period, false, 1<<20)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
	}
	r, err := NewRegion(m, reg, 1, cfg, log)
	if err != nil {
		t.Fatalf("NewRegion: %v", err)
	}
	defer log.Close()
	for i := 0; i < 100; i++ {
		if _, err := r.Spawn(Entity{Owner: 1, HP: 100}); err != nil {
			t.Fatalf("Spawn: %v", err)
		}
	}
	for i := 0; i < 10; i++ { // прогрев ёмкостей буферов
		m.tick.Add(1)
		r.step()
	}
	allocs := testing.AllocsPerRun(200, func() {
		m.tick.Add(1)
		r.step()
	})
	t.Logf("Idle-шаг: %.0f аллокаций", allocs)
	// точная раскладка: rand.New(PCG) = 1, публикация снапшота = 1; всё прочее
	// (дрен, лог-кадр) — 0 по построению; изменение числа — regress или
	// осознанная правка бюджета
	if allocs != 2 {
		t.Fatalf("аллокаций на Idle-шаг = %.0f; want 2 (rand.New + снапшот)", allocs)
	}
}
