package data

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Артефакт статики — производная NCsoft-derived данных: в репо не коммитится.
// Проверка идёт по git-трекнутым файлам (git ls-files), а не по прогулке
// рабочего дерева: gitignored-артефакт на диске (рабочий цикл сборки) ворота
// не красит, а git add -f ловится по индексу.

func trackedFiles(t *testing.T) []string {
	t.Helper()
	if _, err := os.Stat(filepath.Join("..", "..", ".git")); err != nil {
		t.Fatalf("репозиторий без .git: проверка трекнутых файлов невозможна (должна падать, не скипаться): %v", err)
	}
	out, err := exec.Command("git", "-C", filepath.Join("..", ".."), "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var files []string
	for _, f := range bytes.Split(out, []byte{0}) {
		if len(f) > 0 {
			files = append(files, string(f))
		}
	}
	return files
}

func TestNoTrackedArtifact(t *testing.T) {
	for _, f := range trackedFiles(t) {
		if strings.EqualFold(filepath.Ext(f), ".l2a") {
			t.Errorf("артефакт статики в git: %s (NCsoft-derived, ADR-0001)", f)
		}
	}
}

func TestTrackedFilesSizeCap(t *testing.T) {
	const cap = 16 << 20
	for _, f := range trackedFiles(t) {
		info, err := os.Stat(filepath.Join("..", "..", f))
		if err != nil {
			t.Fatalf("stat %s: %v", f, err)
		}
		if info.Size() > cap {
			t.Errorf("трекнутый файл %s = %d байт > %d (бинарарь со встроенной статикой?)", f, info.Size(), cap)
		}
	}
}
