package loginlink

import (
	"errors"
	"log/slog"
	"sort"
	"sync"
)

// ErrLinkClosed — стык остановлен (graceful shutdown): новые регистрации не
// принимаются.
var ErrLinkClosed = errors.New("стык закрыт")

// GameServerEntry — запись реестра GS: адрес/имя из регистрации, ID назначает
// стык (стабилен при замене по hexID — переезды не меняют ServerList).
type GameServerEntry struct {
	ID    byte
	HexID string
	Host  string
	Port  int32
	Name  string
}

// gsRecord — регистрация; идентичность записи — сам указатель (guard
// принадлежности: выход старого потока после замены не удаляет чужую запись —
// unregister сверяет cur == rec).
type gsRecord struct {
	entry     GameServerEntry
	displaced chan struct{} // закрыт при вытеснении новой регистрацией
	kick      chan kickMsg
}

type kickMsg struct {
	account, reason string
}

const kickQueue = 8

// maxServers — потолок записей реестра: ID записи — byte провода ServerList
// (1..255), переполнение не допускается.
const maxServers = 255

// registry — реестр зарегистрированных GS (мьютекс-сервис).
type registry struct {
	mu        sync.Mutex
	byHex     map[string]*gsRecord
	nextID    int
	closed    bool
	shutdownC chan struct{} // закрыт при остановке стыка (клиент реконнектится)
}

func newRegistry() *registry {
	return &registry{
		byHex:     make(map[string]*gsRecord),
		nextID:    1,
		shutdownC: make(chan struct{}),
	}
}

// register создаёт/заменяет запись по hexID; замена закрывает displaced старого
// потока (вытесненный клиент завершается терминально, без реконнекта).
func (r *registry) register(hexID, host string, port int32, name string) (*gsRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrLinkClosed
	}
	if _, exists := r.byHex[hexID]; !exists && r.nextID > maxServers {
		return nil, errors.New("loginlink: реестр GS полон (лимит записей ServerList)")
	}
	rec := &gsRecord{
		entry:     GameServerEntry{HexID: hexID, Host: host, Port: port, Name: name},
		displaced: make(chan struct{}),
		kick:      make(chan kickMsg, kickQueue),
	}
	if old := r.byHex[hexID]; old != nil {
		rec.entry.ID = old.entry.ID // ID стабилен при замене
		close(old.displaced)
	} else {
		rec.entry.ID = byte(r.nextID)
		r.nextID++
	}
	r.byHex[hexID] = rec
	return rec, nil
}

// unregister убирает запись, только если она принадлежит вызывающему потоку
// (сверка указателя): выход старого потока после замены не трогает новую запись.
func (r *registry) unregister(rec *gsRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur := r.byHex[rec.entry.HexID]; cur == rec {
		delete(r.byHex, rec.entry.HexID)
	}
}

// list — записи, сортированные по ID (порядок ServerList).
func (r *registry) list() []GameServerEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	entries := make([]GameServerEntry, 0, len(r.byHex))
	for _, rec := range r.byHex {
		entries = append(entries, rec.entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	return entries
}

// kickAll рассылает Kick каждой живой регистрации (неблокирующе; заглушка P3.4).
func (r *registry) kickAll(account, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sent := 0
	for _, rec := range r.byHex {
		select {
		case rec.kick <- kickMsg{account: account, reason: reason}:
			sent++
		default:
			slog.Warn("loginlink: kick-очередь GS переполнена — дроп",
				"account", account, "reason", reason)
		}
	}
}

// shutdown закрывает стык и запрещает новые регистрации. Событие Displaced не
// шлётся: конец стрима без Displaced для клиента = реконнект с паузой; записи
// убирают сами хендлеры, возвращаясь по shutdownC.
func (r *registry) shutdown() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	close(r.shutdownC)
}
