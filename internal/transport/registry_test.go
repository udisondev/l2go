package transport

import (
	"errors"
	"fmt"
	"testing"
)

func TestRegisterMonotonicIDs(t *testing.T) {
	r := NewRegistry(8)
	var prev EntityID
	for range 100 {
		id := r.Register(&Mailbox{})
		if id == 0 {
			t.Fatalf("выдан зарезервированный EntityID 0")
		}
		if id <= prev {
			t.Fatalf("ID не монотонны: %d после %d", id, prev)
		}
		prev = id
	}
}

func TestSendDeliverAndMiss(t *testing.T) {
	r := NewRegistry(8)
	box := &Mailbox{}
	id := r.Register(box)
	if err := box.Claim(1); err != nil {
		t.Fatal(err)
	}
	r.Send(Envelope{To: Addr{Entity: id, Slot: SlotSelf}, FromID: 1, Kind: KindClientFrame})
	if got := len(box.Extract(1)); got != 1 {
		t.Fatalf("Send не доставил письмо: получено %d; want 1", got)
	}
	// miss по неизвестному id — не тихо: метрика
	r.Send(Envelope{To: Addr{Entity: id + 1000, Slot: SlotSelf}, FromID: 1, Kind: KindClientFrame})
	if st := r.Stats(); st.Misses != 1 {
		t.Errorf("Misses = %d; want 1", st.Misses)
	}
}

func TestRetireSwapsToDeadSingleton(t *testing.T) {
	r := NewRegistry(8)
	box := &Mailbox{}
	id := r.Register(box)
	if err := box.Claim(1); err != nil {
		t.Fatal(err)
	}
	box.Despawn(1)
	r.Retire(id)

	// поздний отправитель попадает в синглтон «мёртв»: классовый дроп с метрикой
	r.Send(Envelope{To: Addr{Entity: id, Slot: SlotSelf}, FromID: 2, Kind: KindXP})
	if st := r.deadBox.Stats(); st.FinalReliable != 1 {
		t.Errorf("синглтон мёртвых: FinalReliable = %d; want 1", st.FinalReliable)
	}
	if st := r.Stats(); st.Misses != 0 {
		t.Errorf("retired id посчитан миссом: %d; want 0 (запись жива, ящик мёртв)", st.Misses)
	}
}

func TestClaimErrors(t *testing.T) {
	r := NewRegistry(8)
	box := &Mailbox{}
	r.Register(box)
	if err := box.Claim(5); err != nil {
		t.Fatalf("первый Claim: %v", err)
	}
	if err := box.Claim(6); !errors.Is(err, ErrBusy) {
		t.Fatalf("второй Claim = %v; want ErrBusy", err)
	}
}

func ExampleRegistry() {
	r := NewRegistry(1024)
	player := &Mailbox{}
	playerID := r.Register(player)
	if err := player.Claim(1); err != nil { // читатель — регион-владелец
		panic(err)
	}
	r.Send(Envelope{To: Addr{Entity: playerID}, FromID: 2, Kind: KindClientFrame})
	for _, env := range player.Extract(1) {
		fmt.Println("применено писем Kind", env.Kind > 0)
	}
	player.Despawn(1)
	r.Retire(playerID)
	// Output:
	// применено писем Kind true
}

// DeadDrops — дельта агрегата при посылках в retired id (метрика страгглеров
// после деспавна, P3.7).
func TestRegistryDeadBoxAggregatesDelta(t *testing.T) {
	t.Parallel()
	r := NewRegistry(4)
	var box Mailbox
	id := r.Register(&box)
	if err := box.Claim(uint64(id)); err != nil {
		t.Fatal(err)
	}
	box.Despawn(uint64(id))
	r.Retire(id)

	faf0, _ := r.DeadDrops()
	for range 3 {
		r.Send(Envelope{To: Addr{Entity: id}, Kind: KindClientFrame, Payload: []byte{1}})
	}
	_, reliable1 := r.DeadDrops()
	r.Send(Envelope{To: Addr{Entity: id}, Kind: KindAggro, Payload: []byte{1}})
	faf1, reliable2 := r.DeadDrops()
	if faf1-faf0 != 3 {
		t.Errorf("FAF-дропы: %d → %d; want +3", faf0, faf1)
	}
	if reliable2-reliable1 != 1 {
		t.Errorf("reliable-дропы: %d → %d; want +1", reliable1, reliable2)
	}
}
