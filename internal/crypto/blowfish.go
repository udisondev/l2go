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

import "fmt"

// staticLoginKey — статический Blowfish-ключ первого Init-пакета логина
// (зашит в клиенте; порт udisondev/interlude@34fe4c86 pkg/crypto).
var staticLoginKey = []byte{
	0x6b, 0x60, 0xcb, 0x5b,
	0x82, 0xce, 0x90, 0xb1,
	0xcc, 0x2b, 0x6c, 0x55,
	0x6c, 0x6c, 0x6c, 0x6c,
}

// bfCipher — Blowfish ECB с LE-упаковкой блоков: L2J BlowfishEngine читает
// полублоки little-endian. Ядро алгоритма и ключевой график — порт
// golang.org/x/crypto/blowfish (BSD-3, The Go Authors; порт C-реализации
// Брюса Шнайера), таблицы — в blowfish_tables.go. Отличие порта: данные
// упаковываются LE, поэтому своп-обёртка вокруг BE-ядра не нужна; слова ключа
// ключевой график читает big-endian (как ядро-источник) — это побайтово
// совпадает с эталонным interlude (x/crypto + свопы данных) и живым трафиком.
type bfCipher struct {
	p              [18]uint32
	s0, s1, s2, s3 [256]uint32
}

func newBFCipher(key []byte) (*bfCipher, error) {
	if len(key) < 1 || len(key) > 56 {
		return nil, fmt.Errorf("ключ Blowfish: длина %d вне 1–56 байт", len(key))
	}
	c := &bfCipher{p: p, s0: s0, s1: s1, s2: s2, s3: s3}
	c.expandKey(key)
	return c, nil
}

// expandKey — ключевой график Blowfish: слова ключа (big-endian, циркулярно)
// XOR-ятся в P-массив, затем каскад шифрования нулевого блока обновляет P и S.
func (c *bfCipher) expandKey(key []byte) {
	j := 0
	for i := range c.p {
		var d uint32
		for range 4 {
			d = d<<8 | uint32(key[j])
			j++
			if j >= len(key) {
				j = 0
			}
		}
		c.p[i] ^= d
	}

	var l, r uint32
	for i := 0; i < 18; i += 2 {
		l, r = c.encryptBlock(l, r)
		c.p[i], c.p[i+1] = l, r
	}
	for i := 0; i < 256; i += 2 {
		l, r = c.encryptBlock(l, r)
		c.s0[i], c.s0[i+1] = l, r
	}
	for i := 0; i < 256; i += 2 {
		l, r = c.encryptBlock(l, r)
		c.s1[i], c.s1[i+1] = l, r
	}
	for i := 0; i < 256; i += 2 {
		l, r = c.encryptBlock(l, r)
		c.s2[i], c.s2[i+1] = l, r
	}
	for i := 0; i < 256; i += 2 {
		l, r = c.encryptBlock(l, r)
		c.s3[i], c.s3[i+1] = l, r
	}
}

func (c *bfCipher) f(x uint32) uint32 {
	return ((c.s0[byte(x>>24)] + c.s1[byte(x>>16)]) ^ c.s2[byte(x>>8)]) + c.s3[byte(x)]
}

func (c *bfCipher) encryptBlock(l, r uint32) (uint32, uint32) {
	xl, xr := l, r
	xl ^= c.p[0]
	for i := 1; i <= 16; i++ {
		if i%2 == 1 {
			xr ^= c.f(xl) ^ c.p[i]
		} else {
			xl ^= c.f(xr) ^ c.p[i]
		}
	}
	xr ^= c.p[17]
	return xr, xl
}

func (c *bfCipher) decryptBlock(l, r uint32) (uint32, uint32) {
	xl, xr := l, r
	xl ^= c.p[17]
	for i := 16; i >= 1; i-- {
		if i%2 == 0 {
			xr ^= c.f(xl) ^ c.p[i]
		} else {
			xl ^= c.f(xr) ^ c.p[i]
		}
	}
	xr ^= c.p[0]
	return xr, xl
}

func guardBlock(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("blowfish: пустой буфер")
	}
	if len(data)%8 != 0 {
		return fmt.Errorf("blowfish: размер %d не кратен 8", len(data))
	}
	return nil
}

func (c *bfCipher) encrypt(data []byte) error {
	if err := guardBlock(data); err != nil {
		return err
	}
	for i := 0; i < len(data); i += 8 {
		blk := data[i : i+8 : i+8]
		l := uint32(blk[0]) | uint32(blk[1])<<8 | uint32(blk[2])<<16 | uint32(blk[3])<<24
		r := uint32(blk[4]) | uint32(blk[5])<<8 | uint32(blk[6])<<16 | uint32(blk[7])<<24
		l, r = c.encryptBlock(l, r)
		blk[0], blk[1], blk[2], blk[3] = byte(l), byte(l>>8), byte(l>>16), byte(l>>24)
		blk[4], blk[5], blk[6], blk[7] = byte(r), byte(r>>8), byte(r>>16), byte(r>>24)
	}
	return nil
}

func (c *bfCipher) decrypt(data []byte) error {
	if err := guardBlock(data); err != nil {
		return err
	}
	for i := 0; i < len(data); i += 8 {
		blk := data[i : i+8 : i+8]
		l := uint32(blk[0]) | uint32(blk[1])<<8 | uint32(blk[2])<<16 | uint32(blk[3])<<24
		r := uint32(blk[4]) | uint32(blk[5])<<8 | uint32(blk[6])<<16 | uint32(blk[7])<<24
		l, r = c.decryptBlock(l, r)
		blk[0], blk[1], blk[2], blk[3] = byte(l), byte(l>>8), byte(l>>16), byte(l>>24)
		blk[4], blk[5], blk[6], blk[7] = byte(r), byte(r>>8), byte(r>>16), byte(r>>24)
	}
	return nil
}
