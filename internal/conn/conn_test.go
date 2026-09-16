package conn

import (
	"bytes"
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/udisondev/l2go/internal/crypto"
)

// fakeOutbound — программируемый шов исходящего для тестов без encode.
// Контракт стейджа: слэбы prev возвращаются владельцу к следующему Take,
// Recycle замыкает только последний батч writeLoop — фейк считает оба пути.
type fakeOutbound struct {
	mu        sync.Mutex
	queue     [][]byte
	close     bool
	wake      chan struct{}
	recycld   atomic.Int64
	reclaimed atomic.Int64
	recycled  atomic.Bool
}

func (f *fakeOutbound) Recycle(prev [][]byte) {
	f.recycled.Store(true)
	f.recycld.Add(int64(len(prev)))
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
	f.reclaimed.Add(int64(len(prev))) // контракт: prev возвращён владельцу
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
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Error("молчаливый коннект жив после HandshakeTimeout")
	}
}

// Пресессионный молчун: после open и SetReadMode(presession) дедлайн —
// IdleTimeout без перезавода; молчание рвёт коннект именно по таймауту.
func TestPresessionIdleSilentBreak(t *testing.T) {
	out := newFakeOutbounds()
	addr, s := startServer(t, testConfig(), out)

	conn := dial(t, addr)
	ev := recvEvent(t, s.Events()) // открытие
	ev.Done()
	s.SetReadMode(ev.Conn, ModePresession)

	// Тайминг-инвариант, не синхронизация: молчание (600мс) превышает
	// IdleTimeout (250мс) с запасом на медленное железо и -race.
	time.Sleep(600 * time.Millisecond)
	ce := recvClose(t, s.Closes())
	ce.Release()
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Error("молчаливый коннект жив после IdleTimeout")
	}
	if st := s.Stats(); st.ClosedTimeout != 1 {
		t.Errorf("ClosedTimeout = %d; want 1 (разрыв именно по idle, не по рукопожатию)", st.ClosedTimeout)
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
	// Со второго кадра поток шифруется (канон): события несут расшифрованные
	// ридэром байты — сверяем содержимое честно.
	enc := crypto.NewGameCrypt(ev.Key)
	enc.Enable()
	sendCrypt := func(body []byte) {
		t.Helper()
		wire := append([]byte(nil), body...)
		if err := enc.Encrypt(wire); err != nil {
			t.Fatal(err)
		}
		sendFrame(t, conn, wire)
	}

	// Кадры перезаводят idle. Фальсификация: без перезавода дедлайн истёк
	// бы к t=IdleTimeout(200мс) от SetReadMode; с перезаводом — к t≈320мс
	// от последнего кадра (t≈200мс). 4-й кадр на t≈320мс (пауза 120мс
	// после третьего, сон-каданс — только между кадрами) жив только при
	// перезаводе; запас до дедлайна 80мс.
	for i := 0; i < 3; i++ {
		sendCrypt([]byte{0x01, byte(i)})
		e := recvEvent(t, s.Events())
		if e.Open {
			t.Fatal("ожидался кадр, пришло открытие")
		}
		e.Done()
		if i < 2 {
			time.Sleep(100 * time.Millisecond) // каданс кадров, не синхронизация
		}
	}
	time.Sleep(120 * time.Millisecond) // молчание внутри окна перезавода
	sendCrypt([]byte{0x01, 0x63})
	fourth := recvEvent(t, s.Events())
	if fourth.Open || fourth.Frame[1] != 0x63 {
		t.Fatalf("живость после паузы: коннект разорван без перезавода idle: %+v", fourth)
	}
	fourth.Done()
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

	// Дольше и IdleTimeout, и HandshakeTimeout: заблокированный Read с
	// дедлайном предыдущей фазы был бы разорван (блокер F47). Один Write
	// не ловит FIN (RST приходит только второй операцией) — оракул метрики:
	// коннект жив и ни одна причина закрытия не сработала.
	time.Sleep(700 * time.Millisecond) // тайминг-инвариант: молчание > дедлайнов, не синхронизация
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatalf("первая запись после молчания: %v (разорван)", err)
	}
	st := s.Stats()
	if st.Conns != 1 {
		t.Fatalf("Conns = %d; want 1 (молчун жив)", st)
	}
	if st.ClosedEOF+st.ClosedTimeout+st.ClosedProtocol+st.ClosedFrameCap+
		st.ClosedOverflow+st.ClosedCrypto+st.ClosedKeyGen != 0 {
		t.Fatalf("молчун разорван: %+v", st)
	}
}

func TestMaxConnsReject(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConns = 1
	out := newFakeOutbounds()
	addr, s := startServer(t, cfg, out)

	// dial с Cleanup: первый коннект занимает слот и держит его до конца теста.
	dial(t, addr)
	recvEvent(t, s.Events())

	second, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	// Отказ — быстрый EOF/RST; таймаут означал бы «принят и молчит» (лимит
	// сломан) — различаем класс ошибки, а не просто «есть ошибка».
	_ = second.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := second.Read(make([]byte, 1)); err == nil ||
		errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("второй коннект сверх лимита обслуживается: err=%v", err)
	}
	if got := s.Stats().Conns; got != 1 {
		t.Errorf("Conns = %d; want 1 (второй не занял слот)", got)
	}
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
	if st := s.Stats(); st.ClosedFrameCap != 1 {
		t.Errorf("ClosedFrameCap = %d; want 1 (разрыв именно по капу, не по дедлайну)", st.ClosedFrameCap)
	}
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
	// rec — полный валидный кадр (заявленная длина совпадает с записью:
	// лишний байт оставлял бы хвост, склеивающийся в мусорные кадры).
	for i := 0; i < 10; i++ {
		rec := []byte{3, 0, 0x02}
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
	if _, err := conn.Read(make([]byte, 1)); err == nil ||
		errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("сокет штормиста жив после переполнения: err=%v", err)
	}
	st := s.Stats()
	if st.ClosedOverflow != 1 {
		t.Errorf("ClosedOverflow = %d; want 1 (разрыв именно overflow, не дедлайн)", st.ClosedOverflow)
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
	// Close-вердикт должен разорвать сокет сразу после флеша — раньше
	// handshake-дедлайна; таймаут чтения означал бы игнор вердикта.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil ||
		errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("сокет жив после close-after-flush: err=%v", err)
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

// F2: кадр в момент Close — дедлайн не воскресает, Close не буксует
// (интерливинг arm↔SetDeadline(past) под dlMu).
func TestConnFrameAtCloseNoDeadlineRevive(t *testing.T) {
	cfg := testConfig()
	cfg.IdleTimeout = 3 * time.Second
	cfg.HandshakeTimeout = 3 * time.Second
	out := newFakeOutbounds()
	addr, s := startServer(t, cfg, out)

	c := dial(t, addr)
	ev := recvEvent(t, s.Events())
	s.SetReadMode(ev.Conn, ModePresession)
	// Кадр в момент остановки: ридёр мог перевооружить дедлайн ПОСЛЕ тычка
	// Close в прошлое — тогда wg.Wait буксовал бы до IdleTimeout.
	done := make(chan struct{})
	go func() { defer close(done); s.Close() }()
	_, _ = c.Write([]byte{1, 0, 'x'}) // укороченный кадр-хвост
	select {
	case <-done:
	case <-time.After(cfg.IdleTimeout - 500*time.Millisecond):
		t.Fatal("Close буксует: дедлайн воскрес после тычка в прошлое")
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 8)
	if _, err := c.Read(buf); err == nil {
		t.Error("сокет жив после Close")
	}
}

// F3: Close до первого кадра — readLoop не вооружает будущий дедлайн,
// wg.Wait не буксует до HandshakeTimeout.
func TestConnCloseBeforeFirstFrameNoArm(t *testing.T) {
	cfg := testConfig()
	cfg.HandshakeTimeout = 2 * time.Second
	out := newFakeOutbounds()
	addr, s := startServer(t, cfg, out)

	c := dial(t, addr)
	ev := recvEvent(t, s.Events())
	// Сразу SetReadMode(stationary) до какого-либо кадра, затем Close:
	// ранний arm не должен пережить остановку.
	s.SetReadMode(ev.Conn, ModeStationary)

	start := time.Now()
	s.Close()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Close буксовал %s (дедлайн воскрес)", elapsed)
	}
	_ = c.Close()
}

// Шов Recycle (P3.7b): writeLoop возвращает финальный батч на обоих выходах
// — close-выход и ошибка записи; порядок Recycle→Unregister гарантирован
// стеком defer'ов. Фейк считает; реальный пул проверяется в encode.
func TestWriteLoopRecyclesOnBothExits(t *testing.T) {
	t.Parallel()
	// close-выход: живой сервер + клиент до стационара, закрытие с флешом.
	cfg := testConfig()
	out := newFakeOutbounds()
	addr, s := startServer(t, cfg, out)
	c := dial(t, addr)
	ev := recvEvent(t, s.Events())
	s.SetReadMode(ev.Conn, ModeStationary)
	fake := out.outs[ev.Conn]
	fake.push([]byte{1, 0, 'x'})
	s.CloseAfterFlush(ev.Conn)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !fake.recycled.Load() {
		time.Sleep(2 * time.Millisecond)
	}
	if !fake.recycled.Load() {
		t.Fatal("close-выход: Recycle не вызван")
	}
	// Слэб вернулся одним из путей контракта: финальным Recycle или
	// возвратом prev к следующему Take (батч дописан до close-флага).
	if n := fake.recycld.Load() + fake.reclaimed.Load(); n == 0 {
		t.Fatal("close-выход: пуш-нутый слэб не вернулся ни Recycle, ни Take")
	}
	_ = c.Close()

	// error-выход: writeLoop напрямую с падающим conn (внутренний доступ
	// к непортированным частям — пакетный тест).
	stub := &errConn{}
	var o2 fakeOutbound
	o2.wake = make(chan struct{}, 1)
	o2.push([]byte{1, 0, 'x'})
	done := make(chan struct{})
	go func() { defer close(done); (&Server{}).writeLoop(stub, &o2) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("writeLoop не вышел на ошибке записи")
	}
	if n := o2.recycld.Load(); n == 0 {
		t.Fatal("error-выход: Recycle не вернул финальный батч")
	}
}

// errConn — net.Conn, чей Write всегда ошибочен.
type errConn struct{}

func (*errConn) Write([]byte) (int, error)        { return 0, os.ErrClosed }
func (*errConn) Read([]byte) (int, error)         { return 0, os.ErrClosed }
func (*errConn) Close() error                     { return nil }
func (*errConn) SetDeadline(time.Time) error      { return nil }
func (*errConn) SetReadDeadline(time.Time) error  { return nil }
func (*errConn) SetWriteDeadline(time.Time) error { return nil }
func (*errConn) LocalAddr() net.Addr              { return nil }
func (*errConn) RemoteAddr() net.Addr             { return nil }
