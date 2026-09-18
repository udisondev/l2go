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
	r, err := NewRegion(m, reg, 1, cfg, log, nullPusher{}, emptyGeo)
	if err != nil {
		b.Fatalf("NewRegion: %v", err)
	}
	if err := r.Wire(901, 900); err != nil {
		b.Fatalf("Wire: %v", err) // Rules.PeriodNS: без Wire advance получает dt=0
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
		[]Portion{{Region: 1, Tick: 100, Envs: []transport.Envelope{{Kind: transport.KindXP}}}}, nil, testEnv(nil))
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
	r, err := NewRegion(m, reg, 1, cfg, log, nullPusher{}, emptyGeo)
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
	// раскладка P4.1 (сетка ячеек; до сетки — 15, раскладка P3.8: Build=7):
	// rand.New(PCG) = 1; Build = 9 (порядок укладки + слоты + записи + сиды +
	// обратный индекс + сегменты + буфер 4 битмапов + копия слот-карты +
	// present-map — плотная укладка, счёт не зависит от числа ячеек);
	// join = 5 (resolveObs-слайс + obsSet-map + стартовые ёмкости staging);
	// composeJoin = 2. Изменение числа — regress или осознанная правка бюджета
	// с записью в реестр задачи.
	if allocs != 17 {
		t.Fatalf("аллокаций на Idle-шаг = %.0f; want 17 (PCG=1 + Build=9 + join=5 + compose=2)", allocs)
	}
}

// spawnMover — житель в движении (отрезок заведён напрямую; прибытие исключено
// огромной дистанцией — бенч мерит стационарный advance, а не arrivals).
func spawnMover(r *Region, x, y int32) {
	if _, err := r.Spawn(Entity{Owner: r.id, HP: 100,
		Pos: Position{X: x, Y: y}, Moving: true,
		Dest:     Position{X: x + 100000, Y: y},
		MoveFrom: Position{X: x, Y: y}, MoveDist: 1 << 50}); err != nil {
		panic("bench: Spawn движущегося: " + err.Error())
	}
}

// BenchmarkRegionPhaseA — фаза A advance: население == движущиеся
// (100/1000/5000) + точка «население 12k, движущихся ~100» (сценарий NPC
// P3.10 — фальсификация решения о фильтре по массиву). Реестр «Тик региона».
func BenchmarkRegionPhaseA(b *testing.B) {
	base := DefaultConfig()
	for _, tc := range []struct {
		population int
		movers     int
	}{
		{100, 100},
		{1000, 1000},
		{5000, 5000},
		{12000, 100},
	} {
		name := fmt.Sprintf("pop=%d/movers=%d", tc.population, tc.movers)
		b.Run(name, func(b *testing.B) {
			r := newBenchRegion(b, base, 0)
			for i := range tc.movers {
				spawnMover(r, int32(i%100)*100, int32(i/100)*100)
			}
			for i := tc.movers; i < tc.population; i++ {
				if _, err := r.Spawn(Entity{Owner: 1, HP: 100}); err != nil {
					b.Fatalf("Spawn: %v", err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				r.metro.tick.Add(1)
				r.step()
			}
		})
	}
}

// BenchmarkJoinStreamCompose — стрим движения: k движущихся × M
// наблюдателей-игроков, компоновка CharMoveToLocation В метрике (новый
// горячий путь P3.9; прецедент P3.8-F4 «путь реестра без бенча = мажор»).
// Отрезки реалистичные (сдвиг за шаг > int32-кванта — запись меняется каждый
// шаг: dirty/EventUpdate/компоновка живы); прибывшие перезапускаются вне
// измеряемой стоимости (перезапуск — до step, как доставка писем в Tick-бенче).
func BenchmarkJoinStreamCompose(b *testing.B) {
	base := DefaultConfig()
	for _, tc := range []struct{ movers, observers int }{
		{100, 100},
		{1000, 100},
	} {
		name := fmt.Sprintf("movers=%d/observers=%d", tc.movers, tc.observers)
		b.Run(name, func(b *testing.B) {
			r := newBenchRegion(b, base, 0)
			for i := range tc.observers { // наблюдатели — живые игроки
				ent := Entity{Owner: 1, HP: 100,
					Pos:    Position{X: int32(i%50) * 10, Y: int32(i/50) * 10},
					Player: &Player{ConnID: uint64(i + 1), SpeedBudget: speedCAP}}
				if _, err := r.Spawn(ent); err != nil {
					b.Fatalf("Spawn: %v", err)
				}
			}
			type benchMover struct {
				e    *Entity
				home int32 // исходная X: пинг-понг восток/запад от неё
			}
			var movers []benchMover
			for i := range tc.movers {
				x, y := int32(i%50)*10+5, int32(i/50)*10+5
				if _, err := r.Spawn(Entity{Owner: 1, HP: 100,
					Pos: Position{X: x, Y: y}, Moving: true,
					Dest:     Position{X: x + 1000, Y: y},
					MoveFrom: Position{X: x, Y: y}, MoveDist: 1_000_000}); err != nil {
					b.Fatalf("Spawn: %v", err)
				}
				movers = append(movers, benchMover{e: r.residents[len(r.residents)-1].ent, home: x})
			}
			for range 3 { // прогрев: вводы в известность, старт dirty-потока
				r.metro.tick.Add(1)
				r.step()
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				for i := range movers { // прибывшие — отрезок от home в противоположную сторону
					e := movers[i].e
					if !e.Moving {
						e.Moving = true
						dest := movers[i].home + 1000
						if e.Pos.X > movers[i].home {
							dest = movers[i].home - 1000
						}
						e.Dest = Position{X: dest, Y: e.Pos.Y}
						e.MoveFrom = e.Pos
						e.MoveDist = 1_000_000
						e.MoveDone = 0
					}
				}
				r.metro.tick.Add(1)
				r.step()
			}
		})
	}
}

// Аллокационный бюджет шага с движущимися (без наблюдателей): advance и
// dirty-сравнение меняющихся записей — 0 аллокаций сверх Idle-бюджета
// (кадры стрима аллоцируются на пару — домен бенча JoinStreamCompose).
func TestRegionStepMovingAllocBudget(t *testing.T) {
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
	r, err := NewRegion(m, reg, 1, cfg, log, nullPusher{}, emptyGeo)
	if err != nil {
		t.Fatalf("NewRegion: %v", err)
	}
	defer log.Close() // осознанный игнор: файл после теста не читается
	for i := range 100 {
		spawnMover(r, int32(i)*100, 0)
	}
	for range 10 { // прогрев ёмкостей
		m.tick.Add(1)
		r.step()
	}
	allocs := testing.AllocsPerRun(200, func() {
		m.tick.Add(1)
		r.step()
	})
	t.Logf("шаг со 100 движущимися: %.0f аллокаций", allocs)
	// раскладка та же, что Idle (advance — чистая арифметика, dirty движущихся
	// ложится в существующие битмапы Build; P4.1: 15 → 17 с сеткой ячеек —
	// см. раскладку TestRegionStepIdleAllocBudget); изменение числа — regress
	// или осознанная правка бюджета с записью в реестр задачи.
	if allocs != 17 {
		t.Fatalf("аллокаций на шаг со 100 движущимися = %.0f; want 17", allocs)
	}
}
