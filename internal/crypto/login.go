package crypto

import (
	"errors"
	"fmt"
)

// ErrBadKey — ключ движка невалиден (длина вне 1–56 байт).
var ErrBadKey = errors.New("ключ Blowfish недопустимой длины")

// ErrKeyAlreadySet — динамический ключ уже установлен повторно.
var ErrKeyAlreadySet = errors.New("динамический ключ уже установлен")

// ErrKeyNotSet — динамические операции до установки ключа.
var ErrKeyNotSet = errors.New("динамический ключ не установлен")

// ErrStaticPhase — static-операции после перехода в динамическую фазу.
var ErrStaticPhase = errors.New("static-фаза завершена")

// MaxFrameOverhead — максимальный прирост кадра над payload (static-ветка:
// заголовок 8 + блок паддинга 8 + хвост 8). Буфер назначения для Encrypt*
// обязан вмещать payload + MaxFrameOverhead; байты в [len(payload), кадр)
// затираются.
const MaxFrameOverhead = 24

// LoginCrypt — шифрование login-канала. Порт udisondev/interlude@34fe4c86
// (pkg/crypto/login_encryption.go); канон паддинга — L2J Mobius CT0 Interlude.
//
// Двухфазен: создаётся до знания ключа (static-фаза: первый Init-пакет
// статическим Blowfish + XOR-pass), после расшифровки Init вызывающий извлекает
// ключ и ставит его SetKey'ом — дальше обе стороны динамические.
//
// Форма отправляемого кадра — канон L2J Mobius CT0 (безусловное добивание до
// кратности 8 + хвостовой блок; изолирована в frameSize). Приём —
// форм-агностичен: длина кадра приходит в префиксе провода, чексумма — последнее
// слово; Decrypt возвращает расшифрованный кадр целиком, парсер пакетов читает
// поля и хвост игнорирует.
type LoginCrypt struct {
	static  *bfCipher
	dynamic *bfCipher
}

// NewLoginCrypt — движок в static-фазе.
func NewLoginCrypt() *LoginCrypt {
	return &LoginCrypt{static: mustBFCipher(staticLoginKey)}
}

func mustBFCipher(key []byte) *bfCipher {
	c, err := newBFCipher(key)
	if err != nil {
		// Ключ-константа пакета; ошибка здесь — инвариант построения.
		panic(fmt.Sprintf("internal/crypto: статический ключ: %v", err))
	}
	return c
}

// SetKey переводит движок в динамическую фазу (ключ из полей Init).
func (lc *LoginCrypt) SetKey(key []byte) error {
	if lc.dynamic != nil {
		return fmt.Errorf("SetKey: %w", ErrKeyAlreadySet)
	}
	c, err := newBFCipher(key)
	if err != nil {
		return fmt.Errorf("SetKey: %w: %w", ErrBadKey, err)
	}
	lc.dynamic = c
	return nil
}

// frameSize — размер кадра над payload; единственное место канона паддинга.
func frameSize(payload int, static bool) int {
	header := 4
	if static {
		header = 8
	}
	size := payload + header
	size += 8 - size%8
	return size + 8
}

// EncryptInit строит кадр первого Init-пакета в dst (static-фаза): копия
// payload, XOR-pass с ключом xorKey (генерация случайного — забота вызывающего),
// паддинг нулями, статический Blowfish. Возвращает размер кадра. Контракт буфера
// тот же, что у Encrypt: cap(dst) >= len(payload) + MaxFrameOverhead; байты в
// [len(payload), кадр) затираются.
func (lc *LoginCrypt) EncryptInit(dst, payload []byte, xorKey uint32) (int, error) {
	if lc.dynamic != nil {
		return 0, fmt.Errorf("EncryptInit: %w", ErrStaticPhase)
	}
	return lc.encryptFrame(dst, payload, lc.static, xorKey, true)
}

// Encrypt строит динамический кадр в dst: копия payload, чексумма, паддинг
// нулями, динамический Blowfish. Возвращает размер кадра. Контракт буфера:
// cap(dst) >= len(payload) + MaxFrameOverhead; байты в [len(payload), кадр)
// затираются.
func (lc *LoginCrypt) Encrypt(dst, payload []byte) (int, error) {
	if lc.dynamic == nil {
		return 0, fmt.Errorf("Encrypt: %w", ErrKeyNotSet)
	}
	n, err := lc.encryptFrame(dst, payload, lc.dynamic, 0, false)
	if err != nil {
		return 0, fmt.Errorf("Encrypt: %w", err)
	}
	return n, nil
}

func (lc *LoginCrypt) encryptFrame(dst, payload []byte, c *bfCipher, xorKey uint32, static bool) (int, error) {
	if len(payload) == 0 {
		return 0, fmt.Errorf("пустой payload")
	}
	frame := frameSize(len(payload), static)
	if cap(dst) < frame {
		return 0, fmt.Errorf("dst %d байт: нужно %d (payload+MaxFrameOverhead)", cap(dst), frame)
	}
	dst = dst[:frame]
	copy(dst, payload)
	for i := len(payload); i < frame; i++ {
		dst[i] = 0
	}
	if static {
		if err := encXORPass(dst, xorKey); err != nil {
			return 0, err
		}
	} else {
		if err := appendChecksum(dst); err != nil {
			return 0, err
		}
	}
	if err := c.encrypt(dst); err != nil {
		return 0, err
	}
	return frame, nil
}

// DecryptInit расшифровывает кадр Init инплейс (статический Blowfish + обратный
// XOR-pass) в static-фазе. Кадр остаётся целиком: поля парсит вызывающий.
func (lc *LoginCrypt) DecryptInit(frame []byte) error {
	if lc.dynamic != nil {
		return fmt.Errorf("DecryptInit: %w", ErrStaticPhase)
	}
	if err := lc.static.decrypt(frame); err != nil {
		return fmt.Errorf("DecryptInit: %w", err)
	}
	if err := decXORPass(frame); err != nil {
		return fmt.Errorf("DecryptInit: %w", err)
	}
	return nil
}

// Decrypt расшифровывает динамический кадр инплейс и проверяет чексумму;
// битая чексумма — ошибка. Возвращает расшифрованный кадр целиком (payload +
// паддинг + чексумма): длину payload криптослой не восстанавливает.
func (lc *LoginCrypt) Decrypt(frame []byte) error {
	if lc.dynamic == nil {
		return fmt.Errorf("Decrypt: %w", ErrKeyNotSet)
	}
	if err := guardFrame(frame); err != nil {
		return fmt.Errorf("Decrypt: %w", err)
	}
	if err := lc.dynamic.decrypt(frame); err != nil {
		return fmt.Errorf("Decrypt: %w", err)
	}
	ok, err := verifyChecksum(frame)
	if err != nil {
		return fmt.Errorf("Decrypt: %w", err)
	}
	if !ok {
		return fmt.Errorf("Decrypt: %w", ErrBadChecksum)
	}
	return nil
}

// ErrBadChecksum — чексумма кадра не сошлась.
var ErrBadChecksum = errors.New("чексумма кадра")
