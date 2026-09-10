package l2client

import (
	"bytes"
	"strings"
	"testing"
)

// Quote: строки полей — в кавычках, управляющие руны экранируются \xHH
// (терминальные инъекции и срыв «одной строки на кадр» исключены).
func TestQuote(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"plain", `"plain"`},
		{"", `""`},
		{"с пробелом", `"с пробелом"`},
		{"tab\there", `"tab\x09here"`},
		{"nl\nhere", `"nl\x0Ahere"`},
		{"esc\x1b]0;x\x07", `"esc\x1B]0;x\x07"`},
		{"del\x7f", `"del\x7F"`},
	}
	for _, tt := range tests {
		if got := Quote(tt.in); got != tt.want {
			t.Errorf("Quote(%q) = %s; want %s", tt.in, got, tt.want)
		}
	}
}

// HexBytes: дамп усечён до 64 Б с маркером остатка — поток мусорных
// 0xFFFF-кадров не заливает лог.
func TestHexBytes(t *testing.T) {
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

// Форматтер unknown-опкода: имя по каталогу или «??(0xNN)»; Ex-семейство —
// имя по sub или «??(0xFE:0xNNNN)»; обрезанные тела не читаются.
func TestUnknownName(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want string
	}{
		{"из каталога", []byte{0x1C, 0x01}, "SUNRISE"},
		{"неизвестный", []byte{0xBA, 0xAB}, "??(0xBA)"},
		{"пустое тело", nil, "??(0x??)"},
		{"Ex по sub", []byte{0xFE, 0x38, 0x00, 0x01}, "EX_SHOW_SCREEN_MESSAGE"},
		{"Ex неизвестный sub", []byte{0xFE, 0x99, 0x09, 0x01}, "??(0xFE:0x0999)"},
		{"Ex обрезанный", []byte{0xFE, 0x38}, "??(0xFE)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := unknownName(tt.body); got != tt.want {
				t.Errorf("unknownName(% X) = %q; want %q", tt.body, got, tt.want)
			}
		})
	}
}
