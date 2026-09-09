package conn

import "fmt"

type fakeHandler struct{}

func (fakeHandler) OnFrame(Frame)  {}
func (fakeHandler) OnClose(ConnID) {}

// compile-time контракт потребительского шва.
var _ Handler = fakeHandler{}

func ExampleFrame() {
	f := Frame{Conn: 5, Payload: []byte{0x00}}
	fmt.Println(f.Conn, len(f.Payload))
	// Output: 5 1
}
