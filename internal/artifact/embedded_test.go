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
