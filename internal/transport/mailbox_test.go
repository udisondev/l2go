package transport

import (
	"encoding/binary"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// testSeq кодирует порядковый номер отправителя в payload — модельный счётчик FIFO.
func testSeq(seq uint64) []byte {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], seq)
	return b[:]
}

func seqOf(env Envelope) uint64 { return binary.LittleEndian.Uint64(env.Payload) }

func newClaimedBox(t *testing.T, fafCap int) (*Mailbox, uint64) {
	t.Helper()
	box := &Mailbox{}
	r := NewRegistry(fafCap)
	r.Register(box)
	const token = 42
	if err := box.Claim(token); err != nil {
		t.Fatalf("Claim(%d) = %v; want nil", token, err)
	}
	return box, token
}

func TestMailboxFIFOExtractOnce(t *testing.T) {
	box, token := newClaimedBox(t, 8)
	for i := uint64(1); i <= 5; i++ {
		box.enqueue(Envelope{FromID: 7, Kind: KindClientFrame, Payload: testSeq(i)})
	}
	got := box.Extract(token)
	if len(got) != 5 {
		t.Fatalf("Extract = %d писем; want 5", len(got))
	}
	for i, env := range got {
		if want := uint64(i + 1); seqOf(env) != want {
			t.Errorf("порядок нарушен: письмо %d имеет seq %d; want %d", i, seqOf(env), want)
		}
	}
	if again := box.Extract(token); len(again) != 0 {
		t.Errorf("повторное изъятие вернуло %d писем; want 0 (одно изъятие на письмо)", len(again))
	}
	if d := box.Depth(); d != 0 {
		t.Errorf("глубина после дрейна = %d; want 0", d)
	}
}

func TestMailboxClaimExclusive(t *testing.T) {
	box, token := newClaimedBox(t, 8)
	if err := box.Claim(99); !errors.Is(err, ErrBusy) {
		t.Errorf("второй Claim = %v; want ErrBusy", err)
	}
	// вытеснения нет: нулевой токен не захват, а ошибка контракта
	if err := box.Claim(0); !errors.Is(err, ErrZeroToken) {
		t.Errorf("Claim(0) = %v; want ErrZeroToken", err)
	}
	box.Release(token)
	if err := box.Claim(7); err != nil {
		t.Errorf("Claim после Release = %v; want nil", err)
	}
}

func TestMailboxReaderContractPanic(t *testing.T) {
	box, _ := newClaimedBox(t, 8)
	defer func() {
		if recover() == nil {
			t.Errorf("изъятие чужим токеном обязано паниковать (контракт читателя)")
		}
	}()
	box.Extract(999)
}

func TestFAFCapDropNew(t *testing.T) {
	const capFAF = 4
	box, token := newClaimedBox(t, capFAF)
	for i := 0; i < capFAF+5; i++ {
		box.enqueue(Envelope{FromID: 1, Kind: KindClientFrame, Payload: testSeq(uint64(i))})
	}
	got := box.Extract(token)
	if len(got) != capFAF {
		t.Fatalf("FAF сквозь кап: доставлено %d; want %d (дропнуты новые)", len(got), capFAF)
	}
	st := box.Stats()
	if st.DroppedFAF != 5 {
		t.Errorf("DroppedFAF = %d; want 5", st.DroppedFAF)
	}
	// reliable кап не подчиняется: живому не дропается никогда
	for i := 0; i < capFAF+10; i++ {
		box.enqueue(Envelope{FromID: 1, Kind: KindAggro, Payload: testSeq(uint64(i))})
	}
	if got := box.Extract(token); len(got) != capFAF+10 {
		t.Errorf("reliable сквозь кап: доставлено %d; want %d", len(got), capFAF+10)
	}
}

func TestFinalDropClasses(t *testing.T) {
	box, token := newClaimedBox(t, 8)
	box.enqueue(Envelope{FromID: 1, Kind: KindAggro}) // останется в ящике к деспавну
	box.Despawn(token)

	// поздние страгглеры — классовый дроп, не тихая потеря
	box.enqueue(Envelope{FromID: 2, Kind: KindBroadcastState})
	box.enqueue(Envelope{FromID: 2, Kind: KindXP})
	box.enqueue(Envelope{FromID: 2, Kind: KindReserve})

	st := box.Stats()
	if !st.Dead {
		t.Fatalf("ящик не мёртв после Despawn")
	}
	if st.FinalReliable != 2 { // доспавн-остаток Aggro + страгглер XP
		t.Errorf("FinalReliable = %d; want 2", st.FinalReliable)
	}
	if st.FinalFireAndForget != 1 {
		t.Errorf("FinalFireAndForget = %d; want 1", st.FinalFireAndForget)
	}
	if st.FinalTransfer != 1 {
		t.Errorf("FinalTransfer = %d; want 1", st.FinalTransfer)
	}
}

func TestUnappliedRemainderDrop(t *testing.T) {
	box, token := newClaimedBox(t, 8)
	for i := 0; i < 4; i++ {
		box.enqueue(Envelope{FromID: 1, Kind: KindAggro, Payload: testSeq(uint64(i))})
	}
	batch := box.Extract(token)
	// применены первые два, регион деспавнится: остаток пачки — классовый дроп
	applied := batch[:2]
	box.DropBatch(batch[len(applied):])
	box.Despawn(token)
	st := box.Stats()
	if st.FinalReliable != 2 {
		t.Errorf("FinalReliable = %d; want 2 (неприменённый остаток класс-дропнут)", st.FinalReliable)
	}
}

func TestSegmentCollectionBelowMark(t *testing.T) {
	box, token := newClaimedBox(t, 8)
	const total = 2*segCap + 3
	for i := 0; i < total; i++ {
		box.enqueue(Envelope{FromID: 1, Kind: KindAggro, Payload: testSeq(uint64(i))})
	}
	if got := len(box.Extract(token)); got != total {
		t.Fatalf("Extract = %d; want %d", got, total)
	}
	// белый ящик: сбор продвинул голову к хвостовому (частично заполненному) сегменту
	if box.head == nil || box.head.next.Load() != nil {
		t.Fatalf("голова не на хвостовом сегменте после сбора ниже знака")
	}
	if int(box.head.cnt.Load()) != box.headOff {
		t.Fatalf("headOff = %d; want %d (всё изъято)", box.headOff, box.head.cnt.Load())
	}
}

func TestNotifyToken(t *testing.T) {
	box, token := newClaimedBox(t, 8)
	if n := len(box.Notify()); n != 0 {
		t.Fatalf("токен до отправки: в канале %d", n)
	}
	box.enqueue(Envelope{FromID: 1, Kind: KindClientFrame})
	if n := len(box.Notify()); n != 1 {
		t.Fatalf("переход пусто→непусто не дал токена: %d", n)
	}
	// повторная отправка не плодит токени (cap-1)
	box.enqueue(Envelope{FromID: 1, Kind: KindClientFrame})
	if n := len(box.Notify()); n != 1 {
		t.Errorf("в канале %d токенов; want 1 (cap-1)", n)
	}
	box.Extract(token)
	box.AckNotify()
	if n := len(box.Notify()); n != 0 {
		t.Fatalf("AckNotify не сбросил токен: %d", n)
	}
	// после сброса новое пусто→непусто снова будит
	box.enqueue(Envelope{FromID: 1, Kind: KindClientFrame})
	select {
	case <-box.Notify():
	default:
		t.Fatalf("токен потерян после AckNotify")
	}
	// деспавн будит
	box.Extract(token)
	box.AckNotify()
	box.Despawn(token)
	select {
	case <-box.Notify():
	default:
		t.Fatalf("деспавн не дал токена")
	}
}

func TestNotifyNoLostWakeup(t *testing.T) {
	box, _ := newClaimedBox(t, 64)
	const producers, perProducer = 6, 300
	var sent atomic.Int64
	done := make(chan struct{})
	for p := 0; p < producers; p++ {
		go func() {
			for i := 0; i < perProducer; i++ {
				box.enqueue(Envelope{FromID: 1, Kind: KindClientFrame})
				sent.Add(1)
				if i%7 == 0 {
					time.Sleep(time.Microsecond)
				}
			}
			done <- struct{}{}
		}()
	}
	var received atomic.Int64
	deadline := time.After(5 * time.Second)
reading:
	for {
		select {
		case <-box.Notify():
		case <-deadline:
			break reading
		}
		for {
			batch := box.Extract(42)
			if len(batch) == 0 {
				box.AckNotify()
				// перечитать после сброса: гонка письмо-в-окне-дрена
				if batch = box.Extract(42); len(batch) == 0 {
					break
				}
			}
			received.Add(int64(len(batch)))
			if received.Load() == int64(producers*perProducer) {
				break reading
			}
		}
	}
	for p := 0; p < producers; p++ {
		<-done
	}
	if got, want := received.Load(), int64(producers*perProducer); got != want {
		t.Fatalf("получено %d из %d: потерянное пробуждение (без тикера)", got, want)
	}
}

func TestMailboxStatsDepth(t *testing.T) {
	box, _ := newClaimedBox(t, 8)
	for i := 0; i < 3; i++ {
		box.enqueue(Envelope{FromID: 1, Kind: KindAggro})
	}
	if d := box.Depth(); d != 3 {
		t.Errorf("Depth = %d; want 3", d)
	}
	if hw := box.Stats().HighWater; hw < 3 {
		t.Errorf("HighWater = %d; want >= 3", hw)
	}
}

// Ленивый первый сегмент: пустой ящик не платит за сегмент (бюджет §11).
func TestMailboxLazyFirstSegment(t *testing.T) {
	box, _ := newClaimedBox(t, 8)
	if box.tail != nil || box.start.Load() != nil {
		t.Fatalf("пустой ящик аллоцировал сегмент при рождении")
	}
	if got := box.Extract(42); len(got) != 0 {
		t.Fatalf("изъятие из пустого с рождения ящика = %d; want 0", len(got))
	}
	box.enqueue(Envelope{FromID: 1, Kind: KindAggro})
	if box.start.Load() == nil {
		t.Fatalf("первое письмо не опубликовало первый сегмент")
	}
	if got := box.Extract(42); len(got) != 1 {
		t.Fatalf("изъятие после первого письма = %d; want 1", len(got))
	}
}
