// Векторы: запуск udisondev/interlude@34fe4c86 (cmd/vectorgen).

package crypto

import (
	"bytes"
	"testing"
)

func TestAppendChecksumGolden(t *testing.T) {
	in := mustHex(t, "000102030405060708090a0b0f0e0d0c")
	want := mustHex(t, "000102030405060708090a0b0c0d0e0f")
	out := bytes.Clone(in)
	if err := appendChecksum(out); err != nil {
		t.Fatalf("appendChecksum: %v", err)
	}
	if !bytes.Equal(out, want) {
		t.Fatalf("golden: got %x, want %x", out, want)
	}
}

func TestVerifyChecksum(t *testing.T) {
	frame := mustHex(t, "000102030405060708090a0b0c0d0e0f")
	ok, err := verifyChecksum(frame)
	if err != nil || !ok {
		t.Fatalf("верный кадр: ok=%v err=%v", ok, err)
	}
	frame[3] ^= 0xFF
	ok, err = verifyChecksum(frame)
	if err != nil || ok {
		t.Fatalf("испорченный кадр: ok=%v err=%v", ok, err)
	}
}

func TestEncXORPassGolden(t *testing.T) {
	in := bytes.Clone(mustHex(t, "303132333435363738393a3b3c3d3e3f4041424344454647"))
	want := mustHex(t, "303132334c5d6e7f8898a8b8d0e3eefdecded0c244454647")
	out := bytes.Clone(in)
	if err := encXORPass(out, 0x11223344); err != nil {
		t.Fatalf("encXORPass: %v", err)
	}
	if !bytes.Equal(out, want) {
		t.Fatalf("golden: got %x, want %x", out, want)
	}
	// Обратный проход восстанавливает [4, len-8); байты [len-8, len-4) — накопленный
	// ключ (семантика L2J: encXORPass хранит ключ в кадре), [len-4, len) не тронуты.
	if err := decXORPass(out); err != nil {
		t.Fatalf("decXORPass: %v", err)
	}
	if !bytes.Equal(out[:16], in[:16]) {
		t.Fatalf("dec: [4,16) не восстановлены: %x", out)
	}
	if !bytes.Equal(out[16:20], want[16:20]) {
		t.Fatalf("dec: зона ключа [16,20) изменилась: %x", out[16:20])
	}
	if !bytes.Equal(out[20:], in[20:]) {
		t.Fatalf("dec: хвост изменился: %x", out[20:])
	}
}

func TestChecksumGuards(t *testing.T) {
	if err := appendChecksum(nil); err == nil {
		t.Fatal("appendChecksum(nil): ожидалась ошибка")
	}
	if err := appendChecksum(make([]byte, 12)); err == nil {
		t.Fatal("appendChecksum(12): не кратно 8 — ожидалась ошибка")
	}
	if _, err := verifyChecksum(make([]byte, 4)); err == nil {
		t.Fatal("verifyChecksum(4): ожидалась ошибка")
	}
	if err := encXORPass(make([]byte, 4), 1); err == nil {
		t.Fatal("encXORPass(4): не кратно 8 — ожидалась ошибка")
	}
	if err := decXORPass(make([]byte, 20)); err == nil {
		t.Fatal("decXORPass(20): не кратно 8 — ожидалась ошибка")
	}
	// 8-байтовый кадр законен (условная форма паддинга) — приём толерантен.
	if err := appendChecksum(make([]byte, 8)); err != nil {
		t.Fatalf("appendChecksum(8): 8-байтовый кадр законен: %v", err)
	}
}
