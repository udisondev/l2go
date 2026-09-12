package geo

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

// flatZeroRegion — минимальный валидный регион: 65536 flat-блоков нулевой
// высоты (196 608 нулевых байт).
func flatZeroRegion() []byte {
	return make([]byte, regionBlocks*3)
}

func writeRegion(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatalf("запись %s: %v", name, err)
	}
}

func TestLoadDirClean(t *testing.T) {
	dir := t.TempDir()
	writeRegion(t, dir, "16_10.l2j", flatZeroRegion())
	m, rep, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if rep.HasErrors() {
		t.Fatalf("ошибки не ждали: %+v", rep.Errors)
	}
	if rep.Files != 1 || rep.Regions != 1 {
		t.Errorf("Files=%d Regions=%d; want 1 и 1", rep.Files, rep.Regions)
	}
	if rep.BlocksFlat != regionBlocks || rep.BlocksComplex != 0 || rep.BlocksMultilayer != 0 {
		t.Errorf("blocks: flat=%d complex=%d ml=%d; want 65536/0/0", rep.BlocksFlat, rep.BlocksComplex, rep.BlocksMultilayer)
	}
	if rep.ExtraTiles != 0 {
		t.Errorf("ExtraTiles = %d; want 0", rep.ExtraTiles)
	}
	if m.RegionAt(16*regionCells, 10*regionCells) == nil {
		t.Error("регион не установлен")
	}
	if m.RegionAt(0, 0) != nil {
		t.Error("пустой слот ≠ nil")
	}
}

func TestLoadDirDuplicates(t *testing.T) {
	dir := t.TempDir()
	writeRegion(t, dir, "16_10.l2j", flatZeroRegion())
	writeRegion(t, dir, "016_010.l2j", flatZeroRegion()) // ведущие нули — тот же регион
	writeRegion(t, dir, "17_11.L2J", flatZeroRegion())   // регистр расширения допустим
	_, rep, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(rep.Errors) != 1 || rep.Errors[0].Code != CodeDup {
		t.Fatalf("ожидали одну dup-ошибку; got %+v", rep.Errors)
	}
	// Сортировка имён: «016_010.l2j» < «16_10.l2j» — дубликатом считается второй.
	if rep.Errors[0].File != "16_10.l2j" {
		t.Errorf("dup обвиняет %q; want 16_10.l2j (второй по сортировке)", rep.Errors[0].File)
	}
	if rep.Regions != 2 {
		t.Errorf("Regions = %d; want 2 (16_10 и 17_11)", rep.Regions)
	}
}

func TestLoadDirCaseExtensionAlone(t *testing.T) {
	// Отдельно от dup: одиночный файл с верхним регистром расширения грузится.
	dir := t.TempDir()
	writeRegion(t, dir, "18_12.L2J", flatZeroRegion())
	m, rep, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if rep.HasErrors() || rep.Regions != 1 {
		t.Fatalf("Regions=%d errors=%+v; want 1 и пусто", rep.Regions, rep.Errors)
	}
	if m.RegionAt(18*regionCells, 12*regionCells) == nil {
		t.Error("регион 18_12 не установлен")
	}
}

func TestLoadDirRangeAndExtraTiles(t *testing.T) {
	dir := t.TempDir()
	writeRegion(t, dir, "99_5.l2j", flatZeroRegion()) // вне сетки 32×32
	writeRegion(t, dir, "5_5.l2j", flatZeroRegion())  // вне тайлов канона, но в сетке
	_, rep, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(rep.Errors) != 1 || rep.Errors[0].Code != CodeRange {
		t.Fatalf("ожидали одну range-ошибку; got %+v", rep.Errors)
	}
	if rep.ExtraTiles != 1 || rep.Regions != 1 {
		t.Errorf("ExtraTiles=%d Regions=%d; want 1 и 1", rep.ExtraTiles, rep.Regions)
	}
}

func TestLoadDirDirComposition(t *testing.T) {
	dir := t.TempDir()
	writeRegion(t, dir, "16_10.l2j", flatZeroRegion())
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, rep, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if rep.HasErrors() {
		t.Fatalf("ошибок не ждали: %+v", rep.Errors)
	}
	if rep.SkippedDirs != 1 {
		t.Errorf("SkippedDirs = %d; want 1", rep.SkippedDirs)
	}
	if rep.IgnoredFiles != 1 {
		t.Errorf("IgnoredFiles = %d; want 1", rep.IgnoredFiles)
	}
}

func TestLoadDirTruncatedKeepsOtherFiles(t *testing.T) {
	dir := t.TempDir()
	writeRegion(t, dir, "16_10.l2j", flatZeroRegion()[:1000]) // обрезан
	writeRegion(t, dir, "17_10.l2j", flatZeroRegion())
	m, rep, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(rep.Errors) != 1 || rep.Errors[0].Code != CodeTrunc || rep.Errors[0].File != "16_10.l2j" {
		t.Fatalf("ожидали trunc на 16_10; got %+v", rep.Errors)
	}
	if rep.Regions != 1 || m.RegionAt(17*regionCells, 10*regionCells) == nil {
		t.Error("валидный регион не установлен после ошибки соседа")
	}
	if m.RegionAt(16*regionCells, 10*regionCells) != nil {
		t.Error("битый регион установлен")
	}
}

func TestLoadDirMissingDir(t *testing.T) {
	_, _, err := LoadDir(filepath.Join(t.TempDir(), "нет-такого"))
	if err == nil {
		t.Fatal("несуществующий каталог: ошибки нет")
	}
}

func TestLoadDirEmptySet(t *testing.T) {
	_, rep, err := LoadDir(t.TempDir())
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if rep.HasErrors() {
		t.Fatalf("пустой каталог не ошибка: %+v", rep.Errors)
	}
	if rep.Regions != 0 {
		t.Errorf("Regions = %d; want 0", rep.Regions)
	}
}

func TestManifestDeterminism(t *testing.T) {
	// Файлы под живым отображением не перезаписываются (предусловие
	// контракта LoadDir) — варианты состава раскладываются по свежим
	// каталогам.
	dir1, dir1again, dir2 := t.TempDir(), t.TempDir(), t.TempDir()
	mutated := flatZeroRegion()
	mutated[len(mutated)-1] = 7 // высота последнего flat-блока
	for _, d := range []string{dir1, dir1again} {
		writeRegion(t, d, "16_10.l2j", flatZeroRegion())
		writeRegion(t, d, "17_10.l2j", flatZeroRegion())
	}
	writeRegion(t, dir2, "16_10.l2j", flatZeroRegion())
	writeRegion(t, dir2, "17_10.l2j", mutated)

	_, rep1, err := LoadDir(dir1)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	_, repSame, err := LoadDir(dir1again)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if rep1.Manifest != repSame.Manifest {
		t.Errorf("манифест недетерминирован: %x ≠ %x", rep1.Manifest, repSame.Manifest)
	}
	_, repOther, err := LoadDir(dir2)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if rep1.Manifest == repOther.Manifest {
		t.Error("изменение байта не изменило манифест")
	}

	// Состав манифеста — установленные регионы: битый файл не входит
	// (манифест пустого состава — SHA-256 пустого ввода).
	dirBroken := t.TempDir()
	writeRegion(t, dirBroken, "16_10.l2j", flatZeroRegion()[:100])
	_, repBroken, err := LoadDir(dirBroken)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if want := sha256.Sum256(nil); repBroken.Manifest != want {
		t.Errorf("манифест без установленных регионов = %x; want %x", repBroken.Manifest, want)
	}
}
