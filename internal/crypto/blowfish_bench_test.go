package crypto

import (
	"fmt"
	"testing"
)

// Размеры кадров: 40 (малый), 256 (средняя точка кривой), 1456 (большой, 8-кратный).
// Ассигнования и throughput обязательны; восстановление входа для decrypt — вне
// замеренного цикла.

func benchSizes() []int { return []int{40, 256, 1456} }

func sizeName(n int) string { return fmt.Sprintf("%dB", n) }

func BenchmarkBlowfishEncrypt(b *testing.B) {
	c, err := newBFCipher(staticLoginKey)
	if err != nil {
		b.Fatal(err)
	}
	for _, size := range benchSizes() {
		b.Run(sizeName(size), func(b *testing.B) {
			data := fill(size)
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if err := c.encrypt(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkBlowfishEncryptDecrypt — раундтрип-пара в одном замере: ns/op —
// суммарная стоимость decrypt+encrypt (SetBytes — удвоенный размер).
// Восстановление входа через StopTimer/StartTimer не используется: таймер-механика
// вносит систематическую добавку (~+100 нс/итерацию, STW ReadMemStats).

func BenchmarkBlowfishEncryptDecrypt(b *testing.B) {
	c, err := newBFCipher(staticLoginKey)
	if err != nil {
		b.Fatal(err)
	}
	for _, size := range benchSizes() {
		b.Run(sizeName(size), func(b *testing.B) {
			data := fill(size)
			b.SetBytes(int64(2 * size))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if err := c.decrypt(data); err != nil {
					b.Fatal(err)
				}
				if err := c.encrypt(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
