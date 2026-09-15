// Интеграционный контур шлюза: провода conn + стейдж encode + транспорт +
// persist-актор + фейковый регион/валидатор против живого l2client.
package gateway

import (
	"context"
	"encoding/json"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/udisondev/l2go/internal/conn"
	"github.com/udisondev/l2go/internal/encode"
	"github.com/udisondev/l2go/internal/l2client"
	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/transport"
)

// fakeValidator — скриптованный SessionValidator; gate (если не nil) держит
// валидацию до закрытия — тесты асинхронных окон; verdict переключается
// атомарно (подмена из теста без гонки с актором).
type fakeValidator struct {
	calls   atomic.Int64
	verdict atomic.Bool
	gate    chan struct{}
}

func (f *fakeValidator) ValidateSession(_ context.Context, _ string,
	_, _, _, _ int32) (bool, error) {
	f.calls.Add(1)
	if f.gate != nil {
		<-f.gate
	}
	return f.verdict.Load(), nil
}

func alwaysValid() *fakeValidator {
	f := &fakeValidator{}
	f.verdict.Store(true)
	return f
}

// playerFake — ящик «игрока», зарегистрированный фейком региона.
type playerFake struct {
	box   *transport.Mailbox
	id    transport.EntityID
	token uint64
}

// fakeRegion — фейк региона: читает контрольные письма, на EnterWorld
// отвечает KindConnBind с адресом свежего ящика игрока.
type fakeRegion struct {
	reg     *transport.Registry
	id      transport.EntityID
	token   uint64
	box     transport.Mailbox
	batch   []transport.Envelope
	enter   atomic.Int64
	link    atomic.Int64
	binds   atomic.Int64
	players map[uint64]*playerFake // connID → игрок
}

func newFakeRegion(t *testing.T, reg *transport.Registry) *fakeRegion {
	t.Helper()
	fr := &fakeRegion{reg: reg, players: make(map[uint64]*playerFake)}
	fr.id = reg.Register(&fr.box)
	fr.token = uint64(fr.id)
	if err := fr.box.Claim(fr.token); err != nil {
		t.Fatal(err)
	}
	return fr
}

// drain читает письма региона; autoBind — отвечать ConnBind'ом.
func (fr *fakeRegion) drain(autoBind bool) {
	fr.batch = fr.box.ExtractInto(fr.token, fr.batch)
	fr.box.AckNotify()
	for i := range fr.batch {
		env := &fr.batch[i]
		switch env.Kind {
		case transport.KindEnterWorld:
			fr.enter.Add(1)
			var m enterWorldMsg
			if json.Unmarshal(env.Payload, &m) != nil {
				continue
			}
			p := &playerFake{box: &transport.Mailbox{}}
			p.id = fr.reg.Register(p.box)
			p.token = uint64(p.id)
			if err := p.box.Claim(p.token); err != nil {
				continue // тестовый фейк: свежий ящик всегда клеймится
			}
			if autoBind {
				payload, _ := json.Marshal(connBindMsg{Conn: m.Conn, Entity: p.id})
				fr.reg.Send(transport.Envelope{
					To:      transport.Addr{Entity: env.FromID},
					FromID:  fr.id,
					Kind:    transport.KindConnBind,
					Payload: payload,
				})
				fr.binds.Add(1)
			}
			fr.players[m.Conn] = p
		case transport.KindLinkDead:
			fr.link.Add(1)
		}
	}
}

func (fr *fakeRegion) playerFrames(p *playerFake) []transport.Envelope {
	if p == nil {
		return nil
	}
	batch := p.box.ExtractInto(p.token, nil)
	p.box.AckNotify()
	return batch
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// harness — собранный контур шлюза (два звонка: шлюз и персист — подписки
// раздельные, как в wire-up cmd).
type harness struct {
	reg         *transport.Registry
	stage       *encode.Stage
	connSrv     *conn.Server
	gw          *Gateway
	region      *fakeRegion
	persist     *persist.Actor
	doorbell    chan struct{}
	persistBell chan struct{}
	addr        string
	cancel      context.CancelFunc
}

// newHarness поднимает контур; startPersist=false оставляет персист-актор
// незапущенным (тесты таймаута: запросы копятся в ящике).
func newHarness(t *testing.T, validator SessionValidator, persistTimeout time.Duration, startPersist bool) *harness {
	t.Helper()
	reg := transport.NewRegistry(64)
	stage, err := encode.NewStage(256 << 10)
	if err != nil {
		t.Fatal(err)
	}
	connCfg := conn.Config{
		MaxConns:         16,
		HandshakeTimeout: 5 * time.Second,
		IdleTimeout:      5 * time.Second,
		WriteTimeout:     2 * time.Second,
		KeepAlive:        30 * time.Second,
		FrameCap:         8192,
		EventQueue:       128,
		PerConnEvents:    8,
	}
	connSrv, err := conn.New(connCfg, StageOutbounds{Stage: stage})
	if err != nil {
		t.Fatal(err)
	}
	doorbell := make(chan struct{})
	persistBell := make(chan struct{})
	actor, err := persist.New(persist.Config{
		Dir:          t.TempDir(),
		DrainTimeout: time.Second,
		PanicLimit:   3,
	}, reg, persistBell)
	if err != nil {
		t.Fatal(err)
	}
	region := newFakeRegion(t, reg)
	gw, err := New(Config{
		Persist:        actor.ID(),
		Region:         transport.Addr{Entity: region.id},
		PersistTimeout: persistTimeout,
		InboxCap:       64,
		DrainCap:       8,
		PanicLimit:     3,
		CompletionsCap: 16,
	}, reg, validator, connSrv, stage, doorbell)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go connSrv.Serve(ln)
	go gw.Run(ctx)
	if startPersist {
		go actor.Run(ctx)
	}
	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
		connSrv.Close()
		<-gw.Done()
	})
	return &harness{
		reg: reg, stage: stage, connSrv: connSrv, gw: gw, region: region,
		persist: actor, doorbell: doorbell, persistBell: persistBell,
		addr: ln.Addr().String(), cancel: cancel,
	}
}

// tick — дверной звонок шлюза (и персиста: подписки отдельные).
func (h *harness) tick() {
	for _, ch := range []chan struct{}{h.doorbell, h.persistBell} {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func dialClient(t *testing.T, addr string) *l2client.GameClient {
	t.Helper()
	gc, err := l2client.DialGame(context.Background(), addr, l2client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gc.Close() })
	return gc
}

func testEndpoint() l2client.GameEndpoint {
	return l2client.GameEndpoint{LoginOk1: 1, LoginOk2: 2, PlayOk1: 3, PlayOk2: 4}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("условие не наступило: %s", what)
}

// Полный контур: хендшейк → создание → выбор → EnterWorld-письмо региону →
// bind → стационарный кадр в ящике игрока → ConnClose закрывает коннект.
func TestGatewayFullFlow(t *testing.T) {
	h := newHarness(t, alwaysValid(), 2*time.Second, true)

	gc := dialClient(t, h.addr)
	if err := gc.Handshake(); err != nil {
		t.Fatal(err)
	}
	if entries, err := gc.Auth(testEndpoint(), "tester"); err != nil || len(entries) != 0 {
		t.Fatalf("список на новом аккаунте: %v, %d записей", err, len(entries))
	}
	if err := gc.CreateChar(protocol.CharacterCreateData{Name: "Hero"}); err != nil {
		t.Fatal(err)
	}
	// Свежий список — новым коннектом (канон: CharSelectionInfo идёт в
	// ответ на AuthLogin).
	_ = gc.Close()
	gc = dialClient(t, h.addr)
	if err := gc.Handshake(); err != nil {
		t.Fatal(err)
	}
	entries, err := gc.Auth(testEndpoint(), "tester")
	if err != nil {
		t.Fatalf("повторный список: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "Hero" {
		t.Fatalf("созданный персонаж не в списке: %+v", entries)
	}
	if err := gc.SelectChar(0); err != nil {
		t.Fatal(err)
	}

	runDone := make(chan error, 1)
	go func() { runDone <- gc.Run(context.Background()) }()
	if err := gc.EnterWorld(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "EnterWorld у региона", func() bool {
		h.region.drain(true)
		return h.region.enter.Load() == 1
	})
	waitFor(t, "bind применён шлюзом", func() bool {
		h.tick()
		return h.region.binds.Load() == 1
	})

	if err := gc.MoveToLocation(1, 2, 3, 4, 5, 6, 1); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "стационарный кадр в ящике игрока", func() bool {
		h.tick()
		h.region.drain(false)
		for _, p := range h.region.players {
			for _, env := range h.region.playerFrames(p) {
				if env.Kind == transport.KindClientFrame &&
					len(env.Payload) > 0 && env.Payload[0] == protocol.OpCMoveToLocation {
					return true
				}
			}
		}
		return false
	})

	h.reg.Send(transport.Envelope{
		To:      transport.Addr{Entity: h.gw.id},
		FromID:  h.region.id,
		Kind:    transport.KindConnClose,
		Payload: mustJSON(connRefMsg{Conn: 2}), // второй коннект харнесса (первый — до reconnect)
	})
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run завершился с ошибкой: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ConnClose не закрыл коннект")
	}
}

// Коалесинг MoveToLocation под лавиной: в ящике игрока — один конверт
// (последний побеждает), метрика растёт.
func TestGatewayMoveCoalescing(t *testing.T) {
	h := newHarness(t, alwaysValid(), 2*time.Second, true)

	gc := dialClient(t, h.addr)
	if err := gc.Handshake(); err != nil {
		t.Fatal(err)
	}
	if _, err := gc.Auth(testEndpoint(), "tester"); err != nil {
		t.Fatal(err)
	}
	if err := gc.CreateChar(protocol.CharacterCreateData{Name: "Hero"}); err != nil {
		t.Fatal(err)
	}
	_ = gc.Close()
	gc = dialClient(t, h.addr)
	if err := gc.Handshake(); err != nil {
		t.Fatal(err)
	}
	if _, err := gc.Auth(testEndpoint(), "tester"); err != nil {
		t.Fatal(err)
	}
	if err := gc.SelectChar(0); err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- gc.Run(context.Background()) }()
	if err := gc.EnterWorld(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "EnterWorld у региона", func() bool {
		h.region.drain(true)
		return h.region.enter.Load() == 1
	})
	waitFor(t, "bind применён", func() bool {
		h.tick()
		return h.region.binds.Load() == 1
	})

	for i := 0; i < 8; i++ {
		if err := gc.MoveToLocation(int32(i), 2, 3, 4, 5, 6, 1); err != nil {
			t.Fatal(err)
		}
	}
	var moves int
	waitFor(t, "дрен коалесированного движения", func() bool {
		h.tick()
		h.region.drain(false)
		for _, p := range h.region.players {
			for _, env := range h.region.playerFrames(p) {
				if env.Kind == transport.KindClientFrame &&
					len(env.Payload) > 0 && env.Payload[0] == protocol.OpCMoveToLocation {
					moves++
				}
			}
		}
		return moves >= 1
	})
	if moves != 1 {
		t.Errorf("MoveToLocation-конвертов %d; want 1 (последний побеждает)", moves)
	}
	if st := h.gw.Stats(); st.Coalesced == 0 {
		t.Error("метрика коалесинга не выросла")
	}
}

// Вытеснение: второй AuthLogin с верными ключами закрывает первого;
// неверные ключи живую сессию не рвут (неаутентифицированный kick невозможен).
func TestGatewayDisplacement(t *testing.T) {
	valid := alwaysValid()
	h := newHarness(t, valid, 2*time.Second, true)

	first := dialClient(t, h.addr)
	if err := first.Handshake(); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Auth(testEndpoint(), "tester"); err != nil {
		t.Fatal(err)
	}

	// Неверные ключи: валидатор отвечает false — первый жив.
	evil := dialClient(t, h.addr)
	if err := evil.Handshake(); err != nil {
		t.Fatal(err)
	}
	valid.verdict.Store(false)
	if _, err := evil.Auth(testEndpoint(), "tester"); err == nil {
		t.Fatal("второй вход с неверными ключами прошёл")
	}
	valid.verdict.Store(true)
	if err := first.CreateChar(protocol.CharacterCreateData{Name: "Alive"}); err != nil {
		t.Fatalf("первый коннект повреждён ложной попыткой: %v", err)
	}

	// Верные ключи: вытеснение — первый закрыт.
	second := dialClient(t, h.addr)
	if err := second.Handshake(); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Auth(testEndpoint(), "tester"); err != nil {
		t.Fatalf("второй вход: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := first.CreateChar(protocol.CharacterCreateData{Name: "Alive2"}); err != nil {
			break // коннект вытеснен — стадия падает на записи/чтении
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st := h.gw.Stats(); st.Displaced == 0 {
		t.Error("метрика вытеснения не выросла")
	}
}

// Закрытие коннекта в окне валидации: бинд не возникает, повторный вход
// проходит (зомби-бинд невозможен).
func TestGatewayCloseDuringValidation(t *testing.T) {
	gate := make(chan struct{})
	v := alwaysValid()
	v.gate = gate
	h := newHarness(t, v, 2*time.Second, true)

	gc := dialClient(t, h.addr)
	if err := gc.Handshake(); err != nil {
		t.Fatal(err)
	}
	authErr := make(chan error, 1)
	go func() {
		_, err := gc.Auth(testEndpoint(), "tester")
		authErr <- err
	}()
	waitFor(t, "валидация в полёте", func() bool { return v.calls.Load() == 1 })
	_ = gc.Close() // обрыв в окне валидации
	close(gate)
	<-authErr

	// Поздний valid=true на закрытый коннект: бинда нет.
	waitFor(t, "завершение обработано", func() bool {
		h.tick()
		return true
	})
	if st := h.gw.Stats(); st.Bound != 0 {
		t.Fatalf("бинд возник на закрытом коннекте: %+v", st)
	}

	// Повторный вход проходит.
	again := dialClient(t, h.addr)
	if err := again.Handshake(); err != nil {
		t.Fatal(err)
	}
	if _, err := again.Auth(testEndpoint(), "tester"); err != nil {
		t.Fatalf("повторный вход после окна: %v", err)
	}
}

// Таймаут персист-ответа (актор не тикает): GSLoginFail + close; поздний
// ответ — dead-letter.
func TestGatewayPersistTimeout(t *testing.T) {
	h := newHarness(t, alwaysValid(), 150*time.Millisecond, false)

	// Шлюз жив, персист-актор не запускается (его звонок не тикает).
	gc := dialClient(t, h.addr)
	if err := gc.Handshake(); err != nil {
		t.Fatal(err)
	}
	if _, err := gc.Auth(testEndpoint(), "tester"); err == nil {
		t.Fatal("вход прошёл без ответа персиста")
	}
	if st := h.gw.Stats(); st.Bound != 0 {
		t.Errorf("бинд при таймауте персиста: %+v", st)
	}
	// Поздний ответ на мёртвое ожидание — dead-letter, кадра не рождает.
	late, _ := persist.EncodeReply(persist.Reply{Op: persist.OpCharList, Corr: 999, OK: true})
	h.reg.Send(transport.Envelope{
		To:      transport.Addr{Entity: h.gw.id},
		FromID:  h.persist.ID(),
		Kind:    transport.KindPersistReply,
		Payload: late,
	})
	waitFor(t, "поздний ответ обработан", func() bool {
		h.tick()
		return h.gw.Stats().DeadLetters >= 1
	})
}
