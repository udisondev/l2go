package gateway

import (
	"testing"
	"time"

	"github.com/udisondev/l2go/internal/conn"
	"github.com/udisondev/l2go/internal/encode"
	"github.com/udisondev/l2go/internal/transport"
)

// FuzzGatewayFrames — злые кадры предсессионной стейт-машины: паники нет,
// детерминированный отказ/разрыв. Гоняется в смоуке CI (Makefile fuzz-smoke).
func FuzzGatewayFrames(f *testing.F) {
	for _, seed := range [][]byte{
		{0x0B, 0x00},            // CharacterCreate обрезанный
		{0x0D, 0x00, 0x00},      // CharacterSelect обрезанный
		{0x0E},                  // NewCharacter
		{0x08, 'a', 0x00, 0x01}, // AuthLogin без хвоста ключей
		{0x77},                  // неизвестный опкод
		{0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 || len(data) > 8192 {
			return
		}
		stage, err := encode.NewStage(1 << 12)
		if err != nil {
			t.Fatal(err)
		}
		reg := transport.NewRegistry(8)
		stage.Register(1, [8]byte{1, 2, 3, 4, 5, 6, 7, 8})
		connSrv, err := conn.New(conn.Config{
			MaxConns: 4, HandshakeTimeout: time.Second, IdleTimeout: time.Second,
			WriteTimeout: time.Second, KeepAlive: time.Second,
			FrameCap: 8192, EventQueue: 4, PerConnEvents: 2,
		}, StageOutbounds{Stage: stage})
		if err != nil {
			t.Fatal(err)
		}
		g := &Gateway{
			cfg: Config{
				Persist:        1,
				Region:         transport.Addr{Entity: 2},
				PersistTimeout: time.Second,
				InboxCap:       4,
				DrainCap:       2,
				PanicLimit:     1000, // фазз: recover не должен рвать процесс
				CompletionsCap: 4,
			},
			reg:            reg,
			stage:          stage,
			conn:           connSrv,
			conns:          make(map[conn.ConnID]*gconn),
			accounts:       make(map[string]conn.ConnID),
			closedUnopened: make(map[conn.ConnID]bool),
			tornDown:       make(map[conn.ConnID]bool),
		}
		g.id = reg.Register(&g.box)
		g.token = uint64(g.id)
		phases := []phase{phHandshake, phAuth, phList, phSelected, phWorld}
		ph := phases[int(data[0])%len(phases)]
		gc := &gconn{id: 1, phase: ph, account: "fuzz"}
		g.conns[1] = gc
		g.onFrame(gc, data) // паника — провал фазза
	})
}
