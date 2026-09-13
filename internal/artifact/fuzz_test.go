package artifact_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/udisondev/l2go/internal/artifact"
	"github.com/udisondev/l2go/internal/data"
)

// FuzzArtifactDecode — малформленные байты не паникуют и не роняют процесс
// ни на уровне секции статики, ни на уровне контейнера (контейнер — с
// перевычисленной контрольной суммой, чтобы гварды за чексаммой получали
// покрытие).
func FuzzArtifactDecode(f *testing.F) {
	out := filepath.Join(f.TempDir(), "seed.l2a")
	st, rep := loadSynthStatic(f)
	m, grep := synthGeoMap(f)
	if _, err := artifact.Build(out, st, rep, m, grep); err != nil {
		f.Fatalf("Build сида: %v", err)
	}
	seed, err := os.ReadFile(out)
	if err != nil {
		f.Fatalf("чтение сида: %v", err)
	}
	f.Add(seed)
	f.Add(seed[:len(seed)/2])
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = data.DecodeStatic(b)
		if len(b) >= 120 {
			c := clone(b)
			rehash(c)
			_, _, _, _ = artifact.Decode(c)
		}
	})
}
