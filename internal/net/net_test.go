package net

import "testing"

type fakeHandler struct{}

func (fakeHandler) OnFrame(Frame)  {}
func (fakeHandler) OnClose(ConnID) {}

func TestHandlerContract(t *testing.T) {
	var _ Handler = fakeHandler{}
	f := Frame{Conn: 5, Payload: []byte{0x00}}
	if f.Conn != 5 {
		t.Errorf("Conn = %d; want 5", f.Conn)
	}
}
