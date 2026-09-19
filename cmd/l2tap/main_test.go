package main_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// -h самодостаточен: все флаги с описаниями, упоминание обоих семейств
// хендшейка (пин P3.14: l2tap тронут задачей).
func TestCmdTapUsage(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "l2tap")
	out, err := exec.Command("go", "build", "-o", bin, "github.com/udisondev/l2go/cmd/l2tap").CombinedOutput()
	if err != nil {
		t.Fatalf("сборка l2tap: %v\n%s", err, out)
	}
	usage, err := exec.Command(bin, "-h").CombinedOutput()
	if err != nil {
		t.Fatalf("-h не должен падать: %v\n%s", err, usage)
	}
	text := string(usage)
	for _, want := range []string{"-map", "-login-map", "-out", "-decode", "-out-log", "-out-fixtures", "265"} {
		if !strings.Contains(text, want) {
			t.Errorf("usage без %q:\n%s", want, text)
		}
	}
}
