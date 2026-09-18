package l2client

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// HexBytes: дамп усечён до 64 Б с маркером остатка — поток мусорных
// 0xFFFF-кадров не заливает лог.
func TestHexBytes(t *testing.T) {
	t.Parallel()
	short := bytes.Repeat([]byte{0xAB}, 10)
	if got, want := HexBytes(short), strings.Repeat("ab", 10); got != want {
		t.Errorf("HexBytes(10 Б) = %s; want %s", got, want)
	}
	long := bytes.Repeat([]byte{0xCD}, 100)
	want := strings.Repeat("cd", 64) + " [+36Б]"
	if got := HexBytes(long); got != want {
		t.Errorf("HexBytes(100 Б) = %.80s…; want усечение до 64 Б + [+36Б]", got)
	}
	if got := HexBytes(nil); got != "" {
		t.Errorf("HexBytes(nil) = %q; want \"\"", got)
	}
}

func TestLogLines(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	LogRecv(&b, "GG_AUTH", Field{K: "response", V: "305419896"})
	LogSend(&b, "LOGOUT")
	LogRecvHex(&b, "SUNRISE", []byte{0x1C, 0x01, 0x02})
	LogRecvHex(&b, "??(0xBA)", []byte{0xBA, 0xAB, 0x01})
	want := "← GG_AUTH response=305419896\n→ LOGOUT\n← SUNRISE hex=1c0102\n← ??(0xBA) hex=baab01\n"
	if b.String() != want {
		t.Errorf("лог = %q; want %q", b.String(), want)
	}
}

// Пайплайн форматтера: поля типизированного пакета и hex-фолбэк.
func ExampleLogRecv() {
	var b bytes.Buffer
	LogRecv(&b, "KEY_PACKET",
		Field{K: "result", V: "1"},
		Field{K: "encryption", V: "true"})
	LogRecvHex(&b, "??(0xFE)", []byte{0xFE, 0x99, 0x09})
	fmt.Println(b.String())
	// Output:
	// ← KEY_PACKET result=1 encryption=true
	// ← ??(0xFE) hex=fe9909
}
