package tap

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// buildJournal — журнал из записей заданными писателями.
func buildJournal(t *testing.T, write func(jw *journalWriter)) []byte {
	t.Helper()
	var buf bytes.Buffer
	jw := newJournalWriter(&buf)
	write(jw)
	if err := jw.flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	return buf.Bytes()
}

// gameRecord — запись провода game-кадра C→S.
func gameRecord(body []byte) []byte { return wireRecord(body) }

// Второй C→S кадр game-ноги до KeyPacket — hex-деградация, не паника
// (S8-раунд-1: nil-GameCrypt).
func TestDecodeGameFrameBeforeKeyPacketNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("паника: %v", r)
		}
	}()
	journal := buildJournal(t, func(jw *journalWriter) {
		_ = jw.connOpen(1, "a:1", "b:2", 0)
		_ = jw.data(recData, 1, DirCtoS, 1, gameRecord([]byte{0x00, 0xEA, 0x02, 0x00, 0x00})) // ProtocolVersion
		_ = jw.data(recData, 1, DirCtoS, 2, gameRecord([]byte{0x09}))                         // LOGOUT — до KeyPacket
	})
	var log bytes.Buffer
	if err := Decode(bytes.NewReader(journal), DecodeOptions{Log: &log}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !strings.Contains(log.String(), "→ LOGOUT hex=09") {
		t.Fatalf("ожидалась hex-деградация LOGOUT:\n%s", log.String())
	}
}

// Обрыв хвоста журнала — salvage: целые записи разбираются, фикстуры
// выгружаются, Decode не возвращает ошибку.
func TestDecodeSalvageTruncatedTail(t *testing.T) {
	journal := buildJournal(t, func(jw *journalWriter) {
		_ = jw.connOpen(1, "a:1", "b:2", 0)
		_ = jw.data(recData, 1, DirCtoS, 1, wireRecord([]byte{0x00, 0xEA, 0x02, 0x00, 0x00}))
		_ = jw.data(recData, 1, DirCtoS, 2, wireRecord([]byte{0x09})) // эта запись будет обрезана
	})
	truncated := journal[:len(journal)-3] // рвём тело последней записи

	var log, fixts bytes.Buffer
	if err := Decode(bytes.NewReader(truncated), DecodeOptions{Log: &log, Fixtures: &fixts}); err != nil {
		t.Fatalf("Decode: %v (ожидался salvage)", err)
	}
	if !strings.Contains(log.String(), "PROTOCOL_VERSION") {
		t.Fatalf("целая запись потеряна при salvage:\n%s", log.String())
	}
	if !strings.Contains(fixts.String(), "PROTOCOL_VERSION") {
		t.Fatal("фикстуры не выгружены при salvage")
	}
}

// data после connClose допускается (F14).
func TestDecodeDataAfterConnClose(t *testing.T) {
	journal := buildJournal(t, func(jw *journalWriter) {
		_ = jw.connOpen(1, "a:1", "b:2", 0)
		_ = jw.data(recData, 1, DirCtoS, 1, wireRecord([]byte{0x00, 0xEA, 0x02, 0x00, 0x00}))
		_ = jw.connClose(1, nil)
		_ = jw.data(recData, 1, DirCtoS, 2, wireRecord([]byte{0x00, 0xEA, 0x02, 0x00, 0x00}))
	})
	var log bytes.Buffer
	if err := Decode(bytes.NewReader(journal), DecodeOptions{Log: &log}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got := strings.Count(log.String(), "PROTOCOL_VERSION"); got != 2 {
		t.Fatalf("PROTOCOL_VERSION ×%d, want 2", got)
	}
}

// Чужой magic изолирован: валидная структура записей с инвертированным
// заголовком — ошибка именно заголовка (S8: мутация проверки magic выживала).
func TestJournalForeignMagicIsolated(t *testing.T) {
	journal := buildJournal(t, func(jw *journalWriter) {
		_ = jw.connOpen(1, "a", "b", 0)
	})
	journal[0] = 'X' // ломаем только magic, записи целы
	jr := newJournalReader(bytes.NewReader(journal))
	if _, err := jr.Next(); err == nil || !errors.Is(err, errBadHeader) {
		t.Fatalf("хотели errBadHeader, получили %v", err)
	}
	// Decode тоже обязан отказать: это чужой файл
	if err := Decode(bytes.NewReader(journal), DecodeOptions{}); err == nil || !errors.Is(err, errBadHeader) {
		t.Fatalf("Decode: хотели errBadHeader, получили %v", err)
	}
}

// Хвост ноги без полного кадра журналируется записью data (S8-минор).
func TestTapTailJournaled(t *testing.T) {
	var journal bytes.Buffer
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
				_, _ = io.Copy(io.Discard, c) // держим ногу открытой для хвоста
				c.Close()
			}(conn)
		}
	}()
	listen := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- Run(ctx, Options{Maps: []Map{{Listen: listen, Upstream: upLn.Addr().String()}}, Log: &journal})
	}()
	conn := dialReady(t, listen)
	if _, err := conn.Write([]byte{0x00, 0x05, 0x01}); err != nil { // «кадр» без полного тела
		t.Fatalf("write: %v", err)
	}
	_ = conn.Close()
	time.Sleep(200 * time.Millisecond) // нога успевает увидеть EOF и журналировать хвост
	cancel()
	<-runDone

	rd := newJournalReader(bytes.NewReader(journal.Bytes()))
	sawPartialData := false
	for {
		rec, err := rd.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if rec.Type == recData && len(rec.Bytes) == 3 {
			sawPartialData = true
		}
	}
	if !sawPartialData {
		t.Fatal("бескарровый хвост не журналирован")
	}
}
