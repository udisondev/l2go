package world

// Нагрузочный датчик перемещений (лестница владельца 100/1000/10000/20000):
// игроки — живые Player (наблюдатели друг для друга — в отличие от NPC-толпы
// P3.10), все движутся пинг-понгом (worst case: dirty каждый шаг). Расклады:
// crowd — все в одном enter-круге (максимальный фан-аут compose);
// clusters — группы по 50 вне взаимных радиусов (реалистичная локальность).
// Датчик лабораторный (фаза 3; бюджеты/боты — фаза 4): «вывезет» = шаг
// укладывается в бюджет тика (период метронома).

import (
	"fmt"
	"testing"

	"github.com/udisondev/l2go/internal/transport"
)

// loadLayout — раскладка позиций игроков.
type loadLayout struct {
	name    string
	cluster int   // игроков в одном enter-круге
	spacing int32 // дистанция между кластерами (>> Exit 4200)
}

var loadLayouts = []loadLayout{
	{"crowd", 1 << 30, 0},     // все в одном круге
	{"clusters50", 50, 12000}, // кластеры по 50, вне взаимных радиусов
}

// playerLoadPos — позиция i-го игрока раскладки.
func playerLoadPos(l loadLayout, i int) Position {
	inCluster := i % l.cluster
	clusterIdx := i / l.cluster
	// кластер — сетка ~20 юнитов (50 игроков ≈ 45×45 юнитов, все в Enter)
	cx := int32(clusterIdx) * l.spacing
	return Position{X: cx + int32(inCluster%8)*20, Y: int32(inCluster/8) * 20, Z: -3000}
}

// framePusher — счётчик всех кадров (метрика compose-фан-аута).
type framePusher struct{ frames int }

func (p *framePusher) Push(uint64, []byte, bool) { p.frames++ }

// BenchmarkRegionPlayerLoad — полный шаг региона на населении движущихся
// игроков + под-бенч фазы AoI (Build+join+compose+Commit) прямым вызовом.
// Итерация = шаг региона; прибывшие перезапускаются вне измеряемой стоимости
// (прецедент BenchmarkJoinStreamCompose), кадры — в метрике frames/op.
func BenchmarkRegionPlayerLoad(b *testing.B) {
	base := DefaultConfig()
	for _, players := range []int{100, 1000, 10000, 20000} {
		for _, l := range loadLayouts {
			if l.name == "crowd" && players > 1000 {
				// crowd-фан-аут квадратичен (dirty×наблюдатели): 1000 — последняя
				// безопасная по памяти точка (10k×10k staging ≈ гигабайты — ООМ
				// лаборатории); большие N экстраполируются аналитически (квадрат)
				continue
			}
			for _, sub := range []string{"step", "aoi"} {
				name := fmt.Sprintf("players=%d/%s/%s", players, l.name, sub)
				b.Run(name, func(b *testing.B) {
					pusher := &framePusher{}
					m, err := NewMetronome(base)
					if err != nil {
						b.Fatal(err)
					}
					reg := transport.NewRegistry(0)
					log, err := NewPortionLog(b.TempDir(), 1, false, 1<<20)
					if err != nil {
						b.Fatal(err)
					}
					b.Cleanup(func() { _ = log.Close() })
					r, err := NewRegion(m, reg, 1, base, log, pusher, emptyGeo)
					if err != nil {
						b.Fatal(err)
					}
					if err := r.Wire(901, 900); err != nil {
						b.Fatal(err)
					}
					type mover struct {
						e    *Entity
						home Position
					}
					var movers []mover
					for i := range players {
						pos := playerLoadPos(l, i)
						if _, err := r.Spawn(Entity{Owner: r.id, HP: 100,
							Pos: pos, Moving: true,
							Dest:     Position{X: pos.X + 1000, Y: pos.Y},
							MoveFrom: pos, MoveDist: 1_000_000,
							Player: &Player{ConnID: uint64(i + 1), SpeedBudget: speedCAP}}); err != nil {
							b.Fatalf("Spawn: %v", err)
						}
						movers = append(movers, mover{e: r.residents[len(r.residents)-1].ent, home: pos})
					}
					for range 3 { // прогрев: вводы в известность, старт dirty-потока
						r.metro.tick.Add(1)
						r.step()
					}
					if len(r.aoiObs) != players {
						b.Fatalf("живость: aoiObs=%d; want %d (наблюдатели не разрешены)", len(r.aoiObs), players)
					}
					pusher.frames = 0
					b.ReportAllocs()
					b.ResetTimer()
					for b.Loop() {
						for i := range movers { // прибывшие — пинг-понг от home
							e := movers[i].e
							if !e.Moving {
								e.Moving = true
								dest := movers[i].home.X + 1000
								if e.Pos.X > movers[i].home.X {
									dest = movers[i].home.X - 1000
								}
								e.Dest = Position{X: dest, Y: e.Pos.Y}
								e.MoveFrom = e.Pos
								e.MoveDist = 1_000_000
								e.MoveDone = 0
							}
							if sub == "aoi" {
								e.Pos.X++ // advance живёт в fold: стимул dirty для изолированной фазы
							}
						}
						if sub == "aoi" {
							// aoi-фаза + доставка кадров без акторских писем:
							// pendingPushes без доставки копились бы до ООМ
							r.aoiStep()
							r.pub.Commit(r.nextBlob)
							for i := r.pushCursor; i < len(r.pendingPushes); i++ {
								p := r.pendingPushes[i]
								r.pusher.Push(p.Client, p.Frame, p.Crypt)
								r.pushCursor = i + 1
							}
							r.pendingPushes = r.pendingPushes[:0]
							r.pushCursor = 0
						} else {
							r.metro.tick.Add(1)
							r.step()
						}
					}
					b.StopTimer()
					if pusher.frames == 0 {
						b.Fatal("живость: ни одного кадра за прогон (мёртвый бенч)")
					}
					b.ReportMetric(float64(pusher.frames)/float64(b.N), "frames/op")
				})
			}
		}
	}
}
