package crypto

import (
	"testing"
)

// Аллокационный гейт: горячие операции не аллоцируют после инициализации.

func TestAllocsGameCrypt(t *testing.T) {
	var wire [8]byte
	copy(wire[:], fill(8))
	gc := NewGameCrypt(wire)
	gc.Enable()
	frame := fill(40)

	n := testing.AllocsPerRun(100, func() {
		if err := gc.Encrypt(frame); err != nil {
			t.Fatal(err)
		}
		if err := gc.Decrypt(frame); err != nil {
			t.Fatal(err)
		}
	})
	if n != 0 {
		t.Fatalf("GameCrypt.Encrypt+Decrypt: %v аллокаций, ожидалось 0", n)
	}
}

func TestAllocsLoginCrypt(t *testing.T) {
	lc := NewLoginCrypt()
	if err := lc.SetKey(fill(16)); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	dst := make([]byte, 64)
	payload := fill(40)

	n := testing.AllocsPerRun(100, func() {
		if _, err := lc.Encrypt(dst, payload); err != nil {
			t.Fatal(err)
		}
	})
	if n != 0 {
		t.Fatalf("LoginCrypt.Encrypt: %v аллокаций, ожидалось 0", n)
	}

	frame := make([]byte, 40, 40+MaxFrameOverhead)
	copy(frame, fill(40))
	frameLen, err := lc.Encrypt(frame, frame[:40])
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	n = testing.AllocsPerRun(100, func() {
		// ECB без состояния: расшифровать и зашифровать обратно детерминированно
		// восстанавливает шифртекст — две операции в одном замере, обе обязаны
		// быть безаллокационными.
		if err := lc.Decrypt(frame[:frameLen]); err != nil {
			t.Fatal(err)
		}
		if _, err := lc.Encrypt(frame, frame[:40]); err != nil {
			t.Fatal(err)
		}
	})
	if n != 0 {
		t.Fatalf("LoginCrypt.Decrypt(+восстановление): %v аллокаций, ожидалось 0", n)
	}
}

func TestAllocsChecksumAndXORPass(t *testing.T) {
	frame := fill(24)
	n := testing.AllocsPerRun(100, func() {
		if err := appendChecksum(frame); err != nil {
			t.Fatal(err)
		}
	})
	if n != 0 {
		t.Fatalf("appendChecksum: %v аллокаций, ожидалось 0", n)
	}

	n = testing.AllocsPerRun(100, func() {
		if err := encXORPass(frame, 0x11223344); err != nil {
			t.Fatal(err)
		}
	})
	if n != 0 {
		t.Fatalf("encXORPass: %v аллокаций, ожидалось 0", n)
	}
}
