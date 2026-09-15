package gateway

import (
	"testing"
	"time"

	"github.com/udisondev/l2go/internal/conn"
	"github.com/udisondev/l2go/internal/encode"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/transport"
)

// Бенч пер-событийного ingest-пути актора (кадрособытие → диспетчер →
// стационарный inbox) и пер-тикового дрена (inbox → коалесинг уже при
// добавлении → слепой push в ящик игрока).

func benchGateway(b *testing.B, conns, framesPerConn int) {
	g := benchGatewayState(b, conns)
	move := make([]byte, protocol.MoveToLocationSize)
	protocol.WriteMoveToLocation(move, 1, 2, 3, 4, 5, 6, 1)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		for i := 0; i < framesPerConn; i++ {
			for _, gc := range g.conns {
				g.onStationaryFrame(gc, move)
			}
		}
		g.onTick()
	}
}

func BenchmarkGatewayIngest1(b *testing.B)  { benchGateway(b, 1, 1) }
func BenchmarkGatewayIngest50(b *testing.B) { benchGateway(b, 50, 1) }
func BenchmarkGatewayTickDrain(b *testing.B) {
	// Приготовленные inbox: тик дренирует DrainCap кадров на коннект.
	g := benchGatewayState(b, 50)
	move := make([]byte, protocol.MoveToLocationSize)
	protocol.WriteMoveToLocation(move, 1, 2, 3, 4, 5, 6, 1)
	other := make([]byte, protocol.ValidatePositionSize)
	protocol.WriteValidatePosition(other, 1, 2, 3, 4, 0)
	for _, gc := range g.conns {
		g.onStationaryFrame(gc, move)
		for i := 0; i < 8; i++ {
			g.onStationaryFrame(gc, bytesClone(other))
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		for _, gc := range g.conns {
			for i := 0; i < 8; i++ {
				g.onStationaryFrame(gc, bytesClone(other))
			}
		}
		g.onTick()
	}
}

func bytesClone(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// benchGatewayState — Gateway без сети: коннекты стационарной фазы с живыми
// ящиками игроков (Send идёт в зарегистрированные адреса).
func benchGatewayState(tb testing.TB, conns int) *Gateway {
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
		conns:    make(map[conn.ConnID]*gconn),
		accounts: make(map[string]conn.ConnID),
	}
	g.id = reg.Register(&g.box)
	g.token = uint64(g.id)
	for i := 1; i <= conns; i++ {
		player := &transport.Mailbox{}
		pid := reg.Register(player)
		if err := player.Claim(uint64(pid)); err != nil {
			tb.Fatal(err)
		}
		g.conns[conn.ConnID(i)] = &gconn{id: conn.ConnID(i), phase: phWorld, entity: pid}
	}
	return g
}
