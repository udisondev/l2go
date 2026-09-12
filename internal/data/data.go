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

// dumpNpcs и dumpTerritories/dumpSpawns — реализации P2.2.
func dumpNpcs(sb *strings.Builder, s *Static)        {}
func dumpTerritories(sb *strings.Builder, s *Static) {}
func dumpSpawns(sb *strings.Builder, s *Static)      {}

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
