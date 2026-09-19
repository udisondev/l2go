// Package pcap — конвертер пассивных захватов pcap/pcapng (dumpcap/Npcap) в
// бинарный журнал тапа. Контейнеры читает gopacket/pcapgo (классик и pcapng —
// Wireshark ≥3 пишет pcapng по умолчанию), диссекцию Eth/VLAN/IPv4/TCP делает
// gopacket/layers; сборка TCP-потоков в соединения — здесь.
//
// Политика потоков: конвертируются все TCP-потоки, начатые SYN (клиент =
// источник SYN; повторный SYN той же 4-тупы после закрытия — новое
// соединение); не-TCP пакеты игнорируются; поток без SYN (захват с середины)
// пропускается с предупреждением и счётчиком; ретрансмиссии и дубли
// дедуплицируются по seq (конфликтующие байты — first-wins); дыра seq
// перепрыгивается при флеше (FIN/RST/конец захвата) с предупреждением и
// счётчиком потерь; snaplen-обрезанные пакеты и IP-фрагменты пропускаются с
// счётчиком. Конвертер однопроходный и синхронный: без горутин и таймеров —
// недоехавшие данные выпадают на флеше конца захвата.
package pcap

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/gopacket/gopacket/pcapgo"
	"github.com/udisondev/l2go/internal/tap"
)

// ErrContainer — вход не является файлом pcap/pcapng.
var ErrContainer = errors.New("не формат pcap/pcapng")

// ErrTruncated — захват оборван посередине записи: целые пакеты
// сконвертированы (salvage), хвост потерян.
var ErrTruncated = errors.New("захват оборван")

// Report — счётчики конвертации: наблюдаемый оракул пропусков и потерь.
type Report struct {
	Conns        int // соединений в журнале (по SYN)
	SkippedNoSyn int // TCP-пакетов потоков без SYN (захват с середины)
	NonTCP       int // пакетов не IPv4/TCP (UDP/ICMP/ARP/IPv6) проигнорировано
	Snapped      int // пакетов с обрезкой snaplen пропущено
	Fragments    int // IP-фрагментов пропущено
	Resyncs      int // перепрыгов через дыры seq
	LostBytes    int // байтов потеряно в дырах seq
}

// Convert читает захват (контейнер определяется по магии), собирает
// TCP-потоки в соединения и пишет журнал тапа. Обрыв хвоста захвата —
// ErrTruncated после salvage целых пакетов.
func Convert(r io.Reader, w io.Writer) (Report, error) {
	br := bufio.NewReader(r)
	magic, err := br.Peek(4)
	if err != nil {
		return Report{}, fmt.Errorf("pcap: заголовок: %w: %w", err, ErrContainer)
	}
	var rep Report
	var readErr error
	switch {
	case bytes.Equal(magic, []byte{0x0A, 0x0D, 0x0D, 0x0A}):
		readErr = convertLoop(&rep, w, func() (gopacket.PacketDataSource, error) {
			rd, err := pcapgo.NewNgReader(br, pcapgo.DefaultNgReaderOptions)
			if err != nil {
				return nil, fmt.Errorf("pcap: pcapng: %w", err)
			}
			return rd, nil
		})
	case isClassicMagic(magic):
		readErr = convertLoop(&rep, w, func() (gopacket.PacketDataSource, error) {
			rd, err := pcapgo.NewReader(br)
			if err != nil {
				return nil, fmt.Errorf("pcap: классик: %w: %w", err, ErrContainer)
			}
			return rd, nil
		})
	default:
		return Report{}, fmt.Errorf("pcap: магия %x: %w", magic, ErrContainer)
	}
	if readErr != nil {
		return rep, readErr
	}
	return rep, nil
}

func isClassicMagic(m []byte) bool {
	classic := [][]byte{
		{0xD4, 0xC3, 0xB2, 0xA1}, // µs LE
		{0xA1, 0xB2, 0xC3, 0xD4}, // µs BE
		{0x4D, 0x3C, 0xB2, 0xA1}, // ns LE
		{0xA1, 0xB2, 0x3C, 0x4D}, // ns BE
	}
	for _, c := range classic {
		if bytes.Equal(m, c) {
			return true
		}
	}
	return false
}

// endpoint — сторона 4-тупы потока.
type endpoint struct {
	addr netip.Addr
	port uint16
}

func (e endpoint) String() string {
	return netip.AddrPortFrom(e.addr, e.port).String()
}

func (e endpoint) less(o endpoint) bool {
	if e.addr != o.addr {
		return e.addr.Less(o.addr)
	}
	return e.port < o.port
}

// pairKey — канонический ключ 4-тупы (обе ориентации — один поток).
type pairKey struct{ lo, hi endpoint }

func pairOf(a, b endpoint) pairKey {
	if b.less(a) {
		a, b = b, a
	}
	return pairKey{lo: a, hi: b}
}

type segment struct {
	seq  uint32
	ts   int64
	data []byte
}

// halfStream — реасемблер одного направления потока: contiguous данные
// уходят в журнал немедленно, будущие сегменты ждут в очереди, дубли
// отбрасываются, перекрытия усекаются first-wins.
type halfStream struct {
	id      uint64
	dir     byte
	jw      *tap.JournalWriter
	started bool
	next    uint32
	pending []segment
}

func (h *halfStream) push(seq uint32, ts int64, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if !h.started {
		h.started = true
		h.next = seq
	}
	end := seq + uint32(len(data))
	switch {
	case end <= h.next: // ретрансмиссия позади окна — дубль
		return nil
	case seq < h.next: // перекрытие: суффикс от next
		data = data[h.next-seq:]
		seq = h.next
	}
	if seq > h.next { // дыра — в очередь до флеша
		h.hold(segment{seq: seq, ts: ts, data: bytes.Clone(data)})
		return nil
	}
	if err := h.emit(ts, data); err != nil {
		return err
	}
	return h.drain()
}

// hold вставляет сегмент в очередь с сохранением порядка по seq.
func (h *halfStream) hold(seg segment) {
	i := len(h.pending)
	for i > 0 && h.pending[i-1].seq > seg.seq {
		i--
	}
	h.pending = append(h.pending, segment{})
	copy(h.pending[i+1:], h.pending[i:])
	h.pending[i] = seg
}

func (h *halfStream) emit(ts int64, data []byte) error {
	if err := h.jw.Data(h.id, h.dir, ts, data); err != nil {
		return err
	}
	h.next += uint32(len(data))
	return nil
}

// drain выталкивает очередь, пока голова достигает окна потока.
func (h *halfStream) drain() error {
	for len(h.pending) > 0 {
		seg := h.pending[0]
		if seg.seq > h.next {
			return nil
		}
		h.pending = h.pending[1:]
		if seg.seq+uint32(len(seg.data)) <= h.next {
			continue // дубль из очереди
		}
		if seg.seq < h.next {
			seg.data = seg.data[h.next-seg.seq:]
		}
		if err := h.emit(seg.ts, seg.data); err != nil {
			return err
		}
	}
	return nil
}

// flush выталкивает очередь принудительно: дыры перепрыгиваются (ресинк с
// счётчиком потерь), все данные направления доезжают.
func (h *halfStream) flush(rep *Report) error {
	for _, seg := range h.pending {
		switch {
		case seg.seq+uint32(len(seg.data)) <= h.next:
			continue // дубль
		case seg.seq <= h.next:
			seg.data = seg.data[h.next-seg.seq:]
		default:
			rep.Resyncs++
			rep.LostBytes += int(seg.seq - h.next)
			slog.Warn("pcap: дыра seq — ресинк направления",
				"connID", h.id, "lost", int(seg.seq-h.next))
		}
		if err := h.emit(seg.ts, seg.data); err != nil {
			return err
		}
	}
	h.pending = nil
	return nil
}

// flow — соединение: идентификатор, стороны и реасемблеры направлений.
type flow struct {
	id             uint64
	client, server endpoint
	c2s, s2c       halfStream
	finC, finS     bool
	closed         bool
}

// converter — состояние прохода по пакетам захвата.
type converter struct {
	jw     *tap.JournalWriter
	rep    *Report
	active map[pairKey]*flow
	order  []*flow
	warned map[pairKey]bool

	eth  layers.Ethernet
	vlan layers.Dot1Q
	lo   layers.Loopback
	ip4  layers.IPv4
	tcp  layers.TCP

	parsers map[layers.LinkType]*gopacket.DecodingLayerParser
	decoded []gopacket.LayerType
}

// convertLoop — общий пакетный цикл обоих контейнеров; salvage на обрыве.
func convertLoop(rep *Report, w io.Writer, open func() (gopacket.PacketDataSource, error)) error {
	src, err := open()
	if err != nil {
		return err
	}
	jw := tap.NewJournalWriter(w)
	c := &converter{
		jw:      jw,
		rep:     rep,
		active:  make(map[pairKey]*flow),
		warned:  make(map[pairKey]bool),
		parsers: make(map[layers.LinkType]*gopacket.DecodingLayerParser),
	}
	var linkType layers.LinkType
	truncated := false
	for {
		data, ci, err := src.ReadPacketData()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			slog.Warn("pcap: захват оборван — сконвертированы целые пакеты", "err", err)
			truncated = true
			break
		}
		if lt, ok := linkOf(src); ok {
			linkType = lt
		}
		if err := c.onPacket(ci.Timestamp.UnixNano(), linkType, data, ci.CaptureLength < ci.Length); err != nil {
			return err
		}
	}
	// конец захвата: флеш и закрытие всех открытых потоков в порядке создания
	for _, f := range c.order {
		if f.closed {
			continue
		}
		if err := c.closeFlow(f, nil); err != nil {
			return err
		}
	}
	if err := jw.Flush(); err != nil {
		return err
	}
	if truncated {
		return fmt.Errorf("pcap: %w", ErrTruncated)
	}
	return nil
}

// linkOf — тип линка контейнера (pcapng отдаёт первый интерфейс; классик —
// свой). Читатели без линк-типа не встречаются среди поддерживаемых.
func linkOf(src gopacket.PacketDataSource) (layers.LinkType, bool) {
	switch r := src.(type) {
	case *pcapgo.Reader:
		return r.LinkType(), true
	case *pcapgo.NgReader:
		return r.LinkType(), true
	}
	return 0, false
}

// parser — диссектор по типу линка; незнакомый линк — ошибка.
func (c *converter) parser(lt layers.LinkType) (*gopacket.DecodingLayerParser, error) {
	if p, ok := c.parsers[lt]; ok {
		return p, nil
	}
	var p *gopacket.DecodingLayerParser
	switch lt {
	case layers.LinkTypeEthernet:
		p = gopacket.NewDecodingLayerParser(layers.LayerTypeEthernet, &c.eth, &c.vlan, &c.ip4, &c.tcp)
	case layers.LinkTypeNull:
		p = gopacket.NewDecodingLayerParser(layers.LayerTypeLoopback, &c.lo, &c.ip4, &c.tcp)
	case layers.LinkTypeRaw:
		p = gopacket.NewDecodingLayerParser(layers.LayerTypeIPv4, &c.ip4, &c.tcp)
	default:
		return nil, fmt.Errorf("pcap: тип линка %d не поддерживается (Ethernet/Null/RawIP)", lt)
	}
	p.IgnoreUnsupported = true // не-IP полезная нагрузка Ethernet — не ошибка
	c.parsers[lt] = p
	return p, nil
}

// onPacket — один пакет захвата.
func (c *converter) onPacket(ts int64, lt layers.LinkType, data []byte, snapped bool) error {
	if snapped {
		c.rep.Snapped++
		slog.Warn("pcap: пакет обрезан snaplen — пропущен", "bytes", len(data))
		return nil
	}
	p, err := c.parser(lt)
	if err != nil {
		return err
	}
	if err := p.DecodeLayers(data, &c.decoded); err != nil {
		return fmt.Errorf("pcap: диссекция: %w", err)
	}
	// фрагмент проверяется до TCP: диссектор не собирает TCP из фрагментов,
	// слоя TCP у такого пакета нет вовсе
	if hasLayer(c.decoded, layers.LayerTypeIPv4) &&
		(c.ip4.Flags&layers.IPv4MoreFragments != 0 || c.ip4.FragOffset != 0) {
		c.rep.Fragments++
		slog.Warn("pcap: IP-фрагмент — пропущен", "src", c.ip4.SrcIP, "dst", c.ip4.DstIP)
		return nil
	}
	if !hasLayer(c.decoded, layers.LayerTypeTCP) {
		c.rep.NonTCP++
		return nil
	}
	srcAddr, ok := netip.AddrFromSlice(c.ip4.SrcIP)
	if !ok {
		c.rep.NonTCP++
		return nil
	}
	dstAddr, ok := netip.AddrFromSlice(c.ip4.DstIP)
	if !ok {
		c.rep.NonTCP++
		return nil
	}
	src := endpoint{addr: srcAddr, port: uint16(c.tcp.SrcPort)}
	dst := endpoint{addr: dstAddr, port: uint16(c.tcp.DstPort)}
	return c.onSegment(ts, src, dst, &c.tcp)
}

func hasLayer(decoded []gopacket.LayerType, want gopacket.LayerType) bool {
	for _, lt := range decoded {
		if lt == want {
			return true
		}
	}
	return false
}

// onSegment — TCP-сегмент потока.
func (c *converter) onSegment(ts int64, src, dst endpoint, tcp *layers.TCP) error {
	key := pairOf(src, dst)
	f, known := c.active[key]
	isSYN := tcp.SYN && !tcp.ACK
	isSYNACK := tcp.SYN && tcp.ACK

	switch {
	case !known && isSYN:
		id := uint64(len(c.order) + 1) // детерминизм: по первому пакету потока
		f = &flow{
			id:     id,
			client: src,
			server: dst,
			c2s:    halfStream{id: id, dir: tap.DirCtoS, jw: c.jw, started: true, next: tcp.Seq + 1},
			s2c:    halfStream{id: id, dir: tap.DirStoC, jw: c.jw},
		}
		c.active[key] = f
		c.order = append(c.order, f)
		c.rep.Conns++
		if err := c.jw.ConnOpen(id, src.String(), dst.String(), ts); err != nil {
			return err
		}
	case !known && !isSYN:
		c.rep.SkippedNoSyn++
		if !c.warned[key] {
			c.warned[key] = true
			slog.Warn("pcap: TCP-поток без SYN (захват с середины) — пропущен",
				"src", src.String(), "dst", dst.String())
		}
		return nil
	case known && isSYN:
		// дублирующийся SYN известного потока — игнор без flip клиента
		return nil
	case known && isSYNACK && !f.s2c.started:
		// SYN-ACK засеивает окно сервера (ISN+1)
		f.s2c.started = true
		f.s2c.next = tcp.Seq + 1
		return nil
	}

	// SYN может нести payload (tcp fast open) — данные идут обычным путём
	half := &f.c2s
	if src != f.client {
		half = &f.s2c
	}
	if err := half.push(tcp.Seq, ts, tcp.Payload); err != nil {
		return err
	}

	if tcp.RST {
		return c.closeFlow(f, errors.New("RST "+src.String()))
	}
	if tcp.FIN {
		if err := half.flush(c.rep); err != nil {
			return err
		}
		if src == f.client {
			f.finC = true
		} else {
			f.finS = true
		}
		if f.finC && f.finS {
			return c.closeFlow(f, nil)
		}
	}
	return nil
}

// closeFlow флешит оба направления потока и пишет connClose; поток уходит из
// активных (повторный SYN той же 4-тупы создаст новое соединение).
func (c *converter) closeFlow(f *flow, cause error) error {
	if err := f.c2s.flush(c.rep); err != nil {
		return err
	}
	if err := f.s2c.flush(c.rep); err != nil {
		return err
	}
	f.closed = true
	delete(c.active, pairOf(f.client, f.server))
	return c.jw.ConnClose(f.id, cause)
}
