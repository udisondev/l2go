package artifact_test

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/udisondev/l2go/internal/artifact"
	"github.com/udisondev/l2go/internal/data"
	"github.com/udisondev/l2go/internal/geo"
)

func loadSynthStatic(t testing.TB) (*data.Static, *data.Report) {
	t.Helper()
	st, rep, err := data.Load(os.DirFS(filepath.Join("..", "data", "testdata", "synth")))
	if err != nil {
		t.Fatalf("data.Load synth: %v", err)
	}
	if rep.HasErrors() {
		t.Fatalf("synth красная: %+v", rep.Errors)
	}
	return st, rep
}

func synthGeoDir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	for name, canon := range map[string]string{
		"golden_flat.l2j": "16_10.l2j",
		"golden_ml.l2j":   "17_10.l2j",
	} {
		raw, err := os.ReadFile(filepath.Join("..", "geo", "testdata", name))
		if err != nil {
			t.Fatalf("чтение %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, canon), raw, 0o644); err != nil {
			t.Fatalf("запись %s: %v", canon, err)
		}
	}
	return dir
}

func synthGeoMap(t testing.TB) (*geo.Map, *geo.Report) {
	t.Helper()
	m, rep, err := geo.LoadDir(synthGeoDir(t))
	if err != nil {
		t.Fatalf("geo.LoadDir synth: %v", err)
	}
	if rep.HasErrors() || rep.Regions == 0 {
		t.Fatalf("синтетическая гео красная: errors=%v regions=%d", rep.Errors, rep.Regions)
	}
	return m, rep
}

func buildSynth(t testing.TB, out string) *artifact.BuildResult {
	t.Helper()
	st, drep := loadSynthStatic(t)
	m, grep := synthGeoMap(t)
	res, err := artifact.Build(out, st, drep, m, grep)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return res
}

func clone(b []byte) []byte { return append([]byte(nil), b...) }

// rehash пересчитывает контрольную сумму после мутации — как сделал бы
// корректный производитель артефакта; упражняет гварды за чексаммой.
func rehash(b []byte) {
	h := sha256.Sum256(append(append([]byte(nil), b[:88]...), b[120:]...))
	copy(b[88:120], h[:])
}

func decodeErrCode(t *testing.T, b []byte) (string, error) {
	t.Helper()
	_, _, _, err := artifact.Decode(b)
	var de *artifact.DecodeError
	if err != nil && !errors.As(err, &de) {
		t.Fatalf("ошибка не DecodeError: %v", err)
	}
	if err == nil {
		return "", nil
	}
	return de.Code, err
}

func TestBuildDecodeRoundtrip(t *testing.T) {
	st, _ := loadSynthStatic(t)
	m, _ := synthGeoMap(t)
	out := filepath.Join(t.TempDir(), "a.l2a")
	if _, err := artifact.Build(out, st, nil, m, nil); err == nil {
		t.Fatalf("Build без отчёта датапака должен требовать отчёты (meta)")
	}
	res := buildSynth(t, out)
	if !res.Fresh {
		t.Errorf("первая сборка не записала файл")
	}

	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("чтение артефакта: %v", err)
	}
	st2, m2, meta, err := artifact.Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if st.Dump() != st2.Dump() {
		t.Fatalf("статика из артефакта разошлась с парсингом (Dump)")
	}
	if meta.Items != 5 || meta.Regions != 2 {
		t.Errorf("meta: items=%d regions=%d, хочу 5/2", meta.Items, meta.Regions)
	}
	type reg struct{ rx, ry, n int }
	collect := func(mm *geo.Map) []reg {
		var out []reg
		mm.EachRegion(func(rx, ry int, raw []byte) bool {
			out = append(out, reg{rx, ry, len(raw)})
			return true
		})
		return out
	}
	a, c := collect(m), collect(m2)
	if len(a) != len(c) {
		t.Fatalf("регионы: %d против %d", len(a), len(c))
	}
	for i := range a {
		if a[i] != c[i] {
			t.Errorf("регион[%d]: %+v против %+v", i, a[i], c[i])
		}
	}
	if m2.RegionAt(16*2048, 10*2048) == nil || m2.RegionAt(17*2048, 10*2048) == nil {
		t.Errorf("лукап регионов артефактной карты пуст")
	}
}

func TestBuildIdempotent(t *testing.T) {
	out := filepath.Join(t.TempDir(), "a.l2a")
	buildSynth(t, out)
	first, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	res := buildSynth(t, out)
	if res.Fresh {
		t.Errorf("повторная сборка переписала байт-идентичный артефакт")
	}
	second, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("артефакт недетерминирован между сборками")
	}
}

func TestDecodeGuards(t *testing.T) {
	out := filepath.Join(t.TempDir(), "a.l2a")
	buildSynth(t, out)
	base, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	_, _, meta, err := artifact.Decode(base)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	geoOff := 120 + 40 + meta.DataLen

	mut := func(f func(b []byte) []byte) []byte {
		return f(clone(base))
	}
	rehashed := func(f func(b []byte) []byte) []byte {
		b := mut(f)
		rehash(b)
		return b
	}

	cases := []struct {
		name string
		b    []byte
		code string
	}{
		{"магия", mut(func(b []byte) []byte { b[0] = 'X'; return b }), artifact.CodeMagic},
		{"версия", mut(func(b []byte) []byte { binary.LittleEndian.PutUint32(b[4:8], 999); return b }), artifact.CodeVersion},
		{"порча манифеста", mut(func(b []byte) []byte { b[8] ^= 1; return b }), artifact.CodeChecksum},
		{"порча payload", mut(func(b []byte) []byte { b[len(b)-1] ^= 0xFF; return b }), artifact.CodeChecksum},
		{"обрыв", mut(func(b []byte) []byte { return b[:len(b)-1] }), artifact.CodeTrunc},
		{"обрыв заголовка", mut(func(b []byte) []byte { return b[:100] }), artifact.CodeTrunc},
		{"хвост", mut(func(b []byte) []byte { return append(b, 0) }), artifact.CodeTail},
		{"wrap dataLen", rehashed(func(b []byte) []byte {
			binary.LittleEndian.PutUint64(b[72:80], 0xFFFFFFFFFFFFFFFF)
			return b
		}), ""},
		{"wrap офсета региона", rehashed(func(b []byte) []byte {
			binary.LittleEndian.PutUint64(b[geoOff+4+8:geoOff+4+16], 0xFFFFFFFFFFFFFFFF)
			return b
		}), artifact.CodeRange},
		{"дубль слота региона", rehashed(func(b []byte) []byte {
			copy(b[geoOff+4+24:geoOff+4+32], b[geoOff+4:geoOff+4+8]) // rx,ry второй записи = первой
			return b
		}), artifact.CodeDup},
		{"счётчик регионов u32max", rehashed(func(b []byte) []byte {
			binary.LittleEndian.PutUint32(b[geoOff:geoOff+4], 0xFFFFFFFF)
			return b
		}), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, err := decodeErrCode(t, tc.b)
			if err == nil {
				t.Fatalf("малформленный артефакт декодирован без ошибки")
			}
			if tc.code != "" && code != tc.code {
				t.Fatalf("код = %s, хочу %s (err: %v)", code, tc.code, err)
			}
		})
	}
}

func TestLoadFileAndRenameOverMapping(t *testing.T) {
	out := filepath.Join(t.TempDir(), "a.l2a")
	buildSynth(t, out)
	st, m, _, _, err := artifact.LoadFile(out)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if st == nil || m == nil {
		t.Fatalf("LoadFile вернул nil")
	}

	// Пересборка в тот же путь (CreateTemp + rename поверх живого отображения):
	// байты под процессом не меняются, старое отображение остаётся консистентным.
	other := filepath.Join(t.TempDir(), "b.l2a")
	st2, drep := loadSynthStatic(t)
	m2, grep := synthGeoMap(t)
	if _, err := artifact.Build(other, st2, drep, m2, grep); err != nil {
		t.Fatalf("Build other: %v", err)
	}
	tmp := out + ".swap"
	data, err := os.ReadFile(other)
	if err != nil {
		t.Fatalf("чтение other: %v", err)
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		t.Fatalf("запись tmp: %v", err)
	}
	if err := os.Rename(tmp, out); err != nil {
		t.Fatalf("rename поверх живого отображения: %v", err)
	}
	if m.RegionAt(16*2048, 10*2048) == nil {
		t.Errorf("живое отображение региона потерялось после rename")
	}
}

// TestGeoLookupsZeroAllocOverArtifact — перенос критерия P2.3: гео-лукапы
// поверх байтов артефакта не аллоцируют (путь P2.4 без изменений).
func TestGeoLookupsZeroAllocOverArtifact(t *testing.T) {
	out := filepath.Join(t.TempDir(), "a.l2a")
	buildSynth(t, out)
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	_, m, _, err := artifact.Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	gx, gy := 16*2048+8, 10*2048+8
	allocs := testing.AllocsPerRun(200, func() {
		cell := m.RegionAt(gx, gy).CellAt(gx, gy)
		cell.Nearest(0)
		cell.LowerZ(-100)
		cell.HigherZ(100)
	})
	if allocs != 0 {
		t.Errorf("гео-лукапы поверх артефакта аллоцируют: %g оп/вызов", allocs)
	}
}

func TestBuildWithoutGeo(t *testing.T) {
	st, drep := loadSynthStatic(t)
	out := filepath.Join(t.TempDir(), "a.l2a")
	res, err := artifact.Build(out, st, drep, nil, nil)
	if err != nil {
		t.Fatalf("Build без гео: %v", err)
	}
	if res.Meta.Regions != 0 {
		t.Errorf("regions = %d, хочу 0", res.Meta.Regions)
	}
	b, _ := os.ReadFile(out)
	_, m2, meta, err := artifact.Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if meta.Regions != 0 || m2 == nil {
		t.Errorf("карта без регионов должна декодироваться пустой")
	}
}
