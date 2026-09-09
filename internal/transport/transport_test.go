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

func ExampleAddr() {
	a := Addr{Entity: 42, Slot: SlotPet}
	fmt.Println(a.Entity, a.Slot)
	// Output: 42 0
}
