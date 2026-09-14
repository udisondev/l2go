package persist

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
)

// Параметры парольной схемы — фикс. константы (КТ-1): OWASP для
// PBKDF2-HMAC-SHA256; смена констант означает пересоздание аккаунтов,
// миграций нет (персональный сервер).
const (
	pbkdf2Iterations = 600_000
	saltLen          = 16
	keyLen           = 32
)

// dummySalt — соль фиктивного вывода: выравнивание времени проверки
// несуществующего логина в закрытом режиме (перечисление аккаунтов).
var dummySalt = []byte("la2-persist-dummy")

// pbkdf2SHA256 — вывод ключа с явным числом итераций (тест-векторы RFC 7914 §12
// прогоняются с итерациями вектора).
func pbkdf2SHA256(password string, salt []byte, iter, keyLength int) ([]byte, error) {
	key, err := pbkdf2.Key(sha256.New, password, salt, iter, keyLength)
	if err != nil {
		return nil, fmt.Errorf("persist: pbkdf2: %w", err)
	}
	return key, nil
}

// NewSalt возвращает свежую соль для хэша пароля.
func NewSalt() ([]byte, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("persist: соль пароля: %w", err)
	}
	return salt, nil
}

// HashPassword выводит ключ пароля PBKDF2-HMAC-SHA256.
func HashPassword(password string, salt []byte) ([]byte, error) {
	key, err := pbkdf2SHA256(password, salt, pbkdf2Iterations, keyLen)
	if err != nil {
		return nil, fmt.Errorf("persist: пароль: %w", err)
	}
	return key, nil
}

// VerifyPassword сверяет пароль с ключом сравнением постоянного времени.
func VerifyPassword(password string, salt, want []byte) bool {
	key, err := pbkdf2SHA256(password, salt, pbkdf2Iterations, keyLen)
	if err != nil {
		// Невозможно при валидных константах; ошибка не паникует и не
		// пропускает неверный пароль.
		return false
	}
	return hmac.Equal(key, want)
}

// burnDummy выполняет фиктивный вывод той же ценой, что и VerifyPassword.
func burnDummy() {
	// Ошибка невозможна (валидные константы) и не имеет получателя.
	_, _ = pbkdf2SHA256("", dummySalt, pbkdf2Iterations, keyLen)
}
