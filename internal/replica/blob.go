package replica

import (
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
// ребро replica←data не открывается). Поля по потребителю.
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
}

// segment — SoA-сегмент одной ячейки: записи по плотным слотам (слот = индекс,
// дырка — нулевой Entity), членство и dirty-манифест — битмапами по слотам.
type segment struct {
	cell    CellID
	records []Record
	member  []uint64
	born    []uint64
	changed []uint64
	gone    []uint64
}

func bitMark(bitmap []uint64, slot int) { bitmap[slot/64] |= 1 << (uint(slot) % 64) }

func bitHas(bitmap []uint64, slot int) bool {
	return bitmap[slot/64]&(1<<(uint(slot)%64)) != 0
}

// Blob — иммутабельное после Commit поколение. Поля неотэкспортированы:
// внешний читатель идёт через Publisher.Read/Committed (механика D4), Join и
// пакетные тесты читают напрямую. Build-результат владеет собственными
// структурами (копия значений записей) — входной слайс мира переиспользуется
// без алиасинга опубликованного поколения.
type Blob struct {
	gen    uint64 // Generation: номер поколения
	base   uint64 // BaseGen: поколение-база dirty-манифеста (норма gen-1)
	seg    segment
	header MembershipHeader // маркеры переезда — фаза 3 всегда пусто

	// Построенные структуры издателя (продвигаются в его состояние Commit-ом):
	slots map[transport.EntityID]int
	free  []int
}

// Publisher — строитель блоба у владельца: стабильные плотные слоты (слот
// присваивается при первом входе записи, освобождается при уходе, реюз —
// младший свободный; реюз детектируется вечным id в слоте). Build не мутирует
// издателя вовсе; Commit пиннует порядок «сначала prev, потом свап» (паника
// между ними оставляет согласованную пару view/prev — опубликованное может
// отстать на поколение, безвредно).
type Publisher struct {
	gen       uint64 // поколение последнего Commit
	slots     map[transport.EntityID]int
	free      []int
	prev      *segment // база dirty-сравнения (последний Commit)
	committed atomic.Pointer[Blob]
}

// NewPublisher создаёт издателя (нулевое значение тоже работоспособно).
func NewPublisher() *Publisher {
	return &Publisher{slots: make(map[transport.EntityID]int)}
}

// Build строит следующее поколение: размещает записи по стабильным слотам
// (прошлые — на своих местах, новые — младшим свободным либо расширением),
// освобождает слоты ушедших, выводит dirty-манифест сравнением полных
// пейлоадов с prev (честный dirty — ловит и смену Flags). Возвращает блоб, НЕ
// публикуя: публикация — Commit в фазе publish шага.
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
	sort.Ints(departed)
	free = append(free, departed...)

	b := &Blob{gen: p.gen + 1, base: p.gen}
	maxSlot := -1
	for _, slot := range slots {
		if slot > maxSlot {
			maxSlot = slot
		}
	}
	startLen := maxSlot + 1
	// gone-битмап покрывает и слоты, жившие только в prev (хвостовые дырки) —
	// иначе усохший сегмент не итерирует ушедшие слоты
	if p.prev != nil && len(p.prev.records) > startLen {
		startLen = len(p.prev.records)
	}
	b.seg.records = make([]Record, startLen)
	for i := range recs {
		rec := recs[i]
		b.seg.cell = rec.Cell
		slot, ok := slots[rec.Entity]
		if !ok {
			if len(free) > 0 {
				slot = free[0] // младший свободный (free отсортирован)
				free = free[1:]
			} else {
				slot = len(b.seg.records)
			}
			slots[rec.Entity] = slot
		}
		for slot >= len(b.seg.records) {
			b.seg.records = append(b.seg.records, Record{})
		}
		b.seg.records[slot] = rec
	}
	words := (len(b.seg.records) + 63) / 64
	b.seg.member = make([]uint64, words)
	b.seg.born = make([]uint64, words)
	b.seg.changed = make([]uint64, words)
	b.seg.gone = make([]uint64, words)
	for slot := range b.seg.records {
		rec := b.seg.records[slot]
		if rec.Entity == 0 {
			continue
		}
		bitMark(b.seg.member, slot)
		prevRec, was := p.prevRecord(slot)
		switch {
		case !was || prevRec.Entity != rec.Entity:
			bitMark(b.seg.born, slot) // новый жилец (включая реюз слота)
		case prevRec != rec:
			bitMark(b.seg.changed, slot)
		}
	}
	// gone: слот занят в prev, но пуст или передан другому в новом поколении
	if p.prev != nil {
		for slot := range p.prev.records {
			prevRec := p.prev.records[slot]
			if prevRec.Entity == 0 {
				continue
			}
			if slot >= len(b.seg.records) || b.seg.records[slot].Entity != prevRec.Entity {
				bitMark(b.seg.gone, slot)
			}
		}
	}
	b.slots = slots
	b.free = free
	b.header = MembershipHeader{Generation: b.gen}
	return b
}

// prevRecord — запись prev-поколения по слоту (nil-сегмент — первый Build).
func (p *Publisher) prevRecord(slot int) (Record, bool) {
	if p.prev == nil || slot >= len(p.prev.records) {
		return Record{}, false
	}
	rec := p.prev.records[slot]
	if rec.Entity == 0 {
		return Record{}, false
	}
	return rec, true
}

// Commit публикует построенное поколение: сначала продвигает prev (базу
// будущего манифеста — детект рассинхрона поколений у Join остаётся
// согласованным при панике между шагами), затем свапает закоммиченное.
func (p *Publisher) Commit(b *Blob) {
	seg := b.seg
	p.prev = &seg
	p.slots = b.slots
	p.free = b.free
	p.gen = b.gen
	p.committed.Store(b)
}

// Committed возвращает текущее закоммиченное поколение (nil до первого Commit).
func (p *Publisher) Committed() *Blob { return p.committed.Load() }

// Read — advisory-точечное чтение по закоммиченному поколению: линейный
// поиск по сегменту (точечные чтения per-candidate редки; aux-индекс — по
// измерениям, база — BenchmarkAdvisoryRead).
func (p *Publisher) Read(cell CellID, id transport.EntityID) (Snapshot, bool) {
	b := p.committed.Load()
	if b == nil {
		return Snapshot{}, false
	}
	for slot := range b.seg.records {
		rec := b.seg.records[slot]
		if rec.Entity == id {
			if rec.Cell != cell {
				return Snapshot{}, false
			}
			return Snapshot{entity: rec.Entity, x: rec.X, y: rec.Y, z: rec.Z, kind: rec.Kind, flags: rec.Flags}, true
		}
	}
	return Snapshot{}, false
}
