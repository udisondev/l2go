package encode

import (
	"fmt"
	"testing"
)

func TestFrameZeroValue(t *testing.T) {
	var f Frame
	if f.ClientID != 0 || f.Payload != nil {
		t.Errorf("нулевое значение Frame не пригодно: %+v", f)
	}
}

func ExampleFrame() {
	f := Frame{ClientID: 5, Payload: []byte{0x01}}
	fmt.Println(f.ClientID, len(f.Payload))
	// Output: 5 1
}
