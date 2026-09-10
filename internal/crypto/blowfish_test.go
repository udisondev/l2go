package crypto

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// Golden-векторы: запуск udisondev/interlude@34fe4c8657d8b6bf338a5d2e788e197489a44f8f
// (cmd/vectorgen, фиксированные входы, буферы занулены).

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex decode: %v", err)
	}
	return b
}

// fill возвращает детерминированный непаттерновый буфер (XOR-словa не зануляются).
func fill(n int) []byte {
	b := make([]byte, n)
	var x uint32 = 0x9E3779B9
	for i := range b {
		x = x*1664525 + 1013904223
		b[i] = byte(x >> 24)
	}
	return b
}

func TestBFCipherGoldenBlock(t *testing.T) {
	c, err := newBFCipher(staticLoginKey)
	if err != nil {
		t.Fatalf("newBFCipher: %v", err)
	}
	block := mustHex(t, "000102030405060708090a0b0c0d0e0f")
	want := mustHex(t, "458ef8cb40966a791b9161dbc9042822")
	orig := bytes.Clone(block)
	if err := c.encrypt(block); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !bytes.Equal(block, want) {
		t.Fatalf("golden LE-вектор: got %x, want %x", block, want)
	}
	if err := c.decrypt(block); err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(block, orig) {
		t.Fatalf("раундтрип: got %x, want %x", block, orig)
	}
}

func TestBFCipherLENotBE(t *testing.T) {
	// Инвариант LE-упаковки: BE-шифрование того же блока (слова в BE-представлении —
	// эквивалент реверса 4-байтовых групп до и после) обязано давать ДРУГИЕ байты.
	c, err := newBFCipher(staticLoginKey)
	if err != nil {
		t.Fatalf("newBFCipher: %v", err)
	}
	le := mustHex(t, "000102030405060708090a0b0c0d0e0f")
	if err := c.encrypt(le); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	be := mustHex(t, "000102030405060708090a0b0c0d0e0f")
	swapWords(be)
	if err := c.encrypt(be); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	swapWords(be)
	if bytes.Equal(le, be) {
		t.Fatal("LE и BE шифротексты совпали: вектор не дискриминирует")
	}
}

// swapWords меняет порядок байтов в каждом 4-байтовом слове (LE↔BE представление).
func swapWords(b []byte) {
	for i := 0; i+4 <= len(b); i += 4 {
		b[i], b[i+1], b[i+2], b[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
}

func TestBFCipherRoundtripProperty(t *testing.T) {
	for _, size := range []int{8, 16, 64, 256, 1024} {
		key := fill(16)
		c, err := newBFCipher(key)
		if err != nil {
			t.Fatalf("newBFCipher(%d): %v", size, err)
		}
		data := fill(size)
		orig := bytes.Clone(data)
		if err := c.encrypt(data); err != nil {
			t.Fatalf("encrypt(%d): %v", size, err)
		}
		if bytes.Equal(data, orig) {
			t.Fatalf("encrypt(%d): данные не изменились", size)
		}
		if err := c.decrypt(data); err != nil {
			t.Fatalf("decrypt(%d): %v", size, err)
		}
		if !bytes.Equal(data, orig) {
			t.Fatalf("раундтрип(%d): байты не восстановлены", size)
		}
	}
}

func TestBFCipherKeyValidation(t *testing.T) {
	for _, tt := range []struct {
		name  string
		n     int
		valid bool
	}{
		{"пустой ключ", 0, false},
		{"1 байт", 1, true},
		{"56 байт", 56, true},
		{"57 байт", 57, false},
	} {
		_, err := newBFCipher(fill(tt.n))
		if tt.valid && err != nil {
			t.Fatalf("%s: неожиданная ошибка: %v", tt.name, err)
		}
		if !tt.valid && err == nil {
			t.Fatalf("%s: ожидалась ошибка", tt.name)
		}
	}
}

func TestBFCipherGuards(t *testing.T) {
	c, err := newBFCipher(staticLoginKey)
	if err != nil {
		t.Fatalf("newBFCipher: %v", err)
	}
	if err := c.encrypt(nil); err == nil {
		t.Fatal("encrypt(nil): ожидалась ошибка")
	}
	if err := c.encrypt(make([]byte, 12)); err == nil {
		t.Fatal("encrypt(12): не кратно 8 — ожидалась ошибка")
	}
	if err := c.decrypt(make([]byte, 4)); err == nil {
		t.Fatal("decrypt(4): ожидалась ошибка")
	}
}
