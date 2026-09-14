// Пакет login — LoginServer: TCP-слушатель :2106 и стейт-машина login-флоу
// поверх писателей/представлений P1.4 (Init → AuthGameGuard → GGAuth →
// RequestAuthLogin → LoginOk → ServerList → PlayOk). Login-процесс вне мира:
// настенные часы допустимы, конкурентность — мьютекс-сервисы (инвариант 3
// AGENTS не касается).
package login

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/udisondev/l2go/internal/crypto"
	"github.com/udisondev/l2go/internal/loginlink"
	"github.com/udisondev/l2go/internal/persist"
)

// Дефолты записи ServerList по канону Mobius CT_0_Interlude: status=1 — up
// (ServerList.writeImpl: status==DOWN?0:1, регистрация статуса не несёт);
// serverType=0x40 — Free (ServerConfig «ServerListType»=free дефолтом,
// LoginServerThread шлёт при регистрации); pvp=true (GameServerInfo.IS_PVP);
// ageLimit=0 (ServerData); brackets=false (GameServerInfo).
const (
	serverStatusUp = 1
	serverTypeFree = 0x40
	defaultPvP     = true
)

// writeStageTimeout — дедлайн одной записи кадра (F25: n<len или зависший
// сокет не держат горутину коннекта).
const writeStageTimeout = 10 * time.Second

// Config — параметры LoginServer; нулевые лимиты/таймауты запрещены
// («0 = без лимита» молча — ловушка F7), адрес слушателя задаёт wire-up cmd.
type Config struct {
	MaxConns         int
	HandshakeTimeout time.Duration // абсолютный дедлайн фазы: accept → LoginOk (F25)
	IdleTimeout      time.Duration // дедлайн до полного кадра после LoginOk (F25)
}

func (c Config) validate() error {
	switch {
	case c.MaxConns <= 0:
		return fmt.Errorf("login: MaxConns = %d: нулевые лимиты запрещены", c.MaxConns)
	case c.HandshakeTimeout <= 0:
		return fmt.Errorf("login: HandshakeTimeout = %s: нулевые таймауты запрещены", c.HandshakeTimeout)
	case c.IdleTimeout <= 0:
		return fmt.Errorf("login: IdleTimeout = %s: нулевые таймауты запрещены", c.IdleTimeout)
	}
	return nil
}

// Server — LoginServer: проверяет пароли через персист, держит сессии в
// сторе стыка, строит ServerList из реестра GS.
type Server struct {
	cfg      Config
	accounts *persist.Accounts
	sessions *loginlink.Sessions
	link     *loginlink.Server

	key        *rsa.PrivateKey
	scrambledN []byte
	ln         net.Listener
	done       chan struct{}
	closeOnce  sync.Once
	wg         sync.WaitGroup
	mu         sync.Mutex // защищает conns и authed
	conns      map[*clientConn]struct{}
	authed     map[string]*clientConn // нормализованный логин → authed-коннект
	connCount  int
}

// New готовит сервер: генерирует RSA-ключ флоу (элемент сессии, не секрет
// топологии) и скрэмблит модуль для Init.
func New(cfg Config, accounts *persist.Accounts, sessions *loginlink.Sessions,
	link *loginlink.Server) (*Server, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		return nil, fmt.Errorf("login: RSA-ключ: %w", err)
	}
	scrambled, err := crypto.RSAScrambleModulus(key.N.Bytes())
	if err != nil {
		return nil, fmt.Errorf("login: скрэмбл модуля: %w", err)
	}
	return &Server{
		cfg:        cfg,
		accounts:   accounts,
		sessions:   sessions,
		link:       link,
		key:        key,
		scrambledN: scrambled,
		done:       make(chan struct{}),
		conns:      make(map[*clientConn]struct{}),
		authed:     make(map[string]*clientConn),
	}, nil
}

// Serve принимает коннекты до Close; сверх лимита — немедленный отказ.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return nil
			default:
			}
			return fmt.Errorf("login: accept: %w", err)
		}
		cc := newClientConn(s, conn)
		if !s.trackConn(cc) {
			slog.Warn("login: лимит коннектов — отказ", "addr", conn.RemoteAddr().String(),
				"limit", s.cfg.MaxConns)
			_ = conn.Close()
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.untrackConn(cc)
			cc.handle()
		}()
	}
}

// trackConn регистрирует коннект против лимита; false — сверх лимита.
func (s *Server) trackConn(cc *clientConn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.connCount >= s.cfg.MaxConns {
		return false
	}
	s.connCount++
	s.conns[cc] = struct{}{}
	return true
}

// untrackConn снимает коннект с учёта.
func (s *Server) untrackConn(cc *clientConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, cc)
	s.connCount--
}

// beginAuthed атомарно (одна критсекция с Put стора — против TOCTOU гонки
// двойного логина): кладёт loginOk-пару и связывает authed-коннект с аккаунтом.
// ok=false — живая сессия занята: стор уже удалил её, old — коннект-владелец
// для кика; ok=true — связка установлена, old — прежний коннект с умершей
// сессией (TTL), не кикается: его следующий кадр отвергнет checkLoginPair.
func (s *Server) beginAuthed(account string, cc *clientConn, k1, k2 int32) (old *clientConn, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old = s.authed[account]
	if err := s.sessions.Put(account, k1, k2); err != nil {
		return old, false
	}
	s.authed[account] = cc
	return old, true
}

// forgetAuthed снимает связку, если она принадлежит коннекту.
func (s *Server) forgetAuthed(norm string, cc *clientConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authed[norm] == cc {
		delete(s.authed, norm)
	}
}

// Close останавливает сервер: листенер закрывается, коннекты растормошены
// (дедлайн в прошлом), дрен до drainTimeout — затем жёсткое закрытие.
func (s *Server) Close(drain time.Duration) {
	s.closeOnce.Do(func() {
		close(s.done)
		s.mu.Lock()
		ln := s.ln
		conns := make([]*clientConn, 0, len(s.conns))
		for cc := range s.conns {
			conns = append(conns, cc)
		}
		s.mu.Unlock()
		if ln != nil {
			_ = ln.Close()
		}
		// Дрен: окно drainTimeout на штатное завершение коннектов; по
		// истечении — дедлайн в прошлом растормошит зависшие Read/Write,
		// затем жёсткое закрытие (F32).
		drained := make(chan struct{})
		go func() {
			s.wg.Wait()
			close(drained)
		}()
		select {
		case <-drained:
		case <-time.After(drain):
			slog.Warn("login: дрен коннектов исчерпан — жёсткое закрытие")
			past := time.Now().Add(-time.Second)
			for _, cc := range conns {
				cc.wake(past)
				cc.close()
			}
		}
	})
}
