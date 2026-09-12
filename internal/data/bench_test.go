package data

import (
	"os"
	"testing"
)

// BenchmarkLoadSynth — синтетический набор категорий в репо; исполняется CI
// с фазы 4 вместе с остальными бенчмарками.
func BenchmarkLoadSynth(b *testing.B) {
	fsys := os.DirFS("testdata/synth")
	b.ReportAllocs()
	for b.Loop() {
		_, rep, err := Load(fsys)
		if err != nil {
			b.Fatalf("Load: %v", err)
		}
		if rep.HasErrors() {
			b.Fatalf("ошибки в синтетике: %+v", rep.Errors)
		}
	}
}

// BenchmarkLoadReal — локальный baseline-замер на реальном дистрибутиве
// (корень данных в L2GO_REAL_DATA); в CI не выполняется. Числа — вход для
// сравнения с загрузкой компилированного артефакта статики: полный состав
// Load (предметы+NPC+спавны), ns/op, B/op, allocs/op.
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
