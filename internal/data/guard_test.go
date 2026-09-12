package data

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot — корень репозитория относительно каталога пакета.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// xmlFiles — все .xml рабочего дерева (кроме .git).
func xmlFiles(t *testing.T) []string {
	t.Helper()
	return filesByExt(t, ".xml")
}

// l2jFiles — все .l2j рабочего дерева.
func l2jFiles(t *testing.T) []string {
	t.Helper()
	return filesByExt(t, ".l2j")
}

// filesByExt — файлы рабочего дерева по расширению (без учёта регистра).
func filesByExt(t *testing.T, ext string) []string {
	t.Helper()
	var out []string
	root := repoRoot(t)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			if d.Type()&fs.ModeSymlink != 0 {
				return fs.SkipDir // ссылки-каталоги в дерево не входят
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ext) {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("обход дерева репо: %v", err)
	}
	return out
}

// TestXMLWhitelist: каждый XML в репозитории перечислен явно — новые xml-файлы
// попадают сюда сознательной правкой. Дистрибутив датапака (NCsoft-derived) в
// репо не коммитится; белый список — машинный барьер этому.
func TestXMLWhitelist(t *testing.T) {
	allow := map[string]struct{}{
		"internal/data/testdata/synth/stats/items/items.xml":   {},
		"internal/data/testdata/synth/stats/npcs/npcs.xml":     {},
		"internal/data/testdata/synth/spawns/Synth/spawns.xml": {},
		"internal/data/testdata/synth/spawns/Synth/off.xml":    {},
	}
	for _, f := range xmlFiles(t) {
		if _, ok := allow[f]; !ok {
			t.Errorf("XML вне белого списка: %s", f)
		}
	}
}

// distroRangePattern — имена диапазонных файлов дистрибутива, сверяются по
// базовому имени (каталог значения не имеет) и без учёта регистра расширения.
var distroRangePattern = regexp.MustCompile(`(?i)^\d{5}-\d{5}\.xml$`)

// TestNoDistroRangeNames: имена диапазонных файлов дистрибутива запрещены
// везде, включая белый список.
func TestNoDistroRangeNames(t *testing.T) {
	for _, f := range xmlFiles(t) {
		if distroRangePattern.MatchString(filepath.Base(f)) {
			t.Errorf("имя диапазонного файла дистрибутива в репо: %s", f)
		}
	}
}

// TestL2JWhitelist: golden-фикстуры геодаты перечислены явно — новые .l2j
// попадают сюда сознательной правкой (дистрибутив геодаты в репо не
// коммитится).
func TestL2JWhitelist(t *testing.T) {
	allow := map[string]struct{}{
		"internal/geo/testdata/golden_flat.l2j": {},
		"internal/geo/testdata/golden_ml.l2j":   {},
	}
	for _, f := range l2jFiles(t) {
		if _, ok := allow[f]; !ok {
			t.Errorf(".l2j вне белого списка: %s", f)
		}
	}
}

// distroGeoPattern — имена файлов геодаты дистрибутива (NN_NN.l2j), включая
// golden-каталог: фикстуры генерируются билдером и дистрибутивных имён не
// носят.
var distroGeoPattern = regexp.MustCompile(`(?i)^\d+_\d+\.l2j$`)

// TestNoDistroGeoNames: имена файлов реальной геодаты запрещены везде.
func TestNoDistroGeoNames(t *testing.T) {
	for _, f := range l2jFiles(t) {
		if distroGeoPattern.MatchString(filepath.Base(f)) {
			t.Errorf("имя файла геодаты дистрибутива в репо: %s", f)
		}
	}
}
