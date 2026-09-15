package persist

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAccountsVerifyVerdicts(t *testing.T) {
	dir := t.TempDir()
	acc, err := OpenAccounts(dir, false)
	if err != nil {
		t.Fatalf("OpenAccounts() error = %v", err)
	}
	if err := acc.Create("Player1", "password1"); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if got := acc.Verify("player1", "password1"); got != VerdictOK {
		t.Errorf("Verify(верный, нормализация регистра) = %v; want %v", got, VerdictOK)
	}
	if got := acc.Verify("player1", "wrong"); got != VerdictBadPassword {
		t.Errorf("Verify(неверный) = %v; want %v", got, VerdictBadPassword)
	}
	if got := acc.Verify("ghost", "whatever"); got != VerdictNoAccount {
		t.Errorf("Verify(закрытый режим, miss) = %v; want %v", got, VerdictNoAccount)
	}
	if err := acc.Create("player1", "x"); err == nil {
		t.Error("Create(существует) = nil; want ошибка")
	}
	// бан — правкой файла (подкоманда/оператор): кэш живого API не
	// перечитывает (F20), переоткрытие видит
	var rec AccountRecord
	if err := acc.store.read("player1.json", &rec); err != nil {
		t.Fatal(err)
	}
	rec.Banned = true
	if err := acc.store.write("player1.json", rec); err != nil {
		t.Fatal(err)
	}
	acc2, err := OpenAccounts(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := acc2.Verify("player1", "password1"); got != VerdictBanned {
		t.Errorf("Verify(banned) = %v; want %v", got, VerdictBanned)
	}
}

func TestAccountsAutoCreate(t *testing.T) {
	dir := t.TempDir()
	acc, err := OpenAccounts(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := acc.Verify("Newbie", "pass123"); got != VerdictOK {
		t.Errorf("Verify(авто-создание) = %v; want %v", got, VerdictOK)
	}
	if _, err := os.Stat(filepath.Join(dir, "accounts", "newbie.json")); err != nil {
		t.Errorf("файл аккаунта не создан: %v", err)
	}
	// повторный вход — тем же паролем, без второго файла
	if got := acc.Verify("newbie", "pass123"); got != VerdictOK {
		t.Errorf("Verify(повторный) = %v; want %v", got, VerdictOK)
	}
	if got := acc.Verify("newbie", "other"); got != VerdictBadPassword {
		t.Errorf("Verify(повторный, неверный) = %v; want %v", got, VerdictBadPassword)
	}
}

func TestAccountsEvilLogins(t *testing.T) {
	dir := t.TempDir()
	acc, _ := OpenAccounts(dir, true)
	for _, login := range []string{"", "a", "../x", "ab/cd", "привет", "fifteenchars155"} {
		if got := acc.Verify(login, "pass"); got != VerdictNoAccount {
			t.Errorf("Verify(злой логин %q) = %v; want %v", login, got, VerdictNoAccount)
		}
		if err := acc.Create(login, "pass"); err == nil {
			t.Errorf("Create(злой логин %q) = nil; want ошибка", login)
		}
	}
	entries := readDirT(t, filepath.Join(dir, "accounts"))
	if len(entries) != 0 {
		t.Errorf("злые логины создали файлы: %d", len(entries))
	}
}

func TestAccountsBadFileSurvives(t *testing.T) {
	dir := t.TempDir()
	acc, _ := OpenAccounts(dir, false)
	_ = acc.Create("good", "pass")
	root := filepath.Join(dir, "accounts")
	if err := os.WriteFile(filepath.Join(root, "broken.json"), []byte("мусор"), 0o600); err != nil {
		t.Fatal(err)
	}
	acc2, err := OpenAccounts(dir, false)
	if err != nil {
		t.Fatalf("OpenAccounts(с битым файлом) error = %v", err)
	}
	if got := acc2.Verify("good", "pass"); got != VerdictOK {
		t.Errorf("битый файл соседа завалил good: %v", got)
	}
	if got := acc2.Verify("broken", "x"); got != VerdictNoAccount {
		t.Errorf("Verify(битый) = %v; want %v", got, VerdictNoAccount)
	}
}

func TestAccountsConcurrent(t *testing.T) {
	dir := t.TempDir()
	acc, _ := OpenAccounts(dir, true)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			login := "user" + string(rune('a'+i))
			for j := 0; j < 10; j++ {
				if got := acc.Verify(login, "pass"+string(rune('a'+i))); got != VerdictOK {
					t.Errorf("Verify(%s) = %v; want %v", login, got, VerdictOK)
					return
				}
			}
		})
	}
	wg.Wait()
	entries := readDirT(t, filepath.Join(dir, "accounts"))
	if len(entries) != 8 {
		t.Errorf("создано аккаунтов %d; want 8", len(entries))
	}
}

// readDirT — чтение каталога с проверкой ошибки (игнор через _ запрещён).
func readDirT(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	e, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("os.ReadDir(%s): %v", dir, err)
	}
	return e
}
