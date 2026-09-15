package gateway

import (
	"testing"
	"time"

	"github.com/udisondev/l2go/internal/conn"
	"github.com/udisondev/l2go/internal/encode"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/transport"
)

// benchState — Gateway без сети: коннекты стационарной фазы с живыми ящиками
// игроков (Send идёт в зарегистрированные адреса; дрен ящиков в итерации
// обязателен — полный faf-ящик превратил бы Send в дроп-ветку).
type benchState struct {
	g       *Gateway
	players []*transport.Mailbox
	tokens  []uint64
	batches [][]transport.Envelope // ретейновые дрен-буферы (нулевая цена харнесса)
}

func newBenchState(tb testing.TB, conns int) *benchState {
	tb.Helper()
	stage, err := encode.NewStage(1 << 18)
	if err != nil {
		tb.Fatal(err)
	}
	reg := transport.NewRegistry(conns + 8)
	connSrv, err := conn.New(conn.Config{
		MaxConns: conns + 1, HandshakeTimeout: time.Second, IdleTimeout: time.Second,
		WriteTimeout: time.Second, KeepAlive: time.Second,
		FrameCap: 8192, EventQueue: 128, PerConnEvents: 8,
	}, StageOutbounds{Stage: stage})
	if err != nil {
		tb.Fatal(err)
	}
	g := &Gateway{
		cfg: Config{
			Persist: 1, Region: transport.Addr{Entity: 2},
			PersistTimeout: time.Second, InboxCap: 64, DrainCap: 8,
			PanicLimit: 3, CompletionsCap: conns + 1,
		},
		reg: reg, stage: stage, conn: connSrv,
		conns:          make(map[conn.ConnID]*gconn),
		accounts:       make(map[string]conn.ConnID),
		closedUnopened: make(map[conn.ConnID]bool),
		tornDown:       make(map[conn.ConnID]bool),
	}
	g.id = reg.Register(&g.box)
	g.token = uint64(g.id)
	bs := &benchState{g: g}
	for i := 1; i <= conns; i++ {
		player := &transport.Mailbox{}
		pid := reg.Register(player)
		if err := player.Claim(uint64(pid)); err != nil {
			tb.Fatal(err)
		}
		g.conns[conn.ConnID(i)] = &gconn{id: conn.ConnID(i), phase: phWorld, entity: pid}
		bs.players = append(bs.players, player)
		bs.tokens = append(bs.tokens, uint64(pid))
		bs.batches = append(bs.batches, nil)
	}
	return bs
}

// drainPlayers вычитывает faf-ящики игроков (в проде это регион).
func (bs *benchState) drainPlayers() {
	for i, box := range bs.players {
		bs.batches[i] = box.ExtractInto(bs.tokens[i], bs.batches[i])
		box.AckNotify()
	}
}

func benchIngest(b *testing.B, conns int) {
	bs := newBenchState(b, conns)
	move := make([]byte, protocol.MoveToLocationSize)
	protocol.WriteMoveToLocation(move, 1, 2, 3, 4, 5, 6, 1)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		for _, gc := range bs.g.conns {
			// Полный ingest-путь: событие → lookup → стейт-машина → inbox.
			bs.g.onEvent(conn.Event{Conn: gc.id, Frame: move})
		}
		bs.g.onTick()
		bs.drainPlayers()
	}
}

func BenchmarkGatewayIngest1(b *testing.B)  { benchIngest(b, 1) }
func BenchmarkGatewayIngest50(b *testing.B) { benchIngest(b, 50) }

func BenchmarkGatewayTickDrain(b *testing.B) {
	bs := newBenchState(b, 50)
	move := make([]byte, protocol.MoveToLocationSize)
	protocol.WriteMoveToLocation(move, 1, 2, 3, 4, 5, 6, 1)
	other := make([]byte, protocol.ValidatePositionSize)
	protocol.WriteValidatePosition(other, 1, 2, 3, 4, 0)
	for _, gc := range bs.g.conns {
		bs.g.onStationaryFrame(gc, move)
		for i := 0; i < 8; i++ {
			bs.g.onStationaryFrame(gc, cloneBytes(other))
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		for _, gc := range bs.g.conns {
			for i := 0; i < 8; i++ {
				bs.g.onStationaryFrame(gc, cloneBytes(other))
			}
		}
		bs.g.onTick()
		bs.drainPlayers()
	}
}

// cloneBytes — кадр уходит в inbox владением: для повторных итераций копия.
func cloneBytes(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// TestGatewayDrainAllocBudget — машинный бюджет пер-тикового пути:
// коалесинг MoveToLocation и дрен тика — 0 аллокаций вне роста inbox/конвертов
// (спека P3.6; недостижимость фиксируется здесь же).
func TestGatewayDrainAllocBudget(t *testing.T) {
	bs := newBenchState(t, 4)
	move := make([]byte, protocol.MoveToLocationSize)
	protocol.WriteMoveToLocation(move, 1, 2, 3, 4, 5, 6, 1)
	var gc *gconn
	for _, c := range bs.g.conns {
		gc = c
		break
	}

	// Прогрев: inbox с коалесированным move; конверты дрена уходят в
	// зарегистрированные ящики (Send — 0 аллок по P3.1).
	bs.g.onStationaryFrame(gc, move)
	bs.g.onTick()
	bs.drainPlayers()

	allocs := testing.AllocsPerRun(200, func() {
		bs.g.onStationaryFrame(gc, move) // коалесинг: замена в inbox
		bs.g.onTick()                    // дрен: конверт + слепой push
		bs.drainPlayers()                // дрен ящиков (не путь шлюза)
	})
	if allocs != 0 {
		t.Errorf("коалесинг+дрен тика: %.0f аллокаций; want 0 (вне роста inbox/конвертов)", allocs)
	}
}
