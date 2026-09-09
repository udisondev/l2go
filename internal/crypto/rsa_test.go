package crypto

import (
	"bytes"
	"crypto/rsa"
	"math/big"
	"testing"
)

// Векторы и пиннутый ключ: запуск udisondev/interlude@34fe4c86 (cmd/vectorgen).
// Ключ сгенерирован референсом, не секретен; приватная экспонента в репо не входит.

// Пиннутый тестовый ключ.
const (
	rsaNHex = "ce37a1c452ec1b653d92968d229ebe2c36766e78fbe0f82182cbbd06e37e47fc9414f7ac67bf6fb0691b41c84c08a32d7252fc90406b540e67f3c2e9b5865a6e8b5c98727c9c4a3a267b86930a7d691dc4dcb6b5db4d1fb54d6989fed754e0e48931129af8ca71d55fffe8e6c4b4d5fc38ff436c8e194b79ddc3b17caa081549"
)

func testPublicKey(t *testing.T) *rsa.PublicKey {
	t.Helper()
	n, ok := new(big.Int).SetString(rsaNHex, 16)
	if !ok {
		t.Fatal("пиннутый N не парсится")
	}
	return &rsa.PublicKey{N: n, E: 65537}
}

func TestRSAScrambleGolden(t *testing.T) {
	n := testPublicKey(t)
	mod := n.N.Bytes()
	if len(mod) != 128 {
		t.Fatalf("модуль %d байт, ожидалось 128", len(mod))
	}
	scr, err := RSAScrambleModulus(mod)
	if err != nil {
		t.Fatalf("RSAScrambleModulus: %v", err)
	}
	want := mustHex(t, "f63585b62e70515f1be9101e289efb9285aad8cd20ade794cfa234f8342aa7181d25e5369f751e6536e4a92e88bc76d14aadbffcce721f77ba3073951f8e4f277d691dc452ec1b653d92968d2250cc3341766e78fbe0f82182cbbd06e37e47fc9414f7ac67bf6fb0691b41c84c08a32d7252fc90406b540e67f3c2e9b5865a6e")
	if !bytes.Equal(scr, want) {
		t.Fatalf("golden: got %x, want %x", scr, want)
	}

	back, err := RSAUnscrambleModulus(scr)
	if err != nil {
		t.Fatalf("RSAUnscrambleModulus: %v", err)
	}
	if !bytes.Equal(back, mod) {
		t.Fatal("скрамбл не обратим: модуль не восстановлен")
	}
}

func TestRSAEncryptNoPaddingGolden(t *testing.T) {
	pub := testPublicKey(t)
	pt := mustHex(t, "101112131415161718191a1b1c1d1e1f202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f404142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f606162636465666768696a6b6c6d6e6f")
	want := mustHex(t, "1cbe09f13b5902040d73fab8c47a0d515b5cd7194f05a3ed7ce1871bc3032c18413bb5ba451e7d19732c0521cda1bfc6b16d1add1442b80055ee66df104f6c62524309068834694ab7fe672cd12ae7d4fa541a5e28fc7aacc0da163339b967559fb10e875da40cb469bc777acdd29bfb7efede80d9794087974610afdc7f7eef")
	ct, err := RSAEncryptNoPadding(pub, pt)
	if err != nil {
		t.Fatalf("RSAEncryptNoPadding: %v", err)
	}
	if len(ct) != 128 {
		t.Fatalf("размер шифртекста: got %d, want 128", len(ct))
	}
	if !bytes.Equal(ct, want) {
		t.Fatalf("golden: got %x, want %x", ct, want)
	}
}

func TestRSAGuards(t *testing.T) {
	pub := testPublicKey(t)
	if _, err := RSAScrambleModulus(make([]byte, 64)); err == nil {
		t.Fatal("RSAScrambleModulus(64): ожидалась ошибка")
	}
	if _, err := RSAUnscrambleModulus(nil); err == nil {
		t.Fatal("RSAUnscrambleModulus(nil): ожидалась ошибка")
	}
	if _, err := RSAEncryptNoPadding(pub, make([]byte, 129)); err == nil {
		t.Fatal("RSAEncryptNoPadding(129): больше ключа — ожидалась ошибка")
	}
	if _, err := RSAEncryptNoPadding(pub, nil); err == nil {
		t.Fatal("RSAEncryptNoPadding(nil): ожидалась ошибка")
	}
	if _, err := RSAEncryptNoPadding(nil, []byte{1}); err == nil {
		t.Fatal("RSAEncryptNoPadding(nil-ключ): ожидалась ошибка")
	}
	if _, err := RSAEncryptNoPadding(&rsa.PublicKey{E: 65537}, []byte{1}); err == nil {
		t.Fatal("RSAEncryptNoPadding(без N): ожидалась ошибка")
	}
	if _, err := RSAEncryptNoPadding(&rsa.PublicKey{N: pub.N, E: 1}, []byte{1}); err == nil {
		t.Fatal("RSAEncryptNoPadding(E=1): ожидалась ошибка")
	}
}
