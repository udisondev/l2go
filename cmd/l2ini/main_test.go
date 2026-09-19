package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/udisondev/l2go/internal/l2ini"
)

// plainForCLI — детерминированный открытый текст (тот же канон, что internal).
func plainForCLI() []byte {
	var b bytes.Buffer
	b.WriteString("[URL]\r\nServerPort=7777\r\n")
	for i := 0; i < 30; i++ {
		b.WriteString("Opt=значение; ") // не-ASCII: кодек байтовый, не текстовый
	}
	return b.Bytes()
}

// C1: roundtrip через CLI-шов — encode файлом, decode файлом.
func TestCmdIniDecodeEncodeRoundtrip(t *testing.T) {
	dir := t.TempDir()
	plainPath := filepath.Join(dir, "plain.txt")
	encPath := filepath.Join(dir, "l2.ini")
	decPath := filepath.Join(dir, "dec.txt")
	if err := os.WriteFile(plainPath, plainForCLI(), 0o644); err != nil {
		t.Fatalf("plaintext: %v", err)
	}
	if err := run([]string{"-encode", plainPath, "-out", encPath}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := run([]string{"-decode", encPath, "-out", decPath}); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, err := os.ReadFile(decPath)
	if err != nil {
		t.Fatalf("чтение результата: %v", err)
	}
	if !bytes.Equal(got, plainForCLI()) {
		t.Errorf("CLI-roundtrip: got %d байт, want %d", len(got), len(plainForCLI()))
	}
}

// C2: verify — целого файла nil, порченного ошибка CRC.
func TestCmdIniVerify(t *testing.T) {
	dir := t.TempDir()
	encPath := filepath.Join(dir, "l2.ini")
	file, err := l2ini.Encode(plainForCLI(), l2ini.Modern)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if err := os.WriteFile(encPath, file, 0o644); err != nil {
		t.Fatalf("запись: %v", err)
	}
	if err := run([]string{"-verify", encPath}); err != nil {
		t.Fatalf("verify(целый) = %v; want nil", err)
	}
	file[40] ^= 0xFF
	if err := os.WriteFile(encPath, file, 0o644); err != nil {
		t.Fatalf("запись: %v", err)
	}
	err = run([]string{"-verify", encPath})
	if err == nil || !strings.Contains(err.Error(), "CRC") {
		t.Fatalf("verify(порченный) = %v; want ошибка CRC", err)
	}
}

// C3: legacy-флаг на modern-файле — громкая ошибка, не паника.
func TestCmdIniLegacyFlagOnModernFile(t *testing.T) {
	dir := t.TempDir()
	encPath := filepath.Join(dir, "l2.ini")
	file, err := l2ini.Encode(plainForCLI(), l2ini.Modern)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if err := os.WriteFile(encPath, file, 0o644); err != nil {
		t.Fatalf("запись: %v", err)
	}
	if err := run([]string{"-decode", encPath, "-legacy", "-out", filepath.Join(dir, "out.txt")}); err == nil {
		t.Fatal("decode(-legacy) modern-файла прошёл молча; want ошибка")
	}
}

// C4: злые аргументы — именованные ошибки с путём.
func TestCmdIniEvilArgsTable(t *testing.T) {
	dir := t.TempDir()
	encPath := filepath.Join(dir, "l2.ini")
	file, err := l2ini.Encode(plainForCLI(), l2ini.Modern)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if err := os.WriteFile(encPath, file, 0o644); err != nil {
		t.Fatalf("запись: %v", err)
	}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"нет режима", nil, "ровно один режим"},
		{"два режима", []string{"-decode", encPath, "-verify", encPath}, "ровно один режим"},
		{"несуществующий", []string{"-decode", filepath.Join(dir, "нет.ini")}, "нет.ini"},
		{"каталог", []string{"-decode", dir}, dir},
		{"мусорный файл", []string{"-decode", writeGarbage(t, dir)}, "заголовок"},
		{"пустой файл", []string{"-decode", writeEmpty(t, dir)}, "короче"},
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

func writeGarbage(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "garbage.ini")
	if err := os.WriteFile(p, bytes.Repeat([]byte{0xAB}, 100), 0o644); err != nil {
		t.Fatalf("запись: %v", err)
	}
	return p
}

func writeEmpty(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "empty.ini")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatalf("запись: %v", err)
	}
	return p
}

// C5: -h самодостаточен — все флаги с описаниями, упоминание семейств.
func TestCmdIniUsage(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "l2ini")
	out, err := exec.Command("go", "build", "-o", bin, "github.com/udisondev/l2go/cmd/l2ini").CombinedOutput()
	if err != nil {
		t.Fatalf("сборка l2ini: %v\n%s", err, out)
	}
	usage, err := exec.Command(bin, "-h").CombinedOutput()
	if err != nil {
		t.Fatalf("-h не должен падать: %v\n%s", err, usage)
	}
	text := string(usage)
	for _, want := range []string{"-decode", "-encode", "-verify", "-out", "-legacy", "modern", "legacy 413"} {
		if !strings.Contains(text, want) {
			t.Errorf("usage без %q:\n%s", want, text)
		}
	}
}
