// Пакет loginlink — сетевой стык LoginServer ↔ GameServer (gRPC, ADR-0005:
// LS — сервер внутреннего порта, GS — клиент; gRPC живёт только в пограничных
// пакетах). Состояние стыка — сессии и реестр GS, мьютекс-сервис по прецеденту
// persist.Accounts (login-процесс вне региона, инвариант 3 AGENTS не касается).
// Регенерация pb — по комментариям в buf.gen.yaml.
package loginlink

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrAccountInUse — живая сессия аккаунта существует: повторный вход отклонён,
// старая сессия удалена (замена со следующей попытки — канон ALREADY_ON_LS).
var ErrAccountInUse = errors.New("аккаунт уже в логине")

// session — четыре ключа сессии (loginOk-пара из LoginOk, playOk-пара из
// PlayOk) и ленивый TTL.
type session struct {
	loginOk1, loginOk2 int32
	playOk1, playOk2   int32
	expiresAt          time.Time
}

// Sessions — in-memory стор сессий, ключ — нормализованный (нижний регистр)
// логин: нормализует сам стор, обе точки входа (флоу login и хендлер
// ValidateSession) приходят с сырой строкой провода.
type Sessions struct {
	mu   sync.Mutex
	ttl  time.Duration
	now  func() time.Time // шов времени тестов
	live map[string]*session
}

// NewSessions создаёт стор с TTL сессий; ttl ≤ 0 — нарушение программного
// контракта wire-up («0 = без лимита» запрещён).
func NewSessions(ttl time.Duration) *Sessions {
	if ttl <= 0 {
		panic("loginlink: NewSessions: ttl <= 0")
	}
	return &Sessions{ttl: ttl, now: time.Now, live: make(map[string]*session)}
}

// liveLocked возвращает живую сессию ключа, просроченную удаляет (ленивый
// TTL — тикера нет); вызывающий держит mu.
func (s *Sessions) liveLocked(key string) *session {
	ses := s.live[key]
	if ses == nil {
		return nil
	}
	if !s.now().Before(ses.expiresAt) {
		delete(s.live, key)
		return nil
	}
	return ses
}

// Put атомарно (check-account-and-put): живой сессии нет — кладёт loginOk-пару;
// есть — удаляет её и возвращает ErrAccountInUse.
func (s *Sessions) Put(account string, loginOk1, loginOk2 int32) error {
	key := strings.ToLower(account)
	s.mu.Lock()
	defer s.mu.Unlock()
	if old := s.liveLocked(key); old != nil {
		delete(s.live, key)
		return fmt.Errorf("loginlink: Put(%s): %w", key, ErrAccountInUse)
	}
	s.live[key] = &session{
		loginOk1:  loginOk1,
		loginOk2:  loginOk2,
		expiresAt: s.now().Add(s.ttl),
	}
	return nil
}

// CheckLoginPair сверяет loginOk-пару с живой сессией (без изъятия) —
// RequestServerList/RequestServerLogin.
func (s *Sessions) CheckLoginPair(account string, loginOk1, loginOk2 int32) bool {
	key := strings.ToLower(account)
	s.mu.Lock()
	defer s.mu.Unlock()
	ses := s.liveLocked(key)
	return ses != nil && ses.loginOk1 == loginOk1 && ses.loginOk2 == loginOk2
}

// SetPlayKeys кладёт playOk-пару в живую сессию (PlayOk); false — сессии нет.
func (s *Sessions) SetPlayKeys(account string, playOk1, playOk2 int32) bool {
	key := strings.ToLower(account)
	s.mu.Lock()
	defer s.mu.Unlock()
	ses := s.liveLocked(key)
	if ses == nil {
		return false
	}
	ses.playOk1, ses.playOk2 = playOk1, playOk2
	return true
}

// Consume атомарно сверяет все четыре ключа и изымает сессию (replay-защита:
// повторная валидация теми же ключами невалидна).
func (s *Sessions) Consume(account string, loginOk1, loginOk2, playOk1, playOk2 int32) bool {
	key := strings.ToLower(account)
	s.mu.Lock()
	defer s.mu.Unlock()
	ses := s.liveLocked(key)
	if ses == nil {
		return false
	}
	if ses.loginOk1 != loginOk1 || ses.loginOk2 != loginOk2 ||
		ses.playOk1 != playOk1 || ses.playOk2 != playOk2 {
		return false
	}
	delete(s.live, key)
	return true
}

// Drop удаляет сессию безусловно (kill-путь повторного входа, shutdown).
func (s *Sessions) Drop(account string) {
	key := strings.ToLower(account)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.live, key)
}

// Len — число живых сессий (метрика/тесты).
func (s *Sessions) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for key := range s.live {
		if s.liveLocked(key) != nil {
			n++
		}
	}
	return n
}
