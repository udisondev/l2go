//go:build embedded

package artifact

import "testing"

// TestLoadEmbedded — бинарарь теста несёт артефакт (синтетический, кладётся
// CI перед прогоном); загрузка идёт без внешних файлов.
func TestLoadEmbedded(t *testing.T) {
	st, m, meta, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	if meta.Items == 0 {
		t.Errorf("артефакт без предметов — вероятно, вшит не тот файл")
	}
	if st == nil || m == nil {
		t.Fatalf("LoadEmbedded вернул nil")
	}
}

// TestEmbeddedGeoLookupsZeroAlloc — перенос критерия P2.3 на embedded-байты:
// лукапы гео над картой из вшитого артефакта не аллоцируют.
func TestEmbeddedGeoLookupsZeroAlloc(t *testing.T) {
	_, m, _, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	if m.RegionAt(16*2048, 10*2048) == nil {
		t.Skip("вшитый артефакт без гео (собран без каталога геодаты)")
	}
	gx, gy := 16*2048+8, 10*2048+8
	allocs := testing.AllocsPerRun(200, func() {
		cell := m.RegionAt(gx, gy).CellAt(gx, gy)
		cell.Nearest(0)
	})
	if allocs != 0 {
		t.Errorf("гео-лукапы поверх embedded-байтов аллоцируют: %g оп/вызов", allocs)
	}
}
