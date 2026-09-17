package persist

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Ошибки хранилища: различимы, с именем файла.
var (
	// ErrBadSchema — чужая версия схемы; миграций нет, пересоздание допустимо.
	ErrBadSchema = errors.New("persist: чужая версия схемы")
	// ErrCorrupt — битая чексумма или JSON; файл требует разбора оператором.
	ErrCorrupt = errors.New("persist: файл повреждён")
)

// schemaVersion — версия схемы файлов персиста.
const schemaVersion = 1

// fileEnvelope — конверт файла: версия схемы, канонические байты записи,
// чексумма (детектор порчи, не MAC: локальный диск доверен оператору).
type fileEnvelope struct {
	Schema int             `json:"schema"`
	Data   json.RawMessage `json:"data"`
	SHA256 string          `json:"sha256"`
}

// store — файловое хранилище одного каталога. Методы не потокобезопасны:
// ровно один владелец-горутина на каталог (актор chars/, аккаунтный API под
// своим мьютексом).
type store struct {
	dir    string
	rename func(oldpath, newpath string) error

	// testFailRename — шов инъекции сбоя записи между temp и rename;
	// testBlockWrite — шов блокировки фазы записи (тест таймаута дрена).
	testFailRename func() error
	testBlockWrite chan struct{}
}

// ensureRoot подготавливает корень персиста: создаёт (0700) и подтягивает
// права существующего.
func ensureRoot(root string) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("persist: корень %s: %w", root, err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return fmt.Errorf("persist: права корня %s: %w", root, err)
	}
	return nil
}

// openStore подготавливает каталог (0700, chmod существующего) и зачищает
// осиротевшие temp-файлы (kill -9 в окне записи; владелец каталога один —
// гонки нет).
func openStore(dir string) (*store, error) {
	if dir == "" {
		return nil, fmt.Errorf("persist: каталог хранилища пуст")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("persist: каталог %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("persist: права каталога %s: %w", dir, err)
	}
	if err := cleanTemp(dir); err != nil {
		return nil, err
	}
	return &store{dir: dir, rename: os.Rename}, nil
}

// cleanTemp удаляет осиротевшие temp-файлы .tmp-* каталога.
func cleanTemp(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("persist: скан %s: %w", dir, err)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".tmp-") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("persist: зачистка temp %s: %w", e.Name(), err)
		}
	}
	return nil
}

// write атомарно записывает value: конверт → temp (0600, fsync) → rename →
// fsync каталога. torn-rename защита: power-loss оставляет старый или новый
// файл, никогда мусор.
func (s *store) write(name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("persist: кодирование %s: %w", name, err)
	}
	sum := sha256.Sum256(data)
	env := fileEnvelope{Schema: schemaVersion, Data: data, SHA256: hex.EncodeToString(sum[:])}
	buf, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return fmt.Errorf("persist: кодирование %s: %w", name, err)
	}
	buf = append(buf, '\n')

	if s.testBlockWrite != nil {
		<-s.testBlockWrite
	}
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("persist: temp для %s: %w", name, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op после успешного rename
	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		return fmt.Errorf("persist: запись temp %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("persist: fsync temp %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("persist: закрытие temp %s: %w", tmpName, err)
	}
	if s.testFailRename != nil {
		if err := s.testFailRename(); err != nil {
			return fmt.Errorf("persist: запись %s: %w", name, err)
		}
	}
	if err := s.renameReplace(tmpName, filepath.Join(s.dir, name)); err != nil {
		return fmt.Errorf("persist: замена %s: %w", name, err)
	}
	s.syncDir()
	return nil
}

// renameReplace — атомарная замена. На Windows назначенный файл, открытый
// в этот момент читателем (поллинг файла тестами/диагностикой), даёт rename
// access denied (errno 5; окно обмена на стороне чтения — 32); короткий
// повтор с бюджетом переживает окно чтения. На POSIX оба номера — чужие
// классы ошибок (EIO/EPIPE от rename не встречаются), ветка мертва.
func (s *store) renameReplace(tmpName, dst string) error {
	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		err := s.rename(tmpName, dst)
		if err == nil || !sharingViolation(err) || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(3 * time.Millisecond)
	}
}

// sharingViolation — ошибка занятости назначенного файла заменой.
func sharingViolation(err error) bool {
	return errors.Is(err, syscall.Errno(5)) || errors.Is(err, syscall.Errno(32))
}

// syncDir — best-effort fsync каталога после rename: усиление, не барьер
// записи; ошибки видны журналом (Warn), запись не фейлят.
func (s *store) syncDir() {
	f, err := os.Open(s.dir)
	if err != nil {
		slog.Warn("persist: fsync каталога пропущен", "dir", s.dir, "err", err)
		return
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		slog.Warn("persist: fsync каталога не исполнен", "dir", s.dir, "err", err)
	}
}

// read читает и проверяет конверт файла в value.
func (s *store) read(name string, value any) error {
	buf, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil {
		return fmt.Errorf("persist: чтение %s: %w", name, err)
	}
	var env fileEnvelope
	if err := json.Unmarshal(buf, &env); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrCorrupt, name, err)
	}
	if env.Schema != schemaVersion {
		return fmt.Errorf("%w: %s: схема %d, ждём %d", ErrBadSchema, name, env.Schema, schemaVersion)
	}
	// Байты data в файле переформатируются отступами MarshalIndent: чексумма
	// сравнивается по компакт-форме с обеих сторон.
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, env.Data); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrCorrupt, name, err)
	}
	sum := sha256.Sum256(compacted.Bytes())
	if hex.EncodeToString(sum[:]) != env.SHA256 {
		return fmt.Errorf("%w: %s: чексумма", ErrCorrupt, name)
	}
	if err := json.Unmarshal(compacted.Bytes(), value); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrCorrupt, name, err)
	}
	return nil
}

// isNotExist сообщает, что ошибка — отсутствие файла (по цепочке %w).
func isNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}

// charStore — хранилище персонажей с индексом уникальных имён. Владелец —
// горутина персист-актора, синхронизация не нужна.
type charStore struct {
	store
	names map[string]string // lowercase имя → аккаунт
	cache map[string][]CharRecord
}

// openCharStore открывает каталог персонажей: полный скан (индекс имён —
// уникальность требует загрузки каталога); temp уже зачищен openStore.
func openCharStore(dir string) (*charStore, error) {
	s, err := openStore(dir)
	if err != nil {
		return nil, err
	}
	cs := &charStore{store: *s, names: make(map[string]string), cache: make(map[string][]CharRecord)}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("persist: скан %s: %w", dir, err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			slog.Warn("persist: посторонний файл в каталоге персонажей игнорирован",
				"file", e.Name())
			continue
		}
		if err := cs.indexFile(e.Name()); err != nil {
			return nil, err
		}
	}
	return cs, nil
}

// indexFile вносит имена одного файла в индекс; битый файл не валит скан.
func (cs *charStore) indexFile(name string) error {
	var recs []CharRecord
	if err := cs.read(name, &recs); err != nil {
		slog.Error("persist: битый файл персонажей не загружен (разбор оператором)",
			"file", name, "err", err)
		return nil
	}
	account := strings.TrimSuffix(name, ".json")
	seen := make(map[string]bool, len(recs))
	for _, r := range recs {
		key := lowercaseASCII(r.Name)
		if seen[key] {
			return fmt.Errorf("persist: дубликат имени %q внутри %s", r.Name, name)
		}
		seen[key] = true
		if owner, ok := cs.names[key]; ok {
			return fmt.Errorf("persist: дубликат имени %q: %s.json и %s", r.Name, owner, name)
		}
		cs.names[key] = account
	}
	cs.cache[account] = recs
	return nil
}

// list возвращает записи аккаунта; отсутствующий файл — пусто без ошибки.
func (cs *charStore) list(account string) ([]CharRecord, error) {
	if recs, ok := cs.cache[account]; ok {
		return recs, nil
	}
	var recs []CharRecord
	if err := cs.read(account+".json", &recs); err != nil {
		if isNotExist(err) {
			cs.cache[account] = nil
			return nil, nil
		}
		return nil, err
	}
	cs.cache[account] = recs
	return recs, nil
}

// saveFile сортирует записи по слоту (детерминированный файл) и записывает.
func (cs *charStore) saveFile(account string, recs []CharRecord) error {
	sort.Slice(recs, func(i, j int) bool { return recs[i].Slot < recs[j].Slot })
	return cs.write(account+".json", recs)
}

// nameOwner возвращает аккаунт-владельца имени (lowercase-ключ).
func (cs *charStore) nameOwner(name string) (string, bool) {
	owner, ok := cs.names[lowercaseASCII(name)]
	return owner, ok
}

// resyncNames приводит индекс имён и кэш аккаунта к записанному файлу.
func (cs *charStore) resyncNames(account string, recs []CharRecord) {
	for key, owner := range cs.names {
		if owner == account {
			delete(cs.names, key)
		}
	}
	for _, r := range recs {
		cs.names[lowercaseASCII(r.Name)] = account
	}
	cs.cache[account] = recs
}
