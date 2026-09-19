// Ключи l2encdec — публичные константы сообщества (портированы из open-l2encdec
// (MIT, Copyright (c) 2024 ritsuwastaken, src/l2encdec.cpp @870fc5b), восходят
// к DStuff l2encdec / acmi L2crypt). Не NCsoft-данные.

package l2ini

import "math/big"

// Key — ключевая пара семейства l2encdec. Большая encrypt-экспонента
// modern-семейства не выражается типом rsa.PublicKey (int) — операции
// блочного возведения в степень идут через math/big напрямую.
type Key struct {
	N       *big.Int
	Encrypt *big.Int // nil — decrypt-only (legacy 413)
	Decrypt *big.Int
}

// Legacy413 — стоковое семейство формата 413: только расшифровка
// (encrypt-экспонента не опубликована).
var Legacy413 = Key{
	N:       mustHex("97df398472ddf737ef0a0cd17e8d172f0fef1661a38a8ae1d6e829bc1c6e4c3cfc19292dda9ef90175e46e7394a18850b6417d03be6eea274d3ed1dde5b5d7bde72cc0a0b71d03608655633881793a02c9a67d9ef2b45eb7c08d4be329083ce450e68f7867b6749314d40511d09bc5744551baa86a89dc38123dc1668fd72d83"),
	Decrypt: big.NewInt(0x35),
}

// Modern — семейство-замена сообщества: им шифруют патчеры и инструменты
// (клиент расшифровывает экспонентой 0x1d).
var Modern = Key{
	N:       mustHex("75b4d6de5c016544068a1acf125869f43d2e09fc55b8b1e289556daf9b8757635593446288b3653da1ce91c87bb1a5c18f16323495c55d7d72c0890a83f69bfd1fd9434eb1c02f3e4679edfa43309319070129c267c85604d87bb65bae205de3707af1d2108881abb567c3b3d069ae67c3a4c6a3aa93d26413d4c66094ae2039"),
	Encrypt: mustHex("30b4c2d798d47086145c75063c8e841e719776e400291d7838d3e6c4405b504c6a07f8fca27f32b86643d2649d1d5f124cdd0bf272f0909dd7352fe10a77b34d831043d9ae541f8263c6fe3d1c14c2f04e43a7253a6dda9a8c1562cbd493c1b631a1957618ad5dfe5ca28553f746e2fc6f2db816c7db223ec91e955081c1de65"),
	Decrypt: big.NewInt(0x1d),
}

// mustHex — константа ключа из hex; падает только на ошибке транскрипции
// (контракт программиста, старт пакета).
func mustHex(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 16)
	if !ok {
		panic("l2ini: ключ не читается как hex: " + s)
	}
	return n
}
