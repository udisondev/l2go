package l2ini

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"
)

// samplePlain — детерминированный открытый текст в духе l2.ini (синтетика).
func samplePlain() []byte {
	var b bytes.Buffer
	b.WriteString("[URL]\r\n")
	b.WriteString("BloodPoint=127.0.0.1\r\n")
	b.WriteString("ServerPort=7777\r\n")
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, "Option%d=%d\r\n", i, i*i)
	}
	return b.Bytes()
}

// legacyTestKey — тестовая ключевая пара формы legacy 413 (decrypt-экспонента
// 0x35): фиксированные простые, e = 0x35⁻¹ mod λ(n). Шов F3: без инъекции
// ключа legacy-ветка нетестируема синтетикой (живая фикстура — NCsoft).
func legacyTestKey(t *testing.T) Key {
	t.Helper()
	nextPrime := func(from *big.Int) *big.Int {
		p := new(big.Int).Set(from)
		if p.Bit(0) == 0 {
			p.Add(p, big.NewInt(1))
		}
		for !p.ProbablyPrime(20) {
			p.Add(p, big.NewInt(2))
		}
		return p
	}
	p := nextPrime(new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 511), big.NewInt(17)))
	q := nextPrime(new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 511), big.NewInt(1234567)))
	n := new(big.Int).Mul(p, q)
	// λ(n) = lcm(p-1, q-1) = (p-1)(q-1)/gcd
	pm1 := new(big.Int).Sub(p, big.NewInt(1))
	qm1 := new(big.Int).Sub(q, big.NewInt(1))
	lambda := new(big.Int).Div(new(big.Int).Mul(pm1, qm1), new(big.Int).GCD(nil, nil, pm1, qm1))
	d := big.NewInt(0x35)
	e := new(big.Int).ModInverse(d, lambda)
	if e == nil {
		t.Fatal("тестовая пара несогласована: 0x35 необратима по λ(n)")
	}
	return Key{N: n, Encrypt: e, Decrypt: d}
}

// B1: roundtrip modern побайтовый на границах чанковки и выравнивания.
func TestIniRoundtripModernByteExact(t *testing.T) {
	cases := []struct {
		name string
		n    int
	}{
		{"1Б", 1},
		{"граница чанка 124", 124},
		{"перелив 125", 125},
		{"хвост %4=1", 125 + 0}, // 125 уже даёт %4=1; отдельные хвосты ниже
		{"хвост %4=2", 126},
		{"хвост %4=3", 127},
		{"сотни блоков", 64 * 1024},
	}
	plain := samplePlain()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var input []byte
			switch {
			case tc.n <= len(plain):
				input = plain[:tc.n]
			default:
				input = make([]byte, tc.n)
				for i := range input {
					input[i] = byte(i * 7)
				}
			}
			file, err := Encode(input, Modern)
			if err != nil {
				t.Fatalf("Encode(%d Б): %v", len(input), err)
			}
			got, err := Decode(file, Modern)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if !bytes.Equal(got, input) {
				t.Errorf("roundtrip %d Б: got %d байт, want %d", len(input), len(got), len(input))
			}
			if err := Verify(file); err != nil {
				t.Errorf("Verify: %v", err)
			}
		})
	}
}

// B2: golden-фикстура — зашифрованный файл с известным ключом в testdata
// (производные байты: Encode детерминирован, дрейф констант/формата красит).
// Генерация: L2_WRITE_GOLDEN=1 go test ./internal/l2ini.
func TestIniDecodeGoldenFixture(t *testing.T) {
	plain := samplePlain()
	if os.Getenv("L2_WRITE_GOLDEN") == "1" {
		file, err := Encode(plain, Modern)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("testdata: %v", err)
		}
		if err := os.WriteFile(filepath.Join("testdata", "sample-modern.l2ini"), file, 0o644); err != nil {
			t.Fatalf("запись фикстуры: %v", err)
		}
		if err := os.WriteFile(filepath.Join("testdata", "sample-plain.txt"), plain, 0o644); err != nil {
			t.Fatalf("запись golden-plaintext: %v", err)
		}
	}
	file, err := os.ReadFile(filepath.Join("testdata", "sample-modern.l2ini"))
	if err != nil {
		t.Fatalf("фикстура: %v (генерация: L2_WRITE_GOLDEN=1 go test ./internal/l2ini)", err)
	}
	got, err := Decode(file, Modern)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "sample-plain.txt"))
	if err != nil {
		t.Fatalf("golden-plaintext: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("golden-дрейф: got %d байт, want %d", len(got), len(want))
	}
	if err := Verify(file); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

// B3: legacy-ветка (decrypt 0x35) на синтетической паре той же формы.
func TestIniLegacyDecodeSyntheticKey(t *testing.T) {
	key := legacyTestKey(t)
	file, err := Encode(samplePlain(), key)
	if err != nil {
		t.Fatalf("Encode тестовым ключом: %v", err)
	}
	got, err := Decode(file, key)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !bytes.Equal(got, samplePlain()) {
		t.Errorf("legacy roundtrip: got %d байт, want %d", len(got), len(samplePlain()))
	}
}

// B4: verify ловит порчу в заголовке, теле и самом CRC.
func TestIniVerifyRejectsCorruptionTable(t *testing.T) {
	file, err := Encode(samplePlain(), Modern)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	for _, tc := range []struct {
		name string
		off  int
	}{
		{"заголовок", 5},
		{"шифроблок", headerSize + 40},
		{"CRC-хвост", len(file) - tailSize + tailCRCOffset + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			corrupt := bytes.Clone(file)
			corrupt[tc.off] ^= 0xFF
			err := Verify(corrupt)
			if !errors.Is(err, ErrCRC) {
				t.Errorf("Verify(порча %s) = %v; want ErrCRC", tc.name, err)
			}
		})
	}
}

// B5: злые входы — именованные ошибки с контекстом, не паники.
func TestIniEvilInputsTable(t *testing.T) {
	valid, err := Encode(samplePlain(), Modern)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// валидная структура блоков + мусорный zlib: пересобираем тело вручную
	garbageBlob := append([]byte{0, 0, 0, 16}, 0xDE, 0xAD, 0xBE, 0xEF)
	garbageBody := pad(garbageBlob)
	garbage := append(header413(), encryptBlocks(garbageBody, Modern)...)
	garbage = append(garbage, make([]byte, tailSize)...)

	decodeCases := []struct {
		name    string
		input   []byte
		wantErr error
	}{
		{"пусто", nil, ErrShortFile},
		{"короче заголовка+хвоста", valid[:headerSize+tailSize-1], ErrShortFile},
		{"чужой заголовок 411", withVersion(valid, "411"), ErrHeader},
		{"чужой заголовок 414", withVersion(valid, "414"), ErrHeader},
		{"хвост отрезан на байт", valid[:len(valid)-1], ErrBlockSize},
		{"нет ни одного блока", append(header413(), make([]byte, tailSize)...), ErrZlib},
		{"мусорный zlib", garbage, ErrZlib},
		{"несходка размера распаковки", sizeMismatch(t, valid), ErrSize},
	}
	for _, tc := range decodeCases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("паника: %v", r)
				}
			}()
			got, err := Decode(tc.input, Modern)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Decode(%s) err = %v; want %v (got %d байт)", tc.name, err, tc.wantErr, len(got))
			}
		})
	}

	// байт длины чанка > 124 (открытие: open-l2encdec молча клампит — порт
	// отвечает громкой ошибкой)
	t.Run("длина чанка >124", func(t *testing.T) {
		key := legacyTestKey(t)
		blob := append([]byte{0, 0, 0, 16}, 1, 2, 3, 4)
		blocks := pad(blob)
		blocks[3] = 200 // невозможно для честного энкодера
		file := append(header413(), encryptBlocks(blocks, key)...)
		file = append(file, make([]byte, tailSize)...)
		if _, err := Decode(file, key); !errors.Is(err, ErrChunkLen) {
			t.Fatalf("Decode(чанк 200) err = %v; want ErrChunkLen", err)
		}
	})

	// verify на мусоре без хвоста — громкая ошибка CRC-домена
	t.Run("verify без хвоста", func(t *testing.T) {
		if err := Verify(valid[:10]); !errors.Is(err, ErrShortFile) {
			t.Fatalf("Verify(10 Б) err = %v; want ErrShortFile", err)
		}
	})
}

// sizeMismatch — валидный файл с неверным u32-префиксом распакованного
// размера: пересборка zlib-блоба с честным потоком, но ложным размером.
func sizeMismatch(t *testing.T, valid []byte) []byte {
	t.Helper()
	got, err := Decode(valid, Modern)
	if err != nil {
		t.Fatalf("Decode базового файла: %v", err)
	}
	var z bytes.Buffer
	binary.Write(&z, binary.LittleEndian, uint32(len(got)+1))
	zw, err := zlib.NewWriterLevel(&z, zlib.BestCompression)
	if err != nil {
		t.Fatalf("zlib-уровень: %v", err)
	}
	zw.Write(got)
	zw.Close()
	blob := z.Bytes()
	file := append(header413(), encryptBlocks(pad(blob), Modern)...)
	return append(file, make([]byte, tailSize)...)
}

// withVersion — подмена версии протокола в заголовке (чужой домен 411/414).
func withVersion(file []byte, version string) []byte {
	out := bytes.Clone(file)
	for i := 0; i < 3; i++ {
		out[headerSize-2*(3-i)] = version[i]
	}
	return out
}

// B6: несовпадение семейства — громкий отказ, не тихий успех.
func TestIniDecodeWrongFamilyNotSilent(t *testing.T) {
	file, err := Encode(samplePlain(), Modern)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("паника: %v", r)
		}
	}()
	got, err := Decode(file, Legacy413)
	if err == nil && bytes.Equal(got, samplePlain()) {
		t.Fatal("decode legacy-ключом тихо вернул modern-plaintext")
	}
}

// B7: чужой (неканоничный) zlib-поток канонизируется по данным.
func TestIniEncodeCanonicalizesForeignZlib(t *testing.T) {
	plain := samplePlain()
	var foreign bytes.Buffer
	binary.Write(&foreign, binary.LittleEndian, uint32(len(plain)))
	zw, err := zlib.NewWriterLevel(&foreign, zlib.HuffmanOnly) // иной канон, чем BestCompression
	if err != nil {
		t.Fatalf("zlib-уровень: %v", err)
	}
	zw.Write(plain)
	zw.Close()
	file := append(header413(), encryptBlocks(pad(foreign.Bytes()), Modern)...)
	file = append(file, make([]byte, tailSize)...)

	first, err := Decode(file, Modern)
	if err != nil {
		t.Fatalf("Decode(чужой zlib): %v", err)
	}
	if !bytes.Equal(first, plain) {
		t.Fatal("чужой zlib не распаковался в исходные данные")
	}
	reenc, err := Encode(first, Modern)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	second, err := Decode(reenc, Modern)
	if err != nil {
		t.Fatalf("Decode(re-encode): %v", err)
	}
	if !bytes.Equal(second, first) {
		t.Fatal("decode(encode(decode(f))) != decode(f)")
	}
	again, err := Encode(first, Modern)
	if err != nil {
		t.Fatalf("Encode повторно: %v", err)
	}
	if !bytes.Equal(again, reenc) {
		t.Fatal("Encode недетерминирован на повторе")
	}
}

// B8: идемпотентность encode (raw-RSA без случайного паддинга).
func TestIniEncodeDeterministicRepeat(t *testing.T) {
	a, err := Encode(samplePlain(), Modern)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	b, err := Encode(samplePlain(), Modern)
	if err != nil {
		t.Fatalf("Encode повторно: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Error("два Encode одного plaintext различаются")
	}
}

// Example — расшифровка golden-фикстуры modern-семейством.
func ExampleDecode() {
	file, err := os.ReadFile(filepath.Join("testdata", "sample-modern.l2ini"))
	if err != nil {
		panic(err)
	}
	plain, err := Decode(file, Modern)
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s", plain[:len("[URL]\r\n")])
	// Output: [URL]
}

// B9: результат decode не алиасит входной буфер.
func TestIniDecodeDoesNotAliasInput(t *testing.T) {
	file, err := Encode(samplePlain(), Modern)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(file, Modern)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	for i := range file {
		file[i] ^= 0xAA
	}
	if bytes.Equal(got, samplePlain()) != true {
		t.Fatal("plaintext изменился после мутации входа — алиасинг")
	}
}
