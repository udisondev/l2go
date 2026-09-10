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
				local := sink // per-goroutine sink: глобальный дал бы write/write гонку
				for pb.Next() {
					buf := p.Get(size)
					local = buf
					p.Put(buf)
				}
				sink = local
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

// BenchmarkGetPutMixed — синтетический микс размеров из 12 элементов:
// фактические доли 50/17/8.3/8.3/8.3/8.3% по бакетам 128..4096 (средний размер
// 747 Б). Расширение бенч-плана сверх решения 8 — запись F16 реестра P1.2.
func BenchmarkGetPutMixed(b *testing.B) {
	p := &Pool{}
	mix := []int{128, 128, 128, 128, 128, 128, 256, 256, 512, 1024, 2048, 4096}
	b.SetBytes(747) // взвешенный средний размер микса
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		local := sink
		i := 0
		for pb.Next() {
			size := mix[i%len(mix)]
			i++
			buf := p.Get(size)
			local = buf
			p.Put(buf)
		}
		sink = local
	})
}
