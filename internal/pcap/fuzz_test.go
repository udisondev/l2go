package pcap

import (
	"bytes"
	"io"
	"testing"
)

// FuzzPcapContainer — произвольные байты как захват: без паник; ошибки
// контейнера/обрыва — именованные, salvage пишет журнал при целых пакетах.
func FuzzPcapContainer(f *testing.F) {
	c := newClassicCapture()
	l2Flow(c)
	f.Add(c.buf.Bytes())
	f.Add(c.buf.Bytes()[:len(c.buf.Bytes())-3])
	f.Add([]byte{0x0A, 0x0D, 0x0D, 0x0A})
	f.Add(bytes.Repeat([]byte{0x00}, 64))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = Convert(bytes.NewReader(data), io.Discard)
	})
}
