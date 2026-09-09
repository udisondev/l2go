// Package crypto — криптодвижок протокола Interlude 746: Blowfish с LE-семантикой
// блоков, XOR-каскад game-канала, XOR-checksum и паддинг login-кадров, скрамбл
// RSA-модулуса логина. Движок на соединение, без внутренней синхронизации: владелец —
// горутина соединения. Все операции инплейс в буфер вызывающего; нарушение контракта
// — ошибка, не паника.
//
// Семантика портирована из udisondev/interlude@34fe4c86 (pkg/crypto); канон
// расхождений референсов — L2J Mobius CT0 Interlude (loginserver/network/
// LoginEncryption.java). Побайтовые golden-векторы — запуск референса interlude.
package crypto

import (
	"fmt"

	//lint:ignore SA1019 Blowfish обязателен: легаси-провод протокола L2 Interlude, не выбор криптографии для новых систем
	"golang.org/x/crypto/blowfish"
)

// staticLoginKey — статический Blowfish-ключ первого Init-пакета логина
// (зашит в клиенте; ported from udisondev/interlude pkg/crypto).
var staticLoginKey = []byte{
	0x6b, 0x60, 0xcb, 0x5b,
	0x82, 0xce, 0x90, 0xb1,
	0xcc, 0x2b, 0x6c, 0x55,
	0x6c, 0x6c, 0x6c, 0x6c,
}

// bfCipher — Blowfish ECB с LE-трактовкой блока: L2J читает блок как пару
// LE-u32, ядро x/crypto — BE, поэтому вокруг блока делаются байтовые свопы
// (ported from udisondev/interlude pkg/crypto/blowfish.go).
type bfCipher struct {
	c *blowfish.Cipher
}

func newBFCipher(key []byte) (*bfCipher, error) {
	c, err := blowfish.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("blowfish key: %w", err)
	}
	return &bfCipher{c: c}, nil
}

// bare возвращает ядро без LE-обвязки (для теста-инварианта LE ≠ BE).
func (b *bfCipher) bare() *blowfish.Cipher { return b.c }

func guardBlock(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("blowfish: пустой буфер")
	}
	if len(data)%blowfish.BlockSize != 0 {
		return fmt.Errorf("blowfish: размер %d не кратен %d", len(data), blowfish.BlockSize)
	}
	return nil
}

func swapLE(block []byte) {
	block[0], block[1], block[2], block[3] = block[3], block[2], block[1], block[0]
	block[4], block[5], block[6], block[7] = block[7], block[6], block[5], block[4]
}

func (b *bfCipher) encrypt(data []byte) error {
	if err := guardBlock(data); err != nil {
		return err
	}
	for i := 0; i < len(data); i += blowfish.BlockSize {
		blk := data[i : i+blowfish.BlockSize]
		swapLE(blk)
		b.c.Encrypt(blk, blk)
		swapLE(blk)
	}
	return nil
}

func (b *bfCipher) decrypt(data []byte) error {
	if err := guardBlock(data); err != nil {
		return err
	}
	for i := 0; i < len(data); i += blowfish.BlockSize {
		blk := data[i : i+blowfish.BlockSize]
		swapLE(blk)
		b.c.Decrypt(blk, blk)
		swapLE(blk)
	}
	return nil
}
