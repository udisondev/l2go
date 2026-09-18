package world

// Датчики NPC-населения P3.10 (обязательства вниз из P3.9: obs×N-скан join
// впервые измеряется на живых наблюдателях; пик ввода в толпе; разовая цена
// разворота 12k). Журнал/benchmarks/, не ворота (бюджеты — фаза 4).

import (
	"fmt"
	"strings"
	"testing"

	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/transport"
)

// benchNPCSkin — общий скин толпы (read-only, алиасинг законен).
var benchNPCSkin = &NpcSkin{
	TemplateID: 20550, Name: "Орк", Title: "Разбойник", Attackable: true,
	RunSpd: 140, WalkSpd: 60, SwimRunSpd: 140, SwimWalkSpd: 60,
	PAtkSpd: 253, MAtkSpd: 333, MoveMultiplier: 1.0, AttackSpeedMultiplier: 1.1,
	CollisionRadius: 13, CollisionHeight: 22.5,
}

// spawnNPC — NPC-житель по координатной сетке (без статики: разворачивание
// — отдельный бенч FirstDeployStep).
func spawnNPC(b *testing.B, r *Region, x, y int32) {
	b.Helper()
	if _, err := r.Spawn(Entity{Owner: r.id, HP: 1,
		Pos: Position{X: x, Y: y, Z: -3000}, Npc: benchNPCSkin}); err != nil {
		b.Fatalf("Spawn NPC: %v", err)
	}
}

// npcCrowd — толпа k NPC компактной сеткой (шаг 10 — весь кластер влезает в
// enter-радиус наблюдателя из его центра).
func npcCrowd(b *testing.B, r *Region, k int) {
	b.Helper()
	for i := range k {
		spawnNPC(b, r, int32(i%100)*10, int32(i/100)*10)
	}
}

// spawnObserver — игрок-наблюдатель (живой Player — F36: наблюдатели без
// Player не исполняют obs×N-скан); pos — точка рождения.
func spawnObserverAt(b *testing.B, r *Region, conn uint64, pos Position) transport.EntityID {
	b.Helper()
	id, err := r.Spawn(Entity{Owner: r.id, HP: 100,
		Pos: pos, Player: &Player{ConnID: conn, SpeedBudget: speedCAP}})
	if err != nil {
		b.Fatalf("Spawn наблюдателя: %v", err)
	}
	return id
}

// npcFramePusher — счётчик кадров NpcInfo (живость и метрика пика ввода —
// только опкод 0x16, не любые кадры).
type npcFramePusher struct{ frames int }

func (p *npcFramePusher) Push(_ uint64, frame []byte, _ bool) {
	if len(frame) > 0 && frame[0] == protocol.OpNpcInfo {
		p.frames++
	}
}

// BenchmarkRegionNPCStep — полный шаг региона на NPC-населении 12k × 100
// наблюдателей-игроков + под-бенч фазы AoI прямым вызовом (прецедент прямого
// r.step()): obs×N-скан join (12k×100 ≈ 1.2M итераций/шаг) впервые измеряется
// с живыми наблюдателями (точка 12k/100 P3.9 шла с нулём). Фазовые доли —
// из под-бенчей (ph-счётчики — исполнители, не время).
func BenchmarkRegionNPCStep(b *testing.B) {
	base := DefaultConfig()
	for _, tc := range []struct{ npcs, observers int }{
		{12000, 100},
		{12000, 10},
		{12000, 0},
	} {
		name := fmt.Sprintf("npcs=%d/observers=%d", tc.npcs, tc.observers)
		b.Run(name, func(b *testing.B) {
			r := newBenchRegion(b, base, 0)
			npcCrowd(b, r, tc.npcs)
			// наблюдатели внутри толпы: каждая пара разрешается сканом
			for i := range tc.observers {
				spawnObserverAt(b, r, uint64(i+1), Position{X: int32(i * 10), Y: 0, Z: -3000})
			}
			for range 3 { // прогрев: вводы в известность, ёмкости буферов
				r.metro.tick.Add(1)
				r.step()
			}
			// живость (урок F36): наблюдатели шага разрешены, скан не выродился
			if len(r.aoiObs) != tc.observers {
				b.Fatalf("живость: aoiObs=%d; want %d (obs×N-скан мёртв)", len(r.aoiObs), tc.observers)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				r.metro.tick.Add(1)
				r.step()
			}
		})
		b.Run(name+"/aoi-only", func(b *testing.B) {
			r := newBenchRegion(b, base, 0)
			npcCrowd(b, r, tc.npcs)
			for i := range tc.observers {
				spawnObserverAt(b, r, uint64(i+1), Position{X: int32(i * 10), Y: 0, Z: -3000})
			}
			for range 3 {
				r.metro.tick.Add(1)
				r.step()
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				r.aoiStep()
				// Commit — фаза publish шага: без него appliedGen отстаёт и
				// каждый вызов уходит в примирение полным эмитом (не
				// стационарный obs×N-скан, а reconcile-шторм).
				r.pub.Commit(r.nextBlob)
			}
		})
	}
}

// BenchmarkNpcIntroDensity — пик ввода: наблюдатель рождается в центре толпы
// k NPC (join fullPass — k вводов NpcInfo одной пачкой; мотивировка радиуса,
// решение 8 фазы 3). Итерация = рождение + шаг + уборка наблюдателя вне
// таймера (стационарность: толпа не меняется, вводы — только новорождённому);
// метрика — счётчик кадров NpcInfo (живость: отказ — мёртвый бенч).
func BenchmarkNpcIntroDensity(b *testing.B) {
	base := DefaultConfig()
	for _, k := range []int{100, 1000} {
		b.Run(fmt.Sprintf("npcs=%d", k), func(b *testing.B) {
			pusher := &npcFramePusher{}
			m, err := NewMetronome(base)
			if err != nil {
				b.Fatal(err)
			}
			reg := transport.NewRegistry(0)
			log, err := NewPortionLog(b.TempDir(), 1, m.period, false, 1<<20)
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
			npcCrowd(b, r, k)
			for range 3 { // прогрев: толпа опубликована
				r.metro.tick.Add(1)
				r.step()
			}
			pusher.frames = 0
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; b.Loop(); i++ {
				id := spawnObserverAt(b, r, uint64(i+1), Position{X: 0, Y: 0, Z: -3000})
				r.metro.tick.Add(1)
				r.step()
				b.StopTimer()
				r.Remove(id) // наблюдатель одного пика — стационарность толпы
				b.StartTimer()
			}
			b.StopTimer()
			if pusher.frames == 0 {
				b.Fatal("живость: ни одного NpcInfo за прогон (мёртвый бенч)")
			}
			b.ReportMetric(float64(pusher.frames)/float64(b.N), "npcInfo-frames/op")
		})
	}
}

// BenchmarkFirstDeployStep — разовая цена разворота 12k: deploySpawns
// (броски/скины/Births) + применение Spawn×12k + запись шага в лог порций
// (энкод 12k NPC-рождений — F18). Очистка населения — вне таймера (ID
// собираются слайсом до Remove: Remove сплайсит на месте, range по живому
// слайсу пропускает элементы).
func BenchmarkFirstDeployStep(b *testing.B) {
	const n = 12000
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?><list enabled="true"><spawn name="Mass"><npc id="1" x="0" y="0" z="0" respawnDelay="60" />`)
	for i := 1; i < n; i++ {
		fmt.Fprintf(&sb, `<npc id="1" x="%d" y="%d" z="0" respawnDelay="60" />`, i%100*100, i/100*100)
	}
	sb.WriteString(`</spawn></list>`)
	npcs := `<?xml version="1.0" encoding="UTF-8"?><list><npc id="1" level="27" type="Monster" name="Массовый"><collision><radius normal="13"/><height normal="22.5"/></collision><stats><speed><walk ground="60"/><run ground="140"/></speed><attack attackSpeed="253"/></stats></npc></list>`
	static := staticOfXML(b, npcs, sb.String())
	cfg := DefaultConfig()
	r := newBenchRegion(b, cfg, 0)
	msg := transport.NPCDeployMsg{CenterX: 100000, CenterY: 100000, Radius: 1 << 30}
	var st State
	ids := make([]transport.EntityID, 0, n)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		res := StepResult{}
		st = State{}
		deploySpawns(1, 100, &st, static, msg, emptyGeo, &res)
		for i := range res.Births {
			id, err := r.Spawn(res.Births[i].Ent)
			if err != nil {
				b.Fatal(err)
			}
			ids = append(ids, id)
		}
		births := make([]AppliedBirth, len(res.Births))
		for i := range res.Births {
			e := res.Births[i].Ent
			births[i] = AppliedBirth{ID: ids[i], Ent: &e}
		}
		if err := r.log.LogStep(StepInput{Tick: 100, Delta: 0, Births: births}); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		ids = ids[:0]
		for _, id := range ids2copy(r) {
			r.Remove(id)
		}
		b.StartTimer()
	}
}

// ids2copy — снимок ID жителей (Remove сплайсит на месте — обход по живому
// слайсу жителей пропускал бы элементы).
func ids2copy(r *Region) []transport.EntityID {
	out := make([]transport.EntityID, len(r.residents))
	for i, res := range r.residents {
		out[i] = res.ent.ID
	}
	return out
}
