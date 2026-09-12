package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "l2data.exe")
	out, err := exec.Command("go", "build", "-o", bin, "github.com/udisondev/l2go/cmd/l2data").CombinedOutput()
	if err != nil {
		t.Fatalf("сборка l2data: %v\n%s", err, out)
	}
	return bin
}

func TestCheckClean(t *testing.T) {
	bin := buildBinary(t)
	root := filepath.Join("..", "..", "internal", "data", "testdata", "synth")
	out, err := exec.Command(bin, "check", root).CombinedOutput()
	if err != nil {
		t.Fatalf("check на чистой синтетике: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "предметов: 5") {
		t.Errorf("вывод без сводки; got:\n%s", out)
	}
}

func TestCheckDirty(t *testing.T) {
	bin := buildBinary(t)
	root := t.TempDir()
	dir := filepath.Join(root, "stats", "items")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.xml"), []byte("<list><item id=\"1\" type=\"Weapon\" name=\"a\"/><item id=\"1\" type=\"Weapon\" name=\"b\"/></list>"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Каталоги остальных категорий обязательны (их отсутствие — фатальная
	// FS-ошибка, а не ошибка данных).
	for _, d := range []string{filepath.Join("stats", "npcs"), "spawns"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}
	out, err := exec.Command(bin, "check", root).CombinedOutput()
	if err == nil {
		t.Fatalf("check на грязных данных должен вернуть ненулевой код; вывод:\n%s", out)
	}
	if !strings.Contains(string(out), "dup_id") {
		t.Errorf("вывод без кода dup_id:\n%s", out)
	}
}

func TestCheckUsage(t *testing.T) {
	bin := buildBinary(t)
	if err := exec.Command(bin).Run(); err == nil {
		t.Fatal("без аргументов должен быть ненулевой код")
	}
}

func TestCheckBrokenLink(t *testing.T) {
	bin := buildBinary(t)
	root := t.TempDir()
	for _, d := range []string{filepath.Join("stats", "items"), filepath.Join("stats", "npcs"), "spawns"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}
	spawn := filepath.Join(root, "spawns", "a.xml")
	if err := os.WriteFile(spawn, []byte(`<list enabled="true"><spawn name="x"><npc id="6666" x="1" y="2" z="3"/></spawn></list>`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	out, err := exec.Command(bin, "check", root).CombinedOutput()
	if err == nil {
		t.Fatalf("битая ссылка должна давать ненулевой код; вывод:\n%s", out)
	}
	if !strings.Contains(string(out), "link") {
		t.Errorf("вывод без кода link:\n%s", out)
	}
}
