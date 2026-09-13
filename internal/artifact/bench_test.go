package artifact

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/udisondev/l2go/internal/data"
	"github.com/udisondev/l2go/internal/geo"
)

// Реальные данные: L2GO_REAL_DATA (корень датапака) + L2GO_REAL_GEO (каталог
// .l2j-регионов); без них бенчи скипаются. Синтетика всегда под руками.

func BenchmarkArtifactDecodeSynth(b *testing.B) {
	st, rep, err := data.Load(os.DirFS(filepath.Join("..", "data", "testdata", "synth")))
	if err != nil || rep.HasErrors() {
		b.Fatalf("synth: %v %+v", err, rep.Errors)
	}
	section := data.EncodeStatic(st)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := data.DecodeStatic(section); err != nil {
			b.Fatalf("DecodeStatic: %v", err)
		}
	}
}

// buildReal единожды собирает артефакт реального состава (вне таймеров).
func buildReal(b *testing.B) []byte {
	b.Helper()
	root := os.Getenv("L2GO_REAL_DATA")
	geoDir := os.Getenv("L2GO_REAL_GEO")
	if root == "" {
		b.Skip("L2GO_REAL_DATA не задан")
	}
	st, rep, err := data.Load(os.DirFS(root))
	if err != nil {
		b.Fatalf("data.Load: %v", err)
	}
	if rep.HasErrors() {
		b.Fatalf("реальный датапак красный: %d ошибок", len(rep.Errors))
	}
	var m *geo.Map
	var grep *geo.Report
	if geoDir != "" {
		m, grep, err = geo.LoadDir(geoDir)
		if err != nil {
			b.Fatalf("geo.LoadDir: %v", err)
		}
		if grep.HasErrors() || grep.Regions == 0 {
			b.Fatalf("реальная гео красная/пустая")
		}
	}
	out := filepath.Join(b.TempDir(), "real.l2a")
	if _, err := Build(out, st, rep, m, grep); err != nil {
		b.Fatalf("Build: %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		b.Fatalf("чтение артефакта: %v", err)
	}
	return raw
}

// BenchmarkArtifactDecodeReal — сравнение данные-к-данным с путём парсинга
// (BenchmarkLoadReal пакета data: 641 мс / 437 МБ / 7,04 млн аллок, P2.6).
func BenchmarkArtifactDecodeReal(b *testing.B) {
	raw := buildReal(b)
	meta, err := parseHeader(raw)
	if err != nil {
		b.Fatalf("parseHeader: %v", err)
	}
	section := raw[120+40 : 120+40+meta.DataLen]
	b.ReportAllocs()
	for b.Loop() {
		if _, err := data.DecodeStatic(section); err != nil {
			b.Fatalf("DecodeStatic: %v", err)
		}
	}
}

// BenchmarkLoadFileRealPhases — whole-path фазовым разрезом (сравнение с
// парсингом — только по фазе data; verify и geo — цена транспорта/целостности).
func BenchmarkLoadFileRealPhases(b *testing.B) {
	raw := buildReal(b)
	meta, err := parseHeader(raw)
	if err != nil {
		b.Fatalf("parseHeader: %v", err)
	}
	payload := raw[120:]
	dataSec := payload[40 : 40+meta.DataLen]
	geoSec := payload[40+meta.DataLen:]
	if _, err := decodeGeoRegions(geoSec); err != nil {
		b.Fatalf("регионы для прогона: %v", err)
	}

	b.Run("verify", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(raw)))
		for b.Loop() {
			if !verifyChecksum(raw) {
				b.Fatalf("checksum")
			}
		}
	})
	b.Run("decode-data", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := data.DecodeStatic(dataSec); err != nil {
				b.Fatalf("DecodeStatic: %v", err)
			}
		}
	})
	b.Run("decode-geo", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := decodeGeoRegions(geoSec); err != nil {
				b.Fatalf("decodeGeoRegions: %v", err)
			}
		}
	})
}

// BenchmarkBuildReal — полный цикл сборки с записью (фазы: parse вне цикла —
// его меряет BenchmarkLoadReal; здесь encode+write).
func BenchmarkBuildReal(b *testing.B) {
	root := os.Getenv("L2GO_REAL_DATA")
	if root == "" {
		b.Skip("L2GO_REAL_DATA не задан")
	}
	st, rep, err := data.Load(os.DirFS(root))
	if err != nil || rep.HasErrors() {
		b.Fatalf("data.Load: %v", err)
	}
	b.ReportAllocs()
	dir := b.TempDir()
	b.ResetTimer()
	for b.Loop() {
		out := filepath.Join(dir, "b.l2a")
		os.Remove(out)
		if _, err := Build(out, st, rep, nil, nil); err != nil {
			b.Fatalf("Build: %v", err)
		}
	}
}

// TestArtifactResidentHeap — резидентная статика из артефакта не хуже парсинга
// (48 МБ после GC, якорь P2.6); методика TestRealResidentHeap.
func TestArtifactResidentHeap(t *testing.T) {
	raw := realArtifactOrSkip(t)
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	base := ms.HeapAlloc
	st, _, _, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	runtime.GC()
	runtime.ReadMemStats(&ms)
	resident := ms.HeapAlloc - base
	t.Logf("резидентная статика из артефакта: %d байт (дамп %d байт)", resident, len(st.Dump()))
	if resident > 48<<20 {
		t.Errorf("резидентная статика %d байт > 48 МиБ (якорь парсинга P2.6)", resident)
	}
}

func realArtifactOrSkip(t *testing.T) []byte {
	t.Helper()
	root := os.Getenv("L2GO_REAL_DATA")
	if root == "" {
		t.Skip("L2GO_REAL_DATA не задан")
	}
	st, rep, err := data.Load(os.DirFS(root))
	if err != nil || rep.HasErrors() {
		t.Fatalf("data.Load: %v", err)
	}
	out := filepath.Join(t.TempDir(), "real.l2a")
	if _, err := Build(out, st, rep, nil, nil); err != nil {
		t.Fatalf("Build: %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	return raw
}
