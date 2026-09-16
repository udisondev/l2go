// Join (ось 4/5 ADR-0004): событийная вычислительная часть известности у
// владельца наблюдателя — чистая функция над блобом, диффом и view; view
// мутирует только Apply. Гистерезис enter/exit, абсолютные удаления по
// вечному id, reconcile-свёртка после паник-шага.

package replica

import (
	"math/bits"

	"github.com/udisondev/l2go/internal/transport"
)

// Радиусы гистерезиса членства (OQ-3). Порт aCis (GPLv3, mirror
// sonizs123/Acis), PcKnownList.getDistanceToWatchObject /
// getDistanceToForgetObject: watch = max(1800, 3600 − 20×|known|) — здесь
// пол 1800 (динамическое сжатие — фаза 4+); forget = round(1.5 × watch).
// Дистанция — 3D (aCis Util.checkIfInShortRadius зовётся с
// includeZAxis=true). Утверждается владельцем на приёмке задачи.
const (
	DefaultEnterRadius int32 = 1800
	DefaultExitRadius  int32 = 2700
)

// enterSq/exitSq — квадраты радиусов (int64, без переполнения: радиус мала).
var (
	enterSq = int64(DefaultEnterRadius) * int64(DefaultEnterRadius)
	exitSq  = int64(DefaultExitRadius) * int64(DefaultExitRadius)
)

// Visible — предикат пары (наблюдатель, цель): одна функция для репликации
// и advisory. Фаза 3: входы-флаги не выставляются (нулевые флаги видны);
// синтетический бит цели скрывает. Гео-LOS отсутствует — документированное
// исключение оси 4 ADR-0004 (выжимка OQ-8 — реестр задачи).
func Visible(obsFlags, tgtFlags uint32) bool {
	return tgtFlags == 0
}

// View — известность наблюдателя («кого клиент знает»): пересобираемое
// outbound-состояние региона-владельца, в модель сущности/чемодан/Dump не
// входит, игровой логике недоступно. Pair-тест — битмап слота + сравнение
// вечного id (детект реюза слота), O(1) array-indexed без хеша.
type View struct {
	knownSlots []uint64
	knownIDs   []transport.EntityID // слот-индексированный снимок; 0 — неизвестен
}

// NewView — пустая известность (наблюдатель новорождён: полный проход).
func NewView() *View { return &View{} }

// Empty — известность пуста (никого не введено).
func (v *View) Empty() bool { return !bitsAny(v.knownSlots) }

// Known — слот известен и держит именно этот вечный id (реюз слота чужой
// записью не считается известностью старого).
func (v *View) Known(slot int, id transport.EntityID) bool {
	return slot>>6 < len(v.knownSlots) && bitGet(v.knownSlots, slot) &&
		slot < len(v.knownIDs) && v.knownIDs[slot] == id
}

// ExitEvent — удаление из известности: вечный id (записи уже может не быть
// в блобе — swap/деспавн) и слот, который он занимал в view.
type ExitEvent struct {
	Slot int
	ID   transport.EntityID
}

// JoinEvents — события известности наблюдателя за вызов: вводы — слоты
// (живые записи блоба, CharInfo/NpcInfo), удаления — вечные id
// (DeleteObject). Маркеров переезда фаза 3 не порождает.
type JoinEvents struct {
	Enters []int
	Exits  []ExitEvent
}

// Apply — применение событий к view (вызывающий кладёт кадры в pendingPushes
// РАНЬШЕ Apply: паника в межоперационном окне доигрывается идемпотентно).
func (e JoinEvents) Apply(v *View, b *Blob) {
	for _, slot := range e.Enters {
		v.enter(slot, b.ID(slot))
	}
	for _, ex := range e.Exits {
		v.exit(ex.Slot)
	}
}

func (v *View) enter(slot int, id transport.EntityID) {
	// Рост ёмкостей удвоением: холодный ввод толпы не копирует слайс на
	// каждый слот (квадратичный мусор — датчик GC outbound-пути).
	if slot >= len(v.knownIDs) {
		n := max(slot+1, 2*len(v.knownIDs), 8)
		grown := make([]transport.EntityID, n)
		copy(grown, v.knownIDs)
		v.knownIDs = grown
	}
	v.knownIDs[slot] = id
	w := slot >> 6
	if w >= len(v.knownSlots) {
		n := max(w+1, 2*len(v.knownSlots), 1)
		grown := make([]uint64, n)
		copy(grown, v.knownSlots)
		v.knownSlots = grown
	}
	v.knownSlots[w] |= 1 << (uint(slot) & 63)
}

func (v *View) exit(slot int) {
	if slot >= len(v.knownIDs) {
		return
	}
	v.knownIDs[slot] = 0
	if w := slot >> 6; w < len(v.knownSlots) {
		v.knownSlots[w] &^= 1 << (uint(slot) & 63)
	}
}

// JoinStats — машинные счётчики событийности (F5): Pairs — дистанционные
// проверки пар (якорь join-бенча), PayloadReads — чтения пейлоадных колонок
// (дифф-скан битмапов пейлоадов не читает). Собственность горутины региона.
type JoinStats struct {
	Pairs        int
	PayloadReads int
}

// JoinMode — режим вызова Join (именованный, не позиционные bool: swap
// компилируется молча).
type JoinMode uint8

const (
	// ModeDiff — событийный режим: только Spawned/Dirty слоты (стационарный
	// путь; вызов обязан быть обусловлен непустым диффом — F5).
	ModeDiff JoinMode = iota
	// ModeObsDirty — собственное движение/флаги наблюдателя: полный проход
	// по Members от новой позиции.
	ModeObsDirty
	// ModeReconcile — доигрывание паник-шага: абсолютная свёртка, раньше и
	// вне условия диффа (F19).
	ModeReconcile
)

// Join — события известности наблюдателя obs за поколение b с диффом d.
// Удаления абсолютны по вечному id (F19): известный слот, не являющийся
// живой записью с тем же id (исчез или занят другой записью — swap),
// удаляется немедленно, без маркера и hold-last.
// Self-пара исключается (наблюдатель не вводится сам себе).
func Join(b *Blob, d *Diff, v *View, obs Record, mode JoinMode, st *JoinStats) JoinEvents {
	var ev JoinEvents
	full := v.Empty() || mode != ModeDiff
	if full {
		v.ensure(b.Len())
	}

	// Абсолютная сверка известных слотов: O(|known|), дёшево всегда.
	for w, word := range v.knownSlots {
		for ; word != 0; word &= word - 1 {
			slot := w<<6 + bits.TrailingZeros64(word)
			if slot >= b.Len() || !b.IsMember(slot) || b.ID(slot) != v.knownIDs[slot] {
				ev.Exits = append(ev.Exits, ExitEvent{Slot: slot, ID: v.knownIDs[slot]})
			}
		}
	}

	// far — точно за радиусом без возведения в квадрат (|d| по одной оси
	// уже превышает радиус: корень суммы не может стать меньше).
	far := func(dxyz int64, r int64) bool { return dxyz > r || dxyz < -r }

	enter := func(slot int) {
		id := b.ID(slot)
		if id == obs.Entity || v.Known(slot, id) {
			return
		}
		st.Pairs++
		st.PayloadReads += 3
		dx := int64(b.X(slot)) - int64(obs.X)
		dy := int64(b.Y(slot)) - int64(obs.Y)
		dz := int64(b.Z(slot)) - int64(obs.Z)
		if far(dx, int64(DefaultEnterRadius)) || far(dy, int64(DefaultEnterRadius)) || far(dz, int64(DefaultEnterRadius)) {
			return
		}
		if dx*dx+dy*dy+dz*dz > enterSq {
			return
		}
		if !Visible(obs.Flags, b.Flags(slot)) {
			st.PayloadReads++
			return
		}
		ev.Enters = append(ev.Enters, slot)
	}

	exitCheck := func(slot int) {
		id := b.ID(slot)
		if !v.Known(slot, id) {
			return
		}
		st.Pairs++
		st.PayloadReads += 4
		dx := int64(b.X(slot)) - int64(obs.X)
		dy := int64(b.Y(slot)) - int64(obs.Y)
		dz := int64(b.Z(slot)) - int64(obs.Z)
		if far(dx, int64(DefaultExitRadius)) || far(dy, int64(DefaultExitRadius)) || far(dz, int64(DefaultExitRadius)) ||
			dx*dx+dy*dy+dz*dz > exitSq {
			ev.Exits = append(ev.Exits, ExitEvent{Slot: slot, ID: id})
			return
		}
		if !Visible(obs.Flags, b.Flags(slot)) {
			ev.Exits = append(ev.Exits, ExitEvent{Slot: slot, ID: id})
		}
	}

	if full {
		for slot := range b.Len() {
			if !b.IsMember(slot) {
				continue
			}
			enter(slot)
			exitCheck(slot)
		}
		return ev
	}

	for w, word := range d.spawned {
		for ; word != 0; word &= word - 1 {
			enter(w<<6 + bits.TrailingZeros64(word))
		}
	}
	for w, word := range d.dirty {
		for ; word != 0; word &= word - 1 {
			slot := w<<6 + bits.TrailingZeros64(word)
			if !b.IsMember(slot) || bitGet(d.spawned, slot) {
				continue
			}
			exitCheck(slot)
			enter(slot)
		}
	}
	return ev
}

// ensure — предварительное расширение view под n слотов (полный проход
// аллоцирует ёмкость один раз, не по вводу).
func (v *View) ensure(n int) {
	if n <= len(v.knownIDs) && (n+63)/64 <= len(v.knownSlots) {
		return
	}
	if n > len(v.knownIDs) {
		grown := make([]transport.EntityID, n)
		copy(grown, v.knownIDs)
		v.knownIDs = grown
	}
	if w := (n + 63) / 64; w > len(v.knownSlots) {
		grown := make([]uint64, w)
		copy(grown, v.knownSlots)
		v.knownSlots = grown
	}
}
