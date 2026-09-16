// Package gateway — пограничный слой входа игрового процесса: предсессионная
// стейт-машина коннекта (хендшейк → AuthLogin → CharList → CharCreate →
// CharSelect → EnterWorld), назначение стационарных кадров в порции тика
// (слепой push в ящик игрока), полигон «один онлайн». Единственное место
// настенных часов процесса; письма — только слепым push через транспорт.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/udisondev/l2go/internal/conn"
	"github.com/udisondev/l2go/internal/encode"
	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/transport"
)

// SessionValidator — шов сверки ключей сессии с LoginServer; реализация —
// loginlink.Client (wire-up в cmd, шлюз loginlink не импортирует).
// err (LS недоступен) ≠ valid=false — политика fail-closed у вызывающего.
type SessionValidator interface {
	ValidateSession(ctx context.Context, account string,
		loginOk1, loginOk2, playOk1, playOk2 int32) (bool, error)
}

// phase — фаза стейт-машины коннекта.
type phase uint8

const (
	phHandshake  phase = iota // ждём ProtocolVersion
	phAuth                    // ждём AuthLogin (валидация не в полёте)
	phValidating              // ValidateSession в полёте
	phList                    // CharList/NewChar/CharacterCreate/CharacterSelect
	phCreating                // запрос создания в полёте
	phSelected                // CharSelected отослан, ждём EnterWorld
	phWorld                   // стационарная фаза
)

// pending — ожидание персист-ответа: op для сверки ответа, seq — монотонный
// номер ожидания коннекта (сверка таймаута), timer снимается ответом/teardown.
type pending struct {
	op    string
	seq   uint64
	timer *time.Timer
}

// gconn — состояние коннекта; владелец — актор шлюза.
type gconn struct {
	id        conn.ConnID
	key       [8]byte
	phase     phase
	account   string // нормализованный; с phList
	sessionID int32  // playOk1 сессии (SessionID списков/выбора — канон)
	waitSeq   uint64 // монотонный номер ожидания персиста
	chars     []persist.CharRecord
	char      *persist.CharRecord // выбранный (phSelected/phWorld)
	entity    transport.EntityID  // после KindConnBind
	cryptOn   bool                // марка крипты после KeyPacket
	pending   *pending
	inbox     [][]byte // стационарные кадры до тика
}

// Config — конфигурация шлюза; нулевые лимиты запрещены.
type Config struct {
	Persist        transport.EntityID // адрес persist-актора
	Region         transport.Addr     // контрольный ящик региона
	PersistTimeout time.Duration      // ожидание ответа персиста
	InboxCap       int                // стационарных кадров на коннект
	DrainCap       int                // кадров/коннект/тик
	PanicLimit     int                // серия паник до let-it-crash
	CompletionsCap int                // канал завершений (≥ MaxConns)
}

func (c Config) validate() error {
	switch {
	case c.Persist == 0:
		return fmt.Errorf("gateway: адрес персиста не задан")
	case c.Region.Entity == 0:
		return fmt.Errorf("gateway: адрес региона не задан")
	case c.PersistTimeout <= 0:
		return fmt.Errorf("gateway: PersistTimeout = %s: нулевые таймауты запрещены", c.PersistTimeout)
	case c.InboxCap <= 0:
		return fmt.Errorf("gateway: InboxCap = %d: нулевые капы запрещены", c.InboxCap)
	case c.DrainCap <= 0:
		return fmt.Errorf("gateway: DrainCap = %d: нулевые капы запрещены", c.DrainCap)
	case c.PanicLimit <= 0:
		return fmt.Errorf("gateway: PanicLimit = %d: нулевые лимиты запрещены", c.PanicLimit)
	case c.CompletionsCap <= 0:
		return fmt.Errorf("gateway: CompletionsCap = %d: нулевые капы запрещены", c.CompletionsCap)
	}
	return nil
}

// completion — асинхронное завершение (результат валидации, таймаут
// ожидания); применяется только при живом коннекте и живом ожидании.
type completion struct {
	conn  conn.ConnID
	kind  uint8
	seq   uint64 // ожидание персиста (cpPersistTimeout); 0 для валидации
	valid bool
	err   error
}

const (
	cpValidated uint8 = iota
	cpPersistTimeout
)

// Gateway — актор шлюза: стейт-машины коннектов, бинды аккаунтов, дрен
// стационарных кадров на тике. Каналы декларированы: события ридёров —
// bounded с per-conn под-лимитом (провал — close-on-overflow ридёром);
// закрытия — cap MaxConns, бездроп по построению (слот резервируется до
// старта эмиттера, освобождает актор); завершения — cap ≥ MaxConns:
// in-flight завершений не больше коннектов.
type Gateway struct {
	cfg       Config
	reg       *transport.Registry
	validator SessionValidator
	conn      *conn.Server
	stage     *encode.Stage
	doorbell  <-chan struct{}

	box   transport.Mailbox
	id    transport.EntityID
	token uint64

	conns          map[conn.ConnID]*gconn
	accounts       map[string]conn.ConnID
	closedUnopened map[conn.ConnID]bool // закрытие до обработки OnOpen
	tornDown       map[conn.ConnID]bool // разобраны актором: tombstone не ставить

	completions chan completion

	panicSeries int
	done        chan struct{}

	connsN      atomic.Int64
	boundN      atomic.Int64
	phaseFrames [7]atomic.Uint64
	// Длины карт интерливингов — атомики-зеркала: единственный мутатор карт
	// актор, но наблюдение извне (тесты/телеметрия) не должно лезть к картам.
	tombstonesN atomic.Int64
	tornDownN   atomic.Int64
	testPanicOn byte // шов инъекции паники (тесты recover-политики)

	deadLetters  atomic.Uint64
	coalesced    atomic.Uint64
	inboxDropped atomic.Uint64
	unknownOps   atomic.Uint64
	pushed       atomic.Uint64
	letters      atomic.Uint64
	failures     atomic.Uint64
	panics       atomic.Uint64
	displaced    atomic.Uint64
}

// New собирает шлюз: ящик в реестре, валидация конфига. Run — актор.
func New(cfg Config, reg *transport.Registry, validator SessionValidator,
	connSrv *conn.Server, stage *encode.Stage, doorbell <-chan struct{}) (*Gateway, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if reg == nil || validator == nil || connSrv == nil || stage == nil {
		return nil, fmt.Errorf("gateway: nil-компонент (реестр/валидатор/провода/стейдж)")
	}
	if doorbell == nil {
		return nil, fmt.Errorf("gateway: дверной звонок nil — тик-фолбэк обязателен")
	}
	g := &Gateway{
		cfg:            cfg,
		reg:            reg,
		validator:      validator,
		conn:           connSrv,
		stage:          stage,
		doorbell:       doorbell,
		conns:          make(map[conn.ConnID]*gconn),
		accounts:       make(map[string]conn.ConnID),
		closedUnopened: make(map[conn.ConnID]bool),
		tornDown:       make(map[conn.ConnID]bool),
		completions:    make(chan completion, cfg.CompletionsCap),
		done:           make(chan struct{}),
	}
	g.id = reg.Register(&g.box)
	g.token = uint64(g.id)
	return g, nil
}

// Done закрывается при выходе актора.
func (g *Gateway) Done() <-chan struct{} { return g.done }

// ID — адрес ящика шлюза (отправитель персист-запросов, получатель биндов).
func (g *Gateway) ID() transport.EntityID { return g.id }

// Stats — снимок метрик шлюза (карты читает только актор; счётчики жилых
// коннектов/биндов и кадров по фазам — атомики, читаемые извне без гонки).
type Stats struct {
	Conns, Bound            int64
	DeadLetters, Coalesced  uint64
	InboxDropped, UnknownOp uint64
	Pushed, Letters         uint64
	Failures, Panics        uint64
	Displaced               uint64
	PhaseFrames             [7]uint64
	Tombstones, TornDown    int64
}

// Stats возвращает снимок метрик.
func (g *Gateway) Stats() Stats {
	var phases [7]uint64
	for i := range g.phaseFrames {
		phases[i] = g.phaseFrames[i].Load()
	}
	return Stats{
		Conns:        g.connsN.Load(),
		Bound:        g.boundN.Load(),
		Tombstones:   g.tombstonesN.Load(),
		TornDown:     g.tornDownN.Load(),
		PhaseFrames:  phases,
		DeadLetters:  g.deadLetters.Load(),
		Coalesced:    g.coalesced.Load(),
		InboxDropped: g.inboxDropped.Load(),
		UnknownOp:    g.unknownOps.Load(),
		Pushed:       g.pushed.Load(),
		Letters:      g.letters.Load(),
		Failures:     g.failures.Load(),
		Panics:       g.panics.Load(),
		Displaced:    g.displaced.Load(),
	}
}

// Run — актор шлюза. Выход по ctx.
func (g *Gateway) Run(ctx context.Context) {
	defer close(g.done)
	if err := g.box.Claim(g.token); err != nil {
		slog.Error("gateway: претензия читателя не удалась", "err", err)
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-g.conn.Events():
			g.safeCall(func() { g.onEvent(ev) })
			ev.Done()
		case ce := <-g.conn.Closes():
			g.safeCall(func() { g.onClose(ce) })
			ce.Release()
		case <-g.box.Notify():
			g.safeCall(g.drainMail)
		case <-g.doorbell:
			g.safeCall(g.onTick)
			g.safeCall(g.drainMail)
		case cp := <-g.completions:
			g.safeCall(func() { g.onCompletion(cp) })
		}
	}
}

// safeCall — recover-политика по прецеденту persist-актора: паника обработки
// деградирует (счётчик failed + алерт), серия PanicLimit — let-it-crash.
func (g *Gateway) safeCall(f func()) {
	defer func() {
		if r := recover(); r != nil {
			g.panics.Add(1)
			g.failures.Add(1)
			g.panicSeries++
			slog.Error("gateway: паника обработки события", "panic", r)
			if g.panicSeries >= g.cfg.PanicLimit {
				panic(r)
			}
		}
	}()
	f()
	if g.panicSeries > 0 {
		g.panicSeries = 0
	}
}

// onEvent — события ридёров: открытия и кадры.
func (g *Gateway) onEvent(ev conn.Event) {
	if ev.Open {
		if g.closedUnopened[ev.Conn] {
			// Закрытие обработано раньше открытия — коннекта больше нет.
			delete(g.closedUnopened, ev.Conn)
			g.tombstonesN.Add(-1)
			g.deadLetters.Add(1)
			return
		}
		g.conns[ev.Conn] = &gconn{id: ev.Conn, key: ev.Key, phase: phHandshake}
		g.connsN.Add(1)
		return
	}
	gc := g.conns[ev.Conn]
	if gc == nil {
		g.deadLetters.Add(1)
		return
	}
	g.phaseFrames[gc.phase].Add(1)
	if g.testPanicOn != 0 && len(ev.Frame) > 0 && ev.Frame[0] == g.testPanicOn {
		panic("gateway: тестовая паника обработки")
	}
	g.onFrame(gc, ev.Frame)
}

// onClose — закрытие коннекта: LinkDead миру, снятие бинда, уборка.
func (g *Gateway) onClose(ce conn.ClosedEvent) {
	if g.tornDown[ce.Conn] {
		// Коннект разобран актором (close-after-fail/вытеснение/ConnClose):
		// tombstone не нужен — OnOpen этого connID давно обработан.
		delete(g.tornDown, ce.Conn)
		g.tornDownN.Add(-1)
		return
	}
	gc := g.conns[ce.Conn]
	if gc == nil {
		// Интерливинг close/OnOpen: поздний OnOpen закрытого погашится.
		if ce.OpenSent {
			g.closedUnopened[ce.Conn] = true
			g.tombstonesN.Add(1)
		}
		return
	}
	g.teardownMark(gc, true, true)
}

// teardown убирает коннект; notifyWorld — послать LinkDead (обрыв/вытеснение),
// false — закрытие уже инициировано миром (KindConnClose). Коннект помечается
// разобранным актором: позднее ClosedEvent не создаёт tombstone (иначе каждый
// close-after-fail оставлял бы вечную запись — записи не накапливаются).
func (g *Gateway) teardown(gc *gconn, notifyWorld bool) {
	g.teardownMark(gc, notifyWorld, false)
}

// teardownMark — teardown; byClose — вызов из обработки close-события
// (tornDown не ставится: close уже обработан, помечать некому).
func (g *Gateway) teardownMark(gc *gconn, notifyWorld, byClose bool) {
	if gc.pending != nil && gc.pending.timer != nil {
		gc.pending.timer.Stop()
		gc.pending = nil
	}
	// По фазе, не по entity: смерть коннекта в RTT бинда (EnterWorld отправлен,
	// бинд ещё не применён) тоже обязана дойти до региона — иначе сущность-
	// сирота навсегда (P3.7-F7); регион резолвит коннект по своим картам.
	if notifyWorld && gc.phase >= phWorld {
		g.sendRegion(transport.KindLinkDead, connRefMsg{Conn: uint64(gc.id)})
	}
	if g.accounts[gc.account] == gc.id {
		delete(g.accounts, gc.account)
		g.boundN.Add(-1)
	}
	delete(g.conns, gc.id)
	g.connsN.Add(-1)
	if !byClose {
		g.tornDown[gc.id] = true
		g.tornDownN.Add(1)
	}
	g.stage.Close(encode.ClientID(gc.id))
	g.conn.CloseAfterFlush(gc.id)
}

// onTick — дрен стационарных inbox в ящики игроков (назначение в порции).
func (g *Gateway) onTick() {
	for _, gc := range g.conns {
		if gc.entity == 0 || len(gc.inbox) == 0 {
			continue
		}
		n := len(gc.inbox)
		if n > g.cfg.DrainCap {
			n = g.cfg.DrainCap
		}
		for _, frame := range gc.inbox[:n] {
			g.reg.Send(transport.Envelope{
				To:      transport.Addr{Entity: gc.entity},
				FromID:  g.id,
				Kind:    transport.KindClientFrame,
				Payload: frame,
			})
			g.pushed.Add(1)
		}
		gc.inbox = append(gc.inbox[:0], gc.inbox[n:]...)
	}
}

// drainMail — письма ящика шлюза: персист-ответы, bind, ConnClose.
func (g *Gateway) drainMail() {
	batch := g.box.ExtractInto(g.token, nil)
	g.box.AckNotify()
	for i := range batch {
		g.onLetter(&batch[i])
	}
}

func (g *Gateway) onLetter(env *transport.Envelope) {
	g.letters.Add(1)
	switch env.Kind {
	case transport.KindPersistReply:
		g.onPersistReply(env)
	case transport.KindConnBind:
		var m connBindMsg
		if err := json.Unmarshal(env.Payload, &m); err != nil {
			slog.Error("gateway: битый KindConnBind", "err", err)
			return
		}
		gc := g.conns[conn.ConnID(m.Conn)]
		if gc == nil {
			g.deadLetters.Add(1)
			return
		}
		gc.entity = m.Entity
	case transport.KindConnClose:
		var m connRefMsg
		if err := json.Unmarshal(env.Payload, &m); err != nil {
			slog.Error("gateway: битый KindConnClose", "err", err)
			return
		}
		if gc := g.conns[conn.ConnID(m.Conn)]; gc != nil {
			g.teardown(gc, false)
		} else {
			g.deadLetters.Add(1)
		}
	default:
		slog.Error("gateway: письмо чужого Kind", "kind", env.Kind, "from", env.FromID)
	}
}

func (g *Gateway) onPersistReply(env *transport.Envelope) {
	reply, err := persist.DecodeReply(env.Payload)
	if err != nil {
		slog.Error("gateway: неразобранный ответ персиста", "err", err)
		return
	}
	gc := g.conns[conn.ConnID(reply.Corr)]
	if gc == nil || gc.pending == nil || gc.pending.op != reply.Op {
		// Поздний ответ после таймаута или чужое ожидание.
		g.deadLetters.Add(1)
		return
	}
	if gc.pending.timer != nil {
		gc.pending.timer.Stop()
	}
	gc.pending = nil
	switch reply.Op {
	case persist.OpCharList:
		if !reply.OK {
			slog.Error("gateway: персист отказал в списке", "conn", gc.id, "err", reply.Err)
			g.failLogin(gc, protocol.GSReasonSystemErrorLoginLater)
			return
		}
		gc.chars = reply.Chars
		g.replyCharList(gc)
	case persist.OpCreateChar:
		g.replyCreate(gc, reply)
	default:
		slog.Error("gateway: неизвестная операция ответа", "op", reply.Op)
	}
}

// onCompletion — асинхронные завершения: сверка живости коннекта и ожидания.
func (g *Gateway) onCompletion(cp completion) {
	gc := g.conns[cp.conn]
	if gc == nil {
		g.deadLetters.Add(1)
		return
	}
	switch cp.kind {
	case cpValidated:
		if gc.phase != phValidating {
			g.deadLetters.Add(1)
			return
		}
		g.onValidated(gc, cp)
	case cpPersistTimeout:
		if gc.pending == nil || gc.pending.seq != cp.seq {
			// Устаревший таймер: ожидание закрыто или сменилось.
			g.deadLetters.Add(1)
			return
		}
		gc.pending = nil
		g.failLogin(gc, protocol.GSReasonSystemErrorLoginLater)
	}
}

// sendRegion — контрольное письмо региону (JSON-кодек по прецеденту персиста).
func (g *Gateway) sendRegion(kind transport.Kind, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Error("gateway: кодирование письма региону", "kind", kind, "err", err)
		return
	}
	g.reg.Send(transport.Envelope{
		To:      g.cfg.Region,
		FromID:  g.id,
		Kind:    kind,
		Payload: body,
	})
}

// StageOutbounds — мост conn.Outbounds ↔ encode.Stage (стороны знают свои
// типы идентификаторов, прямой интерфейс невозможен из-за ConnID/ClientID).
type StageOutbounds struct {
	Stage *encode.Stage
}

// Register публикует клиента стейджа (до старта write-горутины).
func (o StageOutbounds) Register(id conn.ConnID, key [8]byte) conn.Outbound {
	return o.Stage.Register(encode.ClientID(id), key)
}

// Unregister снимает запись клиента.
func (o StageOutbounds) Unregister(id conn.ConnID) { o.Stage.Unregister(encode.ClientID(id)) }

// Close помечает close-after-flush.
func (o StageOutbounds) Close(id conn.ConnID) { o.Stage.Close(encode.ClientID(id)) }
