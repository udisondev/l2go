package l2ini

import "testing"

// Пропускная способность decode: k RSA-блоков + zlib. Точка оптимизации
// оправдана только живым использованием — baseline P3.14-l2ini.
func BenchmarkIniDecode(b *testing.B) {
	file, err := Encode(samplePlain(), Modern)
	if err != nil {
		b.Fatalf("Encode: %v", err)
	}
	b.SetBytes(int64(len(file)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Decode(file, Modern); err != nil {
			b.Fatalf("Decode: %v", err)
		}
	}
}

func BenchmarkIniEncode(b *testing.B) {
	plain := samplePlain()
	b.SetBytes(int64(len(plain)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Encode(plain, Modern); err != nil {
			b.Fatalf("Encode: %v", err)
		}
	}
}
