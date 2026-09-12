// Package data загружает статические данные мира (датапак) из файлового дерева
// в типизированные иммутабельные структуры. Данные после загрузки не меняются:
// записи возвращаются по значению, все вложенные слайсы и мапы — только чтение
// (raw-bag — через метод Set); мутация — нарушение контракта пакета. Ошибки
// целостности (дубликат ID, битая ссылка, кривое число) не прерывают обход:
// Load читает всё дерево, разрешает ссылки после всех категорий и возвращает
// полный отчёт.
package data

import (
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

// Static — загруженная статика мира. Иммутабельна после загрузки: доступ к
// записям — только через методы чтения, мутируемых ссылок наружу не отдаётся;
// слайс Spawns и вложенные слайсы записей — под контрактом «только чтение».
type Static struct {
	items       map[ItemID]Item
	npcs        map[NpcID]Npc
	territories map[string]Territory
	spawns      []NpcSpawn
}

// Item возвращает запись предмета по ID.
func (s *Static) Item(id ItemID) (Item, bool) {
	it, ok := s.items[id]
	return it, ok
}

// Npc возвращает запись NPC по ID.
func (s *Static) Npc(id NpcID) (Npc, bool) {
	n, ok := s.npcs[id]
	return n, ok
}

// Territory возвращает территорию спавна по имени.
func (s *Static) Territory(name string) (Territory, bool) {
	t, ok := s.territories[name]
	return t, ok
}

// Spawns возвращает записи спавнов в детерминированном порядке: путь файла,
// затем позиция в файле (порядок обхода загрузки). Слайс статики — только
// чтение.
func (s *Static) Spawns() []NpcSpawn {
	return s.spawns
}

// Load читает статику из fsys и возвращает её вместе с отчётом валидации.
// Возвращённая ошибка — только фатальные условия самой файловой системы
// (корень или каталог категории не читается); ошибки данных — записи отчёта:
// Load доходит до конца и собирает полный перечень. Static пригоден к
// использованию только когда Report.HasErrors вернула false.
func Load(fsys fs.FS) (*Static, *Report, error) {
	ctx := newLoadCtx()
	for _, l := range loaders {
		if ctx.fatalErr != nil {
			break
		}
		l.run(fsys, ctx)
	}
	if ctx.fatalErr != nil {
		return nil, nil, ctx.fatalErr
	}
	resolveLinks(ctx)
	ctx.rep.Manifest = manifestOf(ctx.inputs)
	return &Static{items: ctx.items, npcs: ctx.npcs, territories: ctx.territories, spawns: ctx.spawns}, ctx.rep, nil
}

// Dump возвращает канонический текстовый вид статики: предметы по возрастанию
// ID, NPC по возрастанию ID (включая дроплисты и миньонов), территории по
// имени, спавны в порядке загрузки; поля в фиксированном порядке, ключи
// параметров отсортированы. Формат стабилен и служит золотым сравнением и
// формой сверки эквивалентности скомпилированного артефакта статики.
func (s *Static) Dump() string {
	if s == nil {
		return ""
	}
	var sb strings.Builder
	dumpItems(&sb, s)
	dumpNpcs(&sb, s)
	dumpTerritories(&sb, s)
	dumpSpawns(&sb, s)
	return sb.String()
}

func dumpItems(sb *strings.Builder, s *Static) {
	ids := make([]int64, 0, len(s.items))
	for id := range s.items {
		ids = append(ids, int64(id))
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, idv := range ids {
		it := s.items[ItemID(idv)]
		fmt.Fprintf(sb,
			"id=%d name=%q type=%q weight=%d price=%d stackable=%t crystal_type=%q crystal_count=%d material=%q bodypart=%q",
			it.ID, it.Name, it.Type, it.Weight, it.Price, it.Stackable,
			it.CrystalType, it.CrystalCount, it.Material, it.BodyPart)
		writeSets(sb, it.set)
		sb.WriteByte('\n')
	}
}

// dumpNpcs пишет канонический текст NPC по возрастанию ID, включая
// дроплисты (порядок файла) и миньонов — сверка артефакта P2.7 без них слепа.
func dumpNpcs(sb *strings.Builder, s *Static) {
	ids := make([]int64, 0, len(s.npcs))
	for id := range s.npcs {
		ids = append(ids, int64(id))
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, idv := range ids {
		n := s.npcs[NpcID(idv)]
		fmt.Fprintf(sb,
			"npc id=%d name=%q title=%q level=%d type=%q race=%q aggro=%d clanHelp=%d aggressive=%t collision=%s/%s",
			n.ID, n.Name, n.Title, n.Level, n.Type, n.Race, n.AggroRange,
			n.ClanHelpRange, n.IsAggressive, ftoa(n.CollisionRadius), ftoa(n.CollisionHeight))
		sb.WriteString(" clans=[")
		for i, c := range n.Clans {
			if i > 0 {
				sb.WriteByte(' ')
			}
			sb.WriteString(c)
		}
		sb.WriteString("] ignore=[")
		for i, id := range n.IgnoreNpcIDs {
			if i > 0 {
				sb.WriteByte(' ')
			}
			fmt.Fprintf(sb, "%d", id)
		}
		sb.WriteString("] minions=[")
		for i, m := range n.Minions {
			if i > 0 {
				sb.WriteByte(' ')
			}
			fmt.Fprintf(sb, "%d:%d/%d/%d/%d", m.NpcID, m.Count, m.Max, m.RespawnTime, m.WeightPoint)
		}
		sb.WriteString("] drops=[")
		for i, dl := range n.DropLists {
			if i > 0 {
				sb.WriteByte(' ')
			}
			sb.WriteString(dl.Type)
			sb.WriteByte('{')
			for _, g := range dl.Groups {
				fmt.Fprintf(sb, "g%s[", ftoa(g.Chance))
				for j, d := range g.Items {
					if j > 0 {
						sb.WriteByte(' ')
					}
					fmt.Fprintf(sb, "%d %d-%d@%s", d.ItemID, d.Min, d.Max, ftoa(d.Chance))
				}
				sb.WriteByte(']')
			}
			if len(dl.Items) > 0 {
				if len(dl.Groups) > 0 {
					sb.WriteByte(' ')
				}
				sb.WriteString("i[")
				for j, d := range dl.Items {
					if j > 0 {
						sb.WriteByte(' ')
					}
					fmt.Fprintf(sb, "%d %d-%d@%s", d.ItemID, d.Min, d.Max, ftoa(d.Chance))
				}
				sb.WriteByte(']')
			}
			sb.WriteByte('}')
		}
		sb.WriteString("]")
		writeSets(sb, n.set)
		sb.WriteByte('\n')
	}
}

// dumpTerritories пишет канонический текст территорий по имени.
func dumpTerritories(sb *strings.Builder, s *Static) {
	names := make([]string, 0, len(s.territories))
	for name := range s.territories {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t := s.territories[name]
		fmt.Fprintf(sb, "terr name=%q minZ=%d maxZ=%d nodes=[", t.Name, t.MinZ, t.MaxZ)
		for i, nd := range t.Nodes {
			if i > 0 {
				sb.WriteByte(' ')
			}
			fmt.Fprintf(sb, "%d,%d", nd[0], nd[1])
		}
		sb.WriteString("] banned=[")
		for i, b := range t.Banned {
			if i > 0 {
				sb.WriteByte(' ')
			}
			fmt.Fprintf(sb, "{%d,%d [", b.MinZ, b.MaxZ)
			for j, nd := range b.Nodes {
				if j > 0 {
					sb.WriteByte(' ')
				}
				fmt.Fprintf(sb, "%d,%d", nd[0], nd[1])
			}
			sb.WriteString("]}")
		}
		sb.WriteString("]")
		writeSets(sb, t.set)
		sb.WriteByte('\n')
	}
}

// dumpSpawns пишет записи спавнов в порядке загрузки.
func dumpSpawns(sb *strings.Builder, s *Static) {
	for _, sp := range s.spawns {
		fmt.Fprintf(sb, "spawn npc=%d", sp.NpcID)
		if sp.HasPoint {
			fmt.Fprintf(sb, " point=%d,%d,%d,%d", sp.Point.X, sp.Point.Y, sp.Point.Z, sp.Point.Heading)
		} else {
			sb.WriteString(" point=-")
		}
		fmt.Fprintf(sb, " terr=%q count=%d respawn=%d", sp.Territory, sp.Count, sp.RespawnDelay)
		writeSets(sb, sp.set)
		sb.WriteByte('\n')
	}
}

// ftoa — каноническая запись числа с плавающей точкой для дампа.
func ftoa(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// writeSets дописывает отсортированный набор параметров записи.
func writeSets(sb *strings.Builder, set map[string]string) {
	if len(set) == 0 {
		sb.WriteString(" sets=[]")
		return
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sb.WriteString(" sets=[")
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(set[k])
	}
	sb.WriteByte(']')
}
