package data

import (
	"os"
	"testing"
)

// BenchmarkLoadReal — локальный baseline-замер на реальном дистрибутиве
// (корень данных в L2GO_REAL_DATA); в CI не выполняется. Числа — вход для
// сравнения с загрузкой компилированного артефакта статики.
func BenchmarkLoadReal(b *testing.B) {
	root := os.Getenv("L2GO_REAL_DATA")
	if root == "" {
		b.Skip("L2GO_REAL_DATA не задан")
	}
	b.ReportAllocs()
	for b.Loop() {
		_, rep, err := Load(os.DirFS(root))
		if err != nil {
			b.Fatalf("Load: %v", err)
		}
		if rep.HasErrors() {
			b.Fatalf("ошибки целостности в реальном наборе: %d", len(rep.Errors))
		}
	}
}
