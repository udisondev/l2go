package pcap

import (
	"bytes"
	"net/netip"
	"testing"
)

// Пропускная способность конвертера на многопоточном синт-захвате
// (10 потоков × 30 пакетов) — baseline P3.14-l2pcap.
func BenchmarkPcapConvert(b *testing.B) {
	c := newClassicCapture()
	for i := range 10 {
		cp := netip.AddrFrom4([4]byte{10, 0, 1, byte(i + 1)})
		c.packet(at(int64(i)), tcpFrame(cp, serverIP, 40000+uint16(i), serverPort, 1000, 0, syn, nil))
		c.packet(at(int64(i)), tcpFrame(serverIP, cp, serverPort, 40000+uint16(i), 2000, 0, syn|ack, nil))
		for j := range 25 {
			payload := bytes.Repeat([]byte{byte(i), byte(j)}, 32)
			c.packet(at(10+int64(i*30+j)), tcpFrame(cp, serverIP, 40000+uint16(i), serverPort, 1001+uint32(j*len(payload)), 0, ack|0x08, payload))
		}
		c.packet(at(1000+int64(i)), tcpFrame(cp, serverIP, 40000+uint16(i), serverPort, 1001+25*64, 0, fin, nil))
		c.packet(at(1001+int64(i)), tcpFrame(serverIP, cp, serverPort, 40000+uint16(i), 2001, 0, fin|ack, nil))
	}
	raw := bytes.Clone(c.buf.Bytes())
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		var out bytes.Buffer
		if _, err := Convert(bytes.NewReader(raw), &out); err != nil {
			b.Fatalf("Convert: %v", err)
		}
	}
}
