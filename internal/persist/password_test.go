package persist

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// Векторы RFC 7914 §12 (PBKDF2-HMAC-SHA-256).
func TestPBKDF2Vectors(t *testing.T) {
	tests := []struct {
		password string
		salt     string
		iter     int
		want     string
	}{
		{
			"passwd", "salt", 1,
			"55ac046e56e3089fec1691c22544b605f94185216dde0465e68b9d57c20dacbc" +
				"49ca9cccf179b645991664b39d77ef317c71b845b1e30bd509112041d3a19783",
		},
		{
			"Password", "NaCl", 80000,
			"4ddcd8f60b98be21830cee5ef22701f9641a4418d04c0414aeff08876b34ab56" +
				"a1d425a1225833549adb841b51c9b3176a272bdebba1d078478f62b397f33c8d",
		},
	}
	for _, tc := range tests {
		got, err := pbkdf2SHA256(tc.password, []byte(tc.salt), tc.iter, 64)
		if err != nil {
			t.Fatalf("pbkdf2SHA256(%q, %q, %d) error = %v", tc.password, tc.salt, tc.iter, err)
		}
		if hex.EncodeToString(got) != tc.want {
			t.Errorf("pbkdf2SHA256(%q, %q, %d) = %x; want %s", tc.password, tc.salt, tc.iter, got, tc.want)
		}
	}
}

func TestHashVerifyRoundtrip(t *testing.T) {
	salt, err := NewSalt()
	if err != nil {
		t.Fatalf("NewSalt() error = %v", err)
	}
	if len(salt) != saltLen {
		t.Errorf("NewSalt() len = %d; want %d", len(salt), saltLen)
	}
	hash, err := HashPassword("секретный пароль", salt)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if !VerifyPassword("секретный пароль", salt, hash) {
		t.Error("VerifyPassword(верный пароль) = false; want true")
	}
	if VerifyPassword("неверный пароль", salt, hash) {
		t.Error("VerifyPassword(неверный пароль) = true; want false")
	}
}

func TestHashVerifyEmptyAndLong(t *testing.T) {
	salt := bytes.Repeat([]byte{7}, saltLen)
	long := strings.Repeat("a", 1<<20)
	for _, password := range []string{"", long} {
		hash, err := HashPassword(password, salt)
		if err != nil {
			t.Fatalf("HashPassword(len=%d) error = %v", len(password), err)
		}
		if !VerifyPassword(password, salt, hash) {
			t.Errorf("VerifyPassword(len=%d) = false; want true", len(password))
		}
		if VerifyPassword(password+"x", salt, hash) {
			t.Errorf("VerifyPassword(len=%d, +1) = true; want false", len(password))
		}
	}
}

func TestVerifyTiming(t *testing.T) {
	salt, _ := NewSalt()
	start := time.Now()
	hash, err := HashPassword("timing", salt)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	elapsed := time.Since(start)
	t.Logf("PBKDF2 %d итераций: %v", pbkdf2Iterations, elapsed)
	if !VerifyPassword("timing", salt, hash) {
		t.Fatal("VerifyPassword() = false; want true")
	}
	if elapsed > 2*time.Second {
		t.Errorf("HashPassword() = %v; want < 2s", elapsed)
	}
}

func TestBurnDummy(t *testing.T) {
	start := time.Now()
	burnDummy()
	t.Logf("фиктивный PBKDF2: %v", time.Since(start))
}
