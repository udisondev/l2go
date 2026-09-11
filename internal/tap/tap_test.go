package tap

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// freeAddr — свободный порт на loopback.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freeAddr: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("freeAddr close: %v", err)
	}
	return addr
}

// record — запись провода [u16 длина][тело].
func wireRecord(body []byte) []byte {
	rec := make([]byte, 2+len(body))
	binary.LittleEndian.PutUint16(rec, uint16(len(body)+2))
	copy(rec[2:], body)
	return rec
}

// Полузакрытие: клиент FIN → ответ сервера доезжает, журнал полон.
func TestTapHalfClose(t *testing.T) {
	upLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("upstream: %v", err)
	}
	defer upLn.Close()
	upDone := make(chan error, 1)
	go func() {
		conn, err := upLn.Accept()
		if err != nil {
			upDone <- err
			return
		}
		defer conn.Close()
		// дочитать до EOF (FIN клиента сквозь тап), затем ответ и закрытие
		if _, err := io.Copy(io.Discard, conn); err != nil {
			upDone <- err
			return
		}
		if _, err := conn.Write(wireRecord([]byte("reply-from-server"))); err != nil {
			upDone <- err
			return
		}
		upDone <- nil
	}()

	listen := freeAddr(t)
	var journal bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- Run(ctx, Options{Maps: []Map{{Listen: listen, Upstream: upLn.Addr().String()}}, Log: &journal})
	}()

	conn := dialReady(t, listen)
	if _, err := conn.Write(wireRecord([]byte("ping"))); err != nil {
		t.Fatalf("write: %v", err)
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		if err := tcp.CloseWrite(); err != nil {
			t.Fatalf("CloseWrite: %v", err)
		}
	}
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Contains(got, []byte("reply-from-server")) {
		t.Fatalf("ответ сервера не дошел после FIN: %q", got)
	}
	select {
	case err := <-upDone:
		if err != nil {
			t.Fatalf("upstream: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upstream не завершился")
	}

	conn.Close()
	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run не завершился")
	}

	// журнал полон: data обеих ног + connClose
	jr := newJournalReader(bytes.NewReader(journal.Bytes()))
	var dataN, closeN int
	for {
		rec, err := jr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		switch rec.Type {
		case recData:
			dataN++
		case recConnClose:
			closeN++
		}
	}
	if dataN != 2 || closeN != 1 {
		t.Fatalf("записей data=%d close=%d; want 2/1", dataN, closeN)
	}
}

// Отмена контекста при живом соединении: Run возвращается, connClose записан,
// журнал читается.
func TestTapShutdown(t *testing.T) {
	upLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("upstream: %v", err)
	}
	defer upLn.Close()
	go func() {
		for {
			conn, err := upLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				_, _ = io.Copy(io.Discard, c)
				c.Close()
			}(conn)
		}
	}()

	listen := freeAddr(t)
	var journal bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- Run(ctx, Options{Maps: []Map{{Listen: listen, Upstream: upLn.Addr().String()}}, Log: &journal})
	}()

	conn := dialReady(t, listen)
	if _, err := conn.Write(wireRecord([]byte("idle"))); err != nil {
		t.Fatalf("write: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // data записана до отмены

	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run не завершился после отмены")
	}
	_ = conn.Close()

	jr := newJournalReader(bytes.NewReader(journal.Bytes()))
	sawClose := false
	for {
		rec, err := jr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if rec.Type == recConnClose {
			sawClose = true
		}
	}
	if !sawClose {
		t.Fatal("connClose не записан при отмене")
	}
}

// Недоступный upstream: соединение закрыто, connOpen+connClose в журнале.
func TestTapEvilUpstream(t *testing.T) {
	listen := freeAddr(t)
	var journal bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- Run(ctx, Options{Maps: []Map{{Listen: listen, Upstream: "127.0.0.1:1"}}, Log: &journal})
	}()

	conn := dialReady(t, listen)
	if _, err := io.ReadAll(conn); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read: %v", err)
	}
	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run не завершился")
	}

	if err := Decode(bytes.NewReader(journal.Bytes()), DecodeOptions{}); err != nil {
		t.Fatalf("Decode журнала злого upstream: %v", err)
	}
}

// dialReady ждёт готовности слушателя тапа (гонка старта Run).
func dialReady(t *testing.T, addr string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			return conn
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("слушатель %s не готов за 5с", addr)
	return nil
}
