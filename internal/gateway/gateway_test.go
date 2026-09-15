// Интеграционный контур шлюза: провода conn + стейдж encode + транспорт +
// persist-актор + фейковый регион/валидатор против живого l2client.
package gateway

import (
	"context"
	"encoding/json"
	"net"
	"strings"
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
	entries, err := gc.CreateChar(protocol.CharacterCreateData{Name: "Hero"})
	if err != nil {
		t.Fatal(err)
	}
	// Канон: свежий CharSelectionInfo следует за CharCreateOk.
	if len(entries) != 1 || entries[0].Name != "Hero" {
		t.Fatalf("свежий список после создания: %+v", entries)
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
		Payload: mustJSON(connRefMsg{Conn: 1}), // первый коннект харнесса
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
	if _, err := gc.CreateChar(protocol.CharacterCreateData{Name: "Hero"}); err != nil {
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
	if _, err := first.CreateChar(protocol.CharacterCreateData{Name: "Alive"}); err != nil {
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
		if _, err := first.CreateChar(protocol.CharacterCreateData{Name: "Alive2"}); err != nil {
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

// Recover-политика: шов инъекции паники — recover + failed-счётчик; серия
// PanicLimit — паника наружу (let-it-crash).
func TestGatewayRecoverPolicy(t *testing.T) {
	h := newHarness(t, alwaysValid(), 2*time.Second, true)
	g := h.gw
	g.cfg.PanicLimit = 3
	gc := &gconn{id: 1, phase: phHandshake, key: keyFixtureGW}
	g.conns[1] = gc
	g.testPanicOn = 0x77

	g.safeCall(func() { g.onEvent(conn.Event{Conn: 1, Frame: []byte{0x77}}) })
	g.safeCall(func() { g.onEvent(conn.Event{Conn: 1, Frame: []byte{0x77}}) })
	if st := g.Stats(); st.Panics != 2 || st.Failures != 2 {
		t.Fatalf("после двух паник Stats = %+v; want Panics=2 Failures=2", st)
	}
	g.safeCall(func() { g.onEvent(conn.Event{Conn: 1, Frame: []byte{0x78}}) }) // без паники — серия обнулена
	if st := g.Stats(); st.Panics != 2 {
		t.Fatalf("успешный шаг не обнулил серию: %+v", st)
	}
	g.safeCall(func() { panic("первая серии") })
	g.safeCall(func() { panic("вторая серии") })
	defer func() {
		if recover() == nil {
			t.Error("серия PanicLimit не перешла в панику наружу (let-it-crash)")
		}
	}()
	g.safeCall(func() { panic("третья серии — наружу") })
}

var keyFixtureGW = [8]byte{1, 2, 3, 4, 5, 6, 7, 8}

// Смешанный регистр аккаунта — один бинд, вторая сущность не рождается
// (оба входа сходятся в один нормализованный ключ).
func TestGatewayAccountCaseFold(t *testing.T) {
	h := newHarness(t, alwaysValid(), 2*time.Second, true)

	upper := dialClient(t, h.addr)
	if err := upper.Handshake(); err != nil {
		t.Fatal(err)
	}
	if _, err := upper.Auth(testEndpoint(), "Tester"); err != nil {
		t.Fatalf("вход «Tester»: %v", err)
	}
	waitFor(t, "бинд верхнего регистра", func() bool { return h.gw.Stats().Bound == 1 })

	lower := dialClient(t, h.addr)
	if err := lower.Handshake(); err != nil {
		t.Fatal(err)
	}
	// «tester» вытесняет «Tester» (один ключ) — не второй бинд.
	if _, err := lower.Auth(testEndpoint(), "tester"); err != nil {
		t.Fatalf("вход «tester»: %v", err)
	}
	waitFor(t, "вытеснение того же ключа", func() bool { return h.gw.Stats().Displaced == 1 })
	if st := h.gw.Stats(); st.Bound != 1 {
		t.Fatalf("Bound = %d; want 1 (регистр сведён)", st.Bound)
	}
}

// ok=false ответа персиста: CharList → GSLoginFail+закрытие; create-ветка →
// CharCreateFail по коду причины.
func TestGatewayPersistRefusal(t *testing.T) {
	h := newHarness(t, alwaysValid(), 2*time.Second, false)

	gc := dialClient(t, h.addr)
	if err := gc.Handshake(); err != nil {
		t.Fatal(err)
	}
	authErr := make(chan error, 1)
	go func() {
		_, err := gc.Auth(testEndpoint(), "tester")
		authErr <- err
	}()
	// Живое ожидание list: подменяем ответ отказом (шов отказывающего
	// актора). Corr=1: первый коннект харнесса.
	_ = keyFixtureGW
	refuse, _ := persist.EncodeReply(persist.Reply{Op: persist.OpCharList, Corr: 1, Code: persist.CodeIO, Err: "io"})
	h.reg.Send(transport.Envelope{
		To:      transport.Addr{Entity: h.gw.id},
		FromID:  h.persist.ID(),
		Kind:    transport.KindPersistReply,
		Payload: refuse,
	})
	if err := <-authErr; err == nil {
		t.Fatal("отказ списка не отверг вход")
	}
}

// Маппинг кодов персиста → причины канона (юнит).
func TestCreateFailReasonMapping(t *testing.T) {
	cases := []struct {
		code string
		want protocol.CharCreateFailReason
	}{
		{persist.CodeCharLimit, protocol.CharCreateReasonTooManyCharacters},
		{persist.CodeNameTaken, protocol.CharCreateReasonNameAlreadyExists},
		{persist.CodeNameInvalid, protocol.CharCreateReasonIncorrectName},
		{persist.CodeAppearance, protocol.CharCreateReasonCreationFailed},
		{"", protocol.CharCreateReasonCreationFailed},
	}
	for _, c := range cases {
		if got := createFailReason(c.code, "текст"); got != c.want {
			t.Errorf("createFailReason(%q) = %d; want %d", c.code, got, c.want)
		}
	}
}

// Churn connect+RST при занятом акторе: записи коннектов не накапливаются
// (tombstone — только для необработанных OnOpen; разбор актором — без него).
func TestGatewayChurnNoGrowth(t *testing.T) {
	h := newHarness(t, alwaysValid(), 2*time.Second, true)
	g := h.gw

	// Путь 1: actor-initiated teardown (чужая версия) — без tombstone.
	for i := 0; i < 8; i++ {
		r := dialRaw(t, h.addr)
		wire := make([]byte, protocol.ProtocolVersionSize)
		protocol.WriteProtocolVersion(wire, 999)
		r.write(wire)
		if f := r.readFrame(2 * time.Second); f == nil {
			t.Fatal("KeyPacket(result=0) не получен")
		}
	}
	// Слоты возвращают потребители закрытий: ждём полного дрена проводов;
	// после него актор разобрал все закрытия и карты больше не мутирует
	// (Release — после обработки, happens-before по атомику слотов).
	waitFor(t, "слоты чурна освобождены", func() bool {
		return h.connSrv.Stats().Conns == 0
	})
	// Путь 2: обрыв до обработки OnOpen — tombstone ставится и гасится
	// поздним OnOpen (закрытие раньше открытия).
	for i := 0; i < 8; i++ {
		dialRaw(t, h.addr).conn.Close() // RST немедленно
	}
	waitFor(t, "слоты RST-чурна освобождены", func() bool {
		return h.connSrv.Stats().Conns == 0
	})
	if len(g.conns) > 2 {
		t.Errorf("записей коннектов %d — рост от чурна", len(g.conns))
	}
	if len(g.closedUnopened) > 0 || len(g.tornDown) > 0 {
		t.Errorf("память интерливингов: closedUnopened=%d tornDown=%d",
			len(g.closedUnopened), len(g.tornDown))
	}
}

// Восьмой персонаж — отказ TooManyCharacters (лимит домена).
func TestGatewayEighthCharRejected(t *testing.T) {
	h := newHarness(t, alwaysValid(), 2*time.Second, true)
	gc := dialClient(t, h.addr)
	if err := gc.Handshake(); err != nil {
		t.Fatal(err)
	}
	if _, err := gc.Auth(testEndpoint(), "tester"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		if _, err := gc.CreateChar(protocol.CharacterCreateData{Name: "Hero" + itoaGW(i)}); err != nil {
			t.Fatalf("создание %d: %v", i+1, err)
		}
	}
	_, err := gc.CreateChar(protocol.CharacterCreateData{Name: "Eight"})
	if err == nil {
		t.Fatal("восьмой персонаж принят")
	}
	if got := err.Error(); !strings.Contains(got, "0x01") {
		t.Errorf("причина восьмого = %q; want TooManyCharacters (0x01)", got)
	}
}

func itoaGW(i int) string { return string(rune('a' + i)) }
