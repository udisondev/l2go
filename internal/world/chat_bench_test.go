package world

// Бенчмарк fan-out рассылки чата (F10 реестра P3.11: горячий путь — O(игроков
// в радиусе) на реплику) и машинный аллок-бюджет того же шага; живость —
// N+1 кадр за шаг (анти-твин «мёртвого бенча» P3.10-F28).

import (
	"math/rand/v2"
	"strconv"
	"testing"

	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/transport"
)

// benchSay2Setup — неизменные входы шага вне измеряемого цикла (пыль RNG и
// конверта порции — не часть fan-out; состояние свёртки на шаг не влияет).
type benchSay2Setup struct {
	st     *State
	ents   []*Entity
	sender *Entity
	rng    *rand.Rand
	env    Env
	letter transport.Envelope
}

func benchSay2Setup_(n int) benchSay2Setup {
	s := benchSay2Setup{st: newState(), rng: rand.New(rand.NewPCG(1, 10)), env: testEnv(nil)}
	s.sender = chatEnt(1, 1, syncPos.X, syncPos.Y, syncPos.Z)
	s.ents = append(s.ents, s.sender)
	for i := range n {
		s.ents = append(s.ents, chatEnt(transport.EntityID(i+2), uint64(i+2),
			syncPos.X+int32(i%900), syncPos.Y, syncPos.Z))
	}
	s.letter = sayLetter(s.sender.ID, "benchmark hello", protocol.ChatGeneral)
	return s
}

// BenchmarkSay2FanOut — один Say2-шаг с N живыми получателями в радиусе
// (ReportAllocs); N=10/100/1000.
func BenchmarkSay2FanOut(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			s := benchSay2Setup_(n)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				s.sender.Player.ChatBudget = chatSayCapMS // холодный fan-out: шаг без рефилла
				res := Fold(10, 0, s.rng, s.st, s.ents, portion(s.letter), nil, s.env)
				if got := len(res.Pushes); got != n+1 {
					b.Fatalf("живость: пушей = %d; want %d (мёртвый бенч)", got, n+1)
				}
			}
		})
	}
}

// TestSay2FanOutAllocBudget — машинный аллок-бюджет шага fan-out (N=100):
// кадр + конверт порции + ~7 реаллокаций роста Pushes на 101 запись
// (фактически 11; бюджет 12 — детектор регрессии, не цель оптимизации;
// прецедент encode/bench_test.go:111).
func TestSay2FanOutAllocBudget(t *testing.T) {
	// без t.Parallel: AllocsPerRun управляет GC (запрещён в параллельных)
	s := benchSay2Setup_(100)
	s.sender.Player.ChatBudget = chatSayCapMS
	allocs := testing.AllocsPerRun(20, func() {
		s.sender.Player.ChatBudget = chatSayCapMS
		res := Fold(10, 0, s.rng, s.st, s.ents, portion(s.letter), nil, s.env)
		if len(res.Pushes) != 101 {
			t.Fatalf("живость: пушей = %d; want 101", len(res.Pushes))
		}
	})
	if allocs > 12 {
		t.Errorf("аллокаций на fan-out шаг (N=100) = %.0f; want ≤12", allocs)
	}
}
