package protocol

import (
	"math"
	"testing"
)

// FuzzRoundtripFixedD — раундтрип D на любом буфере и офсете.
func FuzzRoundtripFixedD(f *testing.F) {
	f.Add([]byte{0xef, 0xbe, 0xad, 0xde}, 0)
	f.Add([]byte{1, 2, 3, 4, 5}, 1)
	f.Add([]byte{}, 0)
	f.Fuzz(func(t *testing.T, b []byte, off int) {
		if off < 0 || off > len(b)-4 || len(b) < 4 {
			return // вне контракта — злые офсеты покрыты таблицей
		}
		var buf [4]byte
		v := int32(leU32(b[off:]))
		WriteD(buf[:], v)
		if buf != [4]byte(b[off:off+4]) {
			t.Fatalf("WriteD(%d) = %x; вход %x", v, buf, b[off:off+4])
		}
		got, ok := ReadD(buf[:], 0)
		if !ok || got != v {
			t.Fatalf("ReadD(WriteD(%d)) = %d, %v", v, got, ok)
		}
	})
}

// FuzzRoundtripS — раундтрип строки: равенство при валидном UTF-8 без U+0000,
// канонизация в U+FFFD/обрезка на терминаторе — на прочих входах.
func FuzzRoundtripS(f *testing.F) {
	f.Add("La2")
	f.Add("ИмяПерсонажа")
	f.Add("\xff")
	f.Add("a\x00b")
	f.Add("😀")
	f.Fuzz(func(t *testing.T, s string) {
		buf := make([]byte, LenS(s))
		n := WriteS(buf, s)
		if n != LenS(s) {
			t.Fatalf("WriteS(%q) = %d; LenS = %d", s, n, LenS(s))
		}
		got, rn, ok := ReadS(buf, 0)
		if !ok || rn != n {
			t.Fatalf("ReadS(%q): %q, %d, %v", s, got, rn, ok)
		}
		if validUTF8(s) && !containsNUL(s) && got != s {
			t.Fatalf("валидная строка %q прошла как %q", s, got)
		}
		if (!validUTF8(s) || containsNUL(s)) && got == s {
			t.Fatalf("невалидный вход %q прошёл без канонизации", s)
		}
	})
}

// FuzzReadOffsets — читатели не паникуют ни на каком офсете.
func FuzzReadOffsets(f *testing.F) {
	f.Add([]byte{1, 2, 3}, -1, math.MaxInt)
	f.Add([]byte{0x05}, 0, 7)
	f.Fuzz(func(t *testing.T, b []byte, off1, off2 int) {
		// ok-семантика: паника недопустима на любых входах
		_, _ = ReadD(b, off1)
		_, _ = ReadH(b, off2)
		_, _ = ReadQ(b, off1)
		_, _ = ReadF(b, off2)
		_, _, _ = ReadS(b, off1)
	})
}

func leU32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func containsNUL(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return true
		}
	}
	return false
}

func validUTF8(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			// RuneError может быть и валидным символом U+FFFD; для фазз-цели
			// считаем вход невалидным — консервативно.
			return false
		}
	}
	return true
}
