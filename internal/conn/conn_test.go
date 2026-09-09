package conn

import (
	"fmt"
	"testing"
)

type fakeHandler struct{}

func (fakeHandler) OnFrame(Frame)  {}
func (fakeHandler) OnClose(ConnID) {}

// compile-time контракт потребительского шва.
var _ Handler = fakeHandler{}

func TestFrameFields(t *testing.T) {
	f := Frame{Conn: 5, Payload: []byte{0x00}}
	if f.Conn != 5 || len(f.Payload) != 1 {
		t.Errorf("Frame = %+v; want Conn=5, payload 1 байт", f)
	}
}

func ExampleFrame() {
	f := Frame{Conn: 5, Payload: []byte{0x00}}
	fmt.Println(f.Conn, len(f.Payload))
	// Output: 5 1
}
