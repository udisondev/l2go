package service

import (
	"fmt"
	"testing"
)

func TestServiceZeroValue(t *testing.T) {
	var s Service
	if s.Kind != 0 || s.ID != 0 {
		t.Errorf("нулевое значение Service не пригодно: %+v", s)
	}
}

func ExampleService() {
	s := Service{Kind: KindParty, ID: 100}
	fmt.Println(s.Kind, s.ID)
	// Output: 1 100
}
