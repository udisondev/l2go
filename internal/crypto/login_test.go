package crypto

import (
	"bytes"
	"errors"
	"testing"
)

const dynKeyHex = "112233445566778899aabbccddeeff00"

func newDynLogin(t *testing.T) *LoginCrypt {
	t.Helper()
	lc := NewLoginCrypt()
	if err := lc.SetKey(mustHex(t, dynKeyHex)); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	return lc
}

func TestLoginCryptDynamicGolden(t *testing.T) {
	// payload ≡ 4 (mod 8) — дискриминирующий размер: различает все три формы паддинга.
	lc := newDynLogin(t)
	payload := mustHex(t, "05000000")
	want := mustHex(t, "f95737f879b213126484afee5241054a8cafab8e64644dda")

	dst := make([]byte, len(payload)+MaxFrameOverhead)
	n, err := lc.Encrypt(dst, payload)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if n != len(want) {
		t.Fatalf("размер кадра: got %d, want %d", n, len(want))
	}
	if !bytes.Equal(dst[:n], want) {
		t.Fatalf("golden: got %x, want %x", dst[:n], want)
	}

	frame := bytes.Clone(dst[:n])
	if err := lc.Decrypt(frame); err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(frame[:len(payload)], payload) {
		t.Fatalf("payload не восстановлен: %x", frame)
	}
}

func TestLoginCryptInitGolden(t *testing.T) {
	// payload ≡ 0 (mod 8) — дискриминирующий размер static-ветки (безусловное
	// добивание против условного: 32 против 24 байт).
	lc := NewLoginCrypt()
	payload := mustHex(t, "6061626364656667")
	want := mustHex(t, "cacf30f034c522ff482aad5bd7be3d4c482aad5bd7be3d4c5a0ac8d4eef3e224")

	dst := make([]byte, len(payload)+MaxFrameOverhead)
	n, err := lc.EncryptInit(dst, payload, 0x11223344)
	if err != nil {
		t.Fatalf("EncryptInit: %v", err)
	}
	if n != 32 {
		t.Fatalf("размер кадра: got %d, want 32", n)
	}
	if !bytes.Equal(dst[:n], want) {
		t.Fatalf("golden: got %x, want %x", dst[:n], want)
	}

	frame := bytes.Clone(dst[:n])
	if err := lc.DecryptInit(frame); err != nil {
		t.Fatalf("DecryptInit: %v", err)
	}
	if !bytes.Equal(frame[:len(payload)], payload) {
		t.Fatalf("payload не восстановлен: %x", frame)
	}
}

func TestLoginCryptDecryptTolerance(t *testing.T) {
	// Один payload в трёх формах кадра (классическая 16 / CT0 24 / условная 8 для
	// выровненного случая): Decrypt принимает любую — приём форм-агностичен.
	payload := []byte{0x05, 0x00, 0x00, 0x00}
	dyn := mustHex(t, dynKeyHex)

	for _, tt := range []struct {
		name  string
		frame []byte
	}{
		{"классическая (безусловная, без хвоста)", buildFrame(t, dyn, payload, false, false)},
		{"CT0 (безусловная, с хвостом)", buildFrame(t, dyn, payload, false, true)},
		{"условная (la2go-форма)", buildFrame(t, dyn, payload, true, false)},
	} {
		lc := NewLoginCrypt()
		if err := lc.SetKey(dyn); err != nil {
			t.Fatalf("%s: SetKey: %v", tt.name, err)
		}
		if err := lc.Decrypt(tt.frame); err != nil {
			t.Fatalf("%s: Decrypt: %v", tt.name, err)
		}
		if !bytes.Equal(tt.frame[:len(payload)], payload) {
			t.Fatalf("%s: payload не восстановлен: %x", tt.name, tt.frame)
		}
	}
}

// buildFrame собирает кадр заданной формы паддинга поверх внутренних примитивов
// (фикстуры толерантности строим сами: BE-шифртекст la2go исключён).
func buildFrame(t *testing.T, key, payload []byte, conditional, tail bool) []byte {
	t.Helper()
	c, err := newBFCipher(key)
	if err != nil {
		t.Fatalf("newBFCipher: %v", err)
	}
	size := len(payload) + 4
	if conditional {
		if size%8 != 0 {
			size += 8 - size%8
		}
	} else {
		size += 8 - size%8
	}
	if tail {
		size += 8
	}
	frame := make([]byte, size)
	copy(frame, payload)
	if err := appendChecksum(frame); err != nil {
		t.Fatalf("appendChecksum: %v", err)
	}
	if err := c.encrypt(frame); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	return frame
}

func TestLoginCryptFrameSize(t *testing.T) {
	for p := 1; p <= 64; p++ {
		for _, static := range []bool{false, true} {
			fs := frameSize(p, static)
			if fs%8 != 0 {
				t.Fatalf("p=%d static=%v: кадр %d не кратен 8", p, static, fs)
			}
			if fs < p+4 || fs > p+MaxFrameOverhead {
				t.Fatalf("p=%d static=%v: кадр %d вне диапазона", p, static, fs)
			}
		}
	}
	// Точки канона CT0: p=4 → 24; p=8 static → 32; p=1440 → 1456; p=40 → 56.
	for _, tt := range []struct {
		p      int
		static bool
		want   int
	}{
		{4, false, 24}, {8, true, 32}, {1440, false, 1456}, {40, false, 56}, {256, false, 272},
	} {
		if got := frameSize(tt.p, tt.static); got != tt.want {
			t.Fatalf("frameSize(%d,%v): got %d, want %d", tt.p, tt.static, got, tt.want)
		}
	}
}

func TestLoginCryptLifecycle(t *testing.T) {
	lc := NewLoginCrypt()
	dst := make([]byte, 64)

	if _, err := lc.Encrypt(dst, []byte{0x01}); err == nil {
		t.Fatal("Encrypt до SetKey: ожидалась ошибка")
	}
	if err := lc.Decrypt(make([]byte, 16)); err == nil {
		t.Fatal("Decrypt до SetKey: ожидалась ошибка")
	}

	if err := lc.SetKey(mustHex(t, dynKeyHex)); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	if err := lc.SetKey(mustHex(t, dynKeyHex)); err == nil {
		t.Fatal("повторный SetKey: ожидалась ошибка")
	}
	if err := lc.DecryptInit(make([]byte, 16)); err == nil {
		t.Fatal("DecryptInit после SetKey: ожидалась ошибка")
	}
	if _, err := lc.EncryptInit(dst, []byte{0x01}, 1); err == nil {
		t.Fatal("EncryptInit после SetKey: ожидалась ошибка")
	}
}

func TestLoginCryptRoundtripProperty(t *testing.T) {
	// Полный цикл обеих веток на всех остатках длины payload mod 8 и много
	// блоков: ловит residue-специфичные ошибки построения кадра (копирование,
	// зануление паддинга, зона XOR-pass коротких static-кадров).
	for p := 1; p <= 64; p++ {
		payload := fill(p)

		// Static: EncryptInit → DecryptInit.
		lc := NewLoginCrypt()
		dst := make([]byte, p+MaxFrameOverhead)
		n, err := lc.EncryptInit(dst, payload, 0x11223344)
		if err != nil {
			t.Fatalf("p=%d EncryptInit: %v", p, err)
		}
		frame := bytes.Clone(dst[:n])
		if err := lc.DecryptInit(frame); err != nil {
			t.Fatalf("p=%d DecryptInit: %v", p, err)
		}
		if !bytes.Equal(frame[:p], payload) {
			t.Fatalf("p=%d static: payload не восстановлен", p)
		}

		// Dynamic: Encrypt → Decrypt (свежий движок со «случайным» ключом).
		lc2 := NewLoginCrypt()
		if err := lc2.SetKey(fill(16)); err != nil {
			t.Fatalf("p=%d SetKey: %v", p, err)
		}
		n, err = lc2.Encrypt(dst, payload)
		if err != nil {
			t.Fatalf("p=%d Encrypt: %v", p, err)
		}
		frame = bytes.Clone(dst[:n])
		if err := lc2.Decrypt(frame); err != nil {
			t.Fatalf("p=%d Decrypt: %v", p, err)
		}
		if !bytes.Equal(frame[:p], payload) {
			t.Fatalf("p=%d dynamic: payload не восстановлен", p)
		}
		if n != frameSize(p, false) {
			t.Fatalf("p=%d: размер кадра %d != frameSize %d", p, n, frameSize(p, false))
		}
	}
}

func TestLoginCryptBadChecksum(t *testing.T) {
	lc := newDynLogin(t)
	frame := buildFrame(t, mustHex(t, dynKeyHex), []byte{0x05, 0x00, 0x00, 0x00}, false, true)
	frame[2] ^= 0xFF // ломаем шифртекст
	if err := lc.Decrypt(frame); err == nil {
		t.Fatal("битый кадр: Decrypt обязан вернуть ошибку")
	}
}

func TestLoginCryptGuards(t *testing.T) {
	lc := newDynLogin(t)
	dst := make([]byte, MaxFrameOverhead)

	if _, err := lc.Encrypt(dst, nil); err == nil {
		t.Fatal("Encrypt(nil): ожидалась ошибка")
	}
	if _, err := lc.Encrypt(dst, fill(MaxFrameOverhead+1)); err == nil {
		t.Fatal("Encrypt без запаса dst: ожидалась ошибка")
	}
	if err := lc.Decrypt(nil); err == nil {
		t.Fatal("Decrypt(nil): ожидалась ошибка")
	}
	if err := lc.Decrypt(make([]byte, 12)); err == nil {
		t.Fatal("Decrypt(12): не кратно 8 — ожидалась ошибка")
	}
	init := NewLoginCrypt()
	if _, err := init.EncryptInit(dst, nil, 1); err == nil {
		t.Fatal("EncryptInit(nil): ожидалась ошибка")
	}
	if err := init.DecryptInit(make([]byte, 12)); err == nil {
		t.Fatal("DecryptInit(12): не кратно 8 — ожидалась ошибка")
	}
}

func TestLoginCryptErrorsWrapped(t *testing.T) {
	lc := NewLoginCrypt()
	err := lc.SetKey(nil)
	if err == nil {
		t.Fatal("SetKey(nil): ожидалась ошибка")
	}
	if !errors.Is(err, ErrBadKey) {
		t.Fatalf("ошибка не идентифицируема errors.Is: %v", err)
	}
}
