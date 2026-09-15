package encode

import (
	"bytes"
	"encoding/binary"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/l2go/internal/crypto"
)

// keyFixture — ключ сессии для тестов (рождается в conn, здесь — константа).
var keyFixture = [8]byte{1, 2, 3, 4, 5, 6, 7, 8}

// decoder — последовательный расшифровщик батча: счётчик каскада
// накапливается между кадрами, как у клиента.
type decoder struct {
	gc *crypto.GameCrypt
}

func newDecoder() decoder {
	gc := crypto.NewGameCrypt(keyFixture)
	gc.Enable()
	return decoder{gc: gc}
}

func (d decoder) plain(t *testing.T, slab []byte) []byte {
	t.Helper()
	body := bytes.Clone(slab[2:])
	if err := d.gc.Decrypt(body); err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	return body
}

func TestStageZeroCapRejected(t *testing.T) {
	t.Parallel()
	if _, err := NewStage(0); err == nil {
		t.Error("NewStage(0) = nil error; want отказ (нулевой кап запрещён)")
	}
}

// Крипто-граница: марка едет с кадром — plain до KeyPacket, crypt после;
// Take шифрует только тела с маркой, заголовок длины открыт.
func TestCryptoBoundaryFollowsMark(t *testing.T) {
	t.Parallel()
	s, err := NewStage(1 << 18)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Unregister(7)
	c := s.Register(7, keyFixture)

	plain := []byte{0x2e, 0x00, 0x00, 0x00}        // ProtocolVersion-подобный кадр
	secret := []byte{0x11, 0x22, 0x33, 0x44, 0x55} // кадр после KeyPacket
	s.Push(7, plain, false)
	s.Push(7, secret, true)

	batch, closing := c.Take(nil)
	if closing {
		t.Fatal("close = true; want false")
	}
	if len(batch) != 2 {
		t.Fatalf("len(batch) = %d; want 2", len(batch))
	}
	if want := plain; !bytes.Equal(batch[0][2:], want) {
		t.Errorf("plain-кадр изменён криптой: % x; want % x", batch[0][2:], want)
	}
	dec := newDecoder()
	if got := dec.plain(t, batch[1]); !bytes.Equal(got, secret) {
		t.Errorf("crypt-кадр расшифровывается в % x; want % x", got, secret)
	}
	if n := binary.LittleEndian.Uint16(batch[1][:2]); int(n) != 2+len(secret) {
		t.Errorf("заголовок длины = %d; want %d", n, 2+len(secret))
	}
}

// Покадровый криптоблок: батч из нескольких криптованных кадров — каждый
// расшифровывается независимо (счётчик и каскад обновляются на кадр).
func TestPerFrameCryptoBatch(t *testing.T) {
	t.Parallel()
	s, err := NewStage(1 << 18)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Unregister(1)
	c := s.Register(1, keyFixture)

	frames := [][]byte{
		{0x0b, 0x00, 0x00, 0x00},
		bytes.Repeat([]byte{0xaa}, 300),
		bytes.Repeat([]byte{0xbb}, 1454),
		{0x49, 0x01},
	}
	for _, f := range frames {
		s.Push(1, f, true)
	}
	batch, _ := c.Take(nil)
	if len(batch) != len(frames) {
		t.Fatalf("len(batch) = %d; want %d", len(batch), len(frames))
	}
	dec := newDecoder()
	for i, want := range frames {
		if got := dec.plain(t, batch[i]); !bytes.Equal(got, want) {
			t.Errorf("кадр %d: расшифрован % x; want % x", i, got, want)
		}
	}
}

// FIFO: порядок кадров одного клиента не перемешивается.
func TestFIFOOrder(t *testing.T) {
	t.Parallel()
	s, _ := NewStage(1 << 18)
	defer s.Unregister(2)
	c := s.Register(2, keyFixture)
	for i := byte(0); i < 8; i++ {
		s.Push(2, []byte{0xa0, i}, false)
	}
	batch, _ := c.Take(nil)
	if len(batch) != 8 {
		t.Fatalf("len(batch) = %d; want 8", len(batch))
	}
	for i := byte(0); i < 8; i++ {
		if got := batch[i][2:]; !bytes.Equal(got, []byte{0xa0, i}) {
			t.Errorf("batch[%d] = % x; want % x", i, got, []byte{0xa0, i})
		}
	}
}

// Кап байтов: push сверх капа не встаёт в очередь, Take возвращает ActClose.
func TestByteCapDisconnect(t *testing.T) {
	t.Parallel()
	s, _ := NewStage(64)
	defer s.Unregister(3)
	c := s.Register(3, keyFixture)

	s.Push(3, bytes.Repeat([]byte{1}, 16), false) // 18 байт провода
	s.Push(3, bytes.Repeat([]byte{2}, 16), false) // ещё 18 — влезает
	s.Push(3, bytes.Repeat([]byte{3}, 40), false) // 42 — сверх капа 64

	batch, closing := c.Take(nil)
	if len(batch) != 2 {
		t.Fatalf("len(batch) = %d; want 2 (третий push не встал)", len(batch))
	}
	if !closing {
		t.Error("close = false; want true")
	}
	if st := s.Stats(); st.CapDrops != 1 {
		t.Errorf("CapDrops = %d; want 1", st.CapDrops)
	}
}

// Close-after-flush: стоящие кадры выдаются, сокет рвётся после флеша.
func TestCloseAfterFlush(t *testing.T) {
	t.Parallel()
	s, _ := NewStage(1 << 16)
	defer s.Unregister(4)
	c := s.Register(4, keyFixture)
	s.Push(4, []byte{0x01, 0x02}, false)
	s.Close(4)
	batch, closing := c.Take(nil)
	if len(batch) != 1 || !closing {
		t.Fatalf("Take после Close: %d кадров, closing %v; want 1, true", len(batch), closing)
	}
	again, closing := c.Take(batch)
	if len(again) != 0 || !closing {
		t.Fatalf("пустой Take: %d кадров, closing %v; want 0, true", len(again), closing)
	}
}

// Паркинг: пустой Take спит до push (или close), не поллит.
func TestTakeParksUntilPush(t *testing.T) {
	t.Parallel()
	s, _ := NewStage(1 << 16)
	defer s.Unregister(5)
	c := s.Register(5, keyFixture)

	got := make(chan [][]byte, 1)
	go func() {
		batch, _ := c.Take(nil)
		got <- batch
	}()
	select {
	case b := <-got:
		t.Fatalf("Take вернулся без push: %v", b)
	case <-time.After(50 * time.Millisecond):
	}
	s.Push(5, []byte{0x99}, false)
	select {
	case b := <-got:
		if len(b) != 1 || b[0][2] != 0x99 {
			t.Errorf("после push Take вернул %v", b)
		}
	case <-time.After(time.Second):
		t.Fatal("push не разбудил паркинг Take")
	}
}

// Возврат слэбов обменом при Take: в стационарном цикле адреса слэбов
// берутся из первых двух аллокаций (ping-pong двух экземпляров), свежих
// аллокаций нет.
func TestSlabReuse(t *testing.T) {
	t.Parallel()
	s, _ := NewStage(1 << 16)
	defer s.Unregister(6)
	c := s.Register(6, keyFixture)

	var batch [][]byte
	seen := make(map[*byte]bool)
	for i := 0; i < 6; i++ {
		s.Push(6, bytes.Repeat([]byte{byte(i)}, 40), false)
		next, _ := c.Take(batch)
		if len(next) != 1 {
			t.Fatalf("итерация %d: len(next) = %d; want 1", i, len(next))
		}
		seen[&next[0][0]] = true
		batch = next
	}
	if len(seen) > 2 {
		t.Errorf("уникальных слэбов %d; want ≤2 (стационарный ping-pong)", len(seen))
	}
}

// Unknown id: Push/Close на ушедшего — no-op, паники нет.
func TestUnknownClientNoop(t *testing.T) {
	t.Parallel()
	s, _ := NewStage(1 << 12)
	s.Push(999, []byte{1}, false) // не зарегистрирован
	s.Close(999)
	s.Unregister(999)
	s.Push(999, []byte{1}, false) // снят
	if st := s.Stats(); st.Pushed != 0 || st.Clients != 0 {
		t.Errorf("Stats = %+v; want пустая стадия", st)
	}
}

// Стресс: параллельные Push (шлюз/мир) против единственного Take под -race —
// порядок каждого батча FIFO, потерь нет.
func TestConcurrentPushSingleTake(t *testing.T) {
	s, _ := NewStage(1 << 22)
	defer s.Unregister(8)
	c := s.Register(8, keyFixture)

	const senders, perSender = 4, 256
	var wg sync.WaitGroup
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < perSender; j++ {
				s.Push(8, []byte{byte(i), byte(j)}, false)
			}
		}(i)
	}

	seen := make(map[[2]byte]int)
	var batch [][]byte
	for total := 0; total < senders*perSender; {
		next, closing := c.Take(batch)
		if closing {
			t.Fatal("close под стрессом: кап не должен срабатывать")
		}
		for _, slab := range next {
			seen[[2]byte{slab[2], slab[3]}]++
		}
		total += len(next)
		batch = next
	}
	wg.Wait()
	for i := 0; i < senders; i++ {
		for j := 0; j < perSender; j++ {
			if seen[[2]byte{byte(i), byte(j)}] != 1 {
				t.Errorf("кадр (%d,%d): %d вхождений; want 1", i, j, seen[[2]byte{byte(i), byte(j)}])
			}
		}
	}
}
