package transport

import "testing"

// KindCount — мощность реестра: без дыр между первым и последним типом,
// счётчики свёртки мира индексируются без выхода за границы.
func TestKindCount(t *testing.T) {
	if got, want := int(KindCount), int(KindConnClose); got != want {
		t.Errorf("KindCount = %d; want %d (последний тип реестра)", got, want)
	}
	for k := 1; k <= int(KindCount); k++ {
		if Kind(k).Class() == 0 {
			t.Errorf("тип %d в границах KindCount не имеет класса (дыра реестра)", k)
		}
	}
	if Kind(KindCount+1).Class() != 0 {
		t.Errorf("тип за границей KindCount имеет класс; want 0")
	}
}

// Mark — чтение водяного знака: потребитель — заголовки порций D5.
func TestMailboxMarkAccessor(t *testing.T) {
	box, _ := newClaimedBox(t, 8)
	if got := box.Mark(); got != 0 {
		t.Fatalf("Mark пустого ящика = %d; want 0", got)
	}
	box.enqueue(Envelope{FromID: 1, Kind: KindAggro})
	box.enqueue(Envelope{FromID: 1, Kind: KindXP})
	batch := box.Extract(42)
	if len(batch) != 2 {
		t.Fatalf("извлечено %d; want 2", len(batch))
	}
	if got := box.Mark(); got != 2 {
		t.Errorf("Mark после изъятия 2 писем = %d; want 2", got)
	}
}

// ExtractAppend — изъятие с дописыванием: пачки нескольких ящиков накапливаются
// в одном буфере владельца (0 аллокаций вне роста).
func TestMailboxExtractAppend(t *testing.T) {
	a, aTok := newClaimedBox(t, 8)
	b := &Mailbox{}
	reg := NewRegistry(0)
	bid := reg.Register(b)
	if err := b.Claim(uint64(bid)); err != nil {
		t.Fatalf("claim: %v", err)
	}
	a.enqueue(Envelope{FromID: 1, Kind: KindAggro})
	a.enqueue(Envelope{FromID: 1, Kind: KindXP})
	b.enqueue(Envelope{FromID: 2, Kind: KindAggro})
	var buf []Envelope
	n1 := len(buf)
	buf = a.ExtractAppend(aTok, buf)
	if got := len(buf[n1:]); got != 2 {
		t.Fatalf("первая пачка = %d; want 2", got)
	}
	n2 := len(buf)
	buf = b.ExtractAppend(uint64(bid), buf)
	if got := len(buf[n2:]); got != 1 {
		t.Fatalf("вторая пачка = %d; want 1 (дописана, не перезаписала)", got)
	}
	if buf[n2].FromID != 2 {
		t.Fatalf("вторая пачка испорчена: %+v", buf[n2])
	}
	if got := b.ExtractAppend(uint64(bid), buf); len(buf) != len(got) || len(got[n2+1:]) != 0 {
		t.Fatalf("пустой ящик дописал лишнее")
	}
}
