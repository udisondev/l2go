package loginlink

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionsPutConsume(t *testing.T) {
	t.Parallel()

	s := NewSessions(time.Minute)
	if err := s.Put("sergei", 1, 2); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !s.Consume("sergei", 1, 2, 0, 0) {
		t.Fatal("Consume(без playOk): сессия без playOk-пары должна валидироваться нулями")
	}
	if s.Len() != 0 {
		t.Fatalf("Len после изъятия = %d; want 0", s.Len())
	}
	if s.Consume("sergei", 1, 2, 0, 0) {
		t.Fatal("повторное Consume теми же ключами: want false (replay-защита)")
	}
}

func TestSessionsPutWithPlayKeys(t *testing.T) {
	t.Parallel()

	s := NewSessions(time.Minute)
	s.Put("a", 1, 2)
	if !s.SetPlayKeys("a", 3, 4) {
		t.Fatal("SetPlayKeys: want true")
	}
	if !s.Consume("a", 1, 2, 3, 4) {
		t.Fatal("Consume(4 ключа): want true")
	}
	if s.SetPlayKeys("a", 5, 6) {
		t.Fatal("SetPlayKeys после изъятия: want false — сессии нет")
	}
}

func TestSessionsAccountInUseReplaces(t *testing.T) {
	t.Parallel()

	s := NewSessions(time.Minute)
	if err := s.Put("sergei", 1, 2); err != nil {
		t.Fatalf("Put: %v", err)
	}
	err := s.Put("sergei", 3, 4)
	if !errors.Is(err, ErrAccountInUse) {
		t.Fatalf("второй Put: err = %v; want ErrAccountInUse", err)
	}
	if s.Len() != 0 {
		t.Fatalf("Len после busy-Put = %d; want 0 (старая сессия удалена)", s.Len())
	}
	// Замена со следующей попытки (канон ALREADY_ON_LS).
	if err := s.Put("sergei", 5, 6); err != nil {
		t.Fatalf("Put после busy: %v; want nil", err)
	}
}

func TestSessionsCheckLoginPair(t *testing.T) {
	t.Parallel()

	s := NewSessions(time.Minute)
	s.Put("sergei", 1, 2)
	if !s.CheckLoginPair("sergei", 1, 2) {
		t.Fatal("CheckLoginPair(верная пара): want true")
	}
	if s.CheckLoginPair("sergei", 1, 9) {
		t.Fatal("CheckLoginPair(неверная): want false")
	}
	if s.CheckLoginPair("ghost", 1, 2) {
		t.Fatal("CheckLoginPair(нет сессии): want false")
	}
	if s.Len() != 1 {
		t.Fatalf("CheckLoginPair не изымает: Len = %d; want 1", s.Len())
	}
}

func TestSessionsTTL(t *testing.T) {
	t.Parallel()

	s := NewSessions(time.Minute)
	now := time.Now()
	s.now = func() time.Time { return now }
	s.Put("sergei", 1, 2)
	now = now.Add(2 * time.Minute) // просрочена
	if s.CheckLoginPair("sergei", 1, 2) {
		t.Fatal("просроченная сессия: want false")
	}
	if s.Len() != 0 {
		t.Fatalf("Len после TTL = %d; want 0 (ленивое удаление)", s.Len())
	}
}

func TestSessionsNormalization(t *testing.T) {
	t.Parallel()

	s := NewSessions(time.Minute)
	if err := s.Put("SerGei", 1, 2); err != nil {
		t.Fatalf("Put(SerGei): %v", err)
	}
	if !s.SetPlayKeys("SERGEI", 3, 4) {
		t.Fatal("SetPlayKeys(SERGEI): want true — ключ стора нормализован")
	}
	if !s.Consume("sergei", 1, 2, 3, 4) {
		t.Fatal("Consume(sergei): want true")
	}
}

func TestSessionsConsumeAtomic(t *testing.T) {
	t.Parallel()

	// Двое одновременно Consume одними ключами → ровно один ok (атомарность
	// check-and-delete под одним локом).
	s := NewSessions(time.Minute)
	s.Put("sergei", 1, 2)
	s.SetPlayKeys("sergei", 3, 4)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if s.Consume("sergei", 1, 2, 3, 4) {
				wins.Add(1)
			}
		})
	}
	wg.Wait()
	if got := wins.Load(); got != 1 {
		t.Fatalf("Consume выиграли %d горутин; want 1", got)
	}
}

func TestSessionsParallelDoublePut(t *testing.T) {
	t.Parallel()

	// Параллельные двойные LoginOk одного аккаунта: ровно один Put успешен,
	// проигравший вытесняет сессию (canon ALREADY_ON_LS) — двух валидируемых
	// сессий не остаётся никогда.
	s := NewSessions(time.Minute)
	var ok, busy atomic.Int32
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if err := s.Put("sergei", 1, 2); err == nil {
				ok.Add(1)
			} else if errors.Is(err, ErrAccountInUse) {
				busy.Add(1)
			}
		})
	}
	wg.Wait()
	if ok.Load() != 1 || busy.Load() != 1 {
		t.Fatalf("Put: ok=%d busy=%d; want 1/1", ok.Load(), busy.Load())
	}
	if s.Len() != 0 {
		t.Fatalf("Len = %d; want 0 (busy-путь удаляет сессию)", s.Len())
	}
}

func TestSessionsRace(t *testing.T) {
	t.Parallel()

	// Стресс под -race: параллельные Put/CheckLoginPair/SetPlayKeys/Consume
	// по пересекающимся аккаунтам.
	s := NewSessions(time.Minute)
	var wg sync.WaitGroup
	for i := range 8 {
		account := string(rune('a' + i%4))
		wg.Go(func() { s.Put(account, int32(i), int32(i+1)) })
		wg.Go(func() { s.CheckLoginPair(account, int32(i), int32(i+1)) })
		wg.Go(func() { s.SetPlayKeys(account, int32(i), int32(i+2)) })
		wg.Go(func() { s.Consume(account, int32(i), int32(i+1), int32(i), int32(i+2)) })
	}
	wg.Wait()
}
