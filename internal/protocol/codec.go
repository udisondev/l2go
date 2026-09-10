// Примитивы кодека протокола L2: запись little-endian в буфер вызывающего
// (семантика udisondev/interlude@34fe4c8 pkg/packet/server/helpers.go;
// отклонения порта — ok-чтение вместо молчаливых нулей, паника-контракт
// записи с диагностикой, WriteS без utf16.Encode — 0 аллокаций,
// непереносимы writeUD/writeZeroD/writeZeroH/writeSpeeds/EncodeUTF16).

package protocol

import (
	"encoding/binary"
	"fmt"
	"math"
	"unicode/utf16"
)

// WriteH пишет v как little-endian uint16 в начало dst.
func WriteH(dst []byte, v int16) {
	if len(dst) < 2 {
		panic(fmt.Sprintf("protocol: WriteH: dst длиной %d байт < 2", len(dst)))
	}
	binary.LittleEndian.PutUint16(dst, uint16(v))
}

// WriteD пишет v как little-endian uint32 в начало dst.
func WriteD(dst []byte, v int32) {
	if len(dst) < 4 {
		panic(fmt.Sprintf("protocol: WriteD: dst длиной %d байт < 4", len(dst)))
	}
	binary.LittleEndian.PutUint32(dst, uint32(v))
}

// WriteQ пишет v как little-endian uint64 в начало dst.
func WriteQ(dst []byte, v int64) {
	if len(dst) < 8 {
		panic(fmt.Sprintf("protocol: WriteQ: dst длиной %d байт < 8", len(dst)))
	}
	binary.LittleEndian.PutUint64(dst, uint64(v))
}

// WriteF пишет v как IEEE 754 double (8 байт, little-endian по битам) в
// начало dst. F-поля Interlude — double (Mobius CT_0_Interlude:
// UserInfo/CharSelectionInfo пишут writeDouble).
func WriteF(dst []byte, v float64) {
	if len(dst) < 8 {
		panic(fmt.Sprintf("protocol: WriteF: dst длиной %d байт < 8", len(dst)))
	}
	binary.LittleEndian.PutUint64(dst, math.Float64bits(v))
}

// LenS возвращает размер WriteS(s) в байтах: юниты UTF-16LE по 2 байта и
// null-терминатор. Верхняя граница — 2*len(s)+2: каждый байт UTF-8 даёт не
// более одного юнита. Буфер для пакета со строками считается этой функцией —
// sizing и запись одним расчётом.
func LenS(s string) int {
	units := 1 // терминатор
	for _, r := range s {
		if r >= 0x10000 {
			units += 2 // астральная плоскость: пара суррогатов
		} else {
			units++
		}
	}
	return units * 2
}

// WriteS пишет s как UTF-16LE с null-терминатором в начало dst и возвращает
// число записанных байт (== LenS(s)). Обходит строку напрямую, без
// utf16.Encode: 0 аллокаций. Невалидные руны заменяются на U+FFFD —
// семантика utf16.Encode([]rune(s)).
func WriteS(dst []byte, s string) int {
	n := LenS(s)
	if len(dst) < n {
		panic(fmt.Sprintf("protocol: WriteS: dst длиной %d байт < LenS(s)=%d", len(dst), n))
	}
	off := 0
	put := func(u uint16) {
		binary.LittleEndian.PutUint16(dst[off:], u)
		off += 2
	}
	// range по string невалидные байты декодирует в U+FFFD — отдельная
	// ветка суррогатов не нужна (Go-строка суррогатов не содержит).
	for _, r := range s {
		if r >= 0x10000 { // астральная плоскость: пара суррогатов
			v := r - 0x10000
			put(uint16(0xD800 + (v >> 10)))
			put(uint16(0xDC00 + (v & 0x3FF)))
		} else {
			put(uint16(r))
		}
	}
	put(0)
	return off
}

// ReadH читает little-endian int16 по смещению off. ok=false при выходе
// офсета за границы (проверка вычитанием — без переполнения на off≈MaxInt).
func ReadH(src []byte, off int) (int16, bool) {
	if off < 0 || 2 > len(src)-off {
		return 0, false
	}
	return int16(binary.LittleEndian.Uint16(src[off:])), true
}

// ReadD читает little-endian int32 по смещению off.
func ReadD(src []byte, off int) (int32, bool) {
	if off < 0 || 4 > len(src)-off {
		return 0, false
	}
	return int32(binary.LittleEndian.Uint32(src[off:])), true
}

// ReadQ читает little-endian int64 по смещению off.
func ReadQ(src []byte, off int) (int64, bool) {
	if off < 0 || 8 > len(src)-off {
		return 0, false
	}
	return int64(binary.LittleEndian.Uint64(src[off:])), true
}

// ReadF читает IEEE 754 double (8 байт) по смещению off; NaN/Inf и -0
// проходят по битам.
func ReadF(src []byte, off int) (float64, bool) {
	if off < 0 || 8 > len(src)-off {
		return 0, false
	}
	return math.Float64frombits(binary.LittleEndian.Uint64(src[off:])), true
}

// ReadS читает UTF-16LE-строку с null-терминатором по смещению off;
// возвращает строку и n — число байт поля включая терминатор. ok=false,
// только если терминатор не найден среди полностью уложившихся юнитов до
// конца буфера (непарный хвостовой байт после терминатора — не причина
// отказа); непарные суррогаты декодируются в U+FFFD (utf16.Decode).
func ReadS(src []byte, off int) (string, int, bool) {
	if off < 0 || off >= len(src) {
		return "", 0, false
	}
	// первый проход — до терминатора: точный размер буфера юнитов без
	// промежуточных ростов слайса
	units := 0
	for i := off; i+1 < len(src); i += 2 {
		if binary.LittleEndian.Uint16(src[i:]) == 0 {
			buf := make([]uint16, units)
			for j := range buf {
				buf[j] = binary.LittleEndian.Uint16(src[off+j*2:])
			}
			return string(utf16.Decode(buf)), i + 2 - off, true
		}
		units++
	}
	return "", 0, false
}
