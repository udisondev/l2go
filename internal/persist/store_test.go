package persist

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStoreRoundtrip(t *testing.T) {
	dir := t.TempDir()
	s, err := openStore(dir)
	if err != nil {
		t.Fatalf("openStore() error = %v", err)
	}
	recs := []CharRecord{{
		Account: "acc", Slot: 1, Name: "Vasya", ClassID: 0, Race: 0,
		Sex: 1, HairStyle: 2, HairColor: 1, Face: 1,
		X: -71338, Y: 258271, Z: -3104, Heading: 1234,
		Level: 1, Exp: 0, HP: 80, MP: 30,
		CreatedUnix: 100, LastSeenUnix: 200,
	}}
	if err := s.write("acc.json", recs); err != nil {
		t.Fatalf("write() error = %v", err)
	}
	var got []CharRecord
	if err := s.read("acc.json", &got); err != nil {
		t.Fatalf("read() error = %v", err)
	}
	if len(got) != 1 || got[0] != recs[0] {
		t.Errorf("read() = %+v; want %+v", got, recs)
	}
}

func TestStoreDeterministicBytes(t *testing.T) {
	dir := t.TempDir()
	s := mustOpenStore(t, dir)
	recs := []CharRecord{{Account: "acc", Slot: 0, Name: "A", Level: 1}}
	if err := s.write("acc.json", recs); err != nil {
		t.Fatalf("write() #1 error = %v", err)
	}
	first, err := os.ReadFile(filepath.Join(dir, "acc.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.write("acc.json", recs); err != nil {
		t.Fatalf("write() #2 error = %v", err)
	}
	second, err := os.ReadFile(filepath.Join(dir, "acc.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("двойная запись одного record: файлы различаются; want бит-в-бит равные")
	}
	if !strings.Contains(string(first), "\"schema\"") {
		t.Error("файл не человекочитаем: нет перевода строк/отступов")
	}
}

func TestStoreBadFiles(t *testing.T) {
	dir := t.TempDir()
	s := mustOpenStore(t, dir)
	recs := []CharRecord{{Account: "acc", Name: "A"}}
	mustWrite(t, s, "acc.json", recs)
	good := readFileT(t, filepath.Join(dir, "acc.json"))

	write := func(content string) {
		if err := os.WriteFile(filepath.Join(dir, "acc.json"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(strings.Replace(string(good), "\"schema\": 1", "\"schema\": 2", 1))
	var out []CharRecord
	err := s.read("acc.json", &out)
	if !errors.Is(err, ErrBadSchema) {
		t.Errorf("чужая схема: read() error = %v; want ErrBadSchema", err)
	}
	if err == nil || !strings.Contains(err.Error(), "acc.json") {
		t.Errorf("ошибка без имени файла: %v", err)
	}

	write(strings.Replace(string(good), `"sha256": "`, `"sha256": "0`, 1))
	err = s.read("acc.json", &out)
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("битая чексумма: read() error = %v; want ErrCorrupt", err)
	}

	write("{не json")
	err = s.read("acc.json", &out)
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("битый JSON: read() error = %v; want ErrCorrupt", err)
	}

	var missing []CharRecord
	err = s.read("nobody.json", &missing)
	if err == nil {
		t.Error("чтение отсутствующего файла = nil; want ошибка")
	}
}

func TestStoreAtomicFail(t *testing.T) {
	dir := t.TempDir()
	s := mustOpenStore(t, dir)
	recs := []CharRecord{{Account: "acc", Name: "Old"}}
	if err := s.write("acc.json", recs); err != nil {
		t.Fatal(err)
	}
	before := readFileT(t, filepath.Join(dir, "acc.json"))

	s.testFailRename = func() error { return errors.New("инъекция: обрыв между temp и rename") }
	if err := s.write("acc.json", []CharRecord{{Account: "acc", Name: "New"}}); err == nil {
		t.Fatal("write() при инъекции сбоя = nil; want ошибка")
	}
	after := readFileT(t, filepath.Join(dir, "acc.json"))
	if string(before) != string(after) {
		t.Error("прежний файл повреждён сбоем записи")
	}
	entries := readDirT(t, dir)
	if len(entries) != 1 {
		t.Errorf("после сбоя в каталоге %d файлов; want 1 (осиротевший temp убран)", len(entries))
	}
}

func TestStorePerms(t *testing.T) {
	// Биты прав на Windows ненаблюдаемы (Stat всегда 0777/0666, chmod —
	// только read-only атрибут); проверка — Linux/CI.
	if runtime.GOOS == "windows" {
		t.Skip("права 0700/0600 ненаблюдаемы на windows")
	}
	dir := t.TempDir()
	s, err := openStore(filepath.Join(dir, "root", "chars"))
	if err != nil {
		t.Fatalf("openStore(новый путь) error = %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "root", "chars"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("созданный каталог = %v; want 0700", info.Mode().Perm())
	}

	existing := filepath.Join(dir, "pre")
	if err := os.Mkdir(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(existing); err != nil {
		t.Fatalf("openStore(существующий) error = %v", err)
	}
	info = statT(t, existing)
	if info.Mode().Perm() != 0o700 {
		t.Errorf("предсозданный каталог 0755 остался %v; want chmod 0700", info.Mode().Perm())
	}

	if err := s.write("acc.json", []CharRecord{{Account: "acc"}}); err != nil {
		t.Fatal(err)
	}
	info = statT(t, filepath.Join(dir, "root", "chars", "acc.json"))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("файл = %v; want 0600", info.Mode().Perm())
	}
}

func mkChar(account, name string, slot int) CharRecord {
	return CharRecord{Account: account, Name: name, Slot: slot, ClassID: 0, Race: 0,
		Level: 1, HP: 80, MP: 30}
}

func TestCharStoreScan(t *testing.T) {
	dir := t.TempDir()
	s := mustOpenStore(t, dir)
	mustWrite(t, s, "alpha.json", []CharRecord{mkChar("alpha", "Vasya", 0)})
	mustWrite(t, s, "beta.json", []CharRecord{mkChar("beta", "Petya", 0)})
	// битый файл третьего аккаунта — не валит остальные
	if err := os.WriteFile(filepath.Join(dir, "gamma.json"), []byte("мусор"), 0o600); err != nil {
		t.Fatal(err)
	}
	// осиротевший temp (kill -9 в окне записи)
	if err := os.WriteFile(filepath.Join(dir, ".tmp-1234"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	cs, err := openCharStore(dir)
	if err != nil {
		t.Fatalf("openCharStore() error = %v", err)
	}
	if _, ok := cs.names[strings.ToLower("Vasya")]; !ok {
		t.Error("имя Vasya не в индексе после скана")
	}
	if _, ok := cs.names["petya"]; !ok {
		t.Error("имя Petya не в индексе после скана")
	}
	if _, ok := cs.names["мус"]; ok {
		t.Error("имена битого файла попали в индекс")
	}
	if _, err := os.Stat(filepath.Join(dir, ".tmp-1234")); !errors.Is(err, os.ErrNotExist) {
		t.Error("осиротевший temp не удалён стартом")
	}
	// gamma.json пережил скан (файл не удаляется), но записи не загружены
	if _, err := os.Stat(filepath.Join(dir, "gamma.json")); err != nil {
		t.Error("битый файл удалён сканом; want остаётся для разбора оператором")
	}
}

func TestCharStoreDuplicateName(t *testing.T) {
	dir := t.TempDir()
	s := mustOpenStore(t, dir)
	mustWrite(t, s, "alpha.json", []CharRecord{mkChar("alpha", "Vasya", 0)})
	mustWrite(t, s, "beta.json", []CharRecord{mkChar("beta", "VASYA", 0)})
	_, err := openCharStore(dir)
	if err == nil {
		t.Fatal("openCharStore() с дубликатом имени = nil; want ошибка")
	}
	msg := err.Error()
	if !strings.Contains(msg, "alpha.json") || !strings.Contains(msg, "beta.json") {
		t.Errorf("ошибка дубликата без имён обоих файлов: %v", err)
	}
}

func TestCharStoreListSave(t *testing.T) {
	dir := t.TempDir()
	cs, err := openCharStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := cs.list("nobody")
	if err != nil || len(recs) != 0 {
		t.Errorf("list(без файла) = (%d, %v); want (0, nil)", len(recs), err)
	}
	if err := cs.saveFile("acc", []CharRecord{mkChar("acc", "Vasya", 0)}); err != nil {
		t.Fatal(err)
	}
	recs, err = cs.list("acc")
	if err != nil || len(recs) != 1 || recs[0].Name != "Vasya" {
		t.Errorf("list() = (%+v, %v); want Vasya", recs, err)
	}
}

func TestGitignoreGuard(t *testing.T) {
	buf, err := os.ReadFile("../../.gitignore")
	if err != nil {
		t.Fatalf("не читается .gitignore: %v", err)
	}
	for _, line := range strings.Split(string(buf), "\n") {
		if strings.TrimSpace(line) == "/var/" {
			return
		}
	}
	t.Error(".gitignore не содержит /var/ — дефолтный каталог персиста коммитится")
}

// mustOpenStore — открытие с проверкой ошибки (игнор через _ запрещён).
func mustOpenStore(t *testing.T, dir string) *store {
	t.Helper()
	st, err := openStore(dir)
	if err != nil {
		t.Fatalf("openStore(%s): %v", dir, err)
	}
	return st
}

// readFileT — os.ReadFile с проверкой ошибки.
func readFileT(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%s): %v", path, err)
	}
	return b
}

// mustWrite — запись с проверкой ошибки (игнор через _ запрещён).
func mustWrite(t *testing.T, s *store, name string, value any) {
	t.Helper()
	if err := s.write(name, value); err != nil {
		t.Fatalf("write(%s): %v", name, err)
	}
}

// statT — os.Stat с проверкой ошибки.
func statT(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("os.Stat(%s): %v", path, err)
	}
	return info
}
