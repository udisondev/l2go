// Package l2ini — кодек конфигурации клиента l2.ini формата 413: расшифровка,
// шифрование и проверка CRC-хвоста.
//
// Порт decode/encode-ветки open-l2encdec (MIT, Copyright (c) 2024
// ritsuwastaken, github.com/ritsuwastaken/open-l2encdec @870fc5b,
// src/{l2encdec,rsa,zlib_utils,utils}.cpp). Формат: [28 байт заголовка
// «Lineage2Ver413» UTF-16LE][RSA-блоки по 128 байт][20-байтовый хвост с
// CRC32 LE на +12 по заголовку+телу]. Блок: 1024-битное число big-endian,
// внутри после возведения в степени — нули, байт длины чанка на +3 (≤124) и
// данные, прижатые к концу блока с выравниванием на 4. Склеенные чанки —
// [u32 LE распакованный размер][zlib-поток].
//
// Ключи l2encdec — публичные константы сообщества (не NCsoft-данные); живые
// l2.ini — файлы клиента, в репозиторий не попадают.
package l2ini

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math/big"
)

// Ошибки кодека: именованные, с контекстом на границах (файл:блок:офсет).
var (
	ErrShortFile = errors.New("файл короче формата")
	ErrHeader    = errors.New("чужой заголовок")
	ErrBlockSize = errors.New("тело не кратно размеру блока")
	ErrChunkLen  = errors.New("длина чанка блока")
	ErrZlib      = errors.New("zlib-поток")
	ErrSize      = errors.New("размер распаковки")
	ErrCRC       = errors.New("CRC-хвост")
	ErrEmpty     = errors.New("пустой вход")
	ErrNoEncrypt = errors.New("ключ без encrypt-экспоненты")
)

// Геометрия формата 413.
const (
	headerSize    = 28 // «Lineage2Ver413» UTF-16LE
	tailSize      = 20
	tailCRCOffset = 12
	blockSize     = 128
	blockBody     = 124
)

// header413 — заголовок формата: строка «Lineage2Ver413» в UTF-16LE.
func header413() []byte {
	const s = "Lineage2Ver413"
	out := make([]byte, len(s)*2)
	for i := 0; i < len(s); i++ {
		out[2*i] = s[i]
		out[2*i+1] = 0
	}
	return out
}

// Decode расшифровывает файл l2.ini ключом семейства (legacy 413 или modern)
// и возвращает открытый текст. CRC не проверяется — это отдельная Verify.
func Decode(data []byte, k Key) ([]byte, error) {
	if len(data) < headerSize+tailSize {
		return nil, fmt.Errorf("l2ini: файл %d байт < %d: %w", len(data), headerSize+tailSize, ErrShortFile)
	}
	if !bytes.Equal(data[:headerSize], header413()) {
		return nil, fmt.Errorf("l2ini: заголовок начинается с %q: %w", preview(data[:headerSize]), ErrHeader)
	}
	body := data[headerSize : len(data)-tailSize]
	if len(body)%blockSize != 0 {
		return nil, fmt.Errorf("l2ini: тело %d байт: %w", len(body), ErrBlockSize)
	}
	blob := make([]byte, 0, len(body)/blockSize*blockBody)
	for off := 0; off < len(body); off += blockSize {
		block := powBlock(body[off:off+blockSize], k.Decrypt, k.N)
		chunk := int(block[3])
		if chunk > blockBody {
			return nil, fmt.Errorf("l2ini: блок %d: %d > %d: %w", off/blockSize, chunk, blockBody, ErrChunkLen)
		}
		start := blockSize - align4(chunk)
		blob = append(blob, block[start:start+chunk]...)
	}
	return inflate(blob)
}

// Encode шифрует открытый текст modern-семейством (или тестовым ключом) и
// достраивает заголовок и CRC-хвост. Выход детерминирован: raw-RSA без
// случайного паддинга.
func Encode(plain []byte, k Key) ([]byte, error) {
	if len(plain) == 0 {
		return nil, fmt.Errorf("l2ini: %w", ErrEmpty)
	}
	if k.Encrypt == nil {
		return nil, fmt.Errorf("l2ini: %w (legacy 413 — только decode)", ErrNoEncrypt)
	}
	var packed bytes.Buffer
	var size [4]byte
	binary.LittleEndian.PutUint32(size[:], uint32(len(plain)))
	packed.Write(size[:])
	zw, err := zlib.NewWriterLevel(&packed, zlib.BestCompression)
	if err != nil {
		return nil, fmt.Errorf("l2ini: zlib: %w", err)
	}
	if _, err := zw.Write(plain); err != nil {
		return nil, fmt.Errorf("l2ini: zlib: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("l2ini: zlib: %w", err)
	}

	out := make([]byte, headerSize)
	copy(out, header413())
	out = append(out, encryptBlocks(pad(packed.Bytes()), k)...)
	var tail [tailSize]byte
	binary.LittleEndian.PutUint32(tail[tailCRCOffset:], crc32.ChecksumIEEE(out))
	return append(out, tail[:]...), nil
}

// Verify проверяет CRC-хвост: CRC32 заголовка+тела против u32 LE хвоста.
func Verify(data []byte) error {
	if len(data) < tailSize {
		return fmt.Errorf("l2ini: файл %d байт < хвоста: %w", len(data), ErrShortFile)
	}
	want := binary.LittleEndian.Uint32(data[len(data)-tailSize+tailCRCOffset:])
	if got := crc32.ChecksumIEEE(data[:len(data)-tailSize]); got != want {
		return fmt.Errorf("l2ini: тело %08X, хвост %08X: %w", got, want, ErrCRC)
	}
	return nil
}

// pad — чанковка zlib-блоба на блоки: нулевой блок, длина чанка на +3,
// данные прижаты к концу с выравниванием на 4.
func pad(blob []byte) []byte {
	blocks := make([]byte, (len(blob)+blockBody-1)/blockBody*blockSize)
	for off, src := 0, 0; src < len(blob); off, src = off+blockSize, src+blockBody {
		chunk := min(len(blob)-src, blockBody)
		blocks[off+3] = byte(chunk)
		start := off + blockSize - align4(chunk)
		copy(blocks[start:start+chunk], blob[src:src+chunk])
	}
	return blocks
}

// encryptBlocks — возведение каждого блока в encrypt-степень.
func encryptBlocks(blocks []byte, k Key) []byte {
	out := make([]byte, len(blocks))
	for off := 0; off < len(blocks); off += blockSize {
		copy(out[off:off+blockSize], powBlock(blocks[off:off+blockSize], k.Encrypt, k.N))
	}
	return out
}

// powBlock — блочное возведение в степень: число big-endian, m^exp mod n,
// выход выровнен до размера блока левыми нулями.
func powBlock(block []byte, exp, mod *big.Int) []byte {
	m := new(big.Int).SetBytes(block)
	out := make([]byte, blockSize)
	new(big.Int).Exp(m, exp, mod).FillBytes(out)
	return out
}

// inflate — [u32 LE размер][zlib-поток] → открытый текст с проверкой размера.
func inflate(blob []byte) ([]byte, error) {
	if len(blob) < 4 {
		return nil, fmt.Errorf("l2ini: сжатый blob %d байт < 4: %w", len(blob), ErrZlib)
	}
	want := binary.LittleEndian.Uint32(blob)
	zr, err := zlib.NewReader(bytes.NewReader(blob[4:]))
	if err != nil {
		return nil, fmt.Errorf("l2ini: %w: %w", ErrZlib, err)
	}
	defer zr.Close()
	plain, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("l2ini: %w: %w", ErrZlib, err)
	}
	if len(plain) != int(want) {
		return nil, fmt.Errorf("l2ini: распаковано %d, заявлено %d: %w", len(plain), want, ErrSize)
	}
	return plain, nil
}

func align4(n int) int { return (n + 3) &^ 3 }

// preview — читаемый префикс байтов заголовка для диагностики.
func preview(b []byte) string {
	const max = 8
	if len(b) > max {
		b = b[:max]
	}
	return fmt.Sprintf("%x", b)
}
