package world

// Датчики NPC-населения P3.10 (обязательства вниз из P3.9: obs×N-скан join
// впервые измеряется на живых наблюдателях; пик ввода в толпе; разовая цена
// разворота 12k). Журнал/benchmarks/, не ворота (бюджеты — фаза 4).

import (
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/udisondev/l2go/internal/data"
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

// spawnObserver — игрок-наблюдатель (живой Player — F36: наблюдатели без
// Player не исполняют obs×N-скан).
func spawnObserver(b *testing.B, r *Region, i, total int) {
	b.Helper()
	step := 100
	if total > 100 {
		step = 10000 / total
	}
	x := int32(i * step)
	if _, err := r.Spawn(Entity{Owner: r.id, HP: 100,
		Pos:    Position{X: x, Y: 5000, Z: -3000},
		Player: &Player{ConnID: uint64(i + 1), SpeedBudget: speedCAP}}); err != nil {
		b.Fatalf("Spawn наблюдателя: %v", err)
	}
}

// benchPusher — счётчик кадров (верификация живости — урок F36).
type benchPusher struct{ frames int }

func (p *benchPusher) Push(uint64, []byte, bool) { p.frames++ }

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
			for i := range tc.npcs {
				spawnNPC(b, r, int32(i%100)*100, int32(i/100)*100)
			}
			for i := range tc.observers {
				spawnObserver(b, r, i, tc.observers)
			}
			for range 3 { // прогрев: вводы в известность, ёмкости буферов
				r.metro.tick.Add(1)
				r.step()
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
			for i := range tc.npcs {
				spawnNPC(b, r, int32(i%100)*100, int32(i/100)*100)
			}
			for i := range tc.observers {
				spawnObserver(b, r, i, tc.observers)
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

// BenchmarkNpcIntroDensity — пик ввода: наблюдатель рождается в толпе k NPC
// (join fullPass — k вводов NpcInfo одной пачкой; мотивировка радиуса,
// решение 8 фазы 3). Итерация = рождение + шаг; кадры в метрике — счётчик
// пушера (живость: вводы > 0).
func BenchmarkNpcIntroDensity(b *testing.B) {
	base := DefaultConfig()
	for _, k := range []int{100, 1000} {
		b.Run(fmt.Sprintf("npcs=%d", k), func(b *testing.B) {
			pusher := &benchPusher{}
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
			for i := range k {
				spawnNPC(b, r, int32(i%100)*10, int32(i/100)*10)
			}
			for range 3 { // прогрев: толпа опубликована
				r.metro.tick.Add(1)
				r.step()
			}
			pusher.frames = 0
			b.ReportAllocs()
			b.ResetTimer()
			i := 0
			for b.Loop() {
				spawnObserver(b, r, i, 1000)
				i++
				r.metro.tick.Add(1)
				r.step()
			}
			b.StopTimer()
			if pusher.frames == 0 {
				b.Fatal("живость: ни одного кадра за прогон (мёртвый бенч)")
			}
		})
	}
}

// BenchmarkFirstDeployStep — разовая цена разворота 12k: deploySpawns
// (броски/скины/Births) + применение Spawn×12k; очистка населения — вне
// таймера. Статика — синтетический датапак с 12k точечных спавнов (генерация
// вне таймера).
func BenchmarkFirstDeployStep(b *testing.B) {
	const n = 12000
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?><list enabled="true"><spawn name="Mass"><npc id="1" x="0" y="0" z="0" respawnDelay="60" />`)
	for i := 1; i < n; i++ {
		fmt.Fprintf(&sb, `<npc id="1" x="%d" y="%d" z="0" respawnDelay="60" />`, i%100*100, i/100*100)
	}
	sb.WriteString(`</spawn></list>`)
	npcs := `<?xml version="1.0" encoding="UTF-8"?><list><npc id="1" level="27" type="Monster" name="Массовый"><collision><radius normal="13"/><height normal="22.5"/></collision><stats><speed><walk ground="60"/><run ground="140"/></speed><attack attackSpeed="253"/></stats></npc></list>`
	static := benchStaticOf(b, npcs, sb.String())
	cfg := DefaultConfig()
	r := newBenchRegion(b, cfg, 0)
	msg := transport.NPCDeployMsg{CenterX: 100000, CenterY: 100000, Radius: 1 << 30}
	var st State
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		res := StepResult{}
		deploySpawns(1, 100, &st, static, msg, emptyGeo, &res)
		for i := range res.Births {
			if _, err := r.Spawn(res.Births[i].Ent); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		st = State{}
		for _, res2 := range r.residents {
			r.Remove(res2.ent.ID)
		}
		b.StartTimer()
	}
}

// benchStaticOf — загрузка мини-датапака в бенчах (зеркало mkStatic).
func benchStaticOf(b *testing.B, npcs, spawns string) *data.Static {
	b.Helper()
	fsys := fstest.MapFS{
		"stats/items/.keep":   {},
		"stats/skills/.keep":  {},
		"zones/.keep":         {},
		"stats/npcs/npcs.xml": {Data: []byte(npcs)},
		"spawns/synth.xml":    {Data: []byte(spawns)},
	}
	st, rep, err := data.Load(fsys)
	if err != nil {
		b.Fatalf("benchStatic: %v", err)
	}
	if rep.HasErrors() {
		b.Fatalf("benchStatic: датапак красный: %v", rep.Errors)
	}
	return st
}
