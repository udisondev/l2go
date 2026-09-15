package transport

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// И5: порядок писем одного отправителя FIFO, дублей/потерь нет (модельный подсчёт).
// Контрольные письма (Regional) идут в тот же ящик тем же порядком — одна FIFO.
func TestStressFIFOOrder(t *testing.T) {
	const senders, perSender = 8, 512
	const controlExtra = perSender + perSender/64 // EnterWorld каждое + LinkDead каждое 64-е
	r := NewRegistry(4096)
	box := &Mailbox{}
	r.Register(box)
	const token = 1
	if err := box.Claim(token); err != nil {
		t.Fatal(err)
	}
	var doneCnt atomic.Int32
	var wg sync.WaitGroup
	for s := range senders {
		s := EntityID(s + 1)
		wg.Go(func() {
			for i := range uint64(perSender) {
				r.Send(Envelope{To: Addr{Entity: 1}, FromID: s, Kind: KindAggro,
					Payload: testSeq(i)})
			}
			doneCnt.Add(1)
		})
	}
	wg.Go(func() {
		c := uint64(0) // свой монотонный счётчик отправителя
		for i := range uint64(perSender) {
			r.Send(Envelope{To: Addr{Entity: 1}, FromID: 99, Kind: KindEnterWorld,
				Payload: testSeq(c)})
			c++
			if i%64 == 0 {
				r.Send(Envelope{To: Addr{Entity: 1}, FromID: 99, Kind: KindLinkDead,
					Payload: testSeq(c)})
				c++
			}
		}
		doneCnt.Add(1)
	})
	last := make(map[EntityID]uint64) // только горутина читателя
	check := func(envs []Envelope) {
		for _, env := range envs {
			if prev, ok := last[env.FromID]; ok && seqOf(env) <= prev {
				t.Fatalf("FIFO отправителя %d нарушен: seq %d после %d", env.FromID, seqOf(env), prev)
			}
			last[env.FromID] = seqOf(env)
		}
	}
	total := 0
	for doneCnt.Load() < senders+1 {
		if batch := box.Extract(token); len(batch) > 0 {
			check(batch)
			total += len(batch)
			continue
		}
		runtime.Gosched()
	}
	wg.Wait()
	for {
		batch := box.Extract(token)
		if len(batch) == 0 {
			break
		}
		check(batch)
		total += len(batch)
	}
	if want := senders*perSender + controlExtra; total != want {
		t.Fatalf("итого %d из %d: потери/дубли", total, want)
	}
}

// И2: конкуренция претендентов — читатель ровно один или ноль.
func TestStressCompetingClaims(t *testing.T) {
	const contenders = 32
	r := NewRegistry(8)
	box := &Mailbox{}
	r.Register(box)
	var wins atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range contenders {
		wg.Go(func() {
			<-start
			if err := box.Claim(uint64(i + 1)); err == nil {
				wins.Add(1)
			}
		})
	}
	close(start)
	wg.Wait()
	if got := wins.Load(); got != 1 {
		t.Fatalf("успешных претензий %d; want 1 (ровно один читатель)", got)
	}
}

// И2/И4: миграция читателя на шве при активных отправителях — exactly-once
// по объединении {пачка ∪ порции после возобновления}.
func TestStressReaderMigrationActiveProducers(t *testing.T) {
	const senders, perSender = 6, 400
	r := NewRegistry(4096)
	box := &Mailbox{}
	r.Register(box)
	var sent atomic.Int64
	var wgSenders sync.WaitGroup
	for s := range senders {
		wgSenders.Go(func() {
			for i := range perSender {
				seq := uint64(s*perSender + i) // s — уникальный префикс отправителя
				r.Send(Envelope{To: Addr{Entity: 1}, FromID: 1, Kind: KindAggro,
					Payload: testSeq(seq)})
				sent.Add(1)
				if seq%13 == 0 {
					time.Sleep(50 * time.Microsecond) // отправители живы в окнах миграции
				}
			}
		})
	}
	seen := make(map[uint64]bool) // модель уникальности: только горутина читателя
	applied := 0
	apply := func(envs []Envelope) {
		for _, env := range envs {
			if seen[seqOf(env)] {
				t.Errorf("дубль письма seq=%d (И4)", seqOf(env))
			}
			seen[seqOf(env)] = true
			applied++
		}
	}
	const tok1, tok2 = 11, 22
	if err := box.Claim(tok1); err != nil {
		t.Fatal(err)
	}
	migrations := 0
	for range 40 {
		batch := box.Extract(tok1)
		if len(batch) == 0 {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		half := len(batch) / 2
		apply(batch[:half])
		unapplied := batch[half:] // недообработанная пачка едет «чемоданом»
		box.Release(tok1)
		if err := box.Claim(tok2); err != nil {
			t.Fatal(err)
		}
		apply(unapplied)
		box.Release(tok2)
		if err := box.Claim(tok1); err != nil {
			t.Fatal(err)
		}
		migrations++
	}
	if migrations == 0 {
		t.Fatalf("стресс миграции прошёл без единой миграции с непустой пачкой")
	}
	wgSenders.Wait()
	for {
		batch := box.Extract(tok1)
		if len(batch) == 0 {
			break
		}
		apply(batch)
	}
	if want := senders * perSender; applied != want {
		t.Fatalf("применено %d из %d: потери/дубли в окне миграции", applied, want)
	}
}

// ADR-0003 §16: компакция карты в стресс-режиме — рождения ∥ деспавны ∥ отправчики;
// модель: каждое отправленное письмо либо доставлено, либо класс-дропнуто с метрикой.
func TestStressRegistryBirthsRetiresSenders(t *testing.T) {
	const spawners, rounds = 4, 50
	const senders = 8
	const lettersPerSender = 2000
	r := NewRegistry(512)

	var liveMu sync.Mutex
	live := make(map[EntityID]*Mailbox)
	var deadMu sync.Mutex
	var deadList []*Mailbox

	var delivered, sent atomic.Int64
	var wgSpawners, wgSenders sync.WaitGroup

	for s := range spawners {
		token := uint64(100 + s)
		wgSpawners.Go(func() {
			var own int64
			for range rounds {
				box := &Mailbox{}
				id := r.Register(box)
				if err := box.Claim(token); err != nil {
					t.Errorf("рождение %d: Claim: %v", id, err)
					return
				}
				liveMu.Lock()
				live[id] = box
				liveMu.Unlock()
				time.Sleep(300 * time.Microsecond)
				own += int64(len(box.Extract(token))) // дрен своего ящика в окне жизни
				box.Despawn(token)
				r.Retire(id)
				liveMu.Lock()
				delete(live, id)
				liveMu.Unlock()
				deadMu.Lock()
				deadList = append(deadList, box)
				deadMu.Unlock()
			}
			delivered.Add(own)
		})
	}
	for range senders {
		wgSenders.Go(func() {
			for i := range lettersPerSender {
				liveMu.Lock()
				n := len(live)
				if n == 0 {
					liveMu.Unlock()
					continue
				}
				// детерминированный выбор без копии карты: i-й по счёту ключ
				var id EntityID
				k := i % n
				for key := range live {
					if k == 0 {
						id = key
						break
					}
					k--
				}
				liveMu.Unlock()
				r.Send(Envelope{To: Addr{Entity: id}, FromID: 1, Kind: KindAggro})
				sent.Add(1)
			}
		})
	}
	wgSenders.Wait()
	wgSpawners.Wait()

	var finals int64
	for _, b := range deadList {
		st := b.Stats()
		finals += st.FinalReliable + st.FinalFireAndForget + st.FinalTransfer
	}
	ds := r.deadBox.Stats()
	singleton := ds.FinalReliable + ds.FinalFireAndForget + ds.FinalTransfer
	if got, want := delivered.Load()+finals+singleton, sent.Load(); got != want {
		t.Fatalf("баланс: доставлено %d + финальные %d + синглтон %d = %d; отправлено %d (тихая потеря)",
			delivered.Load(), finals, singleton, got, want)
	}
	if st := r.Stats(); st.Misses != 0 {
		t.Errorf("Misses = %d; want 0 (отправка только по зарегистрированным id)", st.Misses)
	}
}

// Деспавн при N параллельных отправителях: ни одно письмо не потеряно тихо,
// все дропы классовые с метрикой.
func TestStressDespawnWithSenders(t *testing.T) {
	const senders, perSender = 8, 500
	r := NewRegistry(512)
	box := &Mailbox{}
	id := r.Register(box)
	if err := box.Claim(1); err != nil {
		t.Fatal(err)
	}
	var sent atomic.Int64
	var wg sync.WaitGroup
	for range senders {
		wg.Go(func() {
			for range perSender {
				r.Send(Envelope{To: Addr{Entity: id}, FromID: 2, Kind: KindAggro})
				sent.Add(1)
			}
		})
	}
	time.Sleep(2 * time.Millisecond) // отправители разогнались
	box.Despawn(1)
	r.Retire(id)
	wg.Wait()

	st := box.Stats()
	boxFinals := st.FinalReliable + st.FinalFireAndForget + st.FinalTransfer
	ds := r.deadBox.Stats()
	singleton := ds.FinalReliable + ds.FinalFireAndForget + ds.FinalTransfer
	if got, want := boxFinals+singleton, sent.Load(); got != want {
		t.Fatalf("баланс деспавна: класс-дропы %d; отправлено %d (тихая потеря)", got, want)
	}
}
