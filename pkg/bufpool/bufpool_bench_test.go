package bufpool

import (
	"fmt"
	"testing"
)

// GetPut — стационарный цикл пути реестра (Get → использование → Put);
// ColdGet измеряет свежую аллокацию (New-путь), не пул; AllocMake — эталон
// прямой аллокации. Параллельные семейства ловят конфликт атомарных счётчиков,
// невидимый серийно.

var sink []byte

func benchSizes() []int { return []int{128, 256, 512, 1024, 2048, 4096} }

func sizeName(n int) string { return fmt.Sprintf("%dB", n) }

func BenchmarkGetPut(b *testing.B) {
	for _, size := range benchSizes() {
		b.Run(sizeName(size), func(b *testing.B) {
			p := &Pool{}
			p.Put(p.Get(size)) // прогрев
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				buf := p.Get(size)
				sink = buf
				p.Put(buf)
			}
		})
	}
}

func BenchmarkGetPutParallel(b *testing.B) {
	for _, size := range benchSizes() {
		b.Run(sizeName(size), func(b *testing.B) {
			p := &Pool{}
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					buf := p.Get(size)
					sink = buf
					p.Put(buf)
				}
			})
		})
	}
}

func BenchmarkColdGet(b *testing.B) {
	for _, size := range []int{128, 4096} {
		b.Run(sizeName(size), func(b *testing.B) {
			p := &Pool{} // без Put каждый Get — холодный New-путь
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				sink = p.Get(size)
			}
		})
	}
}

func BenchmarkAllocMake(b *testing.B) {
	for _, size := range []int{128, 4096} {
		b.Run(sizeName(size), func(b *testing.B) {
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				sink = make([]byte, size)
			}
		})
	}
}

// BenchmarkGetPutMixed — микс распределения пакетов (65/20/10/7/2/1%),
// якорь ожидания для живого трафика фазы 3.
func BenchmarkGetPutMixed(b *testing.B) {
	p := &Pool{}
	mix := []int{128, 128, 128, 128, 128, 128, 256, 256, 512, 1024, 2048, 4096}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			size := mix[i%len(mix)]
			i++
			buf := p.Get(size)
			sink = buf
			p.Put(buf)
		}
	})
}
