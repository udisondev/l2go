// Join (ось 4/5 ADR-0004): событийная вычислительная часть известности у
// владельца наблюдателя — чистая функция над блобом, диффом и view; view
// мутирует только Apply. Гистерезис enter/exit, абсолютные удаления по
// вечному id, reconcile-свёртка после паник-шага.

package replica

import "github.com/udisondev/l2go/internal/transport"

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

// KnowsID — id известен наблюдателю (линейный по известным слотам; для
// тестов и метрик, не для тикового пути).
func (v *View) KnowsID(id transport.EntityID) bool {
	for _, k := range v.knownIDs {
		if k == id {
			return true
		}
	}
	return false
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
	if slot >= len(v.knownIDs) {
		grown := make([]transport.EntityID, slot+1)
		copy(grown, v.knownIDs)
		v.knownIDs = grown
	}
	v.knownIDs[slot] = id
	w := slot >> 6
	if w >= len(v.knownSlots) {
		grown := make([]uint64, w+1)
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

// Join — события известности наблюдателя obs за поколение b с диффом d.
// Режимы: полный проход — при пустом view (рождение), obsDirty (собственное
// движение/флаги наблюдателя) или reconcile (доигрывание паник-шага;
// раньше и вне условия диффа); иначе — только Spawned/Dirty слоты.
// Удаления абсолютны по вечному id (F19): известный слот, не являющийся
// живой записью с тем же id (исчез или занят другой записью — swap),
// удаляется немедленно, без маркера и hold-last.
// Self-пара исключается (наблюдатель не вводится сам себе).
func Join(b *Blob, d *Diff, v *View, obs Record, obsDirty, reconcile bool, st *JoinStats) JoinEvents {
	var ev JoinEvents
	full := v.Empty() || obsDirty || reconcile

	// Абсолютная сверка известных слотов: O(|known|), дёшево всегда.
	for w, word := range v.knownSlots {
		for ; word != 0; word &= word - 1 {
			slot := w<<6 + bitsTrailing(word)
			if slot >= b.Len() || !b.IsMember(slot) || b.ID(slot) != v.knownIDs[slot] {
				ev.Exits = append(ev.Exits, ExitEvent{Slot: slot, ID: v.knownIDs[slot]})
			}
		}
	}

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
		if dx*dx+dy*dy+dz*dz > int64(DefaultEnterRadius)*int64(DefaultEnterRadius) {
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
		if dx*dx+dy*dy+dz*dz > int64(DefaultExitRadius)*int64(DefaultExitRadius) {
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
			enter(w<<6 + bitsTrailing(word))
		}
	}
	for w, word := range d.dirty {
		for ; word != 0; word &= word - 1 {
			slot := w<<6 + bitsTrailing(word)
			if !b.IsMember(slot) || bitGet(d.spawned, slot) {
				continue
			}
			exitCheck(slot)
			enter(slot)
		}
	}
	return ev
}

// bitsTrailing — номер младшего единичного бита (бит установлен).
func bitsTrailing(v uint64) int {
	n := 0
	if v&(1<<32-1) == 0 {
		n += 32
		v >>= 32
	}
	if v&(1<<16-1) == 0 {
		n += 16
		v >>= 16
	}
	if v&(1<<8-1) == 0 {
		n += 8
		v >>= 8
	}
	if v&(1<<4-1) == 0 {
		n += 4
		v >>= 4
	}
	if v&(1<<2-1) == 0 {
		n += 2
		v >>= 2
	}
	if v&1 == 0 {
		n++
	}
	return n
}
