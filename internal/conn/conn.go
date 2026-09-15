// Package conn — провода игрового процесса: TCP-коннекты клиентов, разрез
// потока кадрами, расшифровка входящих (входящий криптоконтекст — у горутины
// чтения; ключ сессии рождается здесь же при accept), исходящая
// write-горутина поверх шва Outbound. TCP_NODELAY и read/write deadlines
// обязательны; события потребителю — каналами: события одного коннекта — FIFO
// из одной горутины чтения, OnFrame передаёт владение байтами кадра.
package conn

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/udisondev/l2go/internal/crypto"
	"github.com/udisondev/l2go/internal/protocol"
)

// ConnID — идентификатор коннекта в пределах процесса.
type ConnID uint64

// Outbound — исходящий шов одного коннекта (реализация — клиентская запись
// encode-стейджа; conn encode не импортирует). Take зовёт ровно одна
// write-горутина: prev — слэбы предыдущего батча (запись завершена, слэбы
// возвращаются в пул), next — батч для записи одним куском; close —
// дописать батч и разорвать сокет (close-after-flush, сокет не рвётся до
// флеша очереди).
type Outbound interface {
	Take(prev [][]byte) (next [][]byte, close bool)
}

// Outbounds — фабрика исходящих очередей: регистрирует клиента с ключом
// сессии до старта write-горутины (окна Take-до-регистрации нет), снимает
// после выхода. Реализация — encode.Stage.
type Outbounds interface {
	Register(id ConnID, key [8]byte) Outbound
	Unregister(id ConnID)
	Close(id ConnID)
}

// ReadMode — режим чтения коннекта; переводит потребитель (шлюз) при
// переходах фаз стейт-машины: провода не знают протокола.
type ReadMode uint8

const (
	// ModeHandshake — абсолютный дедлайн accept→AuthLogin (Config.
	// HandshakeTimeout): dribble байтами не продлевает.
	ModeHandshake ReadMode = iota
	// ModePresession — дедлайн до полного кадра, перезаводится каждым
	// кадром (Config.IdleTimeout; человек в экране выбора).
	ModePresession
	// ModeStationary — без read-дедлайна (молчаливый легитимный клиент не
	// страдает); полумёртвый сокет ловит TCP keepalive.
	ModeStationary
)

// Event — событие ридёра: открытие коннекта (Open; Key — ключ сессии) или
// кадр (Frame — расшифрованные байты опкод+тело, владение передано
// получателю). Done возвращает слот per-conn под-лимита канала — потребитель
// зовёт после обработки.
type Event struct {
	Conn    ConnID
	Open    bool
	Key     [8]byte
	Frame   []byte
	release *atomic.Int32
}

// Done возвращает слот под-лимита канала событий.
func (e *Event) Done() {
	if e.release != nil {
		e.release.Add(-1)
	}
}

// ClosedEvent — коннект закрыт; OpenSent — удалось ли ридёру поставить
// событие открытия (ложный поздний OnOpen закрытого коннекта гасится
// получателем). Release возвращает слот MaxConns — потребитель зовёт после
// обработки; дроп закрытия невозможен по построению (ёмкость канала —
// MaxConns, слот резервируется до старта эмиттера).
type ClosedEvent struct {
	Conn     ConnID
	OpenSent bool
	release  func()
}

// Release возвращает слот MaxConns.
func (e *ClosedEvent) Release() {
	if e.release != nil {
		e.release()
	}
}

// Config — параметры сервера; нулевые лимиты/таймауты запрещены («0 = без
// лимита» молча — ловушка), вход из флагов/env недоверен.
type Config struct {
	MaxConns         int           // общий лимит коннектов
	HandshakeTimeout time.Duration // абсолютный accept→AuthLogin
	IdleTimeout      time.Duration // перезаводится полным кадром (пресессия)
	WriteTimeout     time.Duration // стадийный дедлайн каждой записи
	KeepAlive        time.Duration // TCP keepalive стационарной фазы
	FrameCap         int           // кэп входящего кадра (байты)
	EventQueue       int           // ёмкость канала событий
	PerConnEvents    int           // под-лимит слотов канала на коннект
}

func (c Config) validate() error {
	switch {
	case c.MaxConns <= 0:
		return fmt.Errorf("conn: MaxConns = %d: нулевые лимиты запрещены", c.MaxConns)
	case c.HandshakeTimeout <= 0:
		return fmt.Errorf("conn: HandshakeTimeout = %s: нулевые таймауты запрещены", c.HandshakeTimeout)
	case c.IdleTimeout <= 0:
		return fmt.Errorf("conn: IdleTimeout = %s: нулевые таймауты запрещены", c.IdleTimeout)
	case c.WriteTimeout <= 0:
		return fmt.Errorf("conn: WriteTimeout = %s: нулевые таймауты запрещены", c.WriteTimeout)
	case c.KeepAlive <= 0:
		return fmt.Errorf("conn: KeepAlive = %s: нулевые таймауты запрещены", c.KeepAlive)
	case c.FrameCap <= 0:
		return fmt.Errorf("conn: FrameCap = %d: нулевые капы запрещены", c.FrameCap)
	case c.EventQueue <= 0:
		return fmt.Errorf("conn: EventQueue = %d: нулевые капы запрещены", c.EventQueue)
	case c.PerConnEvents <= 0:
		return fmt.Errorf("conn: PerConnEvents = %d: нулевые капы запрещены", c.PerConnEvents)
	}
	return nil
}

// connState — запись живого коннекта: режим чтения переводит потребитель;
// inflight — занятые коннектом слоты канала событий (per-conn под-лимит).
type connState struct {
	mode     atomic.Uint32
	inflight atomic.Int32
	conn     net.Conn
	closing  atomic.Bool
}

// Server — TCP-провода игрового процесса. События — каналами Events/Closes;
// канал закрытий бездропов по построению: слот MaxConns резервируется до
// старта эмиттера, освобождается потребителем обработкой ClosedEvent.
type Server struct {
	cfg      Config
	out      Outbounds
	events   chan Event
	closes   chan ClosedEvent
	slots    atomic.Int64
	nextID   atomic.Uint64
	mu       sync.Mutex
	states   map[ConnID]*connState
	closing  chan struct{}
	closeOne sync.Once
	wg       sync.WaitGroup
}

// New валидирует конфиг и собирает сервер; слушатель подаётся в Serve.
func New(cfg Config, out Outbounds) (*Server, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("conn: фабрика исходящих nil")
	}
	return &Server{
		cfg:     cfg,
		out:     out,
		events:  make(chan Event, cfg.EventQueue),
		closes:  make(chan ClosedEvent, cfg.MaxConns),
		states:  make(map[ConnID]*connState),
		closing: make(chan struct{}),
	}, nil
}

// Events — канал событий ридёров (открытия и кадры).
func (s *Server) Events() <-chan Event { return s.events }

// Closes — канал закрытий коннектов.
func (s *Server) Closes() <-chan ClosedEvent { return s.closes }

// Serve принимает коннекты до Close; сверх лимита — немедленный отказ без
// событий (слот резервируется до старта эмиттера).
func (s *Server) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.closing:
				return nil
			default:
			}
			return fmt.Errorf("conn: accept: %w", err)
		}
		if s.slots.Load() >= int64(s.cfg.MaxConns) {
			slog.Warn("conn: лимит коннектов — отказ", "addr", conn.RemoteAddr().String(),
				"limit", s.cfg.MaxConns)
			_ = conn.Close()
			continue
		}
		s.slots.Add(1)
		s.wg.Add(1)
		go s.handle(conn)
	}
}

// handle — жизненный цикл коннекта: ключ сессии, регистрация исходящего,
// write-горутина, цикл чтения с расшифровкой кадров со второго.
func (s *Server) handle(conn net.Conn) {
	id := ConnID(s.nextID.Add(1))
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.states, id)
		s.mu.Unlock()
		// Разбудить паркинг write-горутины: очередь пуста — Take вернёт
		// close и горутина выйдет (идемпотентно с CloseAfterFlush).
		s.out.Close(id)
		_ = conn.Close()
	}()

	var key [8]byte
	if _, err := rand.Read(key[:]); err != nil {
		slog.Error("conn: ключ сессии не сгенерирован", "conn", id, "err", err)
		return
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetKeepAlivePeriod(s.cfg.KeepAlive)
	}

	st := &connState{conn: conn}
	st.mode.Store(uint32(ModeHandshake))
	s.mu.Lock()
	s.states[id] = st
	s.mu.Unlock()

	out := s.out.Register(id, key)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.out.Unregister(id)
		s.writeLoop(conn, out)
		// write-горутина вышла (close-after-flush или ошибка записи) —
		// рвём сокет: ридёр выйдет и эмитит закрытие.
		_ = conn.Close()
	}()

	// Событие открытия: не влезло — коннект без обслуживания не живёт.
	openSent := s.emit(Event{Conn: id, Open: true, Key: key}, st)
	if openSent {
		s.readLoop(conn, id, st, key)
	}
	// Закрытие: слот MaxConns освобождает потребитель (Release); ёмкость
	// канала равна MaxConns и слот уже зарезервирован — дроп невозможен.
	select {
	case s.closes <- ClosedEvent{Conn: id, OpenSent: openSent, release: s.releaseSlot}:
	default:
		slog.Error("conn: канал закрытий полон — контракт слотов нарушен", "conn", id)
	}
}

// releaseSlot возвращает слот MaxConns (потребитель закрытия).
func (s *Server) releaseSlot() { s.slots.Add(-1) }

// emit ставит событие ридёра с учётом per-conn под-лимита слотов канала
// (один коннект не занимает чужие слоты); false — свой под-лимит полон или
// канал переполнен: ридэр закрывает свой сокет (close-on-overflow).
func (s *Server) emit(ev Event, st *connState) bool {
	if st.inflight.Add(1) > int32(s.cfg.PerConnEvents) {
		st.inflight.Add(-1)
		return false
	}
	ev.release = &st.inflight
	select {
	case s.events <- ev:
		return true
	default:
		st.inflight.Add(-1)
		return false
	}
}

// readLoop читает поток, режет кадры, расшифровывает со второго кадра
// (первый — ProtocolVersion открытым текстом, канон), передаёт владение
// копией кадра. Дедлайны — по режиму чтения.
func (s *Server) readLoop(conn net.Conn, id ConnID, st *connState, key [8]byte) {
	dec := crypto.NewGameCrypt(key)
	dec.Enable()
	buf := make([]byte, 8192)
	var pending []byte
	first := true
	_ = conn.SetReadDeadline(time.Now().Add(s.cfg.HandshakeTimeout))
	for {
		switch ReadMode(st.mode.Load()) {
		case ModePresession:
			_ = conn.SetReadDeadline(time.Now().Add(s.cfg.IdleTimeout))
		case ModeStationary:
			_ = conn.SetReadDeadline(time.Time{})
		}
		n, err := conn.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
			for {
				frame, ferr := protocol.NextFrame(pending)
				if errors.Is(ferr, protocol.ErrFrameIncomplete) {
					break
				}
				if ferr != nil {
					slog.Warn("conn: рамка кадра нарушена — разрыв",
						"conn", id, "err", ferr)
					return
				}
				if len(frame) > s.cfg.FrameCap {
					slog.Warn("conn: кадр сверх капа — разрыв",
						"conn", id, "len", len(frame), "cap", s.cfg.FrameCap)
					return
				}
				body := frame
				if !first {
					if derr := dec.Decrypt(body); derr != nil {
						slog.Warn("conn: расшифровка кадра — разрыв",
							"conn", id, "err", derr)
						return
					}
				}
				first = false
				if !s.emit(Event{Conn: id, Frame: append([]byte(nil), body...)}, st) {
					return // close-on-overflow своего под-лимита
				}
				pending = pending[len(frame)+2:]
			}
			if len(pending) > s.cfg.FrameCap+2 {
				slog.Warn("conn: хвост сверх капа — разрыв", "conn", id)
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// writeLoop — единственный писатель сокета: батчи шва Outbound одним write
// на пробуждение со стадийным дедлайном; close — дописать и разорвать.
func (s *Server) writeLoop(conn net.Conn, out Outbound) {
	var prev [][]byte
	var batch []byte
	for {
		next, close := out.Take(prev)
		prev = next
		if len(prev) > 0 {
			batch = batch[:0]
			for _, slab := range prev {
				batch = append(batch, slab...)
			}
			_ = conn.SetWriteDeadline(time.Now().Add(s.cfg.WriteTimeout))
			if _, err := conn.Write(batch); err != nil {
				return
			}
		}
		if close {
			return
		}
	}
}

// SetReadMode переводит коннект в режим чтения (нисходящий шов потребителя).
// Неизвестный id — no-op.
func (s *Server) SetReadMode(id ConnID, mode ReadMode) {
	s.mu.Lock()
	st, ok := s.states[id]
	s.mu.Unlock()
	if ok {
		st.mode.Store(uint32(mode))
	}
}

// CloseAfterFlush помечает соединение закрываемым после флеша исходящей
// очереди (сокет не рвётся до выдачи стоящих кадров). Неизвестный id — no-op.
func (s *Server) CloseAfterFlush(id ConnID) {
	s.mu.Lock()
	st, ok := s.states[id]
	s.mu.Unlock()
	if !ok {
		return
	}
	if st.closing.CompareAndSwap(false, true) {
		s.out.Close(id)
	}
}

// Close останавливает сервер: слушатель закрывается снаружи, коннекты
// растормашиваются дедлайном в прошлом, дрен горутин до выхода.
func (s *Server) Close() {
	s.closeOne.Do(func() {
		close(s.closing)
		past := time.Now().Add(-time.Second)
		s.mu.Lock()
		states := make([]*connState, 0, len(s.states))
		for _, st := range s.states {
			states = append(states, st)
		}
		s.mu.Unlock()
		for _, st := range states {
			_ = st.conn.SetDeadline(past)
		}
		s.wg.Wait()
	})
}
