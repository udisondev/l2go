package crypto

import (
	"encoding/binary"
	"fmt"
)

// Checksum и XOR-pass login-кадров. Семантика L2J NewCrypt
// (ported from udisondev/interlude pkg/crypto/checksum.go):
// чексумма — XOR всех LE-u32 слов кадра, хранится в последнем слове;
// encXORPass — проход с накапливающимся ключом по словам [4, len-8), накопленный
// ключ записывается в слово [len-8, len-4) — эти 4 байта payload затираются.

// guardFrame допускает любой кадр, кратный 8 и не меньше 8: приём форм-агностичен,
// в живом трафике встречаются 8-байтовые кадры условной формы паддинга.
func guardFrame(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("пустой кадр")
	}
	if len(data)%8 != 0 {
		return fmt.Errorf("кадр %d байт: не кратен 8", len(data))
	}
	return nil
}

func appendChecksum(data []byte) error {
	if err := guardFrame(data); err != nil {
		return fmt.Errorf("appendChecksum: %w", err)
	}
	var sum uint32
	for i := 0; i < len(data)-4; i += 4 {
		sum ^= binary.LittleEndian.Uint32(data[i:])
	}
	binary.LittleEndian.PutUint32(data[len(data)-4:], sum)
	return nil
}

func verifyChecksum(data []byte) (bool, error) {
	if err := guardFrame(data); err != nil {
		return false, fmt.Errorf("verifyChecksum: %w", err)
	}
	var sum uint32
	for i := 0; i < len(data); i += 4 {
		sum ^= binary.LittleEndian.Uint32(data[i:])
	}
	return sum == 0, nil
}

func encXORPass(data []byte, key uint32) error {
	if err := guardFrame(data); err != nil {
		return fmt.Errorf("encXORPass: %w", err)
	}
	ecx := key
	stop := len(data) - 8
	for pos := 4; pos < stop; pos += 4 {
		edx := binary.LittleEndian.Uint32(data[pos:])
		ecx += edx
		edx ^= ecx
		binary.LittleEndian.PutUint32(data[pos:], edx)
	}
	binary.LittleEndian.PutUint32(data[stop:], ecx)
	return nil
}

func decXORPass(data []byte) error {
	if err := guardFrame(data); err != nil {
		return fmt.Errorf("decXORPass: %w", err)
	}
	stop := len(data) - 8
	ecx := binary.LittleEndian.Uint32(data[stop:])
	for pos := stop - 4; pos >= 4; pos -= 4 {
		edx := binary.LittleEndian.Uint32(data[pos:])
		edx ^= ecx
		binary.LittleEndian.PutUint32(data[pos:], edx)
		ecx -= edx
	}
	return nil
}
