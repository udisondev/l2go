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

func BenchmarkBlowfishDecrypt(b *testing.B) {
	c, err := newBFCipher(staticLoginKey)
	if err != nil {
		b.Fatal(err)
	}
	for _, size := range benchSizes() {
		b.Run(sizeName(size), func(b *testing.B) {
			enc := fill(size)
			if err := c.encrypt(enc); err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if err := c.decrypt(enc); err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				if err := c.encrypt(enc); err != nil { // восстановление вне замера
					b.Fatal(err)
				}
				b.StartTimer()
			}
		})
	}
}
