package conn

import (
	"bytes"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/l2go/internal/crypto"
)

// fakeOutbound — программируемый шов исходящего для тестов без encode.
type fakeOutbound struct {
	mu    sync.Mutex
	queue [][]byte
	close bool
	wake  chan struct{}
}

func (f *fakeOutbound) push(b []byte) {
	f.mu.Lock()
	f.queue = append(f.queue, b)
	f.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

func (f *fakeOutbound) markClose() {
	f.mu.Lock()
	f.close = true
	f.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

func (f *fakeOutbound) Take(prev [][]byte) ([][]byte, bool) {
	for {
		f.mu.Lock()
		if len(f.queue) > 0 || f.close {
			next := append(prev[:0], f.queue...)
			f.queue = f.queue[:0]
			close := f.close
			f.mu.Unlock()
			return next, close
		}
		f.mu.Unlock()
		<-f.wake
	}
}

// fakeOutbounds — фабрика: одна запись на коннект.
type fakeOutbounds struct {
	mu   sync.Mutex
	outs map[ConnID]*fakeOutbound
}

func newFakeOutbounds() *fakeOutbounds {
	return &fakeOutbounds{outs: make(map[ConnID]*fakeOutbound)}
}

func (f *fakeOutbounds) Register(id ConnID, key [8]byte) Outbound {
	o := &fakeOutbound{wake: make(chan struct{}, 1)}
	f.mu.Lock()
	f.outs[id] = o
	f.mu.Unlock()
	return o
}

func (f *fakeOutbounds) Unregister(id ConnID) {
	f.mu.Lock()
	delete(f.outs, id)
	f.mu.Unlock()
}

func (f *fakeOutbounds) Close(id ConnID) {
	f.mu.Lock()
	o := f.outs[id]
	f.mu.Unlock()
	if o != nil {
		o.markClose()
	}
}

func (f *fakeOutbounds) get(id ConnID) *fakeOutbound {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.outs[id]
}

// testConfig — быстрые таймауты; отдельные тесты переопределяют поля.
func testConfig() Config {
	return Config{
		MaxConns:         16,
		HandshakeTimeout: 500 * time.Millisecond,
		IdleTimeout:      250 * time.Millisecond,
		WriteTimeout:     time.Second,
		KeepAlive:        30 * time.Second,
		FrameCap:         8192,
		EventQueue:       64,
		PerConnEvents:    4,
	}
}

// startServer поднимает сервер на ephemeral-порте иозвращает адрес и остановку.
func startServer(t *testing.T, cfg Config, out *fakeOutbounds) (addr string, s *Server) {
	t.Helper()
	s, err := New(cfg, out)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve(ln) }()
	t.Cleanup(func() {
		_ = ln.Close()
		s.Close()
	})
	return ln.Addr().String(), s
}

// dial подключает тестового клиента.
func dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// sendFrame пишет проводную запись [длина LE][тело].
func sendFrame(t *testing.T, conn net.Conn, body []byte) {
	t.Helper()
	rec := make([]byte, 2+len(body))
	rec[0] = byte(len(rec))
	rec[1] = byte(len(rec) >> 8)
	copy(rec[2:], body)
	if _, err := conn.Write(rec); err != nil {
		t.Fatalf("запись кадра: %v", err)
	}
}

// recvEvent ждёт событие с таймаутом.
func recvEvent(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case ev := <-events:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("событие не пришло за 2 с")
		return Event{}
	}
}

func recvClose(t *testing.T, closes <-chan ClosedEvent) ClosedEvent {
	t.Helper()
	select {
	case ev := <-closes:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("закрытие не пришло за 2 с")
		return ClosedEvent{}
	}
}

func TestOpenFrameCloseLifecycle(t *testing.T) {
	out := newFakeOutbounds()
	addr, s := startServer(t, testConfig(), out)

	conn := dial(t, addr)
	sendFrame(t, conn, []byte{0x2e, 0x00, 0x00, 0x00}) // ProtocolVersion plaintext

	ev := recvEvent(t, s.Events())
	if !ev.Open {
		t.Fatalf("первое событие — не открытие: %+v", ev)
	}
	key := ev.Key
	ev.Done()

	ev = recvEvent(t, s.Events())
	if ev.Open || ev.Conn == 0 {
		t.Fatalf("второе событие — не кадр: %+v", ev)
	}
	if want := []byte{0x2e, 0x00, 0x00, 0x00}; !bytes.Equal(ev.Frame, want) {
		t.Errorf("кадр = % x; want % x", ev.Frame, want)
	}
	ev.Done()

	// Второй кадр — криптованный клиентом с ключом открытия.
	enc := crypto.NewGameCrypt(key)
	enc.Enable()
	secret := []byte{0x55, 0x66, 0x77}
	body := bytes.Clone(secret)
	if err := enc.Encrypt(body); err != nil {
		t.Fatal(err)
	}
	sendFrame(t, conn, body)
	ev = recvEvent(t, s.Events())
	if !bytes.Equal(ev.Frame, secret) {
		t.Errorf("расшифрованный кадр = % x; want % x", ev.Frame, secret)
	}
	ev.Done()

	_ = conn.Close()
	ce := recvClose(t, s.Closes())
	if ce.Conn != ev.Conn || !ce.OpenSent {
		t.Errorf("ClosedEvent = %+v; want Conn=%d OpenSent=true", ce, ev.Conn)
	}
	ce.Release()
	if got := s.slots.Load(); got != 0 {
		t.Errorf("слоты после Release = %d; want 0", got)
	}
}

func TestHandshakeAbsoluteTimeout(t *testing.T) {
	cfg := testConfig()
	cfg.HandshakeTimeout = 150 * time.Millisecond
	out := newFakeOutbounds()
	addr, s := startServer(t, cfg, out)

	conn := dial(t, addr) // молчим
	ce := recvClose(t, s.Closes())
	ce.Release()
	_ = conn
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Error("молчаливый коннект жив после HandshakeTimeout")
	}
}

func TestPresessionIdlePerFrame(t *testing.T) {
	cfg := testConfig()
	cfg.IdleTimeout = 200 * time.Millisecond
	out := newFakeOutbounds()
	addr, s := startServer(t, cfg, out)

	conn := dial(t, addr)
	ev := recvEvent(t, s.Events()) // открытие
	ev.Done()
	s.SetReadMode(ev.Conn, ModePresession)

	// Кадры перезаводят idle: три кадра с интервалом короче таймаута.
	for i := 0; i < 3; i++ {
		sendFrame(t, conn, []byte{0x01, byte(i)})
		ev := recvEvent(t, s.Events())
		if ev.Open {
			t.Fatal("ожидался кадр, пришло открытие")
		}
		ev.Done()
		time.Sleep(100 * time.Millisecond)
	}
}

func TestStationarySilentAlive(t *testing.T) {
	cfg := testConfig()
	cfg.IdleTimeout = 200 * time.Millisecond
	out := newFakeOutbounds()
	addr, s := startServer(t, cfg, out)

	conn := dial(t, addr)
	ev := recvEvent(t, s.Events())
	ev.Done()
	s.SetReadMode(ev.Conn, ModeStationary)

	time.Sleep(450 * time.Millisecond) // дольше IdleTimeout
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatal("стационарный молчун разорван: записать нельзя")
	}
}

func TestMaxConnsReject(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConns = 1
	out := newFakeOutbounds()
	addr, s := startServer(t, cfg, out)

	first := dial(t, addr)
	recvEvent(t, s.Events()) // первый занял слот

	second, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	_ = second.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := second.Read(make([]byte, 1)); err == nil {
		t.Error("второй коннект сверх лимита обслуживается")
	}
	_ = first
}

func TestFrameCapBreak(t *testing.T) {
	cfg := testConfig()
	cfg.FrameCap = 64
	out := newFakeOutbounds()
	addr, s := startServer(t, cfg, out)

	conn := dial(t, addr)
	recvEvent(t, s.Events())
	big := make([]byte, 200)
	sendFrame(t, conn, big)
	ce := recvClose(t, s.Closes())
	ce.Release()
}

// Close-on-overflow своего под-лимита: потребитель не выгребает события —
// ридэр рвёт свой сокет после PerConnEvents невыгребенных; соседний коннект
// не страдает (под-лимит изолирует виновника).
func TestPerConnOverflowOwnBreak(t *testing.T) {
	cfg := testConfig()
	cfg.PerConnEvents = 2
	out := newFakeOutbounds()
	addr, s := startServer(t, cfg, out)

	neighbour := dial(t, addr)
	neighbourOpen := recvEvent(t, s.Events())
	neighbourOpen.Done()
	defer func() { _ = neighbour.Close() }()

	conn := dial(t, addr)
	recvEvent(t, s.Events()) // открытие (не Done — слоты заняты)

	// Обрыв записи на середине лавины — ожидаем: сервер рвёт виновника.
	for i := 0; i < 10; i++ {
		rec := []byte{3, 0, 0x02, byte(i)}
		if _, err := conn.Write(rec); err != nil {
			break
		}
	}
	ce := recvClose(t, s.Closes())
	if !ce.OpenSent {
		t.Error("OpenSent = false; want true")
	}
	ce.Release()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Error("сокет штормиста жив после переполнения своего под-лимита")
	}
	// Сосед жив: пишет и читает после разрыва виновника.
	if _, err := neighbour.Write([]byte("alive")); err != nil {
		t.Fatal("соседний коннект пострадал от шторма виновника")
	}
}

// Исходящий путь: батч пишется в сокет, close-after-flush рвёт после записи.
func TestWriteLoopBatchAndClose(t *testing.T) {
	out := newFakeOutbounds()
	addr, s := startServer(t, testConfig(), out)

	conn := dial(t, addr)
	ev := recvEvent(t, s.Events())
	ev.Done()

	ob := out.get(ev.Conn)
	if ob == nil {
		t.Fatal("исходящая запись не зарегистрирована")
	}
	ob.push([]byte{3, 0, 1}) // [len=3][0x01]
	ob.markClose()

	buf := make([]byte, 3)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := ioReadFull(conn, buf); err != nil {
		t.Fatalf("чтение батча: %v", err)
	}
	if !bytes.Equal(buf, []byte{3, 0, 1}) {
		t.Errorf("батч = % x; want 03 00 01", buf)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Error("сокет жив после close-after-flush")
	}
	ce := recvClose(t, s.Closes())
	ce.Release()
}

func ioReadFull(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
