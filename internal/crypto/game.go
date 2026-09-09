package crypto

import (
	"encoding/binary"
	"fmt"
)

// GameCrypt — XOR-каскад game-канала (порт udisondev/interlude@34fe4c86,
// pkg/crypto/game_crypt.go; канон L2J GameCrypt.java). Ключ 16 байт: 8 случайных
// из KeyPacket + 8 статических; каскад i&15 с prev = предыдущий шифрбайт; после
// каждого пакета LE-u32 счётчик key[8:12] увеличивается на размер payload.
// До Enable шифрование прозрачно (ProtocolVersion идёт открыто).
type GameCrypt struct {
	inKey   [16]byte
	outKey  [16]byte
	enabled bool
}

// staticGameHalf — вторая половина game-ключа, известна обеим сторонам.
var staticGameHalf = [8]byte{0xc8, 0x27, 0x93, 0x01, 0xa1, 0x6c, 0x31, 0x97}

// NewGameCrypt собирает ключ из 8 проводных байт KeyPacket (без обфускации)
// и статической половины.
func NewGameCrypt(wire [8]byte) *GameCrypt {
	var gc GameCrypt
	for i := range 8 {
		gc.inKey[i] = wire[i]
		gc.outKey[i] = wire[i]
		gc.inKey[i+8] = staticGameHalf[i]
		gc.outKey[i+8] = staticGameHalf[i]
	}
	return &gc
}

// Enable включает шифрование (после открытого ProtocolVersion).
func (gc *GameCrypt) Enable() { gc.enabled = true }

// IsEnabled — состояние шифрования.
func (gc *GameCrypt) IsEnabled() bool { return gc.enabled }

// xorPass — каскад: prev всегда шифрбайт позиции i-1 (в шифровании это записанный
// байт, в расшифровании — исходный).
func xorPass(data []byte, key *[16]byte, encrypt bool) {
	var prev byte
	for i := range data {
		c := data[i]
		data[i] = c ^ key[i&15] ^ prev
		if encrypt {
			prev = data[i]
		} else {
			prev = c
		}
	}
	old := binary.LittleEndian.Uint32(key[8:12])
	binary.LittleEndian.PutUint32(key[8:12], old+uint32(len(data)))
}

// Encrypt шифрует исходящий payload (каскад outKey) инплейс.
func (gc *GameCrypt) Encrypt(payload []byte) error {
	if len(payload) == 0 {
		return fmt.Errorf("GameCrypt.Encrypt: пустой payload")
	}
	if !gc.enabled {
		return nil
	}
	xorPass(payload, &gc.outKey, true)
	return nil
}

// Decrypt расшифровывает входящий payload (каскад inKey) инплейс.
func (gc *GameCrypt) Decrypt(payload []byte) error {
	if len(payload) == 0 {
		return fmt.Errorf("GameCrypt.Decrypt: пустой payload")
	}
	if !gc.enabled {
		return nil
	}
	xorPass(payload, &gc.inKey, false)
	return nil
}
