package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// E1: golden-сэмпл через CLI — журнал байт-в-байт.
func TestCmdPcapConvertGolden(t *testing.T) {
	dir := t.TempDir()
	journalPath := filepath.Join(dir, "session.tap")
	if err := run([]string{"-in", filepath.Join("..", "..", "internal", "pcap", "testdata", "sample-classic.pcap"), "-out", journalPath}); err != nil {
		t.Fatalf("run: %v", err)
	}
	got, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("журнал: %v", err)
	}
	want, err := os.ReadFile(filepath.Join("..", "..", "internal", "pcap", "testdata", "sample-classic.tap"))
	if err != nil {
		t.Fatalf("golden: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("CLI-журнал дрейфовал от golden")
	}
}

// E2: -h самодостаточен — флаги с описаниями, оба контейнера упомянуты.
func TestCmdPcapUsage(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "l2pcap")
	out, err := exec.Command("go", "build", "-o", bin, "github.com/udisondev/l2go/cmd/l2pcap").CombinedOutput()
	if err != nil {
		t.Fatalf("сборка l2pcap: %v\n%s", err, out)
	}
	usage, err := exec.Command(bin, "-h").CombinedOutput()
	if err != nil {
		t.Fatalf("-h не должен падать: %v\n%s", err, usage)
	}
	text := string(usage)
	for _, want := range []string{"-in", "-out", "pcapng", "pcap", "SYN"} {
		if !strings.Contains(text, want) {
			t.Errorf("usage без %q:\n%s", want, text)
		}
	}
}

// E3: злые аргументы — именованные ошибки с путём.
func TestCmdPcapEvilArgsTable(t *testing.T) {
	dir := t.TempDir()
	garbage := filepath.Join(dir, "garbage.pcap")
	if err := os.WriteFile(garbage, bytes.Repeat([]byte{0x55}, 40), 0o644); err != nil {
		t.Fatalf("запись: %v", err)
	}
	outPath := filepath.Join(dir, "out.tap")
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"нет флагов", nil, "-in"},
		{"нет -out", []string{"-in", garbage}, "-out"},
		{"несуществующий", []string{"-in", filepath.Join(dir, "нет.pcap"), "-out", outPath}, "нет.pcap"},
		{"мусорный файл", []string{"-in", garbage, "-out", outPath}, "garbage.pcap"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := run(tc.args)
			if err == nil {
				t.Fatalf("run(%v) = nil; want ошибка", tc.args)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("run(%v) = %q; want содержит %q", tc.args, err.Error(), tc.want)
			}
		})
	}
}
