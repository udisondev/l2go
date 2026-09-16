// Блоб ячейки (ось 3 ADR-0004): per-owner SoA-публикация с полными
// пейлоадами, dirty-манифестом и стабильными плотными слотами. Издатель —
// горутина региона-владельца; после сборки блоб иммутабелен (Build всегда
// копирует).

package replica

import (
	"fmt"
	"sort"

	"github.com/udisondev/l2go/internal/transport"
)

// RecordType — дискриминатор записи блоба.
type RecordType uint8

// Типы записей: игрок (CharInfo/UserInfo) и NPC (NpcInfo).
const (
	RecordPlayer RecordType = iota
	RecordNPC
)

// Record — полный пейлоад записи: поля по потребителю CharInfo/NpcInfo/UserInfo.
// Per-template константы (скорости/коллизии) в блоб не входят — их источник
// при compose. Flags — входы предиката видимости (инвиз/GM; фаза 3 не
// ставится). Epoch — метка записи: источник инкремента появляется с
// хэндоффами фазы 4, потребитель — Snapshot advisory и max-epoch-дедуп.
type Record struct {
	Entity     transport.EntityID
	X, Y, Z    int32
	Heading    int32
	Moving     bool
	Type       RecordType
	Name       string
	ClassID    int32
	Race       int32
	Female     bool
	HairStyle  int32
	HairColor  int32
	Face       int32
	TemplateID int32 // npc: displayId шаблона
	Flags      uint32
	Epoch      uint64
}

// Числовые колонки блоба (фиксированный порядок в бэкинге; строки —
// параллельный строковый массив).
const (
	colID = iota
	colX
	colY
	colZ
	colHeading
	colMoving
	colType
	colClassID
	colRace
	colFemale
	colHairStyle
	colHairColor
	colFace
	colTemplateID
	colFlags
	colEpoch
	numCols
)

// Blob — иммутабельное поколение ячейки. Один числовой бэкинг несёт
// битмапы Members/Dirty и все числовые колонки (stride = число слотов),
// строки — второй бэкинг: ≤3 аллокации на поколение. Слоты стабильны,
// пока житель жив; реюз слота детектируется вечным id записи.
type Blob struct {
	gen     uint64
	n       int
	members []uint64
	dirty   []uint64
	nums    []uint64
	strs    []string
}

// Gen возвращает поколение публикации (+1 на каждую).
func (b *Blob) Gen() uint64 { return b.gen }

// Len — число слотов поколения (включая мёртвые; живые — битмапом Members).
func (b *Blob) Len() int { return b.n }

func bitGet(w []uint64, i int) bool { return w[i>>6]&(1<<(uint(i)&63)) != 0 }

// IsMember — живость слота (битмап).
func (b *Blob) IsMember(i int) bool { return bitGet(b.members, i) }

// IsDirty — запись изменилась против поколения, из которого собран дифф.
func (b *Blob) IsDirty(i int) bool { return bitGet(b.dirty, i) }

// cols — база числовых колонок в бэкинге.
func (b *Blob) cols() int { return 4 * ((b.n + 63) / 64) }

// Колонки-читатели (пейлоады; inline-дешёвые).
func (b *Blob) ID(i int) transport.EntityID { return transport.EntityID(b.nums[b.cols()+colID*b.n+i]) }
func (b *Blob) X(i int) int32               { return int32(b.nums[b.cols()+colX*b.n+i]) }
func (b *Blob) Y(i int) int32               { return int32(b.nums[b.cols()+colY*b.n+i]) }
func (b *Blob) Z(i int) int32               { return int32(b.nums[b.cols()+colZ*b.n+i]) }
func (b *Blob) Heading(i int) int32         { return int32(b.nums[b.cols()+colHeading*b.n+i]) }
func (b *Blob) Moving(i int) bool           { return b.nums[b.cols()+colMoving*b.n+i] != 0 }
func (b *Blob) Type(i int) RecordType       { return RecordType(b.nums[b.cols()+colType*b.n+i]) }
func (b *Blob) ClassID(i int) int32         { return int32(b.nums[b.cols()+colClassID*b.n+i]) }
func (b *Blob) Race(i int) int32            { return int32(b.nums[b.cols()+colRace*b.n+i]) }
func (b *Blob) Female(i int) bool           { return b.nums[b.cols()+colFemale*b.n+i] != 0 }
func (b *Blob) HairStyle(i int) int32       { return int32(b.nums[b.cols()+colHairStyle*b.n+i]) }
func (b *Blob) HairColor(i int) int32       { return int32(b.nums[b.cols()+colHairColor*b.n+i]) }
func (b *Blob) Face(i int) int32            { return int32(b.nums[b.cols()+colFace*b.n+i]) }
func (b *Blob) TemplateID(i int) int32      { return int32(b.nums[b.cols()+colTemplateID*b.n+i]) }
func (b *Blob) Flags(i int) uint32          { return uint32(b.nums[b.cols()+colFlags*b.n+i]) }
func (b *Blob) Epoch(i int) uint64          { return uint64(b.nums[b.cols()+colEpoch*b.n+i]) }
func (b *Blob) Name(i int) string           { return b.strs[i] }

// Diff — дифф поколения против последней публикации: Spawned.removed —
// членство, Dirty — изменившиеся живые записи (ввод — всегда dirty).
type Diff struct {
	spawned []uint64
	removed []uint64
	dirty   []uint64
}

func bitsCount(w []uint64) int {
	n := 0
	for _, v := range w {
		for ; v != 0; v &= v - 1 {
			n++
		}
	}
	return n
}

func bitsAny(w []uint64) bool {
	for _, v := range w {
		if v != 0 {
			return true
		}
	}
	return false
}

// SpawnedCount — число введённых слотов.
func (d *Diff) SpawnedCount() int { return bitsCount(d.spawned) }

// RemovedCount — число исчезнувших слотов.
func (d *Diff) RemovedCount() int { return bitsCount(d.removed) }

// DirtyCount — число изменившихся живых слотов.
func (d *Diff) DirtyCount() int { return bitsCount(d.dirty) }

// Any — есть ли хоть одно событие поколения.
func (d *Diff) Any() bool {
	return bitsAny(d.spawned) || bitsAny(d.removed) || bitsAny(d.dirty)
}

type idSlot struct {
	id   transport.EntityID
	slot int
}

// Builder — издательская сторона ячейки: собственность горутины региона,
// мутабелен между публикациями. Слоты стабильны (первый свободный индекс);
// бухгалтерия id→slot — инкрементально-отсортированный слайс (binary search
// вставка/удаление, прецедент residents). Build вычисляет дифф против vs —
// последнего опубликованного блоба: неопубликованное поколение не существует
// для диффа, паника до publish доигрывается повторным вычислением.
type Builder struct {
	recs []Record
	ids  []idSlot
	free []int
}

// NewBuilder — пустой строитель.
func NewBuilder() *Builder { return &Builder{} }

// Update — upsert записи: существующий житель обновляет свой слот, новый
// занимает первый свободный (или хвост).
func (b *Builder) Update(r Record) error {
	if r.Entity == 0 {
		return fmt.Errorf("replica: Record.Entity 0 — невалидный вечный id")
	}
	i := sort.Search(len(b.ids), func(i int) bool { return b.ids[i].id >= r.Entity })
	if i < len(b.ids) && b.ids[i].id == r.Entity {
		b.recs[b.ids[i].slot] = r
		return nil
	}
	slot := len(b.recs)
	if n := len(b.free); n > 0 {
		slot = b.free[n-1]
		b.free = b.free[:n-1]
		b.recs[slot] = r
	} else {
		b.recs = append(b.recs, r)
	}
	b.ids = append(b.ids, idSlot{})
	copy(b.ids[i+1:], b.ids[i:])
	b.ids[i] = idSlot{id: r.Entity, slot: slot}
	return nil
}

// Remove — деспавн: слот освобождается (Entity слота — 0), реюз — первым
// свободным индексом.
func (b *Builder) Remove(id transport.EntityID) error {
	i := sort.Search(len(b.ids), func(i int) bool { return b.ids[i].id >= id })
	if i >= len(b.ids) || b.ids[i].id != id {
		return fmt.Errorf("replica: Remove(%d) — записи нет", id)
	}
	slot := b.ids[i].slot
	b.recs[slot] = Record{}
	b.free = append(b.free, slot)
	b.ids = append(b.ids[:i], b.ids[i+1:]...)
	return nil
}

// Build — собирает иммутабельное поколение (всегда копирует) и дифф против
// vs (nil — холодный старт: всё введено и dirty). Один числовой бэкинг
// несёт битмапы Members/Dirty/Spawned.removed и колонки; строки — второй:
// блоб+дифф ≈ 4 аллокации на поколение.
func (b *Builder) Build(gen uint64, vs *Blob) (*Blob, *Diff) {
	n := len(b.recs)
	words := (n + 63) / 64
	total := 4*words + numCols*n
	nums := make([]uint64, total)
	members := nums[:words]
	dirty := nums[words : 2*words]
	spawned := nums[2*words : 3*words]
	removed := nums[3*words : 4*words]
	cols := nums[4*words:]
	strs := make([]string, n)

	blob := &Blob{gen: gen, n: n, members: members, dirty: dirty, nums: nums, strs: strs}
	for slot, r := range b.recs {
		if r.Entity == 0 {
			continue
		}
		members[slot>>6] |= 1 << (uint(slot) & 63)
		strs[slot] = r.Name
		cols[colID*n+slot] = uint64(r.Entity)
		cols[colX*n+slot] = i32u(r.X)
		cols[colY*n+slot] = i32u(r.Y)
		cols[colZ*n+slot] = i32u(r.Z)
		cols[colHeading*n+slot] = i32u(r.Heading)
		cols[colMoving*n+slot] = b2u(r.Moving)
		cols[colType*n+slot] = uint64(r.Type)
		cols[colClassID*n+slot] = i32u(r.ClassID)
		cols[colRace*n+slot] = i32u(r.Race)
		cols[colFemale*n+slot] = b2u(r.Female)
		cols[colHairStyle*n+slot] = i32u(r.HairStyle)
		cols[colHairColor*n+slot] = i32u(r.HairColor)
		cols[colFace*n+slot] = i32u(r.Face)
		cols[colTemplateID*n+slot] = i32u(r.TemplateID)
		cols[colFlags*n+slot] = uint64(r.Flags)
		cols[colEpoch*n+slot] = r.Epoch
	}

	// Dirty-манифест диффа — сам битмап блоба (одно поколение — один кусок).
	d := &Diff{spawned: spawned, removed: removed, dirty: dirty}
	if vs == nil {
		copy(spawned, members)
		copy(dirty, members)
		return blob, d
	}
	vWords := (vs.n + 63) / 64
	for w := range spawned {
		var cur, old uint64
		if w < words {
			cur = members[w]
		}
		if w < vWords {
			old = vs.members[w]
		}
		spawned[w] = cur &^ old
		removed[w] = old &^ cur
		dirty[w] = spawned[w] // ввод — всегда dirty
	}
	for slot := range n {
		if !bitGet(members, slot) || bitGet(spawned, slot) || slot >= vs.n || !vs.IsMember(slot) {
			continue
		}
		if recChanged(blob, vs, slot) {
			dirty[slot>>6] |= 1 << (uint(slot) & 63)
		}
	}
	return blob, d
}

// recChanged — изменилась ли живая запись против поколения vs (все колонки
// и имя; сравнение по бэкинговым индексам без доступа через методы).
func recChanged(b, vs *Blob, slot int) bool {
	bb, vb := b.cols(), vs.cols()
	for c := 0; c < numCols; c++ {
		if b.nums[bb+c*b.n+slot] != vs.nums[vb+c*vs.n+slot] {
			return true
		}
	}
	return b.strs[slot] != vs.strs[slot]
}

func i32u(v int32) uint64 { return uint64(uint32(v)) }

func b2u(v bool) uint64 {
	if v {
		return 1
	}
	return 0
}
