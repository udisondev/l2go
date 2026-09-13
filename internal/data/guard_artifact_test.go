package data

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Артефакт статики — производная игровых данных (датапак + геодата): в репо
// не коммитится. Проверка идёт по git-трекнутым файлам (git ls-files), а не
// по прогулке рабочего дерева: gitignored-артефакт на диске (рабочий цикл
// сборки) ворота не красит, а git add -f ловится по индексу.

// trackedArtifactFiles возвращает git-трекнутые .l2a-файлы репозитория root.
func trackedArtifactFiles(t *testing.T, root string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("git ls-files в %s: %v", root, err)
	}
	var found []string
	for _, f := range bytes.Split(out, []byte{0}) {
		if len(f) > 0 && strings.EqualFold(filepath.Ext(string(f)), ".l2a") {
			found = append(found, string(f))
		}
	}
	return found
}

func TestNoTrackedArtifact(t *testing.T) {
	if got := trackedArtifactFiles(t, repoRoot(t)); len(got) > 0 {
		t.Errorf("артефакты статики в git: %v (производная игровых данных — не коммитится)", got)
	}
}

// TestTrackedArtifactDetectsCommitted — фальсифицируемость: во временном
// git-репозитории закоммиченный .l2a ловится проверкой.
func TestTrackedArtifactDetectsCommitted(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@test")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "a.l2a"), []byte("x"), 0o644); err != nil {
		t.Fatalf("запись: %v", err)
	}
	run("add", "a.l2a")
	run("commit", "-qm", "x")
	got := trackedArtifactFiles(t, dir)
	if len(got) != 1 || got[0] != "a.l2a" {
		t.Fatalf("закоммиченный .l2a не найден: %v", got)
	}
}

func TestTrackedFilesSizeCap(t *testing.T) {
	const cap = 16 << 20
	for _, f := range trackedFiles(t) {
		info, err := os.Stat(filepath.Join("..", "..", f))
		if err != nil {
			if os.IsNotExist(err) {
				// удалённый из рабочего дерева, но ещё трекнутый файл
				// (rebase/bisect) не может быть закоммиченным бинарарем
				continue
			}
			t.Fatalf("stat %s: %v", f, err)
		}
		if info.Size() > cap {
			t.Errorf("трекнутый файл %s = %d байт > %d (бинарарь со встроенной статикой?)", f, info.Size(), cap)
		}
	}
}

func trackedFiles(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", repoRoot(t), "ls-files", "-z").Output()
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
