// Разворачивание NPC-населения из спавнов статики (P3.10): срез центр+радиус
// → рождения через свёртку. Семантика разворота — порт L2J_Mobius
// SpawnData/Spawn/ZoneNPoly/NpcSpawnTerritory (GPLv3): территория побеждает
// точку, отсутствующий heading — бросок на каждый экземпляр, Z точки монстра
// — гео-коррекция.
package world

import (
	"log/slog"
	"math/rand/v2"
	"slices"
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
	RHand                 int32
	LHand                 int32
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
// L2J_Mobius SpawnData.checkTemplate: менеджерные спавны осады/рейдов).
var npcNotSpawnedTypes = map[string]bool{
	"SiegeGuard": true, "RaidBoss": true,
}

// npcMaxSpawnAttempts — лимит бросков точки в полигон территории (канон
// после 1000 возвращает точку вне полигона; здесь — пропуск экземпляра со
// счётчиком: невалидная позиция в населении хуже отсутствия сущности).
const npcMaxSpawnAttempts = 64

// npcHeadingRoll — домен броска отсутствующего heading канона Rnd.get(61794).
const npcHeadingRoll = 61794

// npcSkinOf — Npc-шаблон статики → скин репликации (скорости и оружие рук —
// raw-bag с дефолтами канона; swim = fallback run/walk).
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
		RHand:                 npcItemOf(n, "equipment.rhand"),
		LHand:                 npcItemOf(n, "equipment.lhand"),
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

// npcItemOf — предмет экипировки из raw-bag шаблона (оружие рук NpcInfo).
func npcItemOf(n data.Npc, key string) int32 {
	raw, ok := n.Set(key)
	if !ok {
		return 0
	}
	v, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || v < 0 {
		return 0
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
	radius := int64(cfg.Radius)
	total, point, territorial := 0, 0, 0
	for _, sp := range static.Spawns() {
		total++
		tpl, ok := static.Npc(sp.NpcID)
		if !ok || npcNotSpawnedTypes[tpl.Type] {
			st.NPCSkipped++
			continue
		}
		// Фильтр среза: точечный — 2D-дистанция (датапак-Z не фильтрует —
		// канон терпит промах Z до 300); территориальный — bbox ∩ квадрат
		// радиуса (консервативно шире круга: часть развёрнутых окажется вне
		// enter-радиуса — безвредно). Отсечение по осям до квадратов — края
		// домена int32 не переполняют int64 в произведениях.
		if sp.Territory == "" {
			dx, dy := int64(sp.Point.X)-int64(cfg.CenterX), int64(sp.Point.Y)-int64(cfg.CenterY)
			if dx > radius || dx < -radius || dy > radius || dy < -radius || dx*dx+dy*dy > radius*radius {
				continue
			}
		} else {
			terr, ok := static.Territory(sp.Territory)
			if !ok {
				st.NPCSkipped++
				continue
			}
			if int64(terr.MaxX) < int64(cfg.CenterX)-radius ||
				int64(terr.MinX) > int64(cfg.CenterX)+radius ||
				int64(terr.MaxY) < int64(cfg.CenterY)-radius ||
				int64(terr.MinY) > int64(cfg.CenterY)+radius {
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
	// детерминированную свёртку не входит (прецеденты slog свёртки — персист).
	slog.Info("world: разворот NPC-населения",
		"region", region, "tick", tick, "total", total,
		"deployed", point+territorial, "point", point, "territorial", territorial,
		"skipped", st.NPCSkipped)
}

// deployPoint — точечная запись: позиция как есть (у Monster — гео-коррекция
// Z при |Δz| < 300: монстры на устаревшем датапак-Z висят/тонут на живом
// прогоне — порт L2J_Mobius Spawn.initializeNpc), heading датапака маской,
// отсутствующий (−1) — бросок на каждый экземпляр (канон бросает в
// initializeNpc — per Spawn, не per запись).
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
	skin := npcSkinOf(tpl)
	for range sp.Count {
		res.Births = append(res.Births, Birth{Ent: Entity{
			Pos:     Position{X: x, Y: y, Z: z},
			Heading: rollHeading(rng, sp.Point.Heading), HP: 1, Npc: &skin,
		}})
	}
}

// deployTerritory — территориальная запись: count независимых бросков —
// точка в bbox до попадания в полигон, Z = гео-высота с сидом середины
// диапазона (порт ZoneNPoly.getRandomPoint); гео-высота вне полосы
// [MinZ,MaxZ] перебрасывает точку (кламп закопал бы NPC в рельеф — у канона
// верхняя граница лечится перебросом NpcSpawnTerritory, нижняя отсутствием
// гео не встречается); banned — 3D по вычисленному гео-Z. Исчерпание лимита
// — экземпляр пропущен, сиблинги продолжаются (канон спавнит независимые
// Spawn; общий отказ записи — только целиком невычислимый полигон).
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
			continue
		}
		res.Births = append(res.Births, Birth{Ent: Entity{
			Pos: pos, Heading: int32(rng.IntN(npcHeadingRoll)), HP: 1, Npc: &skin,
		}})
	}
}

// rollTerritoryPoint — бросок точки территории: x/y равномерно в bbox
// (включительно; размах в int64 — края домена int32 не переносятся) до
// попадания в полигон (лимит npcMaxSpawnAttempts → false со счётчиком);
// гео-Z обязан лечь в полосу [zlo,zhi] — иначе переброс; banned-полигоны
// отбрасывают точку (3D по вычисленному Z).
func rollTerritoryPoint(rng *rand.Rand, st *State, terr data.Territory, zlo, zhi, midZ int32, gm *geo.Map) (Position, bool) {
	spanX := int64(terr.MaxX) - int64(terr.MinX) + 1
	spanY := int64(terr.MaxY) - int64(terr.MinY) + 1
	for range npcMaxSpawnAttempts {
		x := int32(int64(terr.MinX) + rng.Int64N(spanX))
		y := int32(int64(terr.MinY) + rng.Int64N(spanY))
		z := midZ
		if geo.InWorld(int(x), int(y)) {
			z = int32(gm.NearestZ(geo.Loc{X: int(x), Y: int(y), Z: int(midZ)}))
		}
		if z < zlo || z > zhi {
			continue // рельеф вне полосы: кламп закопал бы — переброс
		}
		if !terr.Contains(x, y, z) {
			continue
		}
		if slices.ContainsFunc(terr.Banned, func(b data.BannedTerritory) bool { return b.Contains(x, y, z) }) {
			continue
		}
		return Position{X: x, Y: y, Z: z}, true
	}
	st.NPCSkipped++
	return Position{}, false
}

// npcAmongResidents — есть ли в населении хоть один NPC (детектор потери
// письма разворота в recovered; только горутина региона).
func npcAmongResidents(residents []*resident) bool {
	for _, res := range residents {
		if res.ent.Npc != nil {
			return true
		}
	}
	return false
}

// rollHeading — heading записи: отсутствующий (−1, территория игнорирует
// точку) — бросок канона Rnd.get(61794); заданный — маска домена [0,65536).
func rollHeading(rng *rand.Rand, heading int32) int32 {
	if heading < 0 {
		return int32(rng.IntN(npcHeadingRoll))
	}
	return heading & 0xFFFF
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}
