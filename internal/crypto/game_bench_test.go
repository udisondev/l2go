package crypto

import "testing"

// GameCrypt: XOR-поток не зависит от данных — восстановление входа не нужно.

func gameBench(b *testing.B, size int, encrypt bool) {
	var wire [8]byte
	copy(wire[:], fill(8))
	gc := NewGameCrypt(wire)
	gc.Enable()
	data := fill(size)
	b.SetBytes(int64(size))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		var err error
		if encrypt {
			err = gc.Encrypt(data)
		} else {
			err = gc.Decrypt(data)
		}
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGameCryptEncrypt(b *testing.B) {
	for _, size := range benchSizes() {
		b.Run(sizeName(size), func(b *testing.B) { gameBench(b, size, true) })
	}
}

func BenchmarkGameCryptDecrypt(b *testing.B) {
	for _, size := range benchSizes() {
		b.Run(sizeName(size), func(b *testing.B) { gameBench(b, size, false) })
	}
}
