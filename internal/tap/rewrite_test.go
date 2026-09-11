package tap

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/udisondev/l2go/internal/crypto"
	"github.com/udisondev/l2go/internal/protocol"
)

// rewriteRecord собирает запись провода из кадра.
func rewriteRecord(frame []byte) []byte {
	rec := make([]byte, 2+len(frame))
	binary.LittleEndian.PutUint16(rec, uint16(len(frame)+2))
	copy(rec[2:], frame)
	return rec
}

// initFrame — валидный static-кадр Init (по писателю P1.4).
func initFrame(t *testing.T) []byte {
	t.Helper()
	var payload [protocol.InitSize]byte
	mod := bytes.Repeat([]byte{0xAB}, 128)
	key := bytes.Repeat([]byte{0xCD}, 16)
	protocol.WriteInit(payload[:], 42, mod, key)
	frame := make([]byte, len(payload)+crypto.MaxFrameOverhead)
	lc := crypto.NewLoginCrypt()
	n, err := lc.EncryptInit(frame, payload[:], 77)
	if err != nil {
		t.Fatalf("EncryptInit: %v", err)
	}
	return frame[:n]
}

// Happy path: Init проходит, одиночный ServerList переписывается,
// остальные кадры нетронуты.
func TestRewriteServerList(t *testing.T) {
	rw := newLoginRewriter([4]byte{127, 0, 0, 1}, 7777)

	out, orig, err := rw.process(rewriteRecord(initFrame(t)))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if orig != nil {
		t.Fatal("Init не должен давать original")
	}
	if len(out) == 0 {
		t.Fatal("Init: пустой выход")
	}

	// ServerList с одним сервером на внешнем адресе
	servers := []protocol.ServerListEntry{{
		ID: 1, IP: [4]byte{37, 228, 91, 208}, Port: 7777,
		CurrentPlayers: 5, MaxPlayers: 100, ServerType: 1,
	}}
	payload := make([]byte, protocol.ServerListSize(servers, nil))
	protocol.WriteServerList(payload, servers, nil, 1)
	lc := crypto.NewLoginCrypt()
	if err := lc.SetKey(bytes.Repeat([]byte{0xCD}, 16)); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	frame := make([]byte, len(payload)+crypto.MaxFrameOverhead)
	n, err := lc.Encrypt(frame, payload)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	rec := rewriteRecord(frame[:n])

	out, orig, err = rw.process(rec)
	if err != nil {
		t.Fatalf("ServerList: %v", err)
	}
	if orig == nil {
		t.Fatal("rewrite не сработал: нет original")
	}
	if bytes.Equal(out, orig) {
		t.Fatal("переписанный кадр совпал с оригиналом")
	}
	// расшифровка переписанного кадра тем же ключом → адрес 127.0.0.1
	dec := make([]byte, len(out)-2)
	copy(dec, out[2:])
	if err := lc.Decrypt(dec); err != nil {
		t.Fatalf("расшифровка переписанного: %v", err)
	}
	v, ok := protocol.NewServerListView(dec)
	if !ok {
		t.Fatalf("переписанный ServerList не разбирается: %d Б", len(dec))
	}
	entry, ok := v.Server(0)
	if !ok || entry.IP != [4]byte{127, 0, 0, 1} || entry.Port != 7777 || entry.ID != 1 {
		t.Fatalf("переписанная запись: %+v", entry)
	}
}

// Мульти-сервер: rewrite обязан отказаться (skip), вернув ошибку.
func TestRewriteMultiServerSkip(t *testing.T) {
	rw := newLoginRewriter([4]byte{127, 0, 0, 1}, 7777)
	if _, _, err := rw.process(rewriteRecord(initFrame(t))); err != nil {
		t.Fatalf("Init: %v", err)
	}
	servers := []protocol.ServerListEntry{
		{ID: 1, IP: [4]byte{37, 228, 91, 208}, Port: 7777},
		{ID: 2, IP: [4]byte{37, 228, 91, 209}, Port: 7777},
	}
	payload := make([]byte, protocol.ServerListSize(servers, nil))
	protocol.WriteServerList(payload, servers, nil, 1)
	lc := crypto.NewLoginCrypt()
	if err := lc.SetKey(bytes.Repeat([]byte{0xCD}, 16)); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	frame := make([]byte, len(payload)+crypto.MaxFrameOverhead)
	n, err := lc.Encrypt(frame, payload)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, _, err := rw.process(rewriteRecord(frame[:n])); err == nil {
		t.Fatal("мульти-сервер: хотели отказ rewrite")
	}
}

// Битый ServerList (валидная крипто-обёртка, обрезанное тело) — ошибка, не
// паника; случайный мусор (не ServerList) проходит нетронутым.
func TestRewriteGarbageNoPanic(t *testing.T) {
	rw := newLoginRewriter([4]byte{127, 0, 0, 1}, 7777)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("паника: %v", r)
		}
	}()
	if _, _, err := rw.process(rewriteRecord(initFrame(t))); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// динамический ключ кадра = ключ Init (0xCD×16)
	lc := crypto.NewLoginCrypt()
	if err := lc.SetKey(bytes.Repeat([]byte{0xCD}, 16)); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	// «ServerList» с count=1 и обрезанным телом
	trunc := []byte{protocol.OpServerList, 1, 1}
	frame := make([]byte, len(trunc)+crypto.MaxFrameOverhead)
	n, err := lc.Encrypt(frame, trunc)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, _, err := rw.process(rewriteRecord(frame[:n])); err == nil {
		t.Fatal("обрезанный ServerList: хотели ошибку разбора")
	}
	// случайный мусор — не ServerList, проходит нетронутым
	garbage := bytes.Repeat([]byte{0x11}, 32)
	gframe := make([]byte, len(garbage)+crypto.MaxFrameOverhead)
	n, err = lc.Encrypt(gframe, garbage)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	out, orig, err := rw.process(rewriteRecord(gframe[:n]))
	if err != nil {
		t.Fatalf("мусорный кадр: %v", err)
	}
	if orig != nil || !bytes.Equal(out, rewriteRecord(gframe[:n])) {
		t.Fatal("мусорный кадр должен пройти нетронутым")
	}
}
