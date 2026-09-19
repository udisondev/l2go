package tap

import (
	"sync"

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
func buildJournal(t *testing.T, write func(jw *JournalWriter)) []byte {
	t.Helper()
	var buf bytes.Buffer
	jw := NewJournalWriter(&buf)
	write(jw)
	if err := jw.Flush(); err != nil {
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
	journal := buildJournal(t, func(jw *JournalWriter) {
		_ = jw.ConnOpen(1, "a:1", "b:2", 0)
		_ = jw.writeData(recData, 1, DirCtoS, 1, gameRecord([]byte{0x00, 0xEA, 0x02, 0x00, 0x00})) // ProtocolVersion
		_ = jw.writeData(recData, 1, DirCtoS, 2, gameRecord([]byte{0x09}))                         // LOGOUT — до KeyPacket
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
	journal := buildJournal(t, func(jw *JournalWriter) {
		_ = jw.ConnOpen(1, "a:1", "b:2", 0)
		_ = jw.writeData(recData, 1, DirCtoS, 1, wireRecord([]byte{0x00, 0xEA, 0x02, 0x00, 0x00}))
		_ = jw.writeData(recData, 1, DirCtoS, 2, wireRecord([]byte{0x09})) // эта запись будет обрезана
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
	journal := buildJournal(t, func(jw *JournalWriter) {
		_ = jw.ConnOpen(1, "a:1", "b:2", 0)
		_ = jw.writeData(recData, 1, DirCtoS, 1, wireRecord([]byte{0x00, 0xEA, 0x02, 0x00, 0x00}))
		_ = jw.ConnClose(1, nil)
		_ = jw.writeData(recData, 1, DirCtoS, 2, wireRecord([]byte{0x00, 0xEA, 0x02, 0x00, 0x00}))
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
	journal := buildJournal(t, func(jw *JournalWriter) {
		_ = jw.ConnOpen(1, "a", "b", 0)
	})
	journal[0] = 'X' // ломаем только magic, записи целы
	jr := NewJournalReader(bytes.NewReader(journal))
	if _, err := jr.Next(); err == nil || !errors.Is(err, errBadHeader) {
		t.Fatalf("хотели errBadHeader, получили %v", err)
	}
	// Decode тоже обязан отказать: это чужой файл
	if err := Decode(bytes.NewReader(journal), DecodeOptions{}); err == nil || !errors.Is(err, errBadHeader) {
		t.Fatalf("Decode: хотели errBadHeader, получили %v", err)
	}
}

// lockedBuffer — журнал-приёмник: Run пишет из своей горутины, тест
// поллит прогресс через снимок bytes() под мьютексом (без мьютекса —
// гонка bytes.Buffer из чужой горутины; атомики не нужны).
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *lockedBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// bytes возвращает снимок содержимого (после quiesce).
func (w *lockedBuffer) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buf.Bytes()...)
}

// journalHasTailData — в снимке журнала есть целая data-запись с 3-байтовым
// телом (хвост без полного кадра).
func journalHasTailData(t *testing.T, snapshot []byte) bool {
	t.Helper()
	rd := NewJournalReader(bytes.NewReader(snapshot))
	for {
		rec, err := rd.Next()
		if errors.Is(err, io.EOF) {
			return false
		}
		if err != nil {
			return false // полу-запись: ждём дальше
		}
		if rec.Type == recData && len(rec.Bytes) == 3 {
			return true
		}
	}
}

// journalHasData — в снимке есть целая data-запись любого размера.
func journalHasData(snapshot []byte) bool {
	rd := NewJournalReader(bytes.NewReader(snapshot))
	for {
		rec, err := rd.Next()
		if err != nil {
			return false
		}
		if rec.Type == recData {
			return true
		}
	}
}

// Хвост ноги без полного кадра журналируется записью data (S8-минор).
func TestTapTailJournaled(t *testing.T) {
	var journal lockedBuffer
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
	ctx, cancel := context.WithCancel(t.Context())
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
	// Нога журналирует хвост после EOF: ждём ПОЛНУЮ data-запись (парсинг
	// среза журнала в каждой итерации — запись из двух Write не должна
	// быть оборвана отменой), не сон.
	deadline := time.Now().Add(2 * time.Second)
	for !journalHasTailData(t, journal.bytes()) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Run не завершился после отмены за 5с")
	}

	if !journalHasTailData(t, journal.bytes()) {
		t.Fatal("бескарровый хвост не журналирован")
	}
}
