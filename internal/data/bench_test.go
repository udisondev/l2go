package data

import (
	"os"
	"runtime"
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

// TestRealResidentHeap — резидентная память статики после Load на реальном
// дистрибутиве (L2GO_REAL_DATA): GC, затем HeapAlloc; выжимка — в журнал
// (baseline для P2.7). Замер строго до построения дампа: дамп — отдельная
// строка, в резидентность статики не входит. В CI не выполняется.
func TestRealResidentHeap(t *testing.T) {
	root := os.Getenv("L2GO_REAL_DATA")
	if root == "" {
		t.Skip("L2GO_REAL_DATA не задан")
	}
	st, rep, err := Load(os.DirFS(root))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep.HasErrors() {
		t.Fatalf("ошибки целостности: %d", len(rep.Errors))
	}
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	// Обращение к st после замера удерживает статику от сбора GC.
	t.Logf("резидентно после GC: HeapAlloc=%d МБ (Npcs=%d, Spawns=%d, Territories=%d, DropItems=%d, спавнов в статике=%d)",
		ms.HeapAlloc>>20, rep.Npcs, rep.Spawns, rep.Territories, rep.DropItems, len(st.Spawns()))
	dump := st.Dump()
	t.Logf("канонический дамп: %d МБ (сверка артефакта P2.7, вне резидентности)", len(dump)>>20)
}
