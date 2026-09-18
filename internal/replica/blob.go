package replica

import (
	"slices"
	"sort"
	"sync/atomic"

	"github.com/udisondev/l2go/internal/transport"
)

// RecordKind — тип AoI-записи.
type RecordKind uint8

// Записи фазы 3: игроки (NPC-население публикует P3.10).
const (
	RecordKindPlayer RecordKind = iota
	RecordKindNPC
)

// Record — полный пейлоад AoI-записи (значение): вечный ID, ячейка позиции,
// кинематика (включая клампнутую цель движения — кадры P3.9 самодостаточны
// пейлоадом события) и поля потребителей CharInfo/NpcInfo (примитивы:
// ребро replica←data не открывается). Поля по потребителю. Cell в построенном
// блобе перезаписывается издателем из координат (единый источник — сетка).
type Record struct {
	Entity    transport.EntityID
	Cell      CellID
	X, Y      int32
	Z         int32
	DestX     int32
	DestY     int32
	DestZ     int32
	Heading   int32
	Moving    bool
	Kind      RecordKind
	Flags     Flags
	Name      string
	Race      int32
	Female    bool
	BaseClass int32
	ClassID   int32
	HairStyle int32
	HairColor int32
	Face      int32

	// Поля NPC (NpcInfo, P3.10; нули у игроков — как Race у NPC):
	// TemplateID — id шаблона датапака (display), скоростной блок и
	// коллизии — из скина рождения.
	TemplateID            int32
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

// segment — сегмент одной ячейки: срез в общих слайсах блоба (плотная
// укладка — 3–4 аллокации на блоб-тик при любом числе ячеек). Слоты
// остаются глобальным стабильным пространством издателя: сегмент лишь
// группирует слоты своей клетки, порядок внутри возрастает.
type segment struct {
	cell    CellID
	slotOff int
	slotLen int
}

// seat — жилец слота поколения: клетка и вечный id (единая чистка хвоста
// join-а и детект реюза слота). Свободный слот — сентинел cellInvalid и
// нулевой id; нулевая клетка валидна и «нет клетки» не означает.
type seat struct {
	cell CellID
	ent  transport.EntityID
}

// Blob — иммутабельное после Commit поколение. Поля неотэкспортированы:
// внешний читатель идёт через Publisher.Read/Committed (механика D4), Join и
// пакетные тесты читают напрямую. Build-результат владеет собственными
// структурами (копия значений записей) — входной слайс мира переиспользуется
// без алиасинга опубликованного поколения.
type Blob struct {
	gen    uint64           // Generation: номер поколения
	base   uint64           // BaseGen: поколение-база dirty-манифеста (норма gen-1)
	header MembershipHeader // маркеры переезда — фаза 3 всегда пусто

	segs       []segment // сортированы по cell; пустых ячеек нет
	slots      []int     // глобальные слоты записей, возрастают внутри сегмента
	records    []Record  // параллельно slots
	seatBySlot []seat    // слот → жилец; дырка — {cellInvalid, 0}
	slotPos    []int32   // слот → индекс в records; дырка — −1 (база сравнения dirty)
	member     []uint64  // битмапы по глобальным слотам
	born       []uint64
	changed    []uint64
	gone       []uint64

	// Построенные структуры издателя (продвигаются в его состояние Commit-ом):
	slotOf map[transport.EntityID]int
	free   []int
}

// segmentOf — сегмент клетки (бинарный поиск по сортированным segs).
func (b *Blob) segmentOf(c CellID) (segment, bool) {
	lo := sort.Search(len(b.segs), func(i int) bool { return b.segs[i].cell >= c })
	if lo < len(b.segs) && b.segs[lo].cell == c {
		return b.segs[lo], true
	}
	return segment{}, false
}

func bitMark(bitmap []uint64, slot int) { bitmap[slot/64] |= 1 << (uint(slot) % 64) }

func bitHas(bitmap []uint64, slot int) bool {
	return bitmap[slot/64]&(1<<(uint(slot)%64)) != 0
}

// Publisher — строитель блоба у владельца: стабильные плотные слоты (слот
// присваивается при первом входе записи, освобождается при уходе, реюз —
// младший свободный; реюз детектируется вечным id в слоте). Build не мутирует
// издателя вовсе; Commit пиннует порядок «сначала prev, потом свап» (паника
// между ними оставляет согласованную пару view/prev — опубликованное может
// отстать на поколение, безвредно).
type Publisher struct {
	gen       uint64 // поколение последнего Commit
	grid      Grid
	prev      *Blob // база dirty-сравнения (последний Commit)
	slots     map[transport.EntityID]int
	free      []int
	committed atomic.Pointer[Blob]
}

// NewPublisher создаёт издателя сетки (нулевое значение тоже работоспособно:
// вырожденная сетка cellSize 1).
func NewPublisher(grid Grid) *Publisher {
	return &Publisher{grid: grid, slots: make(map[transport.EntityID]int)}
}

// buildOrder — ключ плотной укладки: сортировка (клетка, слот).
type buildOrder struct {
	cell CellID
	slot int32
	idx  int32
}

// byCellSlot — порядок плотной укладки (slices.SortFunc без interface-
// конверсии: публикация блоба исполняется каждый тик).
func byCellSlot(a, b buildOrder) int {
	if a.cell != b.cell {
		return int(a.cell) - int(b.cell)
	}
	return int(a.slot) - int(b.slot)
}

// Build строит следующее поколение: размещает записи по стабильным слотам
// (прошлые — на своих местах, новые — младшим свободным либо расширением),
// освобождает слоты ушедших, распределяет записи по ячейкам позиций (плотная
// укладка: общие слайсы + сегменты-срезы) и выводит dirty-манифест
// сравнением полных пейлоадов с prev (честный dirty — ловит и смену Cell,
// и смену Flags). Возвращает блоб, НЕ публикуя: публикация — Commit в фазе
// publish шага.
func (p *Publisher) Build(recs []Record) *Blob {
	slots := make(map[transport.EntityID]int, len(p.slots))
	for k, v := range p.slots {
		slots[k] = v
	}
	free := make([]int, len(p.free))
	copy(free, p.free)

	// ушедшие записи: слот освобождается, карта — под новое население;
	// сбор свободных слотов — по отсортированным ключам (детерминизм
	// наполнения free-list: map-итерация рандомизирована)
	present := make(map[transport.EntityID]struct{}, len(recs))
	for i := range recs {
		present[recs[i].Entity] = struct{}{}
	}
	var departed []int
	for ent, slot := range slots {
		if _, ok := present[ent]; !ok {
			delete(slots, ent)
			departed = append(departed, slot)
		}
	}
	// свободный список отсортирован целиком: конкатенация префикса с хвостом
	// прошлых поколений оставляла бы «младший свободный» зависящим от
	// рандомизированного порядка map-итерации сбора departed
	free = append(free, departed...)
	slices.Sort(free)

	b := &Blob{gen: p.gen + 1, base: p.gen}
	// экстент слотов поколения: за хвостом удержанных И экстентом prev —
	// экстент монотонен, свободные слоты всегда ниже него (освобождены из
	// прошлых поколений): свежий слот не сталкивается со свободным,
	// реюз остаётся младшим свободным
	extent := 0
	if p.prev != nil {
		extent = len(p.prev.slotPos)
	}
	for _, slot := range slots {
		if slot+1 > extent {
			extent = slot + 1
		}
	}
	fresh := extent
	order := make([]buildOrder, len(recs))
	for i := range recs {
		rec := &recs[i]
		slot, ok := slots[rec.Entity]
		if !ok {
			if len(free) > 0 {
				slot = free[0] // младший свободный (список отсортирован)
				free = free[1:]
			} else {
				slot = fresh
				fresh++
			}
			slots[rec.Entity] = slot
		}
		order[i] = buildOrder{cell: p.grid.CellOf(rec.X, rec.Y), slot: int32(slot), idx: int32(i)}
	}
	slices.SortFunc(order, byCellSlot)

	b.slots = make([]int, len(order))
	b.records = make([]Record, len(order))
	b.segs = make([]segment, 0, len(order))
	for k := range order {
		rec := recs[order[k].idx]
		rec.Cell = order[k].cell
		b.slots[k] = int(order[k].slot)
		b.records[k] = rec
		if k == 0 || order[k-1].cell != order[k].cell {
			b.segs = append(b.segs, segment{cell: order[k].cell, slotOff: k})
		}
		b.segs[len(b.segs)-1].slotLen++
	}

	// сиды и обратный индекс несут монотонный экстент: дырки — сентинел
	// (чистка хвоста join-а), хвостовые слоты ушедших покрыты gone
	b.seatBySlot = make([]seat, fresh)
	for i := range b.seatBySlot {
		b.seatBySlot[i].cell = cellInvalid
	}
	b.slotPos = make([]int32, fresh)
	for i := range b.slotPos {
		b.slotPos[i] = -1
	}
	words := (fresh + 63) / 64
	bitmap := make([]uint64, 4*words)
	b.member, b.born, b.changed, b.gone = bitmap[:words], bitmap[words:2*words], bitmap[2*words:3*words], bitmap[3*words:]
	for k := range b.slots {
		slot := b.slots[k]
		rec := &b.records[k]
		bitMark(b.member, slot)
		b.seatBySlot[slot] = seat{cell: rec.Cell, ent: rec.Entity}
		b.slotPos[slot] = int32(k)
		var prevRec Record
		had := false
		if p.prev != nil && slot < len(p.prev.slotPos) {
			if pos := p.prev.slotPos[slot]; pos >= 0 {
				prevRec = p.prev.records[pos]
				had = true
			}
		}
		switch {
		case !had || prevRec.Entity != rec.Entity:
			bitMark(b.born, slot) // новый жилец (включая реюз слота)
		case prevRec != *rec:
			bitMark(b.changed, slot)
		}
	}
	// gone: слот занят в prev, но пуст или передан другому в новом поколении
	// (слоты prev всегда внутри монотонного экстента нового блоба)
	if p.prev != nil {
		for slot := range p.prev.slotPos {
			if p.prev.slotPos[slot] < 0 {
				continue
			}
			if b.seatBySlot[slot].ent != p.prev.records[p.prev.slotPos[slot]].Entity {
				bitMark(b.gone, slot)
			}
		}
	}
	b.slotOf = slots
	b.free = free
	b.header = MembershipHeader{Generation: b.gen}
	return b
}

// Commit публикует построенное поколение: сначала продвигает prev (базу
// будущего манифеста — детект рассинхрона поколений у Join остаётся
// согласованным при панике между шагами), затем свапает закоммиченное.
func (p *Publisher) Commit(b *Blob) {
	p.prev = b
	p.slots = b.slotOf
	p.free = b.free
	p.gen = b.gen
	p.committed.Store(b)
}

// Committed возвращает текущее закоммиченное поколение (nil до первого Commit).
func (p *Publisher) Committed() *Blob { return p.committed.Load() }

// Read — advisory-точечное чтение по закоммиченному поколению: бинарный
// поиск сегмента клетки + линейный поиск внутри сегмента (точечные чтения
// per-candidate редки; aux-индекс — по измерениям, база — BenchmarkAdvisoryRead).
func (p *Publisher) Read(cell CellID, id transport.EntityID) (Snapshot, bool) {
	b := p.committed.Load()
	if b == nil {
		return Snapshot{}, false
	}
	seg, ok := b.segmentOf(cell)
	if !ok {
		return Snapshot{}, false
	}
	for k := seg.slotOff; k < seg.slotOff+seg.slotLen; k++ {
		rec := &b.records[k]
		if rec.Entity != id {
			continue
		}
		return Snapshot{entity: rec.Entity, x: rec.X, y: rec.Y, z: rec.Z, kind: rec.Kind, flags: rec.Flags}, true
	}
	return Snapshot{}, false
}
