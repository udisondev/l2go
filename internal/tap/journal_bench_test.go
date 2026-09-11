package tap

import "testing"

// Пропускная способность записи журнала — цена синхронного писателя
// (компромисс F15: медленный Log блокирует все соединения).
func BenchmarkJournalWrite256(b *testing.B) {
	payload := make([]byte, 256)
	jw := newJournalWriter(discardWriter{})
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for b.Loop() {
		if err := jw.data(recData, 1, DirCtoS, 0, payload); err != nil {
			b.Fatalf("data: %v", err)
		}
	}
}

func BenchmarkJournalWrite1456(b *testing.B) {
	payload := make([]byte, 1456)
	jw := newJournalWriter(discardWriter{})
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for b.Loop() {
		if err := jw.data(recData, 1, DirCtoS, 0, payload); err != nil {
			b.Fatalf("data: %v", err)
		}
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
