package l2ini

import (
	"testing"
)

// FuzzIniDecode — произвольные байты сквозь decode/verify обоих семейств:
// без паник, любой исход — именованная ошибка или корректный разбор.
func FuzzIniDecode(f *testing.F) {
	file, err := Encode(samplePlain(), Modern)
	if err != nil {
		f.Fatalf("Encode: %v", err)
	}
	f.Add(file)
	f.Add([]byte(nil))
	f.Add(file[:len(file)-1])
	f.Add(append(file[:headerSize], make([]byte, 300)...))
	f.Fuzz(func(t *testing.T, data []byte) {
		if _, err := Decode(data, Modern); err == nil && len(data) == 0 {
			t.Fatal("пустой вход декодирован")
		}
		_ = Verify(data) // любой исход допустим: фаззинг ловит паники, не семантику
		if _, err := Decode(data, Legacy413); err == nil && len(data) == 0 {
			t.Fatal("пустой вход декодирован (legacy)")
		}
	})
}
