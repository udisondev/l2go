// Прокси-тап: зеркалирует клиент↔сервер в журнал. Кадровый цикл (не io.Copy) —
// rewrite-ветке login-ноги нужны целые кадры; полузакрытие: EOF ноги —
// CloseWrite противоположной, полная close — по завершении обеих ног.

package tap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/udisondev/l2go/internal/protocol"
)

// Map — пара «слушаю → пересылаю»; Login помечает ногу LoginServer (rewrite).
type Map struct {
	Listen   string
	Upstream string
	Login    bool
	// Raw — прозрачная труба без нарезки на кадры (login-нога с не-L2
	// прологом: сырые рукопожатия античитов совместимы только с ней).
	Raw bool
}

// Options — параметры capture-режима.
type Options struct {
	Maps    []Map
	Log     io.Writer
	Rewrite bool
	// OnReady вызывается однократно после привязки всех слушателей (до
	// первого accept) — сигнал готовности вызывающему.
	OnReady func()
}

// connPair — живое соединение тапа (для shutdown-закрытия).
type connPair struct{ client, upstream net.Conn }

// Run — capture-режим: слушает все Maps до отмены контекста. Отмена закрывает
// listener и все живые соединения, дожидается горутин и flush'ит журнал.
func Run(ctx context.Context, opts Options) error {
	if len(opts.Maps) == 0 {
		return fmt.Errorf("tap: не задано ни одной пары listen=upstream")
	}
	if opts.Log == nil {
		return fmt.Errorf("tap: журнал не задан")
	}
	jw := newJournalWriter(opts.Log)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu      sync.Mutex
		live    []*connPair
		connsWG sync.WaitGroup
		connSeq uint64
	)
	closeLive := func() {
		mu.Lock()
		defer mu.Unlock()
		for _, p := range live {
			_ = p.client.Close()
			if p.upstream != nil {
				_ = p.upstream.Close()
			}
		}
	}
	// сигнал смерти журнала: capture обязан остановиться, а не долбить
	// upstream полусессиями (первая ошибка записи — разрыв всего).
	journalDead := make(chan struct{})
	var journalOnce sync.Once
	stopOnJournalErr := func() {
		journalOnce.Do(func() {
			slog.Error("tap: журнал недоступен — остановка capture")
			close(journalDead)
		})
	}

	errCh := make(chan error, len(opts.Maps)+1)
	var listeners []net.Listener
	for _, m := range opts.Maps {
		ln, err := net.Listen("tcp", m.Listen)
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			closeLive()
			connsWG.Wait()
			_ = jw.flush()
			return fmt.Errorf("tap: слушаю %s: %w", m.Listen, err)
		}
		listeners = append(listeners, ln)
	}
	if opts.OnReady != nil {
		opts.OnReady()
	}
	for i, m := range opts.Maps {
		ln := listeners[i]
		go func(ln net.Listener, m Map) {
			for {
				conn, err := ln.Accept()
				if err != nil {
					select {
					case <-ctx.Done():
						return
					default:
					}
					select {
					case errCh <- fmt.Errorf("tap: accept %s: %w", m.Listen, err):
					default:
					}
					return
				}
				connsWG.Add(1)
				go func(client net.Conn, m Map) {
					defer connsWG.Done()
					id := atomic.AddUint64(&connSeq, 1)
					handleConn(ctx, client, m, opts, jw, id, &mu, &live, stopOnJournalErr)
				}(conn, m)
			}
		}(ln, m)
	}

	var runErr error
	select {
	case <-ctx.Done():
	case err := <-errCh:
		runErr = err
	case <-journalDead:
		runErr = fmt.Errorf("tap: журнал недоступен — capture остановлен")
	}
	for _, ln := range listeners {
		_ = ln.Close()
	}
	closeLive()
	connsWG.Wait()
	if err := jw.flush(); err != nil && runErr == nil {
		runErr = err
	}
	return runErr
}

// handleConn — жизненный цикл одного соединения: регистрация в live до dial
// (отмена контекста закрывает соединение в любом окне), dial с контекстом,
// две ноги зеркалирования, connClose после завершения обеих (data после
// connClose в журнале исключена: запись — после WaitGroup).
func handleConn(ctx context.Context, client net.Conn, m Map, opts Options, jw *journalWriter, id uint64, mu *sync.Mutex, live *[]*connPair, stopOnJournalErr func()) {
	pair := &connPair{client: client}
	mu.Lock()
	*live = append(*live, pair)
	mu.Unlock()
	unregister := func() {
		mu.Lock()
		for i, p := range *live {
			if p == pair {
				*live = append((*live)[:i], (*live)[i+1:]...)
				break
			}
		}
		mu.Unlock()
	}

	if err := jw.connOpen(id, m.Listen, m.Upstream, time.Now().UnixNano()); err != nil {
		stopOnJournalErr()
		unregister()
		_ = client.Close()
		return
	}
	d := net.Dialer{}
	up, err := d.DialContext(ctx, "tcp", m.Upstream)
	if err != nil {
		err = fmt.Errorf("подключение к %s: %w", m.Upstream, err)
		_ = jw.connClose(id, err)
		unregister()
		_ = client.Close()
		return
	}
	mu.Lock()
	pair.upstream = up
	mu.Unlock()

	var rw *loginRewriter
	if opts.Rewrite && m.Login {
		if gameIP, gamePort, ok := gameEndpoint(opts); ok {
			rw = newLoginRewriter(gameIP, gamePort)
		}
	}
	legErrs := make(chan error, 2)
	var legs sync.WaitGroup
	legs.Add(2)
	go func() {
		defer legs.Done()
		legErrs <- pump(up, client, recDirStoC, id, jw, rw, !m.Raw)
	}()
	go func() {
		defer legs.Done()
		legErrs <- pump(client, up, recDirCtoS, id, jw, nil, !m.Raw)
	}()
	legs.Wait()
	err1, err2 := <-legErrs, <-legErrs
	_ = client.Close()
	_ = up.Close()
	unregister()
	connErr := joinLegErrs(err1, err2)
	if cerr := jw.connClose(id, connErr); cerr != nil {
		stopOnJournalErr()
	}
}

func joinLegErrs(err1, err2 error) error {
	switch {
	case err1 == nil:
		return err2
	case err2 == nil:
		return err1
	default:
		return fmt.Errorf("%v; %v", err1, err2)
	}
}

// gameEndpoint — адрес первой не-login Map: rewrite подменяет IP и порт
// ServerList на слушателя game-ноги тапа. Нет game-Map или нераспарсиваемый
// адрес — ok=false: rewrite отключается с предупреждением (тихая подстановка
// нерабочего адреса запрещена).
func gameEndpoint(opts Options) (ip [4]byte, port int32, ok bool) {
	for _, m := range opts.Maps {
		if m.Login {
			continue
		}
		host, portStr, err := net.SplitHostPort(m.Listen)
		if err != nil {
			break
		}
		p, err := strconv.Atoi(portStr)
		if err != nil {
			break
		}
		parsed := net.ParseIP(host)
		if parsed == nil || parsed.To4() == nil {
			break
		}
		copy(ip[:], parsed.To4())
		return ip, int32(p), true
	}
	slog.Warn("tap: адрес game-ноги не определён — rewrite отключён")
	return [4]byte{127, 0, 0, 1}, 0, false
}

// pump — одна нога: читает данные, пишет в журнал и противоположную ногу.
// framed=true: кадровый цикл по [u16 длина][тело] (L2-протокол); framed=false:
// прозрачная труба — байты пересылаются немедленно (login-нога с не-L2
// прологом: античиты с сырыми рукопожатиями вида "READY\n" несовместимы с
// ожиданием полного кадра). EOF — полузакрытие (CloseWrite противоположной
// ноги), ошибка — разрыв соединения вызывающим (после завершения обеих ног).
func pump(rd, wr net.Conn, dir byte, id uint64, jw *journalWriter, rw *loginRewriter, framed bool) error {
	if !framed {
		return pumpRaw(rd, wr, dir, id, jw)
	}
	var buf []byte
	tmp := make([]byte, 4096)
	for {
		n, err := rd.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		for len(buf) >= 2 {
			body, ferr := protocol.NextFrame(buf)
			if errors.Is(ferr, protocol.ErrFrameIncomplete) {
				break
			}
			if ferr != nil {
				return fmt.Errorf("разрез потока: %w", ferr)
			}
			rec := make([]byte, len(body)+2)
			copy(rec, buf[:len(body)+2])
			buf = buf[len(body)+2:]

			out := rec
			if rw != nil {
				rewritten, orig, rerr := rw.process(rec)
				if rerr != nil {
					slog.Warn("tap: rewrite пропущен", "connID", id, "err", rerr)
				} else {
					if orig != nil {
						if jerr := jw.data(recOriginal, id, dir, time.Now().UnixNano(), orig); jerr != nil {
							return jerr
						}
					}
					out = rewritten
				}
			}
			if _, werr := wr.Write(out); werr != nil {
				return fmt.Errorf("запись в ногу: %w", werr)
			}
			if jerr := jw.data(recData, id, dir, time.Now().UnixNano(), out); jerr != nil {
				return jerr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				// хвост без полного кадра — пройти насквозь и журналировать
				// (data = фактически отправленные байты, включая бескарровый хвост)
				if len(buf) > 0 {
					slog.Warn("tap: хвост потока без полного кадра", "bytes", len(buf))
					if _, werr := wr.Write(buf); werr != nil {
						return fmt.Errorf("запись хвоста: %w", werr)
					}
					if jerr := jw.data(recData, id, dir, time.Now().UnixNano(), buf); jerr != nil {
						return jerr
					}
					buf = nil
				}
				if cw, ok := wr.(interface{ CloseWrite() error }); ok {
					if cerr := cw.CloseWrite(); cerr != nil {
						return fmt.Errorf("полузакрытие: %w", cerr)
					}
				}
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("чтение ноги: %w", err)
		}
	}
}

// pumpRaw — прозрачная труба: каждый прочитанный чанк пересылается сразу и
// журналируется записью data (без нарезки на кадры; крипто-состояние ноги
// не отслеживается — разбор делает декодер по журналу).
func pumpRaw(rd, wr net.Conn, dir byte, id uint64, jw *journalWriter) error {
	tmp := make([]byte, 4096)
	for {
		n, err := rd.Read(tmp)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, tmp[:n])
			if _, werr := wr.Write(chunk); werr != nil {
				return fmt.Errorf("запись в ногу: %w", werr)
			}
			if jerr := jw.data(recData, id, dir, time.Now().UnixNano(), chunk); jerr != nil {
				return jerr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if cw, ok := wr.(interface{ CloseWrite() error }); ok {
					if cerr := cw.CloseWrite(); cerr != nil {
						return fmt.Errorf("полузакрытие: %w", cerr)
					}
				}
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("чтение ноги: %w", err)
		}
	}
}
