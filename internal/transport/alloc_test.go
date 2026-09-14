package transport

import (
	"testing"
)

var payloadFixed [64]byte

var benchSink []Envelope // общий sink против выбрасывания работы компилятором

// Машиная проверка бюджета: 0 аллокаций на enqueue вне роста очереди.
func TestEnqueueZeroAllocsOutsideGrowth(t *testing.T) {
	r := NewRegistry(1 << 20)
	box := &Mailbox{}
	r.Register(box)
	const token = 1
	if err := box.Claim(token); err != nil {
		t.Fatal(err)
	}
	env := Envelope{FromID: 1, Kind: KindClientFrame, Payload: payloadFixed[:]}
	// прогрев: первый сегмент уже существует после Register
	res := testing.Benchmark(func(b *testing.B) {
		var sink []Envelope
		for i := 0; i < b.N; i++ {
			box.enqueue(env)
			if box.length.Load() >= segCap {
				sink = box.extractInto(token, sink[:0])
			}
		}
		benchSink = sink
	})
	if n := res.AllocsPerOp(); n != 0 {
		t.Errorf("enqueue: %d allocs/op вне роста очереди; want 0", n)
	}
}

// Машиная проверка бюджета: 0 аллокаций на изъятие пустой порции (опрос пустых ящиков).
func TestExtractEmptyZeroAllocs(t *testing.T) {
	r := NewRegistry(8)
	box := &Mailbox{}
	r.Register(box)
	const token = 1
	if err := box.Claim(token); err != nil {
		t.Fatal(err)
	}
	res := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			benchSink = box.extractInto(token, nil)
		}
	})
	if n := res.AllocsPerOp(); n != 0 {
		t.Errorf("изъятие пустой порции: %d allocs/op; want 0", n)
	}
}
