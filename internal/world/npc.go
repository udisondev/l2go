// Разворачивание NPC-населения из спавнов статики (P3.10): срез центр+радиус
// → рождения через свёртку. Семантика разворота — порт L2J_Mobius
// SpawnData/Spawn/ZoneNPoly/NpcSpawnTerritory (GPLv3): территория побеждает
// точку, отсутствующий heading — бросок, Z точки монстра — гео-коррекция.
package world

import (
	"log/slog"
	"math/rand/v2"
	"strconv"

	"github.com/udisondev/l2go/internal/data"
	"github.com/udisondev/l2go/internal/geo"
	"github.com/udisondev/l2go/internal/transport"
)

// NpcSkin — снимок полей NPC-шаблона для репликации NpcInfo: заполняется при
// рождении из статики, после не меняется (NPC фазы 3 стоят, бессмертны).
type NpcSkin struct {
	TemplateID            int32
	Name                  string
	Title                 string
	Attackable            bool
	CollisionRadius       float64
	CollisionHeight       float64
	RunSpd                int32
	WalkSpd               int32
	SwimRunSpd            int32
	SwimWalkSpd           int32
	PAtkSpd               int32
	MAtkSpd               int32
	MoveMultiplier        float64
	AttackSpeedMultiplier float64
}

// Дефолты скоростей NPC канона (порт L2J_Mobius CreatureTemplate, GPLv3):
// baseRunSpd 120, baseWalkSpd 50, basePAtkSpd 300, baseMAtkSpd 333. Floor на
// дефолт вместо канон-санитизации «≤0 → 0.1»: нулевые скорости в кадре
// запрещены (мотивация класса KT4-1 — объект с нулевой скоростью клиент не
// двигает). AttackSpeedMultiplier 1.1 — дефолт простоя (CreatureStat).
const (
	npcDefaultRunSpd  = int32(120)
	npcDefaultWalkSpd = int32(50)
	npcDefaultPAtkSpd = int32(300)
	npcDefaultMAtkSpd = int32(333)
	npcIdleAtkSpdMult = 1.1
)

// npcAttackableTypes — замыкание Monster-поддерева канона (порт L2J_Mobius:
// setAutoAttackable(true) конструкторов; Guard/FriendlyMob/Defender без
// флага — мирному наблюдателю false).
var npcAttackableTypes = map[string]bool{
	"Monster": true, "RaidBoss": true, "GrandBoss": true, "Chest": true,
	"FestivalMonster": true, "RiftInvader": true, "EventMonster": true,
	"FeedableBeast": true, "TamedBeast": true,
}

// npcNotSpawnedTypes — типы, которых канон статикой не спавнит (порт
// L2J_Mobius SpawnTable.checkTemplate: менеджерные спавны осады/рейдов).
var npcNotSpawnedTypes = map[string]bool{
	"SiegeGuard": true, "RaidBoss": true,
}

// npcMaxSpawnAttempts — лимит бросков точки в полигон территории (канон
// после 1000 возвращает точку вне полигона; здесь — skip со счётчиком:
// невалидная позиция в населении хуже отсутствия записи).
const npcMaxSpawnAttempts = 64

// npcHeadingRoll — домен броска отсутствующего heading канона Rnd.get(61794).
const npcHeadingRoll = 61794

// npcSkinOf — Npc-шаблон статики → скин репликации (скорости — raw-bag с
// дефолтами канона; swim = fallback run/walk).
func npcSkinOf(n data.Npc) NpcSkin {
	run := npcSpeedOf(n, "stats.speed.run.ground", npcDefaultRunSpd)
	walk := npcSpeedOf(n, "stats.speed.walk.ground", npcDefaultWalkSpd)
	return NpcSkin{
		TemplateID:            int32(n.ID),
		Name:                  n.Name,
		Title:                 n.Title,
		Attackable:            npcAttackableTypes[n.Type],
		CollisionRadius:       n.CollisionRadius,
		CollisionHeight:       n.CollisionHeight,
		RunSpd:                run,
		WalkSpd:               walk,
		SwimRunSpd:            run,
		SwimWalkSpd:           walk,
		PAtkSpd:               npcSpeedOf(n, "stats.attack.attackSpeed", npcDefaultPAtkSpd),
		MAtkSpd:               npcDefaultMAtkSpd,
		MoveMultiplier:        1.0,
		AttackSpeedMultiplier: npcIdleAtkSpdMult,
	}
}

// npcSpeedOf — скорость из raw-bag шаблона с floor-дефолтом.
func npcSpeedOf(n data.Npc, key string, def int32) int32 {
	raw, ok := n.Set(key)
	if !ok {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 {
		return def
	}
	return int32(v)
}

// deploySpawns — разворачивание среза спавнов статики в рождения: чистая
// функция письма разворота; RNG — собственный PCG(regionID, tick) (D2, буква
// спеки: потребление изолировано от прочих стримов шага). Обход —
// Static.Spawns() в детерминированном порядке загрузки; Count — N рождений
// записи (точечные — стак в точке, территориальные — независимые броски).
func deploySpawns(region RegionID, tick Tick, st *State, static *data.Static, cfg transport.NPCDeployMsg, gm *geo.Map, res *StepResult) {
	rng := rand.New(rand.NewPCG(uint64(region), uint64(tick)))
	r2 := int64(cfg.Radius) * int64(cfg.Radius)
	point, territorial := 0, 0
	for _, sp := range static.Spawns() {
		tpl, ok := static.Npc(sp.NpcID)
		if !ok || npcNotSpawnedTypes[tpl.Type] {
			st.NPCSkipped++
			continue
		}
		// Фильтр среза: точечный — 2D-дистанция (датапак-Z не фильтрует —
		// канон терпит промах Z до 300); территориальный — bbox ∩ квадрат
		// радиуса (консервативно шире круга: часть развёрнутых окажется вне
		// enter-радиуса — безвредно, известного радиуса известность не
		// требует).
		if sp.Territory == "" {
			dx := int64(sp.Point.X) - int64(cfg.CenterX)
			dy := int64(sp.Point.Y) - int64(cfg.CenterY)
			if dx*dx+dy*dy > r2 {
				continue
			}
		} else {
			terr, ok := static.Territory(sp.Territory)
			if !ok {
				st.NPCSkipped++
				continue
			}
			if int64(terr.MaxX) < int64(cfg.CenterX)-int64(cfg.Radius) ||
				int64(terr.MinX) > int64(cfg.CenterX)+int64(cfg.Radius) ||
				int64(terr.MaxY) < int64(cfg.CenterY)-int64(cfg.Radius) ||
				int64(terr.MinY) > int64(cfg.CenterY)+int64(cfg.Radius) {
				continue
			}
			deployTerritory(rng, st, tpl, sp, terr, gm, res)
			territorial++
			continue
		}
		deployPoint(rng, st, tpl, sp, gm, res)
		point++
	}
	st.NPCDeployed += uint64(point + territorial)
	// Журнал живого прогона (КТ): разворот наблюдаем без дампа; в
	// детерминированную свёртку не входит.
	slog.Info("world: разворот NPC-населения",
		"region", region, "tick", tick,
		"deployed", point+territorial, "point", point, "territorial", territorial,
		"skipped", st.NPCSkipped)
}

// deployPoint — точечная запись: позиция как есть (у Monster — гео-коррекция
// Z при |Δz| < 300: монстры на устаревшем датапак-Z висят/тонут на живом
// прогоне — порт L2J_Mobius Spawn.initializeNpc), heading датапака маской,
// отсутствующий (−1) — бросок.
func deployPoint(rng *rand.Rand, st *State, tpl data.Npc, sp data.NpcSpawn, gm *geo.Map, res *StepResult) {
	x, y, z := sp.Point.X, sp.Point.Y, sp.Point.Z
	// Гео-коррекция — внутри сетки мира (краевые точки датапака вне сетки
	// остаются как есть: контракт geoOf — точка обязана лежать в сетке).
	if npcAttackableTypes[tpl.Type] && geo.InWorld(int(x), int(y)) {
		gz := gm.NearestZ(geo.Loc{X: int(x), Y: int(y), Z: int(z)})
		if abs32(int32(gz)-z) < 300 {
			z = int32(gz)
		}
	}
	heading := rollHeading(rng, sp.Point.Heading)
	skin := npcSkinOf(tpl)
	for range sp.Count {
		res.Births = append(res.Births, Birth{Ent: Entity{
			Pos: Position{X: x, Y: y, Z: z}, Heading: heading, HP: 1, Npc: &skin,
		}})
	}
}

// deployTerritory — территориальная запись: count независимых бросков —
// точка в bbox до попадания в полигон, Z = гео-высота с сидом середины
// диапазона, кламп в [MinZ, MaxZ] (усиление канона: у него только верхний
// кламп); banned — 3D по вычисленному гео-Z (порт NpcSpawnTerritory).
// Исчерпание лимита попыток — рождено меньше count, счётчик пропуска.
func deployTerritory(rng *rand.Rand, st *State, tpl data.Npc, sp data.NpcSpawn, terr data.Territory, gm *geo.Map, res *StepResult) {
	skin := npcSkinOf(tpl)
	zlo, zhi := terr.MinZ, terr.MaxZ
	if zlo > zhi {
		zlo, zhi = zhi, zlo
	}
	midZ := zlo + (zhi-zlo)/2
	for range sp.Count {
		pos, ok := rollTerritoryPoint(rng, st, terr, zlo, zhi, midZ, gm)
		if !ok {
			return
		}
		res.Births = append(res.Births, Birth{Ent: Entity{
			Pos: pos, Heading: int32(rng.IntN(npcHeadingRoll)), HP: 1, Npc: &skin,
		}})
	}
}

// rollTerritoryPoint — бросок точки территории: x/y равномерно в bbox
// (включительно) до попадания в полигон (лимит npcMaxSpawnAttempts → false
// со счётчиком); Z — гео-высота с клампом в диапазон; banned-полигоны
// отбрасывают точку (3D по вычисленному Z).
func rollTerritoryPoint(rng *rand.Rand, st *State, terr data.Territory, zlo, zhi, midZ int32, gm *geo.Map) (Position, bool) {
	for range npcMaxSpawnAttempts {
		x := terr.MinX + int32(rng.Int64N(int64(terr.MaxX-terr.MinX)+1))
		y := terr.MinY + int32(rng.Int64N(int64(terr.MaxY-terr.MinY)+1))
		z := midZ
		if geo.InWorld(int(x), int(y)) {
			z = clamp32(int32(gm.NearestZ(geo.Loc{X: int(x), Y: int(y), Z: int(midZ)})), zlo, zhi)
		}
		if !terr.Contains(x, y, z) {
			continue
		}
		banned := false
		for i := range terr.Banned {
			if terr.Banned[i].Contains(x, y, z) {
				banned = true
				break
			}
		}
		if banned {
			continue
		}
		return Position{X: x, Y: y, Z: z}, true
	}
	st.NPCSkipped++
	return Position{}, false
}

// rollHeading — heading записи: отсутствующий (−1, территория игнорирует
// точку) — бросок канона Rnd.get(61794); заданный — маска домена [0,65536).
func rollHeading(rng *rand.Rand, heading int32) int32 {
	if heading < 0 {
		return int32(rng.IntN(npcHeadingRoll))
	}
	return heading & 0xFFFF
}

func clamp32(v, lo, hi int32) int32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}
