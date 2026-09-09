package persist

import (
	"fmt"
	"testing"
)

func TestRecordZeroValue(t *testing.T) {
	var r Record
	if r.Tick != 0 || r.Envs != nil {
		t.Errorf("нулевое значение Record не пригодно: %+v", r)
	}
}

func ExampleRecord() {
	r := Record{Tick: 3}
	fmt.Println(r.Tick, len(r.Envs))
	// Output: 3 0
}
