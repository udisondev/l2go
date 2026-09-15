package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/udisondev/l2go/internal/conn"
	"github.com/udisondev/l2go/internal/encode"
	"github.com/udisondev/l2go/internal/transport"
)

// FuzzGatewayFrames — злые кадры предсессионной стейт-машины: паники нет,
// детерминированный отказ/разрыв. Гоняется в смоуке CI (Makefile fuzz-smoke).
func FuzzGatewayFrames(f *testing.F) {
	f.Add(byte(0), []byte{0x0B, 0x00})            // CharacterCreate обрезанный в phList
	f.Add(byte(1), []byte{0x08, 'a', 0x00, 0x01}) // AuthLogin без хвоста ключей в phAuth
	f.Add(byte(3), []byte{0x0D, 0x00, 0x00})      // CharacterSelect обрезанный в phSelected
	f.Add(byte(0), []byte{0x0E})                  // NewCharacter в phList
	f.Add(byte(4), []byte{0x77})                  // неизвестный опкод в phWorld
	f.Add(byte(2), []byte{0x03})                  // EnterWorld-опкод в phValidating (вне окна)
	f.Fuzz(func(t *testing.T, phaseSel byte, data []byte) {
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
		// Достижимые ветки зовут валидатор и completions: заглушки, чтобы
		// фазз доходил до кода ветвей, а не падал на nil-полях харнесса.
		g.validator = fuzzValidator{}
		g.completions = make(chan completion, 4)
		phases := []phase{phHandshake, phAuth, phList, phSelected, phWorld}
		ph := phases[int(phaseSel)%len(phases)]
		gc := &gconn{id: 1, phase: ph, account: "fuzz"}
		g.conns[1] = gc
		g.onFrame(gc, data) // паника — провал фазза
	})
}

// fuzzValidator — заглушка шва валидации для фазз-харнесса: мгновенный отказ
// (ветка valid=false детерминирована и не порождает горутин-мусора).
type fuzzValidator struct{}

func (fuzzValidator) ValidateSession(context.Context, string, int32, int32, int32, int32) (bool, error) {
	return false, nil
}
