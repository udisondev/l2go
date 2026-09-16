package world

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
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
	r, err := NewRegion(m, reg, 1, cfg, log, nullPusher{})
	if err != nil {
		b.Fatalf("NewRegion: %v", err)
	}
	b.Cleanup(func() { _ = log.Close() })
	for range population {
		if _, err := r.Spawn(Entity{Owner: 1, HP: 100}); err != nil {
			b.Fatalf("Spawn: %v", err)
		}
	}
	return r
}

// deliverLoad — доставка per писем каждому жителю плюс контрольная смесь
// (каждое 16-е письмо — контрольное в ящик региона; приоритетная ветвь K и
// общий список в цене) — по критерию F7. Контрольный Kind — KindSeed
// (нейтральная default-ветка свёртки): P3.7 научила fold разбирать
// контрольные входа/выхода, JSON-путь EnterWorld исказил бы смысл смеси.
func deliverLoad(r *Region, per int) {
	n := 0
	for _, res := range r.residents {
		for range per {
			if n%16 == 0 {
				r.reg.Send(transport.Envelope{To: transport.Addr{Entity: r.ctrlID}, FromID: 5, Kind: transport.KindSeed})
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

// BenchmarkMetronomeTick — один тик диспетчера метронома: живой Run-цикл
// (Hz высокий), 3 региона-пустышки в активном сете (не читают звонки —
// каждый тик проходит ветку коалесинг-дропа) и подписчик; итерация = один
// полученный звонок подписчика. Прямой вызов тела тика невозможен без
// мастера (цикл встроен в Run), поэтому ns/op — верхняя оценка с пейсингом
// тикера; оракул — аллокации: 0 на тик (сеты — atomic-снапшоты, эпизоды
// вотчдога переживают тик, WaitGroup/каналы — вне b.Loop).
func BenchmarkMetronomeTick(b *testing.B) {
	cfg := DefaultConfig()
	cfg.Hz = 10000
	cfg.HeartbeatTicks = 2
	cfg.WatchdogTicks = 1 << 30 // пустышки не тикают: алерты вотчдога не измеряем
	m, err := NewMetronome(cfg)
	if err != nil {
		b.Fatal(err)
	}
	for range 3 {
		m.Activate(&Region{metro: m, ringCh: make(chan struct{}, 1), fbCh: make(chan struct{}, 1)})
	}
	ch, unsub := m.Subscribe()
	ctx, cancel := context.WithCancel(b.Context())
	defer cancel()
	var wg sync.WaitGroup
	wg.Go(func() { m.Run(ctx) })
	defer func() {
		cancel()
		wg.Wait()
		unsub()
	}()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		<-ch
	}
}

func ExampleFold() {
	st := &State{}
	ents := []*Entity{{ID: 1, Owner: 1}}
	rng := rand.New(rand.NewPCG(1, 100))
	res := Fold(100, 2, rng, st, ents,
		[]Portion{{Region: 1, Tick: 100, Envs: []transport.Envelope{{Kind: transport.KindXP}}}}, nil, testRules())
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
	r, err := NewRegion(m, reg, 1, cfg, log, nullPusher{})
	if err != nil {
		t.Fatalf("NewRegion: %v", err)
	}
	defer log.Close() // осознанный игнор: после теста файл лога не читается, ошибка Close на оракул не влияет
	for range 100 {
		if _, err := r.Spawn(Entity{Owner: 1, HP: 100}); err != nil {
			t.Fatalf("Spawn: %v", err)
		}
	}
	for range 10 { // прогрев ёмкостей буферов
		m.tick.Add(1)
		r.step()
	}
	allocs := testing.AllocsPerRun(200, func() {
		m.tick.Add(1)
		r.step()
	})
	t.Logf("Idle-шаг: %.0f аллокаций", allocs)
	// Точная раскладка: rand.New(PCG) = 1; блоб публикации = 4 (числовой
	// бэкинг, строковый, структура Blob, структура Diff — арена ADR-0004
	// ось 3, бюджет той же константой держит TestBlobArenaAllocBudget
	// реплики); снапшот-заготовка P3.2 удалена (шов занял блоб). Изменение
	// числа — regress или осознанная правка бюджета с записью.
	const wantIdleAllocs = 5
	if allocs != wantIdleAllocs {
		t.Fatalf("аллокаций на Idle-шаг = %.0f; want %d (rand.New + блоб ×4)", allocs, wantIdleAllocs)
	}
}
