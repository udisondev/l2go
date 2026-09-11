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
			_ = p.upstream.Close()
		}
	}

	errCh := make(chan error, len(opts.Maps))
	var listeners []net.Listener
	for _, m := range opts.Maps {
		ln, err := net.Listen("tcp", m.Listen)
		if err != nil {
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
					handleConn(ctx, client, m, opts, jw, id, &mu, &live)
				}(conn, m)
			}
		}(ln, m)
	}

	var runErr error
	select {
	case <-ctx.Done():
	case err := <-errCh:
		runErr = err
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

// handleConn — жизненный цикл одного соединения: dial upstream, две ноги
// зеркалирования, connClose после завершения обеих (data после connClose в
// журнале исключена: запись — после WaitGroup).
func handleConn(ctx context.Context, client net.Conn, m Map, opts Options, jw *journalWriter, id uint64, mu *sync.Mutex, live *[]*connPair) {
	_ = jw.connOpen(id, m.Listen, m.Upstream, time.Now().UnixNano())
	up, err := net.Dial("tcp", m.Upstream)
	if err != nil {
		err = fmt.Errorf("подключение к %s: %w", m.Upstream, err)
		_ = jw.connClose(id, err)
		_ = client.Close()
		return
	}

	pair := &connPair{client: client, upstream: up}
	mu.Lock()
	*live = append(*live, pair)
	mu.Unlock()

	var rw *loginRewriter
	if opts.Rewrite && m.Login {
		gameIP, gamePort := gameEndpoint(opts)
		rw = newLoginRewriter(gameIP, gamePort)
	}
	legErrs := make(chan error, 2)
	var legs sync.WaitGroup
	legs.Add(2)
	go func() {
		defer legs.Done()
		legErrs <- pump(up, client, recDirStoC, id, jw, rw)
	}()
	go func() {
		defer legs.Done()
		legErrs <- pump(client, up, recDirCtoS, id, jw, nil)
	}()
	legs.Wait()
	err1, err2 := <-legErrs, <-legErrs
	_ = client.Close()
	_ = up.Close()
	mu.Lock()
	for i, p := range *live {
		if p == pair {
			*live = append((*live)[:i], (*live)[i+1:]...)
			break
		}
	}
	mu.Unlock()
	connErr := joinLegErrs(err1, err2)
	if cerr := jw.connClose(id, connErr); cerr != nil {
		slog.Error("tap: журнал недоступен, capture остановлен", "connID", id, "err", cerr)
		_ = client.Close()
		_ = up.Close()
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
// ServerList на слушателя game-ноги тапа.
func gameEndpoint(opts Options) ([4]byte, int32) {
	for _, m := range opts.Maps {
		if !m.Login {
			ip, port, err := net.SplitHostPort(m.Listen)
			if err != nil {
				break
			}
			p, err := strconv.Atoi(port)
			if err != nil {
				break
			}
			return parseIPv4(ip), int32(p)
		}
	}
	return [4]byte{127, 0, 0, 1}, 0
}

func parseIPv4(host string) [4]byte {
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil {
		return [4]byte{127, 0, 0, 1}
	}
	var out [4]byte
	copy(out[:], ip.To4())
	return out
}

// pump — одна нога: читает кадры, пишет в журнал и противоположную ногу.
// EOF — полузакрытие (CloseWrite противоположной ноги), ошибка — разрыв
// соединения вызывающим (после завершения обеих ног).
func pump(rd, wr net.Conn, dir byte, id uint64, jw *journalWriter, rw *loginRewriter) error {
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
				// хвост без полного кадра — пройти насквозь без журнала
				if len(buf) > 0 {
					slog.Warn("tap: хвост потока без полного кадра", "bytes", len(buf))
					if _, werr := wr.Write(buf); werr != nil {
						return fmt.Errorf("запись хвоста: %w", werr)
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
