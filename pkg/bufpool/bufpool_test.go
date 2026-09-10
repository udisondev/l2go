// Векторы поведения и границы — порт референса udisondev/interlude@34fe4c86
// (pkg/bufpool); тесты написаны до реализации (красная фаза).
package bufpool

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

func TestGetBoundaries(t *testing.T) {
	p := &Pool{}
	for _, tt := range []struct {
		size         int
		wantCap      int  // 0 — не из бакета (overflow)
		wantOverflow bool
	}{
		{1, 128, false}, {128, 128, false}, {129, 256, false},
		{256, 256, false}, {257, 512, false}, {512, 512, false},
		{513, 1024, false}, {1024, 1024, false}, {1025, 2048, false},
		{2048, 2048, false}, {2049, 4096, false}, {4096, 4096, false},
		{4097, 0, true}, {10000, 0, true},
	} {
		before := p.Stats()
		b := p.Get(tt.size)
		if len(b) != tt.size {
			t.Fatalf("Get(%d): len=%d", tt.size, len(b))
		}
		if tt.wantCap != 0 && cap(b) != tt.wantCap {
			t.Fatalf("Get(%d): cap=%d, хочу бакет %d", tt.size, cap(b), tt.wantCap)
		}
		after := p.Stats()
		if after.Gets != before.Gets+1 {
			t.Fatalf("Get(%d): Gets не вырос", tt.size)
		}
		if got := after.Overflows > before.Overflows; got != tt.wantOverflow {
			t.Fatalf("Get(%d): overflow=%v, хочу %v", tt.size, got, tt.wantOverflow)
		}
	}
}

func TestGetZeroedByCapCrossSize(t *testing.T) {
	p := &Pool{}
	b := p.Get(128)
	for i := range b {
		b[i] = 0xCC
	}
	p.Put(b)
	s := p.Get(64) // кросс-размерная выдача того же бакета
	if cap(s) != 128 {
		t.Fatalf("Get(64): cap=%d, хочу 128", cap(s))
	}
	for i, v := range s[:cap(s)] {
		if v != 0 {
			t.Fatalf("хвост cap не нулевой: байт %d = %#x (протечка прошлого владельца)", i, v)
		}
	}
}

func TestPut(t *testing.T) {
	p := &Pool{}

	before := p.Stats()
	p.Put(nil) // no-op
	if p.Stats() != before {
		t.Fatal("Put(nil) изменил статистику")
	}

	p.Put(make([]byte, 100)) // чужая ёмкость — отброс
	if p.Stats() != before {
		t.Fatal("Put чужого cap изменил статистику")
	}

	b := p.Get(200)
	after := p.Stats()
	p.Put(b)
	if got := p.Stats().Puts; got != after.Puts+1 {
		t.Fatal("Put буфера из Get не учтён")
	}
}

func TestReuseNoAllocs(t *testing.T) {
	p := &Pool{}
	p.Put(p.Get(128)) // прогрев
	n := testing.AllocsPerRun(100, func() {
		p.Put(p.Get(128))
	})
	if n != 0 {
		t.Fatalf("прогретый Get+Put: %v аллокаций, ожидалось 0", n)
	}
}

func TestZeroValue(t *testing.T) {
	var p Pool // без конструктора
	b := p.Get(300)
	if len(b) != 300 || cap(b) != 512 {
		t.Fatalf("нулевое значение: Get(300) = len %d cap %d", len(b), cap(b))
	}
	p.Put(b)
	if p.Stats().Puts != 1 {
		t.Fatal("нулевое значение: Put не работает")
	}
}

func TestPanicContract(t *testing.T) {
	p := &Pool{}
	for _, size := range []int{0, -1} {
		func() {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("Get(%d): ожидалась паника", size)
				}
				if msg := fmt.Sprint(r); msg == "" || !contains(msg, "size") {
					t.Fatalf("Get(%d): паника без диагностики: %v", size, r)
				}
			}()
			p.Get(size)
		}()
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestDoublePutAlias(t *testing.T) {
	// Документирующий тест контракта: двойной Put запрещён; пул не детектирует
	// дубликат и молча выдаст один массив двоим.
	p := &Pool{}
	b := p.Get(128)
	p.Put(b)
	p.Put(b) // нарушение контракта
	a1 := p.Get(100)
	a2 := p.Get(100)
	if &a1[0] == &a2[0] {
		// ожидаемое следствие нарушения: перекрытие владений
		return
	}
}

func TestStressOwnership(t *testing.T) {
	// Полный GOMAXPROCS (per-P слоты при P=1 вырождены). Паттерн-во-владении —
	// единственная проверка двойной выдачи без -race; ограничение: ловит только
	// перекрытие окон fill/verify, последовательный интерливинг не детектируется
	// в принципе. Нули по cap при каждой выдаче — непрерывная проверка zeroed.
	p := &Pool{}
	workers := runtime.GOMAXPROCS(0)
	cycles := 2000
	var expGets, expPuts, expOver atomic.Int64

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < cycles; i++ {
				size := 1 + (w*cycles+i)%5000 // все границы + overflow
				b := p.Get(size)
				for j, v := range b[:cap(b)] {
					if v != 0 {
						panic(fmt.Sprintf("ненулевой байт %d при выдаче (size=%d)", j, size))
					}
				}
				for j := range b {
					b[j] = byte(w + 1)
				}
				for j, v := range b {
					if v != byte(w+1) {
						panic(fmt.Sprintf("паттерн нарушен: байт %d = %#x", j, v))
					}
				}
				wasOverflow := size > 4096
				p.Put(b)
				expGets.Add(1)
				expPuts.Add(1)
				if wasOverflow {
					expOver.Add(1)
				}
			}
		}(w)
	}
	wg.Wait()
	s := p.Stats()
	if s.Gets != expGets.Load() || s.Puts != expPuts.Load() || s.Overflows != expOver.Load() {
		t.Fatalf("сходимость: got %+v, хочу gets/puts/overflows=%d/%d/%d",
			s, expGets.Load(), expPuts.Load(), expOver.Load())
	}
}

func TestStressHandoff(t *testing.T) {
	// Кросс-горутинная форма контракта владения: производитель Get+fill+send,
	// потребитель verify+Put — единственный владелец в каждый момент.
	p := &Pool{}
	const pairs = 8
	ch := make(chan []byte, pairs)
	var wg sync.WaitGroup
	for i := 0; i < pairs; i++ {
		wg.Add(2)
		go func(i int) { // производитель
			defer wg.Done()
			for cycle := 0; cycle < 500; cycle++ {
				b := p.Get(1 + (i*500+cycle)%3000)
				for j := range b {
					b[j] = byte(i + 1)
				}
				ch <- b // владение уходит потребителю
			}
		}(i)
		go func(i int) { // потребитель
			defer wg.Done()
			for cycle := 0; cycle < 500; cycle++ {
				b := <-ch
				for j, v := range b {
					if v != byte(i+1) {
						panic("паттерн нарушен при хэндоффе")
					}
				}
				p.Put(b)
			}
		}(i)
	}
	wg.Wait()
	close(ch)
}
