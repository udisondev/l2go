package crypto

import (
	"bytes"
	"testing"
)

// Векторы: запуск udisondev/interlude@34fe4c86 (cmd/vectorgen).
// wantAfterEncrypt — исходящий каскад (outKey), wantAfterDecrypt — входящий
// (inKey) на тех же байтах payload; это слепки трансформов каждого направления,
// а не одна проводная пара «шифртекст→расшифровка».

func TestGameCryptGoldenSequence(t *testing.T) {
	golden := []struct {
		payload          []byte
		wantAfterEncrypt []byte
		wantAfterDecrypt []byte
	}{
		{
			mustHex(t, "0000002c01"),
			mustHex(t, "a113d028cc"),
			mustHex(t, "a1b2c3f8c8"),
		},
		{
			bytes.Repeat([]byte{0xAB}, 20),
			mustHex(t, "0a137b044a17bd1472fec66c66a13b070d147c03"),
			mustHex(t, "0ab2c3d4e5f60102cd279301a16c3197a1b2c3d4"),
		},
		{
			bytes.Repeat([]byte{0xCD}, 100),
			mustHex(t, "6c131d042c17db1438d28c402c8d712b4738362f073cf03f13f9a76b07a65a006c131d042c17db1438d28c402c8d712b4738362f073cf03f13f9a76b07a65a006c131d042c17db1438d28c402c8d712b4738362f073cf03f13f9a76b07a65a006c131d04"),
			mustHex(t, "6cb2c3d4e5f60102e1279301a16c3197a1b2c3d4e5f60102e1279301a16c3197a1b2c3d4e5f60102e1279301a16c3197a1b2c3d4e5f60102e1279301a16c3197a1b2c3d4e5f60102e1279301a16c3197a1b2c3d4e5f60102e1279301a16c3197a1b2c3d4"),
		},
	}

	var wire [8]byte
	copy(wire[:], mustHex(t, "a1b2c3d4e5f60102"))
	gc := NewGameCrypt(wire)
	gc.Enable()

	for i, f := range golden {
		enc := bytes.Clone(f.payload)
		if err := gc.Encrypt(enc); err != nil {
			t.Fatalf("p%d Encrypt: %v", i, err)
		}
		if !bytes.Equal(enc, f.wantAfterEncrypt) {
			t.Fatalf("p%d Encrypt: got %x, want %x", i, enc, f.wantAfterEncrypt)
		}

		dec := bytes.Clone(f.payload)
		if err := gc.Decrypt(dec); err != nil {
			t.Fatalf("p%d Decrypt: %v", i, err)
		}
		if !bytes.Equal(dec, f.wantAfterDecrypt) {
			t.Fatalf("p%d Decrypt: got %x, want %x", i, dec, f.wantAfterDecrypt)
		}
	}
}

func TestGameCryptCounterAdvances(t *testing.T) {
	// Один и тот же пакет дважды подряд шифруется по-разному: счётчик сдвигается.
	var wire [8]byte
	copy(wire[:], mustHex(t, "0102030405060708"))
	gc := NewGameCrypt(wire)
	gc.Enable()
	p := fill(20)
	a, b := bytes.Clone(p), bytes.Clone(p)
	if err := gc.Encrypt(a); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if err := gc.Encrypt(b); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("счётчик не сдвинулся: повторный пакет зашифрован теми же байтами")
	}
}

func TestGameCryptPeerToPeer(t *testing.T) {
	// Встречный обмен: пара движков с одним ключом; каждая сторона шифрует своим
	// направлением и расшифровывает встречное; оба счётчика идут в ногу.
	var wire [8]byte
	copy(wire[:], fill(8))
	client, server := NewGameCrypt(wire), NewGameCrypt(wire)
	client.Enable()
	server.Enable()

	for i := 0; i < 30; i++ {
		c2s := fill(1 + (i*7)%200)
		s2c := fill(1 + (i*13)%200)

		want := bytes.Clone(c2s)
		if err := client.Encrypt(c2s); err != nil {
			t.Fatalf("i=%d client.Encrypt: %v", i, err)
		}
		if err := server.Decrypt(c2s); err != nil {
			t.Fatalf("i=%d server.Decrypt: %v", i, err)
		}
		if !bytes.Equal(c2s, want) {
			t.Fatalf("i=%d: клиент→сервер байты не восстановлены", i)
		}

		want = bytes.Clone(s2c)
		if err := server.Encrypt(s2c); err != nil {
			t.Fatalf("i=%d server.Encrypt: %v", i, err)
		}
		if err := client.Decrypt(s2c); err != nil {
			t.Fatalf("i=%d client.Decrypt: %v", i, err)
		}
		if !bytes.Equal(s2c, want) {
			t.Fatalf("i=%d: сервер→клиент байты не восстановлены", i)
		}
	}
}

func TestGameCryptPassthroughBeforeEnable(t *testing.T) {
	var wire [8]byte
	gc := NewGameCrypt(wire)
	if gc.IsEnabled() {
		t.Fatal("до Enable движок включён")
	}
	p := fill(32)
	want := bytes.Clone(p)
	if err := gc.Encrypt(p); err != nil {
		t.Fatalf("Encrypt до Enable: %v", err)
	}
	if !bytes.Equal(p, want) {
		t.Fatal("до Enable Encrypt не прозрачен")
	}
	if err := gc.Decrypt(p); err != nil {
		t.Fatalf("Decrypt до Enable: %v", err)
	}
	if !bytes.Equal(p, want) {
		t.Fatal("до Enable Decrypt не прозрачен")
	}
}

func TestGameCryptGuards(t *testing.T) {
	var wire [8]byte
	gc := NewGameCrypt(wire)
	gc.Enable()
	if err := gc.Encrypt(nil); err == nil {
		t.Fatal("Encrypt(nil): ожидалась ошибка")
	}
	if err := gc.Decrypt(nil); err == nil {
		t.Fatal("Decrypt(nil): ожидалась ошибка")
	}
}
