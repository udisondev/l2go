package transport

import (
	"fmt"
	"testing"
)

func TestServantAddrDisambiguation(t *testing.T) {
	self := Addr{Entity: 42, Slot: SlotSelf}
	pet := Addr{Entity: 42, Slot: SlotPet}
	if self.Slot == pet.Slot {
		t.Errorf("адрес сущности и адрес слуги неразличимы: слот %d", self.Slot)
	}
	if want := Slot(-1); self.Slot != want {
		t.Errorf("SlotSelf = %d; want %d", self.Slot, want)
	}
	if want := Slot(0); pet.Slot != want {
		t.Errorf("SlotPet = %d; want %d (нумерация слуг без сдвига)", pet.Slot, want)
	}
}

func TestKindClassRegistry(t *testing.T) {
	want := map[Kind]Class{
		KindApplyDamage:    ClassFireAndForget,
		KindBroadcastState: ClassFireAndForget,
		KindClientFrame:    ClassFireAndForget,
		KindAggro:          ClassReliable,
		KindKillCredit:     ClassReliable,
		KindXP:             ClassReliable,
		KindControlEffect:  ClassReliable,
		KindMemberStatus:   ClassReliable,
		KindServiceMsg:     ClassReliable,
		KindInstallAck:     ClassReliable,
		KindConfirmAck:     ClassReliable,
		KindRetire:         ClassReliable,
		KindSeed:           ClassReliable,
		KindPersistRequest: ClassReliable,
		KindPersistReply:   ClassReliable,
		KindEnterWorld:     ClassReliable,
		KindLinkDead:       ClassReliable,
		KindConnClose:      ClassReliable,
		KindReserve:        ClassTransfer,
		KindCommit:         ClassTransfer,
		KindAbort:          ClassTransfer,
		KindLootPickup:     ClassTransfer,
		KindSpoil:          ClassTransfer,
		KindSweep:          ClassTransfer,
		KindSuitcase:       ClassTransfer,
	}
	// полнота реестра: каждый тип от первого до последнего имеет класс
	for k := KindApplyDamage; k <= KindConnClose; k++ {
		c, ok := want[k]
		if !ok {
			t.Errorf("тип %d не покрыт таблицей теста", k)
			continue
		}
		if got := k.Class(); got != c {
			t.Errorf("Kind(%d).Class() = %d; want %d", k, got, c)
		}
	}
	if extra := len(want) - int(KindConnClose); extra != 0 {
		t.Errorf("в таблице %d лишних типов", extra)
	}
	if unknown := Kind(999).Class(); unknown != 0 {
		t.Errorf("неизвестный тип имеет класс %d; want 0 (отклоняется валидацией)", unknown)
	}
}

func TestKindRegionalAndService(t *testing.T) {
	regional := map[Kind]bool{
		KindSuitcase: true, KindInstallAck: true, KindConfirmAck: true,
		KindRetire: true, KindSeed: true, KindEnterWorld: true, KindLinkDead: true,
	}
	service := map[Kind]bool{KindMemberStatus: true, KindServiceMsg: true}
	for k := KindApplyDamage; k <= KindConnClose; k++ {
		if got := k.Regional(); got != regional[k] {
			t.Errorf("Kind(%d).Regional() = %v; want %v", k, got, regional[k])
		}
		if got := k.Service(); got != service[k] {
			t.Errorf("Kind(%d).Service() = %v; want %v", k, got, service[k])
		}
	}
}

func TestDomainAllows(t *testing.T) {
	cases := []struct {
		d    Domain
		k    Kind
		want bool
	}{
		{DomainWorld, KindApplyDamage, true},
		{DomainWorld, KindClientFrame, true},
		{DomainWorld, KindEnterWorld, true},
		{DomainWorld, KindSuitcase, true},
		{DomainWorld, KindMemberStatus, false},
		{DomainParty, KindMemberStatus, true},
		{DomainParty, KindApplyDamage, false},
		{DomainChat, KindServiceMsg, true},
		{DomainChat, KindMemberStatus, false},
		{DomainMarket, KindReserve, false},
		{Domain(0), KindXP, false}, // default deny
		{Domain(99), KindXP, false},
	}
	for _, c := range cases {
		if got := c.d.Allows(c.k); got != c.want {
			t.Errorf("Domain(%d).Allows(%d) = %v; want %v", c.d, c.k, got, c.want)
		}
	}
}

func TestEnvelopeCarriesKind(t *testing.T) {
	// Класс доставки — производная Kind: конверт несёт тип, но не дублирует класс.
	env := Envelope{To: Addr{Entity: 7, Slot: SlotSelf}, FromID: 1, Kind: KindClientFrame}
	if got := env.Kind.Class(); got != ClassFireAndForget {
		t.Errorf("конверт KindClientFrame: класс = %d; want %d", got, ClassFireAndForget)
	}
}

func ExampleAddr() {
	a := Addr{Entity: 42, Slot: SlotPet}
	fmt.Println(a.Entity, a.Slot)
	// Output: 42 0
}

func ExampleKind_Class() {
	fmt.Println(KindAggro.Class(), KindReserve.Class(), KindSuitcase.Regional())
	// Output: 2 3 true
}
