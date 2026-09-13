package data

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"sort"
)

// Коды ошибок декодера секции статики.
const (
	dcTrunc  = "trunc"  // данные кончились внутри структуры
	dcRange  = "range"  // счётчик или длина вне остатка буфера
	dcDup    = "dup"    // повтор ключа категории
	dcTail   = "tail"   // непотреблённый остаток секции
	dcDepth  = "depth"  // raw-дерево глубже потолка
	dcStruct = "struct" // несогласованность структуры записи
)

// decodeError — ошибка декодирования секции с кодом и офсетом.
type decodeError struct {
	Code   string
	Offset int
	Msg    string
}

func (e *decodeError) Error() string {
	return fmt.Sprintf("data: decode %s @%d: %s", e.Code, e.Offset, e.Msg)
}

// maxRawDepth — симметричный потолок глубины raw-деревьев: превышен и в
// парсере, и в декодере секции (парс-зелёное всегда декодируется).
const maxRawDepth = 64

// Минимальные кодированные размеры записей коллекций (поимённо): основа
// потолка счётчиков — амплитуда аллокаций по злому счётчику ограничена
// отношением резидентного размера записи к минимальному кодированному.
// Дети raw-дерева — исключение: вложенность компаундит усиление (до 64
// уровней), поэтому их порция не резервируется вовсе (растёт append'ом).
const (
	recU32    = 4  // один u32 (счётчик/длина/ID)
	recStr    = 4  // пустая строка: u32 длина
	recStrMap = 8  // запись bag: две u32-длины
	recPair   = 8  // узел: два i32
	recItem   = 53 // ID+имя+тип+вес+цена+стек+кристаллы×2+материал+слот+bag-счётчик
	recNpc    = 65
	recTerr   = 36
	recSpawn  = 37
	recZone   = 60
	recDef    = 40
	recMinion = 20
	recDrop   = 20
	recGroup  = 12
	recList   = 12
	recTable  = 8 // имя + счётчик значений
	recAttr   = 8
	recRaw    = 16 // имя + атрибуты + текст + дети
	recSkill  = 36 // три строки + шесть i32 + bool (минимум 37 — запас вниз безопасен)
)

// enc — little-endian кодировщик секции.
type enc struct{ buf []byte }

func (e *enc) u8(v uint8)   { e.buf = append(e.buf, v) }
func (e *enc) u32(v uint32) { e.buf = binary.LittleEndian.AppendUint32(e.buf, v) }
func (e *enc) u64(v uint64) { e.buf = binary.LittleEndian.AppendUint64(e.buf, v) }
func (e *enc) i8(v int8)    { e.u8(uint8(v)) }
func (e *enc) i32(v int32)  { e.u32(uint32(v)) }
func (e *enc) i64(v int64)  { e.u64(uint64(v)) }
func (e *enc) f64(v float64) {
	e.u64(math.Float64bits(v))
}

func (e *enc) boolean(v bool) {
	if v {
		e.u8(1)
	} else {
		e.u8(0)
	}
}

func (e *enc) str(s string) {
	e.u32(uint32(len(s)))
	e.buf = append(e.buf, s...)
}

func (e *enc) strMap(m map[string]string) {
	e.u32(uint32(len(m)))
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		e.str(k)
		e.str(m[k])
	}
}

// decoder — курсор с гвардами: каждое чтение проверяется против остатка,
// ёмкости не выделяются вперёд больше остатка, первая ошибка останавливает
// разбор. Строки интернируются словарём декод-сессии (прецедент keyDict).
type decoder struct {
	b    []byte
	off  int
	dict map[string]string
	err  *decodeError
}

func (d *decoder) fail(code, msg string) bool {
	if d.err == nil {
		d.err = &decodeError{Code: code, Offset: d.off, Msg: msg}
	}
	return false
}

func (d *decoder) need(n int) bool {
	if n < 0 || n > len(d.b)-d.off {
		return d.fail(dcTrunc, fmt.Sprintf("нужно %d байт, остаток %d", n, len(d.b)-d.off))
	}
	return true
}

func (d *decoder) u8() uint8 {
	if !d.need(1) {
		return 0
	}
	v := d.b[d.off]
	d.off++
	return v
}

func (d *decoder) u32() uint32 {
	if !d.need(4) {
		return 0
	}
	v := binary.LittleEndian.Uint32(d.b[d.off:])
	d.off += 4
	return v
}

func (d *decoder) u64() uint64 {
	if !d.need(8) {
		return 0
	}
	v := binary.LittleEndian.Uint64(d.b[d.off:])
	d.off += 8
	return v
}

func (d *decoder) i32() int32   { return int32(d.u32()) }
func (d *decoder) i64() int64   { return int64(d.u64()) }
func (d *decoder) f64() float64 { return math.Float64frombits(d.u64()) }

func (d *decoder) boolean() bool { return d.u8() != 0 }

func (d *decoder) str() string {
	n := d.u32()
	if d.err != nil {
		return ""
	}
	if uint64(n) > uint64(len(d.b)-d.off) {
		d.fail(dcRange, fmt.Sprintf("строка %d байт против остатка %d", n, len(d.b)-d.off))
		return ""
	}
	s := string(d.b[d.off : d.off+int(n)])
	d.off += int(n)
	if c, ok := d.dict[s]; ok {
		return c
	}
	d.dict[s] = s
	return s
}

// count читает счётчик коллекции, ограниченный остатком: порция элементов
// не может быть длиннее остатка, поделённого на минимальный размер записи
// коллекции. Сравнение в uint64 — int(n) на 32-битной цели знаково
// заворачивается.
func (d *decoder) count(minRec int) int {
	n := d.u32()
	if d.err != nil {
		return 0
	}
	if max := uint64((len(d.b) - d.off) / minRec); uint64(n) > max {
		d.fail(dcRange, fmt.Sprintf("счётчик %d против потолка %d по остатку", n, max))
		return 0
	}
	return int(n)
}

func (d *decoder) strMap() map[string]string {
	n := d.count(recStrMap)
	m := make(map[string]string, n)
	for i := 0; i < n && d.err == nil; i++ {
		k, v := d.str(), d.str()
		if d.err != nil {
			return nil
		}
		if _, dup := m[k]; dup {
			d.fail(dcDup, "повтор ключа raw-bag: "+k)
			return nil
		}
		m[k] = v
	}
	return m
}

// EncodeStatic кодирует статику в детерминированный плоский little-endian
// формат: обход отображений по отсортированным ключам, две кодировки одной
// статики байт-в-байт идентичны. Заголовок секции — счётчики категорий.
func EncodeStatic(s *Static) []byte {
	if s == nil {
		panic("data: EncodeStatic(nil)")
	}
	e := &enc{buf: make([]byte, 0, 1<<20)}
	e.u32(uint32(len(s.items)))
	e.u32(uint32(len(s.npcs)))
	e.u32(uint32(len(s.territories)))
	e.u32(uint32(len(s.spawns)))
	e.u32(uint32(len(s.zones)))
	e.u32(uint32(len(s.skills)))

	for _, id := range sortedKeysOf(s.items) {
		it := s.items[id]
		e.i32(int32(it.ID))
		e.str(it.Name)
		e.str(it.Type)
		e.i64(it.Weight)
		e.i64(it.Price)
		e.boolean(it.Stackable)
		e.str(it.CrystalType)
		e.i64(it.CrystalCount)
		e.str(it.Material)
		e.str(it.BodyPart)
		e.strMap(it.set)
	}

	for _, id := range sortedKeysOf(s.npcs) {
		n := s.npcs[id]
		e.i32(int32(n.ID))
		e.str(n.Name)
		e.str(n.Title)
		e.i32(n.Level)
		e.str(n.Type)
		e.str(n.Race)
		e.i32(n.AggroRange)
		e.i32(n.ClanHelpRange)
		e.boolean(n.IsAggressive)
		e.f64(n.CollisionRadius)
		e.f64(n.CollisionHeight)
		e.u32(uint32(len(n.Clans)))
		for _, c := range n.Clans {
			e.str(c)
		}
		e.u32(uint32(len(n.IgnoreNpcIDs)))
		for _, ign := range n.IgnoreNpcIDs {
			e.i32(int32(ign))
		}
		e.u32(uint32(len(n.Minions)))
		for _, m := range n.Minions {
			e.i32(int32(m.NpcID))
			e.i32(m.Count)
			e.i32(m.Max)
			e.i32(m.RespawnTime)
			e.i32(m.WeightPoint)
		}
		e.u32(uint32(len(n.DropLists)))
		for _, dl := range n.DropLists {
			e.str(dl.Type)
			e.u32(uint32(len(dl.Groups)))
			for _, g := range dl.Groups {
				e.f64(g.Chance)
				e.u32(uint32(len(g.Items)))
				for _, it := range g.Items {
					e.i32(int32(it.ItemID))
					e.i32(it.Min)
					e.i32(it.Max)
					e.f64(it.Chance)
				}
			}
			e.u32(uint32(len(dl.Items)))
			for _, it := range dl.Items {
				e.i32(int32(it.ItemID))
				e.i32(it.Min)
				e.i32(it.Max)
				e.f64(it.Chance)
			}
		}
		e.strMap(n.set)
	}

	for _, name := range sortedKeysOf(s.territories) {
		t := s.territories[name]
		e.str(t.Name)
		e.i32(t.MinZ)
		e.i32(t.MaxZ)
		e.i32(t.MinX)
		e.i32(t.MaxX)
		e.i32(t.MinY)
		e.i32(t.MaxY)
		encNodes(e, t.Nodes)
		e.u32(uint32(len(t.Banned)))
		for _, b := range t.Banned {
			e.i32(b.MinZ)
			e.i32(b.MaxZ)
			encNodes(e, b.Nodes)
		}
		e.strMap(t.set)
	}

	for _, sp := range s.spawns {
		e.i32(int32(sp.NpcID))
		e.boolean(sp.HasPoint)
		e.i32(sp.Point.X)
		e.i32(sp.Point.Y)
		e.i32(sp.Point.Z)
		e.i32(sp.Point.Heading)
		e.str(sp.Territory)
		e.i32(sp.Count)
		e.i32(sp.RespawnDelay)
		e.strMap(sp.set)
	}

	for _, zn := range s.zones {
		e.u8(uint8(zn.ShapeKind))
		e.i32(zn.MinZ)
		e.i32(zn.MaxZ)
		e.i32(zn.ZLo)
		e.i32(zn.ZHi)
		e.i32(zn.Rad)
		e.i32(zn.MinX)
		e.i32(zn.MaxX)
		e.i32(zn.MinY)
		e.i32(zn.MaxY)
		encNodes(e, zn.Nodes)
		e.i32(int32(zn.ID))
		e.boolean(zn.HasID)
		e.str(zn.Name)
		e.str(zn.Type)
		e.u32(uint32(len(zn.SpawnPts)))
		for _, p := range zn.SpawnPts {
			e.i32(p.X)
			e.i32(p.Y)
			e.i32(p.Z)
			e.str(p.Type)
		}
		e.u32(uint32(len(zn.RacePts)))
		for _, p := range zn.RacePts {
			e.str(p.Race)
			e.str(p.Point)
		}
		e.strMap(zn.set)
	}

	for _, id := range sortedKeysOf(s.skills) {
		encSkillDef(e, s.skills[id])
	}
	return e.buf
}

func encNodes(e *enc, nodes [][2]int32) {
	e.u32(uint32(len(nodes)))
	for _, nd := range nodes {
		e.i32(nd[0])
		e.i32(nd[1])
	}
}

func encSkill(e *enc, sk Skill) {
	e.str(sk.Name)
	e.str(sk.OperateType)
	e.str(sk.TargetType)
	e.i32(int32(sk.ID))
	e.i32(sk.Level)
	e.i32(sk.HitTime)
	e.i32(sk.ReuseDelay)
	e.i32(sk.AbnormalTime)
	e.i32(sk.IsMagic)
	e.boolean(sk.IsDebuff)
}

func encSkillDef(e *enc, d *SkillDef) {
	e.i32(int32(d.ID))
	e.str(d.Name)
	e.i32(d.Levels)
	e.u32(uint32(len(d.Enchant)))
	for _, en := range d.Enchant {
		e.i8(en.Route)
	}
	e.u32(uint32(len(d.Base)))
	for _, sk := range d.Base {
		encSkill(e, sk)
	}
	e.u32(uint32(len(d.EnchantLevels)))
	for _, levels := range d.EnchantLevels {
		e.u32(uint32(len(levels)))
		for _, sk := range levels {
			encSkill(e, sk)
		}
	}
	e.u32(uint32(len(d.tables)))
	for _, name := range sortedKeysOf(d.tables) {
		e.str(name)
		vals := d.tables[name]
		e.u32(uint32(len(vals)))
		for _, v := range vals {
			e.str(v)
		}
	}
	e.strMap(d.set)
	routes := make([]int, 0, len(d.enchantOverrides))
	for r := range d.enchantOverrides {
		routes = append(routes, int(r))
	}
	sort.Ints(routes)
	e.u32(uint32(len(routes)))
	for _, r := range routes {
		e.i8(int8(r))
		e.strMap(d.enchantOverrides[int8(r)])
	}
	e.u32(uint32(len(d.raw)))
	for _, n := range d.raw {
		encRawNode(e, n)
	}
}

func encRawNode(e *enc, n RawNode) {
	e.str(n.Name)
	e.u32(uint32(len(n.Attrs)))
	for _, a := range n.Attrs {
		e.str(a.Name)
		e.str(a.Value)
	}
	e.str(n.Text)
	e.u32(uint32(len(n.Children)))
	for _, c := range n.Children {
		encRawNode(e, c)
	}
}

// DecodeStatic декодирует формат EncodeStatic. Малформленный вход — ошибка
// с кодом и офсетом, не паника: границы проверяются на каждом чтении,
// счётчики ограничены остатком, дубликаты ключей — ошибка, точное
// потребление секции — инвариант. Чтение полей — последовательными
// операторами: порядок не зависит от порядка вычисления операндов.
func DecodeStatic(b []byte) (*Static, error) {
	d := &decoder{b: b, dict: map[string]string{}}
	nItems, nNpcs, nTerr := d.count(recItem), d.count(recNpc), d.count(recTerr)
	nSpawns, nZones, nSkills := d.count(recSpawn), d.count(recZone), d.count(recDef)
	st := &Static{
		items:       make(map[ItemID]Item, nItems),
		npcs:        make(map[NpcID]Npc, nNpcs),
		territories: make(map[string]Territory, nTerr),
		skills:      make(map[SkillID]*SkillDef, nSkills),
	}

	for i := 0; i < nItems && d.err == nil; i++ {
		var it Item
		it.ID = ItemID(d.i32())
		it.Name = d.str()
		it.Type = d.str()
		it.Weight = d.i64()
		it.Price = d.i64()
		it.Stackable = d.boolean()
		it.CrystalType = d.str()
		it.CrystalCount = d.i64()
		it.Material = d.str()
		it.BodyPart = d.str()
		it.set = d.strMap()
		if d.err != nil {
			break
		}
		if _, dup := st.items[it.ID]; dup {
			d.fail(dcDup, fmt.Sprintf("повтор предмета %d", it.ID))
			break
		}
		st.items[it.ID] = it
	}

	for i := 0; i < nNpcs && d.err == nil; i++ {
		var n Npc
		n.ID = NpcID(d.i32())
		n.Name = d.str()
		n.Title = d.str()
		n.Level = d.i32()
		n.Type = d.str()
		n.Race = d.str()
		n.AggroRange = d.i32()
		n.ClanHelpRange = d.i32()
		n.IsAggressive = d.boolean()
		n.CollisionRadius = d.f64()
		n.CollisionHeight = d.f64()
		nClans := d.count(recStr)
		n.Clans = make([]string, 0, nClans)
		for j := 0; j < nClans && d.err == nil; j++ {
			n.Clans = append(n.Clans, d.str())
		}
		nIgnore := d.count(recU32)
		n.IgnoreNpcIDs = make([]NpcID, 0, nIgnore)
		for j := 0; j < nIgnore && d.err == nil; j++ {
			n.IgnoreNpcIDs = append(n.IgnoreNpcIDs, NpcID(d.i32()))
		}
		nMinions := d.count(recMinion)
		n.Minions = make([]MinionRef, 0, nMinions)
		for j := 0; j < nMinions && d.err == nil; j++ {
			var m MinionRef
			m.NpcID = NpcID(d.i32())
			m.Count = d.i32()
			m.Max = d.i32()
			m.RespawnTime = d.i32()
			m.WeightPoint = d.i32()
			n.Minions = append(n.Minions, m)
		}
		nDrops := d.count(recList)
		n.DropLists = make([]DropList, 0, nDrops)
		for j := 0; j < nDrops && d.err == nil; j++ {
			n.DropLists = append(n.DropLists, decDropList(d))
		}
		n.set = d.strMap()
		if d.err != nil {
			break
		}
		if _, dup := st.npcs[n.ID]; dup {
			d.fail(dcDup, fmt.Sprintf("повтор NPC %d", n.ID))
			break
		}
		st.npcs[n.ID] = n
	}

	for i := 0; i < nTerr && d.err == nil; i++ {
		var t Territory
		t.Name = d.str()
		t.MinZ = d.i32()
		t.MaxZ = d.i32()
		t.MinX = d.i32()
		t.MaxX = d.i32()
		t.MinY = d.i32()
		t.MaxY = d.i32()
		t.Nodes = decNodes(d)
		nBanned := d.count(recList)
		t.Banned = make([]BannedTerritory, 0, nBanned)
		for j := 0; j < nBanned && d.err == nil; j++ {
			var b BannedTerritory
			b.MinZ = d.i32()
			b.MaxZ = d.i32()
			b.Nodes = decNodes(d)
			t.Banned = append(t.Banned, b)
		}
		t.set = d.strMap()
		if d.err != nil {
			break
		}
		if _, dup := st.territories[t.Name]; dup {
			d.fail(dcDup, "повтор территории: "+t.Name)
			break
		}
		st.territories[t.Name] = t
	}

	st.spawns = make([]NpcSpawn, 0, nSpawns)
	for i := 0; i < nSpawns && d.err == nil; i++ {
		var sp NpcSpawn
		sp.NpcID = NpcID(d.i32())
		sp.HasPoint = d.boolean()
		sp.Point.X = d.i32()
		sp.Point.Y = d.i32()
		sp.Point.Z = d.i32()
		sp.Point.Heading = d.i32()
		sp.Territory = d.str()
		sp.Count = d.i32()
		sp.RespawnDelay = d.i32()
		sp.set = d.strMap()
		if d.err != nil {
			break
		}
		st.spawns = append(st.spawns, sp)
	}

	st.zones = make([]Zone, 0, nZones)
	for i := 0; i < nZones && d.err == nil; i++ {
		var zn Zone
		zn.ShapeKind = ShapeKind(d.u8())
		zn.MinZ = d.i32()
		zn.MaxZ = d.i32()
		zn.ZLo = d.i32()
		zn.ZHi = d.i32()
		zn.Rad = d.i32()
		zn.MinX = d.i32()
		zn.MaxX = d.i32()
		zn.MinY = d.i32()
		zn.MaxY = d.i32()
		zn.Nodes = decNodes(d)
		zn.ID = ZoneID(d.i32())
		zn.HasID = d.boolean()
		zn.Name = d.str()
		zn.Type = d.str()
		nSpawnPts := d.count(recGroup)
		zn.SpawnPts = make([]ZoneSpawn, 0, nSpawnPts)
		for j := 0; j < nSpawnPts && d.err == nil; j++ {
			var p ZoneSpawn
			p.X = d.i32()
			p.Y = d.i32()
			p.Z = d.i32()
			p.Type = d.str()
			zn.SpawnPts = append(zn.SpawnPts, p)
		}
		nRacePts := d.count(recStrMap)
		zn.RacePts = make([]ZoneRacePoint, 0, nRacePts)
		for j := 0; j < nRacePts && d.err == nil; j++ {
			var p ZoneRacePoint
			p.Race = d.str()
			p.Point = d.str()
			zn.RacePts = append(zn.RacePts, p)
		}
		zn.set = d.strMap()
		if d.err != nil {
			break
		}
		if zn.ShapeKind > ShapeCylinder {
			d.fail(dcStruct, fmt.Sprintf("неизвестный вид геометрии зоны %d", zn.ShapeKind))
			break
		}
		st.zones = append(st.zones, zn)
	}

	for i := 0; i < nSkills && d.err == nil; i++ {
		def := decSkillDef(d)
		if d.err != nil {
			break
		}
		if _, dup := st.skills[def.ID]; dup {
			d.fail(dcDup, fmt.Sprintf("повтор скилла %d", def.ID))
			break
		}
		st.skills[def.ID] = def
	}

	if d.err != nil {
		return nil, d.err
	}
	if d.off != len(d.b) {
		return nil, &decodeError{Code: dcTail, Offset: d.off,
			Msg: fmt.Sprintf("непотреблённый остаток секции: %d байт", len(d.b)-d.off)}
	}
	if len(st.items) != nItems || len(st.npcs) != nNpcs || len(st.territories) != nTerr ||
		len(st.spawns) != nSpawns || len(st.zones) != nZones || len(st.skills) != nSkills {
		return nil, &decodeError{Code: dcStruct, Offset: 0,
			Msg: "декодировано меньше записей, чем заявлено в заголовке секции"}
	}
	return st, nil
}

func decDropList(d *decoder) DropList {
	var dl DropList
	dl.Type = d.str()
	nGroups := d.count(recGroup)
	dl.Groups = make([]DropGroup, 0, nGroups)
	for j := 0; j < nGroups && d.err == nil; j++ {
		var g DropGroup
		g.Chance = d.f64()
		nItems := d.count(recDrop)
		g.Items = make([]Drop, 0, nItems)
		for k := 0; k < nItems && d.err == nil; k++ {
			g.Items = append(g.Items, decDrop(d))
		}
		dl.Groups = append(dl.Groups, g)
	}
	nItems := d.count(recDrop)
	dl.Items = make([]Drop, 0, nItems)
	for k := 0; k < nItems && d.err == nil; k++ {
		dl.Items = append(dl.Items, decDrop(d))
	}
	return dl
}

func decDrop(d *decoder) Drop {
	var it Drop
	it.ItemID = ItemID(d.i32())
	it.Min = d.i32()
	it.Max = d.i32()
	it.Chance = d.f64()
	return it
}

func decNodes(d *decoder) [][2]int32 {
	n := d.count(recPair)
	nodes := make([][2]int32, 0, n)
	for i := 0; i < n && d.err == nil; i++ {
		x, y := d.i32(), d.i32()
		nodes = append(nodes, [2]int32{x, y})
	}
	return nodes
}

func decSkill(d *decoder) Skill {
	var sk Skill
	sk.Name = d.str()
	sk.OperateType = d.str()
	sk.TargetType = d.str()
	sk.ID = SkillID(d.i32())
	sk.Level = d.i32()
	sk.HitTime = d.i32()
	sk.ReuseDelay = d.i32()
	sk.AbnormalTime = d.i32()
	sk.IsMagic = d.i32()
	sk.IsDebuff = d.boolean()
	return sk
}

func decSkillDef(d *decoder) *SkillDef {
	def := &SkillDef{}
	def.ID = SkillID(d.i32())
	def.Name = d.str()
	def.Levels = d.i32()
	nEnch := d.count(1)
	def.Enchant = make([]SkillEnchant, 0, nEnch)
	seenRoute := make(map[int8]bool, nEnch)
	for j := 0; j < nEnch && d.err == nil; j++ {
		r := int8(d.u8())
		if r < 1 || r > 8 {
			d.fail(dcStruct, fmt.Sprintf("энчант-маршрут %d вне 1..8", r))
			return def
		}
		if seenRoute[r] {
			d.fail(dcDup, fmt.Sprintf("повтор энчант-маршрута %d", r))
			return def
		}
		seenRoute[r] = true
		def.Enchant = append(def.Enchant, SkillEnchant{Route: r})
	}
	nBase := d.count(recSkill)
	def.Base = make([]Skill, 0, nBase)
	for j := 0; j < nBase && d.err == nil; j++ {
		def.Base = append(def.Base, decSkill(d))
	}
	nRoutes := d.count(recU32)
	def.EnchantLevels = make([][]Skill, 0, nRoutes)
	for j := 0; j < nRoutes && d.err == nil; j++ {
		nLevels := d.count(recSkill)
		levels := make([]Skill, 0, nLevels)
		for k := 0; k < nLevels && d.err == nil; k++ {
			levels = append(levels, decSkill(d))
		}
		def.EnchantLevels = append(def.EnchantLevels, levels)
	}
	if d.err == nil && len(def.EnchantLevels) != len(def.Enchant) {
		d.fail(dcStruct, fmt.Sprintf("маршрутов уровней %d против маршрутов %d",
			len(def.EnchantLevels), len(def.Enchant)))
		return def
	}
	nTables := d.count(recTable)
	def.tables = make(map[string][]string, nTables)
	for j := 0; j < nTables && d.err == nil; j++ {
		name := d.str()
		nVals := d.count(recStr)
		vals := make([]string, 0, nVals)
		for k := 0; k < nVals && d.err == nil; k++ {
			vals = append(vals, d.str())
		}
		if d.err != nil {
			break
		}
		if _, dup := def.tables[name]; dup {
			d.fail(dcDup, "повтор таблицы: "+name)
			break
		}
		def.tables[name] = vals
	}
	def.set = d.strMap()
	nOv := d.count(recU32)
	def.enchantOverrides = make(map[int8]map[string]string, nOv)
	for j := 0; j < nOv && d.err == nil; j++ {
		r := int8(d.u8())
		if r < 1 || r > 8 {
			d.fail(dcStruct, fmt.Sprintf("маршрут override %d вне 1..8", r))
			return def
		}
		if _, dup := def.enchantOverrides[r]; dup {
			d.fail(dcDup, fmt.Sprintf("повтор override маршрута %d", r))
			return def
		}
		def.enchantOverrides[r] = d.strMap()
	}
	nRaw := d.count(recRaw)
	def.raw = make([]RawNode, 0, nRaw)
	for j := 0; j < nRaw && d.err == nil; j++ {
		def.raw = append(def.raw, decRawNode(d, 1))
	}
	return def
}

func decRawNode(d *decoder, depth int) RawNode {
	if depth > maxRawDepth {
		d.fail(dcDepth, fmt.Sprintf("raw-дерево глубже %d", maxRawDepth))
		return RawNode{}
	}
	var n RawNode
	n.Name = d.str()
	nAttrs := d.count(recAttr)
	n.Attrs = make([]RawAttr, 0, nAttrs)
	for i := 0; i < nAttrs && d.err == nil; i++ {
		var a RawAttr
		a.Name = d.str()
		a.Value = d.str()
		n.Attrs = append(n.Attrs, a)
	}
	n.Text = d.str()
	// Дети raw-дерева не резервируются по счётчику: вложенность до 64 уровней
	// компаундит усиление злого счётчика в аллокации — порция растёт append'ом
	// по фактически декодированным узлам.
	nChildren := d.count(recRaw)
	for i := 0; i < nChildren && d.err == nil; i++ {
		n.Children = append(n.Children, decRawNode(d, depth+1))
	}
	return n
}

// sortedKeysOf — отсортированные ключи отображения (детерминизм кодирования).
func sortedKeysOf[K cmp.Ordered, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
