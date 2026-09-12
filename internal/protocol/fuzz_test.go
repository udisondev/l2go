package protocol

import (
	"encoding/hex"
	"errors"
	"math"
	"testing"
)

// roundtrip — параметризация fuzz-раундтрипа фиксированных примитивов.
type roundtrip struct {
	name  string
	size  int
	write func(dst []byte, v uint64)
	read  func(src []byte, off int) (uint64, bool)
}

var roundtrips = []roundtrip{
	{"D", 4,
		func(dst []byte, v uint64) { WriteD(dst, int32(v)) },
		func(src []byte, off int) (uint64, bool) {
			v, ok := ReadD(src, off)
			return uint64(uint32(v)), ok
		}},
	{"H", 2,
		func(dst []byte, v uint64) { WriteH(dst, int16(v)) },
		func(src []byte, off int) (uint64, bool) {
			v, ok := ReadH(src, off)
			return uint64(uint16(v)), ok
		}},
	{"Q", 8,
		func(dst []byte, v uint64) { WriteQ(dst, int64(v)) },
		func(src []byte, off int) (uint64, bool) {
			v, ok := ReadQ(src, off)
			return uint64(v), ok
		}},
	{"F", 8,
		func(dst []byte, v uint64) { WriteF(dst, math.Float64frombits(v)) },
		func(src []byte, off int) (uint64, bool) {
			v, ok := ReadF(src, off)
			return math.Float64bits(v), ok
		}},
}

// FuzzRoundtripFixed — раундтрип каждого фиксированного примитива
// (write→read == значение по битам); селектор примитива — fuzz-аргумент.
func FuzzRoundtripFixed(f *testing.F) {
	f.Add([]byte{0xef, 0xbe, 0xad, 0xde}, 0, 0)
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8}, 1, 3)
	f.Add([]byte{0, 0, 0xf8, 0x7f}, 0, 3)
	f.Fuzz(func(t *testing.T, b []byte, off int, which int) {
		idx := which % len(roundtrips)
		if idx < 0 {
			idx += len(roundtrips)
		}
		rt := roundtrips[idx]
		if off < 0 || off > len(b)-rt.size || len(b) < rt.size {
			return // вне контракта — злые офсеты покрыты таблицей
		}
		var buf [8]byte
		v := uint64(0)
		for i := 0; i < rt.size; i++ {
			v |= uint64(b[off+i]) << (8 * i) // LE: бит-образ входных байтов
		}
		rt.write(buf[:], v)
		if hex.EncodeToString(buf[:rt.size]) != hex.EncodeToString(b[off:off+rt.size]) {
			t.Fatalf("Write%s: записано %x; вход %x", rt.name, buf[:rt.size], b[off:off+rt.size])
		}
		got, ok := rt.read(buf[:], 0)
		if !ok || got != v {
			t.Fatalf("Read%s(Write%s(%x)) = %x, %v", rt.name, rt.name, v, got, ok)
		}
	})
}

// FuzzRoundtripS — раундтрип строки против оракула канонизации: ожидание —
// string([]rune(s)), где декодер UTF-8 заменяет невалидные байты на U+FFFD
// (utf16-цикл кодирования-декодирования вокруг рунного образа идемпотентен);
// вложенный NUL обрезается терминатором.
func FuzzRoundtripS(f *testing.F) {
	f.Add("La2")
	f.Add("ИмяПерсонажа")
	f.Add("\xff")
	f.Add("a\x00b")
	f.Add("😀")
	f.Add("�") // валидный литеральный U+FFFD — не «невалидный вход»
	f.Fuzz(func(t *testing.T, s string) {
		want := string([]rune(s)) // оракул канонизации
		buf := make([]byte, LenS(s))
		n := WriteS(buf, s)
		if n != LenS(s) {
			t.Fatalf("WriteS(%q) = %d; LenS = %d", s, n, LenS(s))
		}
		got, rn, ok := ReadS(buf, 0)
		if !ok {
			t.Fatalf("ReadS(%q): ok=false", s)
		}
		if containsNUL(s) {
			// NUL — терминатор: поле пишется целиком, чтение останавливается
			// на первом NUL; равенство n==rn не требуется.
			return
		}
		if rn != n {
			t.Fatalf("ReadS(%q): n=%d; want %d", s, rn, n)
		}
		if got != want {
			t.Fatalf("ReadS(WriteS(%q)) = %q; оракул канонизации %q", s, got, want)
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

func containsNUL(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return true
		}
	}
	return false
}

// FuzzNextFrame — нарезка произвольного буфера на кадры: без паник; вернувшийся
// кадр лежит внутри буфера, потребление всегда len(body)+2; ошибки — только
// сентинелы ErrFrameIncomplete/ErrFrameLength (обёрнутые).
func FuzzNextFrame(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{0x02, 0x00})
	f.Add([]byte{0x00, 0x00})
	f.Add([]byte{0x05, 0x00, 1, 2, 3})
	f.Add([]byte{0xff, 0xff, 1, 2, 3})
	f.Fuzz(func(t *testing.T, b []byte) {
		body, err := NextFrame(b)
		switch {
		case err == nil:
			if len(body)+2 > len(b) {
				t.Fatalf("кадр вне буфера: len=%d buf=%d", len(body), len(b))
			}
		case errors.Is(err, ErrFrameIncomplete), errors.Is(err, ErrFrameLength):
		default:
			t.Fatalf("чужая ошибка: %v", err)
		}
	})
}
