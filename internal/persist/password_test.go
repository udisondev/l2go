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
	salt, err := newSalt()
	if err != nil {
		t.Fatalf("newSalt() error = %v", err)
	}
	if len(salt) != saltLen {
		t.Errorf("newSalt() len = %d; want %d", len(salt), saltLen)
	}
	hash, err := hashPassword("секретный пароль", salt)
	if err != nil {
		t.Fatalf("hashPassword() error = %v", err)
	}
	if !verifyPassword("секретный пароль", salt, hash) {
		t.Error("verifyPassword(верный пароль) = false; want true")
	}
	if verifyPassword("неверный пароль", salt, hash) {
		t.Error("verifyPassword(неверный пароль) = true; want false")
	}
}

func TestHashVerifyEmptyAndLong(t *testing.T) {
	salt := bytes.Repeat([]byte{7}, saltLen)
	long := strings.Repeat("a", 1<<20)
	for _, password := range []string{"", long} {
		hash, err := hashPassword(password, salt)
		if err != nil {
			t.Fatalf("hashPassword(len=%d) error = %v", len(password), err)
		}
		if !verifyPassword(password, salt, hash) {
			t.Errorf("verifyPassword(len=%d) = false; want true", len(password))
		}
		if verifyPassword(password+"x", salt, hash) {
			t.Errorf("verifyPassword(len=%d, +1) = true; want false", len(password))
		}
	}
}

func TestVerifyTiming(t *testing.T) {
	salt := mustSalt(t)
	start := time.Now()
	hash, err := hashPassword("timing", salt)
	if err != nil {
		t.Fatalf("hashPassword() error = %v", err)
	}
	elapsed := time.Since(start)
	t.Logf("PBKDF2 %d итераций: %v", pbkdf2Iterations, elapsed)
	if !verifyPassword("timing", salt, hash) {
		t.Fatal("verifyPassword() = false; want true")
	}
	if elapsed > 2*time.Second {
		t.Errorf("hashPassword() = %v; want < 2s", elapsed)
	}
}

// TestBurnDummyTiming — ослабленное сравнение времени: фиктивный вывод не
// дешевле трети честного (иначе выравнивание miss-тайминга сломано). Порог
// с широким коридором — тайминг-тесты на CI шумят.
func TestBurnDummyTiming(t *testing.T) {
	hashMin, dummyMin := time.Hour, time.Hour
	for i := 0; i < 3; i++ {
		salt := mustSalt(t)
		start := time.Now()
		// ошибка вывода невозможна (валидные константы) и не имеет получателя
		_, _ = hashPassword("probe", salt)
		if d := time.Since(start); d < hashMin {
			hashMin = d
		}
		start = time.Now()
		burnDummy()
		if d := time.Since(start); d < dummyMin {
			dummyMin = d
		}
	}
	t.Logf("pbkdf2: честный %v, фиктивный %v", hashMin, dummyMin)
	if dummyMin < hashMin/3 {
		t.Errorf("фиктивный вывод %v дешевле трети честного %v", dummyMin, hashMin)
	}
}

// mustSalt — соль с проверкой ошибки (пустая соль прошла бы тайминг молча).
func mustSalt(t *testing.T) []byte {
	t.Helper()
	s, err := newSalt()
	if err != nil {
		t.Fatalf("newSalt: %v", err)
	}
	return s
}
