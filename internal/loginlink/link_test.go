package loginlink

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/udisondev/l2go/pkg/mtls"
)

// waitFor поллит cond до timeout: замена sleep-ожиданиям асинхронных событий.
func waitFor(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("условие не наступило за %s: %s", timeout, desc)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// linkTLS — конфиги одной mTLS-пары: сервер и клиент против общего CA.
type linkTLS struct {
	server, client *tls.Config
}

// newTestMaterial генерит материал mTLS во временный каталог.
func newTestMaterial(t *testing.T) linkTLS {
	t.Helper()
	m, err := mtls.GenerateMaterial("тест loginlink", nil)
	if err != nil {
		t.Fatalf("GenerateMaterial: %v", err)
	}
	dir := t.TempDir()
	if err := mtls.WriteMaterial(dir, m); err != nil {
		t.Fatalf("WriteMaterial: %v", err)
	}
	stack := linkTLS{}
	stack.server, err = mtls.ServerConfig(
		filepath.Join(dir, mtls.CAFile), filepath.Join(dir, mtls.ServerCertFile), filepath.Join(dir, mtls.ServerKeyFile))
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}
	stack.client, err = mtls.ClientConfig(
		filepath.Join(dir, mtls.CAFile), filepath.Join(dir, mtls.ClientCertFile), filepath.Join(dir, mtls.ClientKeyFile), "localhost")
	if err != nil {
		t.Fatalf("ClientTLSConfig: %v", err)
	}
	return stack
}

// startLink поднимает сервер стыка на 127.0.0.1:0 (mTLS).
func startLink(t *testing.T, stack linkTLS) (*Server, *grpc.Server, string) {
	t.Helper()
	srv := NewServer(NewSessions(time.Minute))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	gs := grpc.NewServer(GRPCServerOptions(stack.server)...)
	RegisterLoginLinkServer(gs, srv)
	go func() { _ = gs.Serve(ln) }()
	t.Cleanup(gs.Stop)
	return srv, gs, ln.Addr().String()
}

// runClient регистрирует GS-клиента; возвращает клиент и канал завершения Run.
func runClient(t *testing.T, addr, hexID string, clientTLS *tls.Config,
	onKick func(account, reason string)) (*Client, chan error) {
	t.Helper()
	c, err := Dial(ClientConfig{
		Addr: addr, HexID: []byte(hexID),
		Host: "10.1.2.3", Port: 7777, Name: "Bartz",
		TLS:    clientTLS,
		OnKick: onKick,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	// t.Context() отменяется перед Cleanup: Run завершится до Close.
	done := make(chan error, 1)
	go func() { done <- c.Run(t.Context()) }()
	t.Cleanup(func() { _ = c.Close() })
	return c, done
}

func TestLinkRegisterListDisappear(t *testing.T) {
	stack := newTestMaterial(t)
	srv, _, addr := startLink(t, stack)
	c, _ := runClient(t, addr, "hex-a", stack.client, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := c.WaitRegistered(ctx); err != nil {
		t.Fatalf("WaitRegistered: %v", err)
	}
	entries := srv.Servers()
	if len(entries) != 1 {
		t.Fatalf("Servers после регистрации: %d записей; want 1", len(entries))
	}
	e := entries[0]
	if e.ID != 1 || e.Host != "10.1.2.3" || e.Port != 7777 || e.Name != "Bartz" {
		t.Fatalf("запись = %+v; want ID=1 host=10.1.2.3 port=7777 name=Bartz", e)
	}
	// Обрыв стыка → GS исчезает из ServerList.
	_ = c.Close()
	waitFor(t, 5*time.Second, "GS исчезает из ServerList после обрыва", func() bool {
		return len(srv.Servers()) == 0
	})
}

func TestLinkValidateSessionVerdicts(t *testing.T) {
	stack := newTestMaterial(t)
	srv, _, addr := startLink(t, stack)
	c, _ := runClient(t, addr, "hex-a", stack.client, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := c.WaitRegistered(ctx); err != nil {
		t.Fatalf("WaitRegistered: %v", err)
	}
	if err := srv.sessions.Put("sergei", 1, 2); err != nil {
		t.Fatalf("Put: %v", err)
	}
	srv.sessions.SetPlayKeys("sergei", 3, 4)

	valid, err := c.ValidateSession(context.Background(), "sergei", 1, 2, 3, 4)
	if err != nil || !valid {
		t.Fatalf("ValidateSession(верные) = (%v, %v); want (true, nil)", valid, err)
	}
	valid, err = c.ValidateSession(context.Background(), "sergei", 1, 2, 3, 4)
	if err != nil || valid {
		t.Fatalf("ValidateSession(replay изъятых) = (%v, %v); want (false, nil)", valid, err)
	}
	valid, err = c.ValidateSession(context.Background(), "sergei", 9, 9, 9, 9)
	if err != nil || valid {
		t.Fatalf("ValidateSession(неверные ключи) = (%v, %v); want (false, nil)", valid, err)
	}
	valid, err = c.ValidateSession(context.Background(), "ghost", 1, 2, 3, 4)
	if err != nil || valid {
		t.Fatalf("ValidateSession(чужой аккаунт) = (%v, %v); want (false, nil)", valid, err)
	}
}

func TestLinkReplaceGuardAndDisplaced(t *testing.T) {
	// Интерливинг F4: замена по hexID → выход старого потока НЕ удаляет новую
	// запись; вытесненный клиент завершается терминально (без реконнекта).
	stack := newTestMaterial(t)
	srv, _, addr := startLink(t, stack)
	a, aDone := runClient(t, addr, "hex-a", stack.client, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := a.WaitRegistered(ctx); err != nil {
		t.Fatalf("WaitRegistered(A): %v", err)
	}
	b, bDone := runClient(t, addr, "hex-a", stack.client, nil)
	if err := b.WaitRegistered(ctx); err != nil {
		t.Fatalf("WaitRegistered(B): %v", err)
	}

	select {
	case err := <-aDone:
		if !errors.Is(err, ErrDisplaced) {
			t.Fatalf("Run(A) = %v; want ErrDisplaced", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("вытесненный A не завершился за 5 с")
	}
	select {
	case err := <-bDone:
		t.Fatalf("B завершён без причины: %v", err)
	default:
	}
	entries := srv.Servers()
	if len(entries) != 1 || entries[0].ID != 1 {
		t.Fatalf("после выхода A запись B жива: %+v; want одна запись ID=1", entries)
	}
}

func TestLinkKickEvent(t *testing.T) {
	stack := newTestMaterial(t)
	srv, _, addr := startLink(t, stack)
	got := make(chan [2]string, 1)
	c, _ := runClient(t, addr, "hex-a", stack.client, func(account, reason string) {
		select {
		case got <- [2]string{account, reason}:
		default:
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := c.WaitRegistered(ctx); err != nil {
		t.Fatalf("WaitRegistered: %v", err)
	}
	srv.Kick("sergei", "duplicate")
	select {
	case m := <-got:
		if m != [2]string{"sergei", "duplicate"} {
			t.Fatalf("kick = %v; want [sergei duplicate]", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Kick-событие не доставлено клиентской половине за 5 с")
	}
}

func TestLinkShutdownStreamsFast(t *testing.T) {
	// F3: остановка стыка при живом регистрированном GS завершается быстро —
	// GracefulStop не ждёт вечные потоки регистрации.
	stack := newTestMaterial(t)
	srv, gs, addr := startLink(t, stack)
	c, done := runClient(t, addr, "hex-a", stack.client, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := c.WaitRegistered(ctx); err != nil {
		t.Fatalf("WaitRegistered: %v", err)
	}

	stopped := make(chan struct{})
	go func() { gs.GracefulStop(); close(stopped) }()
	srv.ShutdownStreams()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("GracefulStop висит на потоке регистрации после ShutdownStreams")
	}
	// Клиент жив: конец стрима без Displaced = реконнект с паузой.
	select {
	case err := <-done:
		t.Fatalf("клиент завершился после остановки сервера: %v; want реконнект", err)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestLinkReconnect(t *testing.T) {
	// Переподключение: рестарт LS на том же порту (тот же материал) — GS
	// перерегистрируется в пределах паузы реконнекта.
	stack := newTestMaterial(t)
	_, gs1, addr := startLink(t, stack)
	c, _ := runClient(t, addr, "hex-a", stack.client, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := c.WaitRegistered(ctx); err != nil {
		t.Fatalf("WaitRegistered: %v", err)
	}

	gs1.Stop()
	srv2 := NewServer(NewSessions(time.Minute))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("relisten(%s): %v", addr, err)
	}
	gs2 := grpc.NewServer(GRPCServerOptions(stack.server)...)
	RegisterLoginLinkServer(gs2, srv2)
	go func() { _ = gs2.Serve(ln) }()
	t.Cleanup(gs2.Stop)

	waitFor(t, 3*ReRegisterPause+5*time.Second, "GS перерегистрировался после рестарта LS",
		func() bool { return len(srv2.Servers()) != 0 })
}

func TestClientValidateUnreachable(t *testing.T) {
	// F23: недоступный LS — err (fail-closed у потребителя), не вечная блокировка.
	stack := newTestMaterial(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // порт свободен и ничего не слушает
	c, err := Dial(ClientConfig{Addr: addr, HexID: []byte("x"), TLS: stack.client})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 2*ValidateTimeout)
	defer cancel()
	if _, err := c.ValidateSession(ctx, "sergei", 1, 2, 3, 4); err == nil {
		t.Fatal("ValidateSession на недоступном LS: want err")
	}
}

func TestLinkParallelRegistrations(t *testing.T) {
	// Реестр под параллельными регистрациями разных hexID (стресс для -race):
	// все регистрируются, список содержит все записи с уникальными ID.
	stack := newTestMaterial(t)
	srv, _, addr := startLink(t, stack)
	const n = 8
	// Run штатно держит регистрацию, пока жив ctx: после проверок ctx
	// отменяется явно, дожидаться без отмены — дедлок.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			c, err := Dial(ClientConfig{
				Addr: addr, HexID: []byte(fmt.Sprintf("hex-%02d", i)),
				Host: "10.1.2.3", Port: 7777, Name: "Bartz", TLS: stack.client,
			})
			if err != nil {
				errs <- err
				return
			}
			defer c.Close()
			if err := c.Run(ctx); err != nil {
				errs <- err
			}
		})
	}
	waitFor(t, 10*time.Second, "все GS регистрируются", func() bool {
		return len(srv.Servers()) == n
	})
	entries := srv.Servers()
	ids := make(map[byte]bool, len(entries))
	for _, e := range entries {
		if ids[e.ID] {
			t.Fatalf("дубль ID=%d в ServerList", e.ID)
		}
		ids[e.ID] = true
	}
	cancel()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
