package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func synthRoot() string {
	return filepath.Join("..", "..", "internal", "data", "testdata", "synth")
}

func synthGeoDirCmd(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, canon := range map[string]string{
		"golden_flat.l2j": "16_10.l2j",
		"golden_ml.l2j":   "17_10.l2j",
	} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "geo", "testdata", name))
		if err != nil {
			t.Fatalf("чтение %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, canon), raw, 0o644); err != nil {
			t.Fatalf("запись %s: %v", canon, err)
		}
	}
	return dir
}

func runBin(t *testing.T, bin string, args ...string) (string, error) {
	t.Helper()
	out, err := exec.Command(bin, args...).CombinedOutput()
	return string(out), err
}

func TestBuildCmd(t *testing.T) {
	bin := buildBinary(t)
	out := filepath.Join(t.TempDir(), "a.l2a")
	got, err := runBin(t, bin, "build", synthRoot(), synthGeoDirCmd(t), "-o", out)
	if err != nil {
		t.Fatalf("build на синтетике: %v\n%s", err, got)
	}
	if !strings.Contains(got, "манифест") {
		t.Errorf("вывод без манифеста:\n%s", got)
	}
	info, err := os.Stat(out)
	if err != nil || info.Size() == 0 {
		t.Fatalf("артефакт не записан: %v", err)
	}
}

func TestBuildCmdWithoutGeo(t *testing.T) {
	bin := buildBinary(t)
	out := filepath.Join(t.TempDir(), "a.l2a")
	got, err := runBin(t, bin, "build", synthRoot(), "-o", out)
	if err != nil {
		t.Fatalf("build без гео: %v\n%s", err, got)
	}
	if !strings.Contains(got, "регионов: 0") {
		t.Errorf("вывод без счётчика регионов:\n%s", got)
	}
}

func TestBuildCmdRedDoesNotTouchArtifact(t *testing.T) {
	bin := buildBinary(t)
	out := filepath.Join(t.TempDir(), "a.l2a")
	if got, err := runBin(t, bin, "build", synthRoot(), synthGeoDirCmd(t), "-o", out); err != nil {
		t.Fatalf("исходная сборка: %v\n%s", err, got)
	}
	before, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("чтение артефакта: %v", err)
	}

	// Ломаем копию синтетики: битая ссылка спавн→несуществующий NPC
	// (вне диапазона fake players — ошибка, не счётчик).
	broken := t.TempDir()
	copyTreeCmd(t, synthRoot(), broken)
	spawnsPath := filepath.Join(broken, "spawns", "Synth", "spawns.xml")
	raw, err := os.ReadFile(spawnsPath)
	if err != nil {
		t.Fatalf("чтение spawns.xml: %v", err)
	}
	bad := strings.Replace(string(raw), `<npc id="20551"`, `<npc id="77777"`, 1)
	if bad == string(raw) {
		t.Fatalf("в синтетике не найден спавн 20551 для поломки")
	}
	if err := os.WriteFile(spawnsPath, []byte(bad), 0o644); err != nil {
		t.Fatalf("запись spawns.xml: %v", err)
	}

	got, err := runBin(t, bin, "build", broken, synthGeoDirCmd(t), "-o", out)
	if err == nil {
		t.Fatalf("красная сборка прошла:\n%s", got)
	}
	if !strings.Contains(got, "link") {
		t.Errorf("вывод без ошибки ссылки:\n%s", got)
	}
	after, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("чтение артефакта после красной сборки: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("красная сборка изменила артефакт")
	}
}

func TestBuildCmdIdempotent(t *testing.T) {
	bin := buildBinary(t)
	out := filepath.Join(t.TempDir(), "a.l2a")
	if got, err := runBin(t, bin, "build", synthRoot(), synthGeoDirCmd(t), "-o", out); err != nil {
		t.Fatalf("первая сборка: %v\n%s", err, got)
	}
	first, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	got, err := runBin(t, bin, "build", synthRoot(), synthGeoDirCmd(t), "-o", out)
	if err != nil {
		t.Fatalf("вторая сборка: %v\n%s", err, got)
	}
	if !strings.Contains(got, "актуален") {
		t.Errorf("вторая сборка не признала артефакт актуальным:\n%s", got)
	}
	second, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("две сборки дали разные байты (недетерминизм между процессами)")
	}
}

func TestLoadCmd(t *testing.T) {
	bin := buildBinary(t)
	out := filepath.Join(t.TempDir(), "a.l2a")
	if got, err := runBin(t, bin, "build", synthRoot(), synthGeoDirCmd(t), "-o", out); err != nil {
		t.Fatalf("сборка: %v\n%s", err, got)
	}
	got, err := runBin(t, bin, "load", out)
	if err != nil {
		t.Fatalf("load: %v\n%s", err, got)
	}
	for _, want := range []string{"манифест", "предметов", "verify"} {
		if !strings.Contains(got, want) {
			t.Errorf("вывод load без %q:\n%s", want, got)
		}
	}

	bad := filepath.Join(t.TempDir(), "bad.l2a")
	os.WriteFile(bad, []byte("мусор"), 0o644)
	if _, err := runBin(t, bin, "load", bad); err == nil {
		t.Fatalf("load мусора прошёл без ошибки")
	}
}

func copyTreeCmd(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("копия дерева: %v", err)
	}
}

// TestBuildCmdManifestCycle — цикл «изменил один XML → артефакт пересобрался;
// откат → снова идентичен»: манифест входов виден в байтах артефакта.
func TestBuildCmdManifestCycle(t *testing.T) {
	bin := buildBinary(t)
	geoDir := synthGeoDirCmd(t)
	root := t.TempDir()
	copyTreeCmd(t, synthRoot(), root)
	out := filepath.Join(t.TempDir(), "a.l2a")

	if got, err := runBin(t, bin, "build", root, geoDir, "-o", out); err != nil {
		t.Fatalf("исходная сборка: %v\n%s", err, got)
	}
	before, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}

	// Правка одного XML: новый валидный предмет.
	itemsPath := filepath.Join(root, "stats", "items", "items.xml")
	raw, err := os.ReadFile(itemsPath)
	if err != nil {
		t.Fatalf("чтение items.xml: %v", err)
	}
	idx := strings.LastIndex(string(raw), "</list>")
	modified := string(raw[:idx]) + `<item id="9999" type="EtcItem" name="новый"/>` + string(raw[idx:])
	if err := os.WriteFile(itemsPath, []byte(modified), 0o644); err != nil {
		t.Fatalf("запись items.xml: %v", err)
	}
	got, err := runBin(t, bin, "build", root, geoDir, "-o", out)
	if err != nil {
		t.Fatalf("сборка после правки: %v\n%s", err, got)
	}
	if !strings.Contains(got, "записан") {
		t.Fatalf("изменённый состав не перезаписал артефакт:\n%s", got)
	}
	after, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if string(before) == string(after) {
		t.Fatalf("правка XML не изменила артефакт (манифест не участвует)")
	}

	// Откат: артефакт снова байт-идентичен исходному (перезапись назад).
	if err := os.WriteFile(itemsPath, raw, 0o644); err != nil {
		t.Fatalf("откат items.xml: %v", err)
	}
	got, err = runBin(t, bin, "build", root, geoDir, "-o", out)
	if err != nil {
		t.Fatalf("сборка после отката: %v\n%s", err, got)
	}
	reverted, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if string(before) != string(reverted) {
		t.Fatalf("откат не восстановил исходные байты артефакта")
	}
}
