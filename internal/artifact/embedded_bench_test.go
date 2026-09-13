//go:build embedded

package artifact

import "testing"

// BenchmarkLoadEmbeddedReal — время загрузки вшитого артефакта на полных
// данных владельца (полный артефакт кладётся в embedded/ перед прогоном).
func BenchmarkLoadEmbeddedReal(b *testing.B) {
	for b.Loop() {
		if _, _, _, err := LoadEmbedded(); err != nil {
			b.Fatalf("LoadEmbedded: %v", err)
		}
	}
}
