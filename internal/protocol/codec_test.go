package protocol

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"testing"
	"unicode/utf16"
)

// --- Писатели: кодирование значений ---

func TestWriteFixedPrimitives(t *testing.T) {
	cases := []struct {
		name string
		write func(dst []byte)
		want  string // hex
	}{
		{"WriteD отрицательное", func(d []byte) { WriteD(d, -2) }, "feffffff"},
		{"WriteD max", func(d []byte) { WriteD(d, math.MaxInt32) }, "ffffffff"},
		{"WriteH", func(d []byte) { WriteH(d, -2) }, "feff"},
		{"WriteQ", func(d []byte) { WriteQ(d, -1) }, "ffffffffffffffff"},
		{"WriteF", func(d []byte) { WriteF(d, 1.5) }, "000000000000f83f"},
	}
	for _, c := range cases {
		var dst [8]byte
		c.write(dst[:])
		if got := hexStr(dst[:]); got != c.want {
			t.Errorf("%s = %s; want %s", c.name, got, c.want)
		}
	}
}

// --- Паника-контракт писателей: короткий dst — паника с диагностикой. ---

func TestWritePanicsOnShortDst(t *testing.T) {
	cases := []struct {
		name  string
		write func()
		want  string // подстрока диагностики
	}{
		{"WriteD", func() { WriteD(make([]byte, 3), 1) }, "WriteD"},
		{"WriteH", func() { WriteH(make([]byte, 1), 1) }, "WriteH"},
		{"WriteQ", func() { WriteQ(make([]byte, 7), 1) }, "WriteQ"},
		{"WriteF", func() { WriteF(make([]byte, 7), 1) }, "WriteF"},
		{"WriteS", func() { WriteS(make([]byte, 3), "ab") }, "WriteS"},
		{"WriteS без места под терминатор", func() { WriteS(make([]byte, 2), "a") }, "WriteS"},
	}
	for _, c := range cases {
		func() {
			defer func() {
				r := recover()
				if r == nil {
					t.Errorf("%s: нет паники на коротком dst", c.name)
					return
				}
				msg, _ := r.(string)
				if !strings.Contains(msg, c.want) {
					t.Errorf("%s: паника %q без имени примитива", c.name, msg)
				}
			}()
			c.write()
		}()
	}
}

// --- LenS ↔ WriteS: длина и запись одним расчётом, включая не-BMP. ---

func TestLenSMatchesWriteS(t *testing.T) {
	strs := []string{
		"",
		"a",
		"ИмяПерсонажа",
		"😀👍",       // не-BMP: 2 юнита на руну
		"a\xed\xa0\x80b", // невалидный UTF-8: замены U+FFFD
		strings.Repeat("щ", 300),
	}
	for _, s := range strs {
		dst := make([]byte, LenS(s))
		if LenS(s) > 2*len(s)+2 {
			t.Errorf("LenS(%q) = %d превышает границу 2*len+2 = %d", s, LenS(s), 2*len(s)+2)
		}
		n := WriteS(dst, s) // паника при несогласованности — тест-падение
		if n != LenS(s) {
			t.Errorf("LenS(%q) = %d; WriteS записала %d", s, LenS(s), n)
		}
	}
}

// --- Раундтрипы фиксированных примитивов. ---

func TestRoundtripFixed(t *testing.T) {
	var buf [8]byte
	dvals := []int32{0, 1, -1, math.MinInt32, math.MaxInt32, 123456}
	for _, v := range dvals {
		WriteD(buf[:], v)
		got, ok := ReadD(buf[:], 0)
		if !ok || got != v {
			t.Errorf("ReadD(WriteD(%d)) = %d, %v", v, got, ok)
		}
	}
	hvals := []int16{0, -1, math.MinInt16, math.MaxInt16}
	for _, v := range hvals {
		WriteH(buf[:], v)
		got, ok := ReadH(buf[:], 0)
		if !ok || got != v {
			t.Errorf("ReadH(WriteH(%d)) = %d, %v", v, got, ok)
		}
	}
	qvals := []int64{0, -1, math.MinInt64, math.MaxInt64}
	for _, v := range qvals {
		WriteQ(buf[:], v)
		got, ok := ReadQ(buf[:], 0)
		if !ok || got != v {
			t.Errorf("ReadQ(WriteQ(%d)) = %d, %v", v, got, ok)
		}
	}
	// float — по битам, включая NaN/Inf/-0.
	fvals := []float64{0, 1.5, -1.5, math.MaxFloat64, math.SmallestNonzeroFloat64,
		math.Inf(1), math.Inf(-1), math.NaN(), math.Copysign(0, -1)}
	for _, v := range fvals {
		WriteF(buf[:], v)
		got, ok := ReadF(buf[:], 0)
		if !ok || math.Float64bits(got) != math.Float64bits(v) {
			t.Errorf("ReadF(WriteF(%v)) = %v, %v; биты %x want %x",
				v, got, ok, math.Float64bits(got), math.Float64bits(v))
		}
	}
}

// --- Раундтрип строк: равенство — для валидного UTF-8 без U+0000. ---

func TestRoundtripString(t *testing.T) {
	strs := []string{
		"",
		"a",
		"ИмяПерсонажа",
		"😀👍 第七天堂", // astral + CJK
		strings.Repeat("λ", 100),
	}
	for _, s := range strs {
		dst := make([]byte, LenS(s))
		n := WriteS(dst, s)
		got, rn, ok := ReadS(dst, 0)
		if !ok {
			t.Errorf("ReadS(%q): ok=false", s)
			continue
		}
		if rn != n {
			t.Errorf("ReadS(%q): n=%d; want %d", s, rn, n)
		}
		if got != s {
			t.Errorf("ReadS(WriteS(%q)) = %q", s, got)
		}
	}
}

// Канонизация: невалидные руны и непарные суррогаты → U+FFFD; NUL обрезает.
func TestStringCanonicalization(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\xff", "�"},
		{"a\xc0\xafb", "a��b"},
		{"\xed\xa0\x80", "���"}, // WTF-8 половинка суррогата: 3 байта → 3 замены
		{"a\x00b", "a"},        // NUL в середине — терминатор
	}
	for _, c := range cases {
		dst := make([]byte, LenS(c.in))
		WriteS(dst, c.in)
		got, _, ok := ReadS(dst, 0)
		if !ok || got != c.want {
			t.Errorf("канонизация %q: got %q, ok=%v; want %q", c.in, got, ok, c.want)
		}
	}
}

// WriteS побайтово эквивалентен utf16.Encode-порту интерлюда (оракул).
func TestWriteSMatchesUTF16Encode(t *testing.T) {
	strs := []string{"", "a", "Имя", "😀👍", "\xff\xed\xa0\x80", strings.Repeat("β", 50)}
	for _, s := range strs {
		units := utf16.Encode([]rune(s))
		want := make([]byte, 0, (len(units)+1)*2)
		for _, u := range units {
			var b [2]byte
			binary.LittleEndian.PutUint16(b[:], u)
			want = append(want, b[:]...)
		}
		want = append(want, 0, 0) // null-терминатор

		got := make([]byte, LenS(s))
		WriteS(got, s)
		if hexStr(got) != hexStr(want) {
			t.Errorf("WriteS(%q) = %s; оракул utf16.Encode %s", s, hexStr(got), hexStr(want))
		}
	}
}

// --- Злые входы читателей: ok=false, нулевые значения; форма без переполнения. ---

func TestReadEvilOffsets(t *testing.T) {
	src := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	cases := []struct {
		name string
		call func() bool // true = ok
	}{
		{"ReadD off=-1", func() { _, ok := ReadD(src, -1); return ok }},
		{"ReadD off=len", func() { _, ok := ReadD(src, len(src)); return ok }},
		{"ReadD off=len-3 (хвост)", func() { _, ok := ReadD(src, len(src)-3); return ok }},
		{"ReadD off=MaxInt", func() { _, ok := ReadD(src, math.MaxInt); return ok }},
		{"ReadD off=MinInt", func() { _, ok := ReadD(src, math.MinInt); return ok }},
		{"ReadH off=-2", func() { _, ok := ReadH(src, -2); return ok }},
		{"ReadQ off=MaxInt-2", func() { _, ok := ReadQ(src, math.MaxInt-2); return ok }},
		{"ReadF off=-8", func() { _, ok := ReadF(src, -8); return ok }},
	}
	for _, c := range cases {
		if ok := c.call(); ok {
			t.Errorf("%s: ok=true на злом офсете", c.name)
		}
	}
}

func TestReadSNoTerminator(t *testing.T) {
	src := []byte{'a', 0, 'b', 0, 'c'} // терминатора нет: c — непарный хвост
	s, n, ok := ReadS(src, 0)
	if ok || s != "" || n != 0 {
		t.Errorf("ReadS без терминатора = %q, %d, %v; want \"\", 0, false", s, n, ok)
	}
	// терминатор найден — непарный хвост после него не причина отказа
	src2 := []byte{'a', 0, 0x05}
	s, n, ok = ReadS(src2, 0)
	if !ok || s != "a" || n != 4 {
		t.Errorf("ReadS с непарным хвостом = %q, %d, %v; want \"a\", 4, true", s, n, ok)
	}
}

func TestReadSEvilOffsets(t *testing.T) {
	src := []byte{'a', 0}
	if _, _, ok := ReadS(src, -1); ok {
		t.Error("ReadS off=-1: ok=true")
	}
	if _, _, ok := ReadS(src, len(src)); ok {
		t.Error("ReadS off=len: ok=true")
	}
	if _, _, ok := ReadS(src, math.MaxInt); ok {
		t.Error("ReadS off=MaxInt: ok=true")
	}
}

func hexStr(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, x := range b {
		out = append(out, digits[x>>4], digits[x&0xf])
	}
	return string(out)
}

func ExampleWriteS() {
	s := "La2"
	buf := make([]byte, LenS(s))
	n := WriteS(buf, s)
	// n учитывает null-терминатор UTF-16LE.
	fmt.Println(n)
	// Output: 8
}
