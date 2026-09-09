package crypto

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"math/big"
)

// ErrBadLength — вход RSA-операции неверной длины.
var ErrBadLength = errors.New("длина входа RSA")

// RSA-блок логина: модулус RSA-1024 (e = 65537) путешествует в Init-пакете в
// четырёхшаговой обфускации L2J ScrambledKeyPair; учётные данные AuthLogin
// шифруются открытым ключом без паддинга (RSA/ECB/NoPadding).
// Портировано из udisondev/interlude@34fe4c86 (pkg/crypto/rsa.go).

// rsaModulusSize — размер модулуса RSA-1024 в байтах.
const rsaModulusSize = 128

// RSAScrambleModulus применяет четырёхшаговую обфускацию к 128-байтовому
// модулусу (шаги: своп 0x00–0x03 ↔ 0x4D–0x50; XOR первых 0x40 с последними;
// XOR 0x0D–0x10 с 0x34–0x37; XOR последних 0x40 с первыми).
func RSAScrambleModulus(modulus []byte) ([]byte, error) {
	if len(modulus) != rsaModulusSize {
		return nil, fmt.Errorf("RSAScrambleModulus: модулус %d байт, ожидалось %d: %w",
			len(modulus), rsaModulusSize, ErrBadLength)
	}
	s := make([]byte, rsaModulusSize)
	copy(s, modulus)
	for i := range 4 {
		s[0x00+i], s[0x4D+i] = s[0x4D+i], s[0x00+i]
	}
	for i := range 0x40 {
		s[0x00+i] ^= s[0x40+i]
	}
	for i := range 4 {
		s[0x0D+i] ^= s[0x34+i]
	}
	for i := range 0x40 {
		s[0x40+i] ^= s[0x00+i]
	}
	return s, nil
}

// RSAUnscrambleModulus — обратная обфускация.
func RSAUnscrambleModulus(scrambled []byte) ([]byte, error) {
	if len(scrambled) != rsaModulusSize {
		return nil, fmt.Errorf("RSAUnscrambleModulus: вход %d байт, ожидалось %d: %w",
			len(scrambled), rsaModulusSize, ErrBadLength)
	}
	m := make([]byte, rsaModulusSize)
	copy(m, scrambled)
	for i := range 0x40 {
		m[0x40+i] ^= m[0x00+i]
	}
	for i := range 4 {
		m[0x0D+i] ^= m[0x34+i]
	}
	for i := range 0x40 {
		m[0x00+i] ^= m[0x40+i]
	}
	for i := range 4 {
		m[0x00+i], m[0x4D+i] = m[0x4D+i], m[0x00+i]
	}
	return m, nil
}

// RSAEncryptNoPadding шифрует блок открытым ключом без паддинга
// (c = m^e mod n), результат выравнен до размера ключа.
func RSAEncryptNoPadding(pub *rsa.PublicKey, plaintext []byte) ([]byte, error) {
	if pub == nil || pub.N == nil || pub.E < 2 {
		return nil, fmt.Errorf("RSAEncryptNoPadding: вырожденный открытый ключ: %w", ErrBadKey)
	}
	keySize := (pub.N.BitLen() + 7) / 8
	if len(plaintext) == 0 || len(plaintext) > keySize {
		return nil, fmt.Errorf("RSAEncryptNoPadding: вход %d байт против ключа %d: %w",
			len(plaintext), keySize, ErrBadLength)
	}
	m := new(big.Int).SetBytes(plaintext)
	e := big.NewInt(int64(pub.E))
	c := new(big.Int).Exp(m, e, pub.N)
	out := make([]byte, keySize)
	c.FillBytes(out)
	return out, nil
}
