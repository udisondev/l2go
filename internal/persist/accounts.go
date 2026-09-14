package persist

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"
)

// Verdict — различимые вердикты проверки аккаунта (потребитель — причины
// LoginFail задачи логина).
type Verdict int

const (
	VerdictNoAccount Verdict = iota // аккаунта нет (закрытый режим)
	VerdictBadPassword
	VerdictBanned
	VerdictOK
)

// String — имя вердикта для логов и тестов.
func (v Verdict) String() string {
	switch v {
	case VerdictNoAccount:
		return "no_account"
	case VerdictBadPassword:
		return "bad_password"
	case VerdictBanned:
		return "banned"
	case VerdictOK:
		return "ok"
	}
	return "unknown"
}

// Accounts — аккаунтная половина персиста (каталог accounts/, владелец —
// login-процесс). Синхронный API для горутин коннектов: внутренний мьютекс
// (сервис вне региона), файлы — тот же атомарный store-слой. Подкоманда
// создания аккаунта выполняется только при останованном сервере:
// межпроцессная инвалидация кэша отсутствует.
type Accounts struct {
	mu         sync.Mutex
	store      store
	cache      map[string]*AccountRecord
	autoCreate bool
}

// OpenAccounts открывает каталог аккаунтов; autoCreate разрешает создание
// аккаунта при первом входе (КТ-1, дефолт плана).
func OpenAccounts(root string, autoCreate bool) (*Accounts, error) {
	if err := ensureRoot(root); err != nil {
		return nil, err
	}
	s, err := openStore(filepath.Join(root, "accounts"))
	if err != nil {
		return nil, err
	}
	return &Accounts{store: *s, cache: make(map[string]*AccountRecord), autoCreate: autoCreate}, nil
}

// Verify проверяет пароль аккаунта с различимым вердиктом. Несуществующий
// логин в закрытом режиме прогоняет фиктивный вывод (выравнивание времени).
func (a *Accounts) Verify(login, password string) Verdict {
	normalized, err := NormalizeLogin(login)
	if err != nil {
		return VerdictNoAccount
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	rec, err := a.load(normalized)
	if err != nil {
		// Битый файл аккаунта — инцидент в журнале, вход отклонён.
		slog.Error("persist: аккаунт не прочитан", "login", normalized, "err", err)
		return VerdictNoAccount
	}
	if rec == nil {
		if !a.autoCreate {
			burnDummy()
			return VerdictNoAccount
		}
		if _, err := a.createLocked(normalized, password); err != nil {
			slog.Error("persist: авто-создание аккаунта не удалось",
				"login", normalized, "err", err)
			return VerdictNoAccount
		}
		return VerdictOK
	}
	if !verifyPassword(password, rec.Salt, rec.Hash) {
		return VerdictBadPassword
	}
	if rec.Banned {
		return VerdictBanned
	}
	return VerdictOK
}

// Create заводит аккаунт с паролем (подкоманда cmd/l2login при останованном
// сервере); существующий — ошибка.
func (a *Accounts) Create(login, password string) error {
	normalized, err := NormalizeLogin(login)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	rec, err := a.load(normalized)
	if err != nil {
		return err
	}
	if rec != nil {
		return fmt.Errorf("persist: аккаунт %s существует", normalized)
	}
	_, err = a.createLocked(normalized, password)
	return err
}

// load возвращает запись из кэша или файла; nil — аккаунта нет.
func (a *Accounts) load(login string) (*AccountRecord, error) {
	if rec, ok := a.cache[login]; ok {
		return rec, nil
	}
	var rec AccountRecord
	if err := a.store.read(login+".json", &rec); err != nil {
		if isNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	a.cache[login] = &rec
	return &rec, nil
}

// createLocked создаёт и записывает новый аккаунт; вызывающий держит мьютекс.
func (a *Accounts) createLocked(login, password string) (*AccountRecord, error) {
	salt, err := newSalt()
	if err != nil {
		return nil, err
	}
	hash, err := hashPassword(password, salt)
	if err != nil {
		return nil, err
	}
	rec := &AccountRecord{
		Login:       login,
		Salt:        salt,
		Hash:        hash,
		CreatedUnix: time.Now().Unix(),
	}
	if err := a.store.write(login+".json", rec); err != nil {
		return nil, err
	}
	a.cache[login] = rec
	return rec, nil
}
