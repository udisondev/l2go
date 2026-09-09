package crypto

import "testing"

// LoginCrypt: payload 40/256/1440 → кадры 56/272/1456 (канон CT0: +4,
// безусловное добивание, +8). Буфер — кадр + MaxFrameOverhead, до цикла.

func loginBenchPayloads() []int { return []int{40, 256, 1440} }

func BenchmarkLoginCryptEncrypt(b *testing.B) {
	for _, p := range loginBenchPayloads() {
		b.Run(sizeName(p), func(b *testing.B) {
			lc := NewLoginCrypt()
			if err := lc.SetKey(fill(16)); err != nil {
				b.Fatal(err)
			}
			dst := make([]byte, p+MaxFrameOverhead)
			payload := fill(p)
			b.SetBytes(int64(frameSize(p, false)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := lc.Encrypt(dst, payload); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkLoginCryptDecrypt(b *testing.B) {
	for _, p := range loginBenchPayloads() {
		b.Run(sizeName(p), func(b *testing.B) {
			lc := NewLoginCrypt()
			if err := lc.SetKey(fill(16)); err != nil {
				b.Fatal(err)
			}
			frame := make([]byte, p, p+MaxFrameOverhead)
			copy(frame, fill(p))
			n, err := lc.Encrypt(frame, frame[:p])
			if err != nil {
				b.Fatal(err)
			}
			frame = frame[:n]
			b.SetBytes(int64(n))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if err := lc.Decrypt(frame); err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				if _, err := lc.Encrypt(frame, frame[:p]); err != nil { // восстановление вне замера
					b.Fatal(err)
				}
				b.StartTimer()
			}
		})
	}
}
