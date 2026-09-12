package crypto

import (
	"testing"
)

// FuzzLoginDecrypt — малформенные login-кадры (кратно 8 Б) сквозь обе фазы
// движка: без паник, ошибки всегда содержательные (не пустые). Ключ —
// фиксированный валидный, чтобы фаззер дошёл до чексуммы, а не до ErrKeyNotSet.
func FuzzLoginDecrypt(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8})
	f.Add(make([]byte, 64))
	f.Add([]byte{0xde, 0xad, 0xbe, 0xef, 0xde, 0xad, 0xbe, 0xef})
	f.Fuzz(func(t *testing.T, frame []byte) {
		if len(frame) == 0 || len(frame)%8 != 0 {
			t.Skip()
		}
		lc1 := NewLoginCrypt()
		if err := lc1.DecryptInit(frame); err != nil && err.Error() == "" {
			t.Fatalf("DecryptInit: пустая ошибка")
		}
		lc2 := NewLoginCrypt()
		if err := lc2.SetKey([]byte("0123456789abcdef")); err != nil {
			t.Fatalf("SetKey: %v", err)
		}
		if err := lc2.Decrypt(frame); err != nil && err.Error() == "" {
			t.Fatalf("Decrypt: пустая ошибка")
		}
	})
}

// FuzzGameDecrypt — малформенные game-кадры через GameCrypt: без паник,
// ошибки содержательные.
func FuzzGameDecrypt(f *testing.F) {
	f.Add([]byte{1, 2, 3})
	f.Add(make([]byte, 256))
	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) == 0 {
			t.Skip()
		}
		var wire [8]byte
		copy(wire[:], "abcdefgh")
		gc := NewGameCrypt(wire)
		gc.Enable()
		if err := gc.Decrypt(payload); err != nil && err.Error() == "" {
			t.Fatalf("Decrypt: пустая ошибка")
		}
	})
}
