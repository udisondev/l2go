package pcap

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/gopacket/gopacket/pcapgo"
	"github.com/udisondev/l2go/internal/crypto"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/tap"
)

// --- билдеры захвата (классик — руками на stdlib: оракул независим от
// gopacket-писателя; pcapng — pcapgo.NgWriter) ---

type capture struct {
	buf bytes.Buffer
}

func newClassicCapture() *capture {
	c := &capture{}
	// глобальный заголовок классик-pcap: magic LE, v2.4, snaplen, Ethernet
	var head [24]byte
	copy(head[0:4], []byte{0xD4, 0xC3, 0xB2, 0xA1})
	binary.LittleEndian.PutUint16(head[4:], 2)
	binary.LittleEndian.PutUint16(head[6:], 4)
	binary.LittleEndian.PutUint32(head[16:], 65535)
	binary.LittleEndian.PutUint32(head[20:], 1) // LINKTYPE_ETHERNET
	c.buf.Write(head[:])
	return c
}

func (c *capture) packet(ts time.Time, frame []byte) {
	var rec [16]byte
	binary.LittleEndian.PutUint32(rec[0:], uint32(ts.Unix()))
	binary.LittleEndian.PutUint32(rec[4:], uint32(ts.Nanosecond()/1000))
	binary.LittleEndian.PutUint32(rec[8:], uint32(len(frame)))
	binary.LittleEndian.PutUint32(rec[12:], uint32(len(frame)))
	c.buf.Write(rec[:])
	c.buf.Write(frame)
}

// snappedPacket — запись с incl_len < orig_len (обрезка snaplen).
func (c *capture) snappedPacket(ts time.Time, frame []byte, keep int) {
	var rec [16]byte
	binary.LittleEndian.PutUint32(rec[0:], uint32(ts.Unix()))
	binary.LittleEndian.PutUint32(rec[4:], uint32(ts.Nanosecond()/1000))
	binary.LittleEndian.PutUint32(rec[8:], uint32(keep))
	binary.LittleEndian.PutUint32(rec[12:], uint32(len(frame)))
	c.buf.Write(rec[:])
	c.buf.Write(frame[:keep])
}

var (
	clientIP = netip.MustParseAddr("10.0.0.1")
	serverIP = netip.MustParseAddr("10.0.0.2")
)

const (
	clientPort = 49152
	serverPort = 7777
)

// tcpFrame — Eth/IPv4/TCP-кадр с флагами и нагрузкой.
func tcpFrame(src, dst netip.Addr, sport, dport uint16, seq uint32, ackNum uint32, flags byte, payload []byte) []byte {
	ip := make([]byte, 20)
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:], uint16(20+20+len(payload)))
	ip[8] = 64
	ip[9] = 6 // TCP
	copy(ip[12:], src.AsSlice())
	copy(ip[16:], dst.AsSlice())

	tcp := make([]byte, 20)
	binary.BigEndian.PutUint16(tcp[0:], sport)
	binary.BigEndian.PutUint16(tcp[2:], dport)
	binary.BigEndian.PutUint32(tcp[4:], seq)
	binary.BigEndian.PutUint32(tcp[8:], ackNum)
	tcp[12] = 5 << 4
	tcp[13] = flags
	binary.BigEndian.PutUint16(tcp[14:], 0x2000)

	eth := make([]byte, 14)
	eth[12] = 0x08
	eth[13] = 0x00
	out := make([]byte, 0, 54+len(payload))
	out = append(out, eth...)
	out = append(out, ip...)
	out = append(out, tcp...)
	return append(out, payload...)
}

// vlanTcpFrame — Eth/802.1Q/IPv4/TCP.
func vlanTcpFrame(src, dst netip.Addr, sport, dport uint16, seq uint32, ackNum uint32, flags byte, payload []byte) []byte {
	inner := tcpFrame(src, dst, sport, dport, seq, ackNum, flags, payload)
	out := make([]byte, 0, 18+len(inner)-14)
	out = append(out, inner[:12]...)          // MAC-адреса
	out = append(out, 0x81, 0x00, 0x00, 0x64) // 802.1Q, VLAN 100
	out = append(out, 0x08, 0x00)             // IPv4
	return append(out, inner[14:]...)
}

// fragmentedTcpFrame — IPv4 с флагом MF (первый фрагмент полного кадра).
func fragmentedTcpFrame(src, dst netip.Addr, sport, dport uint16, seq uint32, payload []byte) []byte {
	frame := tcpFrame(src, dst, sport, dport, seq, 0, 0x10, payload)
	ip := frame[14:]
	binary.BigEndian.PutUint16(ip[6:], 0x2000) // MF, offset 0
	return frame
}

// udpFrame — Eth/IPv4/UDP (не-TCP шум).
func udpFrame(src, dst netip.Addr, sport, dport uint16) []byte {
	ip := make([]byte, 20)
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:], 20+8)
	ip[8] = 64
	ip[9] = 17
	copy(ip[12:], src.AsSlice())
	copy(ip[16:], dst.AsSlice())
	udp := make([]byte, 8)
	binary.BigEndian.PutUint16(udp[0:], sport)
	binary.BigEndian.PutUint16(udp[2:], dport)
	binary.BigEndian.PutUint16(udp[4:], 8)
	eth := make([]byte, 14)
	eth[12], eth[13] = 0x08, 0x00
	return append(append(eth, ip...), udp...)
}

// arpFrame — Eth/ARP (не-IP шум).
func arpFrame() []byte {
	out := make([]byte, 14+28)
	out[12], out[13] = 0x08, 0x06
	return out
}

// флаги TCP
const (
	fin = 0x01
	syn = 0x02
	rst = 0x04
	ack = 0x10
)

var baseTime = time.Unix(1700000000, 123456000)

func at(i int64) time.Time { return baseTime.Add(time.Duration(i) * time.Millisecond) }

// c2s/s2c — направление относительно клиента (клиент = источник SYN).
func c2s(seq uint32, flags byte, payload []byte) []byte {
	return tcpFrame(clientIP, serverIP, clientPort, serverPort, seq, 0, flags, payload)
}
func s2c(seq uint32, flags byte, payload []byte) []byte {
	return tcpFrame(serverIP, clientIP, serverPort, clientPort, seq, 0, flags, payload)
}

// l2Flow — полный L2 game-флоу: SYN → ProtocolVersion (классик 5Б) →
// SYN-ACK → KeyPacket → шифрованные кадры → FIN-обмен.
func l2Flow(c *capture) []byte {
	var key [8]byte
	for i := range key {
		key[i] = byte(0xA0 + i)
	}
	keyPacket := make([]byte, protocol.KeyPacketSize)
	protocol.WriteKeyPacket(keyPacket, 1, key[:], true, 1)
	clientFrame := make([]byte, protocol.LogoutSize)
	protocol.WriteLogout(clientFrame)
	cliCrypt := crypto.NewGameCrypt(key)
	cliCrypt.Enable()
	cliCrypt.Encrypt(clientFrame)

	pv := make([]byte, 2+protocol.ProtocolVersionSize)
	binary.LittleEndian.PutUint16(pv, uint16(protocol.ProtocolVersionSize+2))
	protocol.WriteProtocolVersion(pv[2:], protocol.ProtocolVersionInterlude)
	kp := make([]byte, 2+len(keyPacket))
	binary.LittleEndian.PutUint16(kp, uint16(len(keyPacket)+2))
	copy(kp[2:], keyPacket)
	lg := make([]byte, 2+len(clientFrame))
	binary.LittleEndian.PutUint16(lg, uint16(len(clientFrame)+2))
	copy(lg[2:], clientFrame)

	c.packet(at(0), c2s(1000, syn, nil))                     // SYN, ISN=999+1
	c.packet(at(1), s2c(2000, syn|ack, nil))                 // SYN-ACK
	c.packet(at(2), c2s(1001, ack, nil))                     // ACK
	c.packet(at(3), c2s(1001, ack|0x08, pv))                 // ProtocolVersion
	c.packet(at(4), s2c(2001, ack|0x08, kp))                 // KeyPacket
	c.packet(at(5), c2s(uint32(1001+len(pv)), ack|0x08, lg)) // Logout (зашифрован)
	c.packet(at(6), c2s(uint32(1001+len(pv)+len(lg)), fin, nil))
	c.packet(at(7), s2c(uint32(2001+len(kp)), fin|ack, nil))
	return lg
}

// readJournal — записи журнала из выхода конвертера.
func readJournal(t *testing.T, raw []byte) []tap.Record {
	t.Helper()
	jr := tap.NewJournalReader(bytes.NewReader(raw))
	var recs []tap.Record
	for {
		rec, err := jr.Next()
		if errors.Is(err, io.EOF) {
			return recs
		}
		if err != nil {
			t.Fatalf("журнал конвертера не читается: %v", err)
		}
		recs = append(recs, rec)
	}
}

// D1: классик-pcap → журнал: одна connOpen с адресами 4-тупы, направления
// относительно клиента, TS пакета, декод журнала именован.
func TestPcapConvertClassicToJournal(t *testing.T) {
	c := newClassicCapture()
	l2Flow(c)
	var journal bytes.Buffer
	rep, err := Convert(bytes.NewReader(c.buf.Bytes()), &journal)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if rep.Conns != 1 {
		t.Fatalf("Conns = %d, want 1; отчёт: %+v", rep.Conns, rep)
	}
	recs := readJournal(t, journal.Bytes())
	if len(recs) != 5 { // connOpen + 3 data + connClose
		t.Fatalf("записей %d, want 5: %+v", len(recs), recs)
	}
	open := recs[0]
	if open.Type != 1 || open.ConnID != 1 ||
		open.Listen != "10.0.0.1:49152" || open.Upstream != "10.0.0.2:7777" ||
		open.OpenedAt != at(0).UnixNano() {
		t.Fatalf("connOpen: %+v", open)
	}
	var datas []tap.Record
	for _, r := range recs {
		if r.Type == 2 {
			datas = append(datas, r)
		}
	}
	if len(datas) != 3 {
		t.Fatalf("data-записей %d, want 3", len(datas))
	}
	if datas[0].Dir != tap.DirCtoS || datas[0].TS != at(3).UnixNano() {
		t.Fatalf("первая data: dir=%d ts=%d, want CtoS ts=%d", datas[0].Dir, datas[0].TS, at(3).UnixNano())
	}
	if datas[1].Dir != tap.DirStoC || datas[1].TS != at(4).UnixNano() {
		t.Fatalf("вторая data: dir=%d ts=%d, want StoC ts=%d", datas[1].Dir, datas[1].TS, at(4).UnixNano())
	}
	var log bytes.Buffer
	if err := tap.Decode(bytes.NewReader(journal.Bytes()), tap.DecodeOptions{Log: &log}); err != nil {
		t.Fatalf("Decode журнала: %v", err)
	}
	for _, want := range []string{"PROTOCOL_VERSION version=746", "KEY_PACKET", "LOGOUT"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("лог без %q:\n%s", want, log.String())
		}
	}
}

// D2: тот же сценарий в pcapng-контейнере — тот же журнал.
func TestPcapConvertNgContainer(t *testing.T) {
	classic := newClassicCapture()
	l2Flow(classic)

	var ngBuf bytes.Buffer
	nw, err := pcapgo.NewNgWriter(&ngBuf, layers.LinkTypeEthernet)
	if err != nil {
		t.Fatalf("NgWriter: %v", err)
	}
	// переписываем те же кадры: классик-билдер дал байты, но временные метки
	// нужны по пакетно — собираем заново через кадры из l2Flow
	// (используем предопределённые кадры через билдер выше)
	frames := classicFrames()
	for i, fr := range frames {
		if err := nw.WritePacket(gopacket.CaptureInfo{
			Timestamp:     at(int64(i)),
			CaptureLength: len(fr),
			Length:        len(fr),
		}, fr); err != nil {
			t.Fatalf("WritePacket: %v", err)
		}
	}
	if err := nw.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	var journal bytes.Buffer
	rep, err := Convert(bytes.NewReader(ngBuf.Bytes()), &journal)
	if err != nil {
		t.Fatalf("Convert(pcapng): %v", err)
	}
	if rep.Conns != 1 {
		t.Fatalf("Conns = %d, want 1; отчёт: %+v", rep.Conns, rep)
	}
	var log bytes.Buffer
	if err := tap.Decode(bytes.NewReader(journal.Bytes()), tap.DecodeOptions{Log: &log}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !strings.Contains(log.String(), "LOGOUT") {
		t.Errorf("лог без LOGOUT:\n%s", log.String())
	}
}

// classicFrames — кадры l2Flow по отдельности (для pcapng-писателя).
func classicFrames() [][]byte {
	frames := make([][]byte, 0, 8)
	add := func(fr []byte) { frames = append(frames, bytes.Clone(fr)) }
	add(c2s(1000, syn, nil))
	add(s2c(2000, syn|ack, nil))
	add(c2s(1001, ack, nil))
	pv := wirePV()
	add(c2s(1001, ack|0x08, pv))
	kp := wireKeyPacket()
	add(s2c(2001, ack|0x08, kp))
	lg := wireLogout()
	add(c2s(uint32(1001+len(pv)), ack|0x08, lg))
	add(c2s(uint32(1001+len(pv)+len(lg)), fin, nil))
	add(s2c(uint32(2001+len(kp)), fin|ack, nil))
	return frames
}

func wirePV() []byte {
	pv := make([]byte, 2+protocol.ProtocolVersionSize)
	binary.LittleEndian.PutUint16(pv, uint16(protocol.ProtocolVersionSize+2))
	protocol.WriteProtocolVersion(pv[2:], protocol.ProtocolVersionInterlude)
	return pv
}

func wireKeyPacket() []byte {
	var key [8]byte
	for i := range key {
		key[i] = byte(0xA0 + i)
	}
	kp := make([]byte, protocol.KeyPacketSize)
	protocol.WriteKeyPacket(kp, 1, key[:], true, 1)
	out := make([]byte, 2+len(kp))
	binary.LittleEndian.PutUint16(out, uint16(len(kp)+2))
	copy(out[2:], kp)
	return out
}

func wireLogout() []byte {
	var key [8]byte
	for i := range key {
		key[i] = byte(0xA0 + i)
	}
	frame := make([]byte, protocol.LogoutSize)
	protocol.WriteLogout(frame)
	cliCrypt := crypto.NewGameCrypt(key)
	cliCrypt.Enable()
	cliCrypt.Encrypt(frame)
	out := make([]byte, 2+len(frame))
	binary.LittleEndian.PutUint16(out, uint16(len(frame)+2))
	copy(out[2:], frame)
	return out
}

// --- D3–D6: реасемблер без контейнера ---

// newHalf — направление с засеянной базой seq (как это делает SYN в
// конвертере); ленивый старт на первом прибывшем — только у серверной стороны
// без SYN-ACK, юнит-кейсы сидируют базу явно.
func newHalf(t *testing.T, base uint32) (*halfStream, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	jw := tap.NewJournalWriter(&buf)
	return &halfStream{id: 1, dir: tap.DirCtoS, jw: jw, started: true, next: base}, &buf
}

func dataRecords(t *testing.T, buf *bytes.Buffer) []tap.Record {
	t.Helper()
	var recs []tap.Record
	jr := tap.NewJournalReader(buf)
	for {
		rec, err := jr.Next()
		if errors.Is(err, io.EOF) {
			return recs
		}
		if err != nil {
			t.Fatalf("журнал: %v", err)
		}
		if rec.Type == 2 {
			recs = append(recs, rec)
		}
	}
}

// D3: ретрансмиссии и дубли — по одному вхождению байтов.
func TestPcapRetransmissionsDeduplicated(t *testing.T) {
	h, buf := newHalf(t, 500)
	payload := []byte("HELLO")
	for range 3 {
		if err := h.push(500, 1, payload); err != nil {
			t.Fatalf("push: %v", err)
		}
	}
	// дубль после флеша (FIN уже был) — тоже отбрасывается
	var rep Report
	if err := h.flush(&rep); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := h.push(500, 9, payload); err != nil {
		t.Fatalf("push после флеша: %v", err)
	}
	recs := dataRecords(t, buf)
	if len(recs) != 1 || !bytes.Equal(recs[0].Bytes, payload) {
		t.Fatalf("данные продублированы: %+v", recs)
	}
}

// D4: out-of-order — журнал в порядке seq.
func TestPcapOutOfOrderReordered(t *testing.T) {
	h, buf := newHalf(t, 500)
	if err := h.push(600, 3, []byte("CCC")); err != nil {
		t.Fatalf("push: %v", err)
	}
	if err := h.push(500, 1, []byte("AAA")); err != nil {
		t.Fatalf("push: %v", err)
	}
	if err := h.push(503, 2, []byte("BBB")); err != nil {
		t.Fatalf("push: %v", err)
	}
	var rep Report
	if err := h.flush(&rep); err != nil {
		t.Fatalf("flush: %v", err)
	}
	recs := dataRecords(t, buf)
	var got []byte
	for _, r := range recs {
		got = append(got, r.Bytes...)
	}
	if !bytes.Equal(got, []byte("AAABBBCCC")) {
		t.Fatalf("порядок потока: %q, want AAABBBCCC", got)
	}
}

// D5: перекрывающая ретрансмиссия длиннее исходного — хвост один раз.
func TestPcapOverlapExtends(t *testing.T) {
	h, buf := newHalf(t, 500)
	if err := h.push(500, 1, []byte("AAAA")); err != nil {
		t.Fatalf("push: %v", err)
	}
	if err := h.push(500, 2, []byte("AAAAAABB")); err != nil {
		t.Fatalf("push: %v", err)
	}
	recs := dataRecords(t, buf)
	var got []byte
	for _, r := range recs {
		got = append(got, r.Bytes...)
	}
	if !bytes.Equal(got, []byte("AAAAAABB")) {
		t.Fatalf("перекрытие: %q, want AAAAAABB", got)
	}
}

// D6: дыра seq — ресинк с счётчиком, данные после дыры доезжают.
func TestPcapSeqGapResyncsLoudly(t *testing.T) {
	h, buf := newHalf(t, 500)
	if err := h.push(500, 1, []byte("AAA")); err != nil {
		t.Fatalf("push: %v", err)
	}
	if err := h.push(600, 2, []byte("BBB")); err != nil {
		t.Fatalf("push: %v", err)
	}
	var rep Report
	if err := h.flush(&rep); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if rep.Resyncs != 1 {
		t.Fatalf("Resyncs = %d, want 1", rep.Resyncs)
	}
	if rep.LostBytes != 97 { // 600 - (500+3)
		t.Fatalf("LostBytes = %d, want 97", rep.LostBytes)
	}
	recs := dataRecords(t, buf)
	var got []byte
	for _, r := range recs {
		got = append(got, r.Bytes...)
	}
	if !bytes.Equal(got, []byte("AAABBB")) {
		t.Fatalf("данные после дыры: %q", got)
	}
}

// D7: поток без SYN пропускается громко (счётчик), нормальный — полностью.
func TestPcapFlowWithoutSynSkippedLoudly(t *testing.T) {
	c := newClassicCapture()
	// SYN-less: данные в обе стороны без SYN
	c.packet(at(0), c2s(1000, ack|0x08, []byte("nostart")))
	c.packet(at(1), s2c(2000, ack|0x08, []byte("noanswer")))
	l2Flow(c)
	var journal bytes.Buffer
	rep, err := Convert(bytes.NewReader(c.buf.Bytes()), &journal)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if rep.SkippedNoSyn == 0 {
		t.Fatal("SYN-less поток пропущен молча: SkippedNoSyn = 0")
	}
	if rep.Conns != 1 {
		t.Fatalf("Conns = %d, want 1", rep.Conns)
	}
	var log bytes.Buffer
	if err := tap.Decode(bytes.NewReader(journal.Bytes()), tap.DecodeOptions{Log: &log}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if strings.Contains(log.String(), "noanswer") {
		t.Fatal("SYN-less данные попали в журнал")
	}
}

// D8: FIN-обмен, RST, повторное использование порта, SYN-ACK без flip.
func TestPcapFinRstAndPortReuse(t *testing.T) {
	c := newClassicCapture()
	// поток 1: FIN-обмен
	c.packet(at(0), c2s(1000, syn, nil))
	c.packet(at(1), s2c(2000, syn|ack, nil))
	c.packet(at(2), c2s(1001, ack, nil))
	c.packet(at(3), c2s(1001, fin|ack, nil))
	c.packet(at(4), s2c(2001, fin|ack, nil))
	// поток 2: тот же 4-тупл после FIN — новый connID
	c.packet(at(5), c2s(5000, syn, nil))
	c.packet(at(6), s2c(8000, syn|ack, nil))
	c.packet(at(7), c2s(5001, ack, nil))
	c.packet(at(8), c2s(5001, rst|ack, nil))
	// SYN-ACK без предшествующего SYN — не создаёт поток и не flipает клиента
	c.packet(at(9), s2c(9999, syn|ack, nil))

	var journal bytes.Buffer
	rep, err := Convert(bytes.NewReader(c.buf.Bytes()), &journal)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if rep.Conns != 2 {
		t.Fatalf("Conns = %d, want 2 (повторное использование порта)", rep.Conns)
	}
	recs := readJournal(t, journal.Bytes())
	var closes []tap.Record
	for _, r := range recs {
		if r.Type == 4 {
			closes = append(closes, r)
		}
	}
	if len(closes) != 2 {
		t.Fatalf("connClose %d, want 2", len(closes))
	}
	if closes[0].ConnID != 1 || closes[0].Err != "" {
		t.Fatalf("чистое закрытие: %+v", closes[0])
	}
	if closes[1].ConnID != 2 || !strings.Contains(closes[1].Err, "RST") {
		t.Fatalf("RST-закрытие: %+v", closes[1])
	}
	// клиент остался источником SYN: первая data-запись — CtoS
	opens := []tap.Record{}
	for _, r := range recs {
		if r.Type == 1 {
			opens = append(opens, r)
		}
	}
	if len(opens) != 2 || opens[0].ConnID != 1 || opens[1].ConnID != 2 {
		t.Fatalf("connOpen: %+v", opens)
	}
	if opens[0].Listen != "10.0.0.1:49152" {
		t.Fatalf("клиент flipнулся: %+v", opens[0])
	}
}

// D9: не-TCP шум игнорируется, не-L2 TCP-поток конвертируется (hex на decode).
func TestPcapIgnoresNonTcpNoise(t *testing.T) {
	c := newClassicCapture()
	c.packet(at(0), udpFrame(clientIP, serverIP, clientPort, serverPort))
	c.packet(at(1), arpFrame())
	// не-L2 TCP-поток на чужом порту
	c.packet(at(2), tcpFrame(clientIP, serverIP, 12345, 54321, 700, 0, syn, nil))
	c.packet(at(3), tcpFrame(serverIP, clientIP, 54321, 12345, 900, 0, syn|ack, nil))
	c.packet(at(4), tcpFrame(clientIP, serverIP, 12345, 54321, 701, 0, ack, nil))
	// нагрузка — проволочный кадр [u16 длина][тело], как ждёт нарезка тапа
	c.packet(at(5), tcpFrame(clientIP, serverIP, 12345, 54321, 701, 0, ack, []byte{0x05, 0x00, 0xAB, 0xCD, 0xEF}))
	c.packet(at(6), tcpFrame(clientIP, serverIP, 12345, 54321, 706, 0, fin, nil))
	c.packet(at(7), tcpFrame(serverIP, clientIP, 54321, 12345, 901, 0, fin|ack, nil))

	var journal bytes.Buffer
	rep, err := Convert(bytes.NewReader(c.buf.Bytes()), &journal)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if rep.NonTCP != 2 {
		t.Fatalf("NonTCP = %d, want 2 (UDP+ARP)", rep.NonTCP)
	}
	if rep.Conns != 1 {
		t.Fatalf("Conns = %d, want 1", rep.Conns)
	}
	var log bytes.Buffer
	if err := tap.Decode(bytes.NewReader(journal.Bytes()), tap.DecodeOptions{Log: &log}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !strings.Contains(log.String(), "hex=abcdef") {
		t.Errorf("не-L2 поток не доехал:\n%s", log.String())
	}
}

// D10: snaplen-обрезанный пакет пропускается.
func TestPcapSnappedPacketSkipped(t *testing.T) {
	c := newClassicCapture()
	c.packet(at(0), c2s(1000, syn, nil))
	c.packet(at(1), s2c(2000, syn|ack, nil))
	frame := c2s(1001, ack, nil) // чистый ACK: обрезка не роняет поток
	c.snappedPacket(at(2), frame, 10)
	c.packet(at(3), c2s(1001, fin|ack, nil))
	c.packet(at(4), s2c(2001, fin|ack, nil))
	var journal bytes.Buffer
	rep, err := Convert(bytes.NewReader(c.buf.Bytes()), &journal)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if rep.Snapped != 1 {
		t.Fatalf("Snapped = %d, want 1", rep.Snapped)
	}
	if rep.Conns != 1 {
		t.Fatalf("Conns = %d, want 1", rep.Conns)
	}
}

// D11: IP-фрагмент пропускается с счётчиком.
func TestPcapFragmentedIpSkipped(t *testing.T) {
	c := newClassicCapture()
	c.packet(at(0), c2s(1000, syn, nil))
	c.packet(at(1), s2c(2000, syn|ack, nil))
	c.packet(at(2), fragmentedTcpFrame(clientIP, serverIP, clientPort, serverPort, 1001, []byte("XX")))
	c.packet(at(3), c2s(1001, fin|ack, nil))
	c.packet(at(4), s2c(2001, fin|ack, nil))
	var journal bytes.Buffer
	rep, err := Convert(bytes.NewReader(c.buf.Bytes()), &journal)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if rep.Fragments != 1 {
		t.Fatalf("Fragments = %d, want 1", rep.Fragments)
	}
}

// D12: VLAN-тег — диссекция доходит до TCP.
func TestPcapVlanTaggedFrames(t *testing.T) {
	c := newClassicCapture()
	c.packet(at(0), c2s(1000, syn, nil))
	c.packet(at(1), s2c(2000, syn|ack, nil))
	pv := wirePV()
	c.packet(at(2), vlanTcpFrame(clientIP, serverIP, clientPort, serverPort, 1001, 0, ack|0x08, pv))
	c.packet(at(3), c2s(uint32(1001+len(pv)), fin|ack, nil))
	c.packet(at(4), s2c(2001, fin|ack, nil))
	var journal bytes.Buffer
	rep, err := Convert(bytes.NewReader(c.buf.Bytes()), &journal)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	var log bytes.Buffer
	if err := tap.Decode(bytes.NewReader(journal.Bytes()), tap.DecodeOptions{Log: &log}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !strings.Contains(log.String(), "PROTOCOL_VERSION") {
		t.Errorf("VLAN-кадр не доехал:\n%s", log.String())
	}
	if rep.Conns != 1 {
		t.Fatalf("Conns = %d, want 1", rep.Conns)
	}
}

// D13: детерминизм — два прогона байт-в-байт (connID по первому пакету).
func TestPcapConverterDeterministic(t *testing.T) {
	c := newClassicCapture()
	// три чередующихся потока
	for i := range 3 {
		cp := netip.AddrFrom4([4]byte{10, 0, 0, byte(i + 1)})
		c.packet(at(int64(i)), tcpFrame(cp, serverIP, 40000+uint16(i), serverPort, 1000, 0, syn, nil))
	}
	for i := range 3 {
		cp := netip.AddrFrom4([4]byte{10, 0, 0, byte(i + 1)})
		c.packet(at(10+int64(i)), tcpFrame(serverIP, cp, serverPort, 40000+uint16(i), 2000, 0, syn|ack, nil))
		if i >= 1 {
			// два потока не закрыты: их connClose пишет флеш конца захвата —
			// порядок обхода открытых потоков обязан быть детерминирован
			// (обход map в Go рандомен)
			continue
		}
		c.packet(at(20+int64(i)), tcpFrame(cp, serverIP, 40000+uint16(i), serverPort, 1001, 0, fin, nil))
		c.packet(at(30+int64(i)), tcpFrame(serverIP, cp, serverPort, 40000+uint16(i), 2001, 0, fin|ack, nil))
	}
	// 16 прогонов байт-в-байт: обход map рандомен — единичная пара прогонов
	// ловит флип с вероятностью 1/2, пачка ловит практически всегда
	var want []byte
	for i := range 16 {
		var got bytes.Buffer
		if _, err := Convert(bytes.NewReader(c.buf.Bytes()), &got); err != nil {
			t.Fatalf("Convert %d: %v", i+1, err)
		}
		if want == nil {
			want = got.Bytes()
			continue
		}
		if !bytes.Equal(want, got.Bytes()) {
			t.Fatalf("прогон %d отличается от первого — конвертер недетерминирован", i+1)
		}
	}
	// хвост закрытий — по возрастанию connID (поток 1 закрыт в захвате,
	// открытые 2 и 3 закрывает флеш конца захвата)
	var closes []uint64
	for _, rec := range readJournal(t, want) {
		if rec.Type == 4 {
			closes = append(closes, rec.ConnID)
		}
	}
	for i, id := range closes {
		if id != uint64(i+1) {
			t.Fatalf("порядок закрытий %v, want [1 2 3]", closes)
		}
	}
}

// D14: таймстемпы µs → UnixNano точно (включая нулевую эпоху).
func TestPcapTimestampsMicrosToNanos(t *testing.T) {
	c := newClassicCapture()
	ts := time.Unix(1, 1000) // 1 с + 1 мкс
	c.packet(ts, c2s(1000, syn, nil))
	c.packet(ts.Add(time.Microsecond), s2c(2000, syn|ack, nil))
	c.packet(ts.Add(2*time.Microsecond), c2s(1001, ack|0x08, []byte{7}))
	c.packet(ts.Add(3*time.Microsecond), c2s(1002, fin, nil))
	c.packet(ts.Add(4*time.Microsecond), s2c(2001, fin|ack, nil))
	var journal bytes.Buffer
	if _, err := Convert(bytes.NewReader(c.buf.Bytes()), &journal); err != nil {
		t.Fatalf("Convert: %v", err)
	}
	for _, rec := range readJournal(t, journal.Bytes()) {
		if rec.Type == 2 && rec.TS != ts.Add(2*time.Microsecond).UnixNano() {
			t.Fatalf("data TS = %d, want %d", rec.TS, ts.Add(2*time.Microsecond).UnixNano())
		}
	}
	// нулевая эпоха не особый случай
	c2 := newClassicCapture()
	zero := time.Unix(0, 0)
	c2.packet(zero, c2s(1000, syn, nil))
	c2.packet(zero, s2c(2000, syn|ack, nil))
	c2.packet(zero, c2s(1001, ack|0x08, []byte{9}))
	c2.packet(zero, c2s(1002, fin, nil))
	c2.packet(zero, s2c(2001, fin|ack, nil))
	var j2 bytes.Buffer
	if _, err := Convert(bytes.NewReader(c2.buf.Bytes()), &j2); err != nil {
		t.Fatalf("Convert(нулевая эпоха): %v", err)
	}
	for _, rec := range readJournal(t, j2.Bytes()) {
		if rec.Type == 1 && rec.OpenedAt != 0 {
			t.Fatalf("нулевая эпоха: OpenedAt = %d, want 0", rec.OpenedAt)
		}
	}
}

// D15: пустой захват и захват из одного SYN.
func TestPcapEmptyAndSynOnly(t *testing.T) {
	empty := newClassicCapture()
	var journal bytes.Buffer
	rep, err := Convert(bytes.NewReader(empty.buf.Bytes()), &journal)
	if err != nil {
		t.Fatalf("Convert(пустой): %v", err)
	}
	if !bytes.Equal(journal.Bytes()[:6], []byte("L2TAP\x01")) || len(journal.Bytes()) != 6 {
		t.Fatalf("пустой журнал = %x, want только magic", journal.Bytes())
	}

	synOnly := newClassicCapture()
	synOnly.packet(at(0), c2s(1000, syn, nil))
	var j2 bytes.Buffer
	rep, err = Convert(bytes.NewReader(synOnly.buf.Bytes()), &j2)
	if err != nil {
		t.Fatalf("Convert(SYN-only): %v", err)
	}
	if rep.Conns != 1 {
		t.Fatalf("Conns = %d, want 1", rep.Conns)
	}
	recs := readJournal(t, j2.Bytes())
	if len(recs) != 2 || recs[0].Type != 1 || recs[1].Type != 4 {
		t.Fatalf("SYN-only журнал: %+v", recs)
	}
}

// D16: мусорные контейнеры и обрыв хвоста.
func TestPcapEvilContainersTable(t *testing.T) {
	c := newClassicCapture()
	l2Flow(c)
	valid := c.buf.Bytes()

	cases := []struct {
		name string
		in   []byte
	}{
		{"случайный мусор", bytes.Repeat([]byte{0xDE, 0xAD}, 50)},
		{"валидный l2.ini", func() []byte {
			// заголовок 413-файла — чужой домен, не pcap
			out := make([]byte, 64)
			copy(out, "L\x00i\x00n\x00")
			return out
		}()},
		{"обрыв хвоста", valid[:len(valid)-7]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("паника: %v", r)
				}
			}()
			var journal bytes.Buffer
			_, err := Convert(bytes.NewReader(tc.in), &journal)
			switch tc.name {
			case "обрыв хвоста":
				if err == nil || !errors.Is(err, ErrTruncated) {
					t.Fatalf("err = %v; want ErrTruncated", err)
				}
				// salvage: целые пакеты сконвертированы
				if !bytes.Contains(journal.Bytes(), []byte("L2TAP\x01")) {
					t.Fatal("salvage не написал журнал")
				}
			default:
				if err == nil || !errors.Is(err, ErrContainer) {
					t.Fatalf("err = %v; want ErrContainer", err)
				}
			}
		})
	}
}

// D17: служебные сегменты без нагрузки не пишутся.
func TestPcapPureAcksNoDataRecords(t *testing.T) {
	c := newClassicCapture()
	c.packet(at(0), c2s(1000, syn, nil))
	c.packet(at(1), s2c(2000, syn|ack, nil))
	c.packet(at(2), c2s(1001, ack, nil))
	c.packet(at(3), c2s(1001, fin|ack, nil))
	c.packet(at(4), s2c(2001, fin|ack, nil))
	var journal bytes.Buffer
	if _, err := Convert(bytes.NewReader(c.buf.Bytes()), &journal); err != nil {
		t.Fatalf("Convert: %v", err)
	}
	for _, rec := range readJournal(t, journal.Bytes()) {
		if rec.Type == 2 {
			t.Fatalf("появилась data-запись без нагрузки: %+v", rec)
		}
	}
}

// D18: сквозной golden — закоммиченный классик-сэмпл → конвертер → журнал
// байт-в-байт + декод журнала против golden-лога. Генерация:
// L2_WRITE_GOLDEN=1 go test ./internal/pcap.
func TestPcapJournalDecodesFullFlow(t *testing.T) {
	samplePath := filepath.Join("testdata", "sample-classic.pcap")
	if os.Getenv("L2_WRITE_GOLDEN") == "1" {
		c := newClassicCapture()
		l2Flow(c)
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("testdata: %v", err)
		}
		if err := os.WriteFile(samplePath, c.buf.Bytes(), 0o644); err != nil {
			t.Fatalf("запись сэмпла: %v", err)
		}
		var journal bytes.Buffer
		if _, err := Convert(bytes.NewReader(c.buf.Bytes()), &journal); err != nil {
			t.Fatalf("Convert: %v", err)
		}
		if err := os.WriteFile(filepath.Join("testdata", "sample-classic.tap"), journal.Bytes(), 0o644); err != nil {
			t.Fatalf("запись журнала: %v", err)
		}
		var log bytes.Buffer
		if err := tap.Decode(bytes.NewReader(journal.Bytes()), tap.DecodeOptions{Log: &log}); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if err := os.WriteFile(filepath.Join("testdata", "sample-classic.log"), log.Bytes(), 0o644); err != nil {
			t.Fatalf("запись лога: %v", err)
		}
	}
	raw, err := os.ReadFile(samplePath)
	if err != nil {
		t.Fatalf("сэмпл: %v (генерация: L2_WRITE_GOLDEN=1 go test ./internal/pcap)", err)
	}
	var journal bytes.Buffer
	if _, err := Convert(bytes.NewReader(raw), &journal); err != nil {
		t.Fatalf("Convert: %v", err)
	}
	wantJournal, err := os.ReadFile(filepath.Join("testdata", "sample-classic.tap"))
	if err != nil {
		t.Fatalf("golden-журнал: %v", err)
	}
	if !bytes.Equal(journal.Bytes(), wantJournal) {
		t.Error("журнал конвертера дрейфовал от golden")
	}
	var log bytes.Buffer
	if err := tap.Decode(bytes.NewReader(journal.Bytes()), tap.DecodeOptions{Log: &log}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	wantLog, err := os.ReadFile(filepath.Join("testdata", "sample-classic.log"))
	if err != nil {
		t.Fatalf("golden-лог: %v", err)
	}
	if log.String() != string(wantLog) {
		t.Errorf("лог декодера дрейфовал:\ngot:\n%s\nwant:\n%s", log.String(), wantLog)
	}
}
