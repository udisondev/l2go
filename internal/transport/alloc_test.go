package transport

import (
	"testing"
)

var payloadFixed [64]byte

var benchSink []Envelope // общий sink против выбрасывания работы компилятором

// Машиная проверка бюджета: 0 аллокаций на enqueue вне роста очереди.
// Первый сегмент рождается первым же письмом внутри замера; ротация сегментов
// (одна аллокация на segCap писем) амортизируется в 0 целочисленным делением
// AllocsPerOp — см. 64 B/op в baseline.
func TestEnqueueZeroAllocsOutsideGrowth(t *testing.T) {
	r := NewRegistry(1 << 20)
	box := &Mailbox{}
	r.Register(box)
	const token = 1
	if err := box.Claim(token); err != nil {
		t.Fatal(err)
	}
	env := Envelope{FromID: 1, Kind: KindClientFrame, Payload: payloadFixed[:]}
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

// Машиная проверка бюджета: 0 аллокаций на изъятие пустой порции — обе ветки
// пустоты: «пусто с рождения» и «пусто после дрена» (доминирующий профиль
// опроса ящиков на тике).
func TestExtractEmptyZeroAllocs(t *testing.T) {
	r := NewRegistry(8)
	box := &Mailbox{}
	r.Register(box)
	const token = 1
	if err := box.Claim(token); err != nil {
		t.Fatal(err)
	}
	// прогрев: ветка «пусто после дрена» (head != nil, всё изъято)
	box.enqueue(Envelope{FromID: 1, Kind: KindClientFrame, Payload: payloadFixed[:]})
	benchSink = box.extractInto(token, nil)
	res := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			benchSink = box.extractInto(token, nil)
		}
	})
	if n := res.AllocsPerOp(); n != 0 {
		t.Errorf("изъятие пустой порции: %d allocs/op; want 0", n)
	}
}

// Машиная проверка бюджета: изъятие непустой порции в готовый буфер —
// 0 аллокаций вне роста/передачи пачки (письма лежат в существующем сегменте).
func TestExtractNonEmptyZeroAllocs(t *testing.T) {
	r := NewRegistry(1 << 20)
	box := &Mailbox{}
	r.Register(box)
	const token = 1
	if err := box.Claim(token); err != nil {
		t.Fatal(err)
	}
	env := Envelope{FromID: 1, Kind: KindClientFrame, Payload: payloadFixed[:]}
	buf := make([]Envelope, 0, segCap)
	res := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			box.enqueue(env)
			buf = box.extractInto(token, buf[:0])
		}
	})
	benchSink = buf
	if n := res.AllocsPerOp(); n != 0 {
		t.Errorf("изъятие непустой порции в готовый буфер: %d allocs/op; want 0", n)
	}
	if d := box.Depth(); d != 0 {
		t.Fatalf("глубина после цикла = %d; want 0", d)
	}
}
