package persist

import (
	"fmt"
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
	acc := mustOpenAccounts(t, dir, true)
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
	acc := mustOpenAccounts(t, dir, false)
	if err := acc.Create("good", "pass"); err != nil {
		t.Fatalf("Create(good) error = %v", err)
	}
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
	acc := mustOpenAccounts(t, dir, true)
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

// mustOpenAccounts — открытие аккаунтов с проверкой ошибки (игнор через _ запрещён).
func mustOpenAccounts(t *testing.T, dir string, autoCreate bool) *Accounts {
	t.Helper()
	acc, err := OpenAccounts(dir, autoCreate)
	if err != nil {
		t.Fatalf("OpenAccounts(%s): %v", dir, err)
	}
	return acc
}

// Двойной первый вход одного аккаунта (KDF вне лока, запись с двойной
// проверкой): ровно одна запись; победитель — OK; проигравший — по своей
// паре (OK при совпадении, BadPassword при иной); файлов ровно один.
func TestAccountsParallelAutoCreateRace(t *testing.T) {
	dir := t.TempDir()
	acc, err := OpenAccounts(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	const racers = 4
	verdicts := make([]Verdict, racers)
	var wg sync.WaitGroup
	for i := range racers {
		i := i
		wg.Go(func() {
			pass := "same-pass"
			if i == racers-1 {
				pass = "other-pass"
			}
			verdicts[i] = acc.Verify("racer", pass)
		})
	}
	wg.Wait()
	okN, badN := 0, 0
	for _, v := range verdicts {
		switch v {
		case VerdictOK:
			okN++
		case VerdictBadPassword:
			badN++
		default:
			t.Fatalf("вердикт гонщика: %v", v)
		}
	}
	// Ровно один создатель; развязка зависит от того, чья пара победила:
	// победила same-pass → ok=racers-1/bad=1; победила other-pass → 1/racers-1.
	var winnerPass string
	switch okN {
	case racers - 1:
		winnerPass = "same-pass"
	case 1:
		winnerPass = "other-pass"
	default:
		t.Fatalf("вердикты: ok=%d bad=%d; want 1 или %d создателей", okN, badN, racers-1)
	}
	// Аккаунт один; вердикты повторных входов соответствуют победившей паре.
	loserPass := "other-pass"
	if winnerPass == "other-pass" {
		loserPass = "same-pass"
	}
	if v := acc.Verify("racer", winnerPass); v != VerdictOK {
		t.Fatalf("повторный вход победившей парой: %v", v)
	}
	if v := acc.Verify("racer", loserPass); v != VerdictBadPassword {
		t.Fatalf("проигравшая пара после гонки: %v", v)
	}
}

// Параллельная проверка существующих аккаунтов не сериализуется локом
// (KDF вне критсекции): смок под race-детектором; поведение — все OK.
func TestAccountsParallelVerifySmoke(t *testing.T) {
	acc, err := OpenAccounts(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		if v := acc.Verify(fmt.Sprintf("user%02d", i), "pass"); v != VerdictOK {
			t.Fatalf("создание user%02d: %v", i, v)
		}
	}
	var wg sync.WaitGroup
	for i := range 8 {
		i := i
		wg.Go(func() {
			if v := acc.Verify(fmt.Sprintf("user%02d", i), "pass"); v != VerdictOK {
				t.Errorf("user%02d: %v", i, v)
			}
		})
	}
	wg.Wait()
}
