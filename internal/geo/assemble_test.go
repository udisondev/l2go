package geo

import (
	"os"
	"path/filepath"
	"testing"
)

func goldenRegion(t *testing.T, name string, rx, ry int) *Region {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("чтение %s: %v", name, err)
	}
	reg, err := DecodeRegion(rx, ry, raw)
	if err != nil {
		t.Fatalf("DecodeRegion(%s): %v", name, err)
	}
	return reg
}

func TestNewMapFromRegions(t *testing.T) {
	reg := goldenRegion(t, "golden_flat.l2j", 16, 10)
	m, err := NewMapFromRegions([]*Region{reg})
	if err != nil {
		t.Fatalf("NewMapFromRegions: %v", err)
	}
	if got := m.RegionAt(16*256, 10*256); got != reg {
		t.Errorf("RegionAt региона 16_10 не вернул установленный регион")
	}
	if got := m.RegionAt(17*256, 10*256); got != nil {
		t.Errorf("RegionAt пустого слота вернул %v, хочу nil", got)
	}
}

func TestNewMapFromRegionsDupSlot(t *testing.T) {
	a := goldenRegion(t, "golden_flat.l2j", 16, 10)
	b := goldenRegion(t, "golden_ml.l2j", 16, 10) // тот же слот, другие байты
	if _, err := NewMapFromRegions([]*Region{a, b}); err == nil {
		t.Fatalf("повтор слота региона не дал ошибку")
	}
}

func TestEachRegion(t *testing.T) {
	flat := goldenRegion(t, "golden_flat.l2j", 16, 10)
	ml := goldenRegion(t, "golden_ml.l2j", 17, 10)
	m, err := NewMapFromRegions([]*Region{ml, flat}) // порядок входа не отсортирован
	if err != nil {
		t.Fatalf("NewMapFromRegions: %v", err)
	}
	type entry struct {
		rx, ry int
		n      int
	}
	var got []entry
	m.EachRegion(func(rx, ry int, raw []byte) bool {
		got = append(got, entry{rx, ry, len(raw)})
		return true
	})
	want := []entry{{16, 10, len(flat.data)}, {17, 10, len(ml.data)}}
	if len(got) != len(want) {
		t.Fatalf("EachRegion обошёл %d регионов, хочу %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("регион[%d] = %+v, хочу %+v (порядок — возрастание координат)", i, got[i], want[i])
		}
	}
}

func TestEachRegionStop(t *testing.T) {
	flat := goldenRegion(t, "golden_flat.l2j", 16, 10)
	ml := goldenRegion(t, "golden_ml.l2j", 17, 10)
	m, err := NewMapFromRegions([]*Region{flat, ml})
	if err != nil {
		t.Fatalf("NewMapFromRegions: %v", err)
	}
	n := 0
	m.EachRegion(func(rx, ry int, raw []byte) bool {
		n++
		return false
	})
	if n != 1 {
		t.Errorf("после возврата false обошито %d регионов, хочу 1", n)
	}
}

func TestMapFileNoCeiling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.bin")
	// Размер заведомо выше потолка файла региона: усечение расширяет файл
	// нулями, не записывая их (разрежённый хвост).
	const size = int64(maxRegionBytes) + 8
	if err := os.Truncate(path, size); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	b, unmap, err := MapFile(path)
	if err != nil {
		t.Fatalf("MapFile надпотолкового размера: %v (потолок региона не должен резать артефакты)", err)
	}
	defer unmap()
	if int64(len(b)) != size {
		t.Fatalf("len(b) = %d, хочу %d", len(b), size)
	}
}
