package replica

import (
	"fmt"
	"math/bits"
	"sync/atomic"

	"github.com/udisondev/l2go/internal/transport"
)

// JoinConfig — радиусы членства наблюдателя-игрока (гистерезис: вход при
// d²≤Enter², выход при d²>Exit², между — удержание; Exit > Enter — иначе
// «watch-forget-watch-forget»).
type JoinConfig struct {
	Enter int32
	Exit  int32
}

// CanonJoinConfig — канонная пара тира «пустое поле» PlayerKnownList L2J
// Interlude: watch 3500 / forget 4200 (адаптивные тиры по размеру knownlist —
// механизм деградации под толпу, фаза 6).
func CanonJoinConfig() JoinConfig { return JoinConfig{Enter: 3500, Exit: 4200} }

// Observer — наблюдатель-игрок у владельца (мир передаёт живых).
type Observer struct {
	Entity transport.EntityID
	ConnID uint64
}

// EventKind — класс события пары (наблюдатель, цель).
type EventKind uint8

// События: ввод (CharInfo/NpcInfo — P3.10), удаление (DeleteObject),
// позиционный апдейт (каркас — кадры наполняет P3.9).
const (
	EventIntroduce EventKind = iota
	EventRemove
	EventUpdate
)

// Event — исходящее событие пары. Target — указатель на запись цели в блобе
// шага (ввод/апдейт; блоб иммутабелен в окне Step → компоновка → Apply —
// без копии ~200-байтной записи на событие: 490k событий/шаг на лестнице
// 10k игроков). При Remove запись недоступна (деспавн) — Target nil, вечный
// id цели несёт Entity. slot — слот цели (применяется стадингом Apply).
type Event struct {
	Obs    Observer
	Target *Record
	Entity transport.EntityID
	Kind   EventKind
	slot   int
}

// viewSet — view set наблюдателя: плотный слайс вечных id по слотам сегмента
// (id на слоте — детект реюза слота; 0 — не член) и битмап членства по
// слотам: чистка хвоста обходит СЛОВА битмапа (нулевые слова скипаются —
// стоимость O(|view|/64 + |view|), не O(слот-пространства)). Контракт оси 3:
// доступ на пару — array-indexed по слоту.
type viewSet struct {
	ids  []transport.EntityID
	bits []uint64
}

// Join — событийный join у владельца наблюдателя: окно 3×3 ячейки вокруг
// наблюдателя вместо всего мира (стоимость детерминирована от толпы вне
// окна с точностью до бинарного поиска сегментов), событийный dirty-детект
// в пределах окна и единая чистка хвоста view по сидам слотов. Step
// вычисляет и стадирует диффы (view НЕ мутирует), Apply применяет стадинг;
// порядок у владельца: Step → компоновка кадров → Apply → merge кадров
// (реестр P3.8, решение 2). Только горутина владельца.
type Join struct {
	cfg        JoinConfig
	views      map[transport.EntityID]*viewSet
	appliedGen uint64
	last       *Blob
	staging    []Event // единый буфер событий шага: мир читает после Step, Apply применяет

	// ForcePanicInApply — тестовый шов recover-политики владельца: паника на
	// применении стадинга после продвижения appliedGen (детект примирения
	// обязан сработать); выключено в поставке (прецедент Region.forcePanic;
	// атомик: пишется тест-горутиной при живом Run).
	ForcePanicInApply atomic.Bool
}

// NewJoin создаёт join с конфигурацией радиусов. Фабрик-инвариант
// cellSize ≥ Exit: гистерезисное кольцо (Enter, Exit] обязано целиком
// помещаться в окно 3×3 — иначе член за Exit остаётся в окне соседних
// клеток и осциллирует при тюнинге сетки (фаза 6); нарушение — ошибка
// конструирования.
func NewJoin(grid Grid, cfg JoinConfig) (*Join, error) {
	if size := grid.cellSize(); size < int64(cfg.Exit) {
		return nil, fmt.Errorf("replica: NewJoin: cellSize %d < Exit %d: гистерезисное кольцо не помещается в окно 3×3",
			size, cfg.Exit)
	}
	return &Join{cfg: cfg, views: make(map[transport.EntityID]*viewSet)}, nil
}

// Step обрабатывает dirty-манифест блоба × наблюдателей и возвращает события
// для компоновки кадров; view мутирует только Apply. При next.BaseGen ≠
// appliedGen (паник-окна: view применён к незакоммиченному или применён
// частично) — один полный примирительный проход с полным эмитом.
func (j *Join) Step(obs []Observer, blob *Blob) []Event {
	j.staging = j.staging[:0]
	j.last = blob
	if blob.base != j.appliedGen {
		j.reconcile(obs, blob)
	} else {
		j.stepEvents(obs, blob)
	}
	return j.staging
}

// Apply применяет стадинг к view set. ПЕРВОЙ операцией продвигает appliedGen —
// бинарное покрытие паник-окон: паника до начала Apply ⇒ базы равны, view
// нетронут, событийный пере-вывод; после начала (включая частичное применение)
// ⇒ базы разошлись, примирение полным эмитом покрывает любую степень
// применённости стадинга.
func (j *Join) Apply() {
	j.appliedGen = j.last.gen
	if j.ForcePanicInApply.Load() && len(j.staging) > 0 {
		panic(fmt.Sprintf("replica: инъекция сбоя в Apply (после appliedGen=%d)", j.appliedGen))
	}
	// события шага сгруппированы обходом по наблюдателям: view резолвится
	// при смене наблюдателя (M лукапов на проход, не на пару — ось 3);
	// view-set новорождённого создаётся здесь — применением стадинга
	var cur *viewSet
	var curObs transport.EntityID
	for i := range j.staging {
		ev := &j.staging[i]
		if i == 0 || ev.Obs.Entity != curObs {
			cur, curObs = j.views[ev.Obs.Entity], ev.Obs.Entity
			if cur == nil {
				cur = &viewSet{}
				j.views[curObs] = cur
			}
		}
		for ev.slot >= len(cur.ids) {
			cur.ids = append(cur.ids, 0)
		}
		for words := ev.slot/64 + 1; words > len(cur.bits); {
			cur.bits = append(cur.bits, 0)
		}
		switch ev.Kind {
		case EventIntroduce:
			cur.ids[ev.slot] = ev.Entity
			bitMark(cur.bits, ev.slot)
		case EventRemove:
			// сверка вечного id: слот мог быть реюзнут новыми жильцом (эмиты
			// Remove(старого) и Introduce(нового) в любом порядке)
			if cur.ids[ev.slot] == ev.Entity {
				cur.ids[ev.slot] = 0
				cur.bits[ev.slot/64] &^= 1 << (uint(ev.slot) % 64)
			}
		case EventUpdate:
			// членство не меняется; last-known не хранится — compose фазы 3
			// самодостаточен пейлоадом события (P3.9 дополнит при надобности)
		}
	}
	j.staging = j.staging[:0]
}

// obsRef — разрешение наблюдателя на время шага: слот и запись вычисляются
// РАЗ (хеш-lookup только здесь); доступ на пару далее — array-indexed по
// слоту (контракт оси 3: хеш на пару запрещён).
type obsRef struct {
	o    Observer
	slot int
	rec  *Record
	v    *viewSet
}

// resolveObs — разрешение наблюдателей шага: слот, запись и view-указатель
// вычисляются РАЗ (хеш-lookups слотов/таблицы наблюдателей — только здесь);
// доступ на пару далее — array-indexed (контракт оси 3: хеш на пару запрещён).
func resolveObs(j *Join, obs []Observer, blob *Blob) []obsRef {
	refs := make([]obsRef, len(obs))
	for i, o := range obs {
		refs[i] = obsRef{o: o, slot: -1, v: j.views[o.Entity]}
		slot, ok := blob.slotOf[o.Entity]
		if !ok || slot >= len(blob.slotPos) || blob.slotPos[slot] < 0 {
			continue
		}
		rec := &blob.records[blob.slotPos[slot]]
		if rec.Entity != o.Entity {
			continue // слот реюзнут другим жильцом
		}
		refs[i].slot = slot
		refs[i].rec = rec
	}
	return refs
}

// stepEvents — событийный путь (базы поколений согласованы). Наблюдатели
// новорождённые (view нет) и двинувшиеся (слот changed) покрываются ТОЛЬКО
// своим полным проходом окна — dirty-цикл им не нужен: каждая пара
// обрабатывается ровно один раз, дедуп-бухгалтерия не нужна. Dirty-детект и
// удержание — по слотам сегментов окна; единая чистка хвоста (в) — для
// каждого наблюдателя с view: источник Remove по слотовым признакам (вне
// окна / свободный слот / чужой жилец) — деспавн и реюз ловятся ею на любом
// пути шага.
func (j *Join) stepEvents(obs []Observer, blob *Blob) {
	refs := resolveObs(j, obs, blob)
	obsSet := make(map[transport.EntityID]struct{}, len(obs))
	var win [9]CellID
	for i := range refs {
		obsSet[obs[i].Entity] = struct{}{}
		ref := &refs[i]
		if ref.slot < 0 {
			continue // без записи в сегменте пары не разрешаются
		}
		// newborn (view нет): сам view-set создаётся ТОЛЬКО применением
		// стадинга в Apply: паника между Step и Apply не оставляет «пустого
		// скелета», слепящего новорождённого (следующий Step снова видит его
		// новорождённым)
		n := windowInto(ref.rec.Cell, &win)
		if ref.v == nil || bitHas(blob.changed, ref.slot) {
			j.fullPass(*ref, blob, win[:n], false)
		} else {
			j.dirtyWindow(*ref, blob, win[:n])
		}
		j.cleanupTail(*ref, blob)
	}
	// дроп view наблюдателей, отсутствующих и в obs, и в блобе (умерли/ушли);
	// Leaving-наблюдатель (не в obs, жив в блобе) view удерживает до Retire
	for ent := range j.views {
		if _, live := obsSet[ent]; live {
			continue
		}
		if _, inBlob := blob.slotOf[ent]; !inBlob {
			delete(j.views, ent)
		}
	}
}

// dirtyWindow — событийный dirty-цикл по слотам сегментов окна: вводы
// (Enter/born), удержание (beyondExit/!Visible), апдейты (changed). Слоты
// вне окна не итерируются — стоимость детерминирована от толпы вне окна.
func (j *Join) dirtyWindow(ref obsRef, blob *Blob, win []CellID) {
	v := ref.v
	for _, c := range win {
		seg, ok := blob.segmentOf(c)
		if !ok {
			continue
		}
		for k := seg.slotOff; k < seg.slotOff+seg.slotLen; k++ {
			slot := blob.slots[k]
			born := bitHas(blob.born, slot)
			changed := bitHas(blob.changed, slot)
			if !born && !changed {
				continue
			}
			rec := &blob.records[k]
			if rec.Entity == ref.o.Entity {
				continue
			}
			member := slot < len(v.ids) && v.ids[slot] == rec.Entity
			switch {
			case !member && inEnter(ref.rec, rec, j.cfg):
				j.emit(Event{Obs: ref.o, Target: rec, Entity: rec.Entity, Kind: EventIntroduce, slot: slot})
			case member && changed && (beyondExit(ref.rec, rec, j.cfg) || !Visible(ref.rec.Flags, rec.Flags)):
				j.emit(Event{Obs: ref.o, Entity: rec.Entity, Kind: EventRemove, slot: slot})
			case member && changed:
				j.emit(Event{Obs: ref.o, Target: rec, Entity: rec.Entity, Kind: EventUpdate, slot: slot})
			}
		}
	}
}

// fullPass — полный проход пар окна: Introduce всем в членстве (при
// fullEmit — включая уже членов: повторные вводы безвредны по канону —
// примирение), Remove членам за Exit/невидимым. Дистанционный выход и
// невидимость — здесь (гистерезисный случай «обе клетки в окне»); чистка
// хвоста по слотовым признакам — отдельной проходкой cleanupTail.
func (j *Join) fullPass(ref obsRef, blob *Blob, win []CellID, fullEmit bool) {
	empty := viewSet{}
	v := j.views[ref.o.Entity]
	if v == nil {
		v = &empty // newborn: членства нет — ввод всех в enter-радиусе окна
	}
	for _, c := range win {
		seg, ok := blob.segmentOf(c)
		if !ok {
			continue
		}
		for k := seg.slotOff; k < seg.slotOff+seg.slotLen; k++ {
			slot := blob.slots[k]
			rec := &blob.records[k]
			if rec.Entity == ref.o.Entity {
				continue
			}
			member := slot < len(v.ids) && v.ids[slot] == rec.Entity
			switch {
			case member:
				// удержание: выход только за exit-радиус (гистерезис) или предикат
				if beyondExit(ref.rec, rec, j.cfg) || !Visible(ref.rec.Flags, rec.Flags) {
					j.emit(Event{Obs: ref.o, Entity: rec.Entity, Kind: EventRemove, slot: slot})
				} else if fullEmit {
					j.emit(Event{Obs: ref.o, Target: rec, Entity: rec.Entity, Kind: EventIntroduce, slot: slot}) // повторный ввод — примирение
				} else if bitHas(blob.changed, slot) {
					// изменившаяся цель члена: полный проход — единственная точка
					// обработки пар covered-наблюдателя — апдейт движения здесь,
					// иначе одновременное движение наблюдателя и цели теряет стрим пары
					j.emit(Event{Obs: ref.o, Target: rec, Entity: rec.Entity, Kind: EventUpdate, slot: slot})
				}
			case inEnter(ref.rec, rec, j.cfg):
				j.emit(Event{Obs: ref.o, Target: rec, Entity: rec.Entity, Kind: EventIntroduce, slot: slot})
			}
		}
	}
}

// cleanupTail — единая чистка хвоста view: единственный источник Remove по
// слотовым признакам. Член view легитимен, пока его слот несёт того же
// жильца в клетке окна; иначе (вне окна — цель за пределами окна обязана
// быть за Exit; свободный слот — деспавн; чужой жилец — реюз слота) ровно
// один Remove по вечному id из view. Обход — по словам битмапа членства
// (нулевые слова пропущены): стоимость O(|view|), не O(слот-пространства).
// Двойной Remove за шаг исключён структурно: дистанционные случаи — только
// в dirty/fullPass по занятым слотам окна, слотовые — только здесь.
func (j *Join) cleanupTail(ref obsRef, blob *Blob) {
	v := j.views[ref.o.Entity]
	if v == nil {
		return // newborn: хвоста нет
	}
	for w, word := range v.bits {
		if word == 0 {
			continue
		}
		for word != 0 {
			b := bits.TrailingZeros64(word)
			word &^= 1 << uint(b)
			slot := w*64 + int(b)
			if seatInWindow(blob, slot, v.ids[slot], ref.rec.Cell) {
				continue
			}
			j.emit(Event{Obs: ref.o, Entity: v.ids[slot], Kind: EventRemove, slot: slot})
		}
	}
}

// seatInWindow — легитимность члена: слот занят тем же вечным id в клетке
// окна наблюдателя (смежность клеток — арифметика декодированных осей,
// константа).
func seatInWindow(blob *Blob, slot int, id transport.EntityID, obsCell CellID) bool {
	if slot >= len(blob.seatBySlot) {
		return false // слот усох вместе с населением
	}
	st := blob.seatBySlot[slot]
	if st.cell == cellInvalid || st.ent != id {
		return false // деспавн либо реюз слота новым жильцом
	}
	return cellsAdjacent(st.cell, obsCell)
}

// cellsAdjacent — обе клетки в окне 3×3 друг друга (оси 0..0x7FFF —
// декодирование без знака).
func cellsAdjacent(a, b CellID) bool {
	dx := int32(a>>16) - int32(b>>16)
	if dx > 1 || dx < -1 {
		return false
	}
	dy := int32(a&0xFFFF) - int32(b&0xFFFF)
	return dy <= 1 && dy >= -1
}

// inEnter — правило ввода пары: не self ∧ предикат ∧ d²≤Enter².
func inEnter(obs, rec *Record, cfg JoinConfig) bool {
	if rec.Entity == obs.Entity {
		return false
	}
	if !Visible(obs.Flags, rec.Flags) {
		return false
	}
	dx, dy, dz, _ := deltas(obs, rec, cfg)
	enter := int64(cfg.Enter)
	return dx*dx+dy*dy+dz*dz <= enter*enter
}

// beyondExit — правило выхода члена: d²>Exit² (гистерезис: кольцо
// Enter<d≤Exit удерживает члена).
func beyondExit(obs, rec *Record, cfg JoinConfig) bool {
	dx, dy, dz, exit := deltas(obs, rec, cfg)
	return dx*dx+dy*dy+dz*dz > exit*exit
}

// deltas — int64-дельты пары с отсечкой дальних до возведения в квадрат
// (|d|>Exit ⇒ дальняя: переполнение исключено, сравнение дешевле квадратов;
// возвращаемые dx/dy/dz при дальней паре обрезаны до Exit+1 — сумма квадратов
// гарантированно больше Enter²).
func deltas(obs, rec *Record, cfg JoinConfig) (dx, dy, dz, limit int64) {
	limit = int64(cfg.Exit)
	dx = int64(obs.X) - int64(rec.X)
	dy = int64(obs.Y) - int64(rec.Y)
	dz = int64(obs.Z) - int64(rec.Z)
	if dx > limit || dx < -limit || dy > limit || dy < -limit || dz > limit || dz < -limit {
		return limit + 1, limit + 1, limit + 1, limit
	}
	return dx, dy, dz, limit
}

// emit — стадирует событие в единый буфер (возврат Step — этот же слайс;
// запись события предшествует любой мутации view — инвариент применяемости).
func (j *Join) emit(ev Event) {
	j.staging = append(j.staging, ev)
}

// reconcile — примирительный полный проход (детект BaseGen ≠ appliedGen):
// полный эмит всем наблюдателям (дубли канон-толерантны) + чистка хвоста
// (в) — примирение обязано доезжать и слотовые удаления, дроп view умерших.
func (j *Join) reconcile(obs []Observer, blob *Blob) {
	alive := make(map[transport.EntityID]struct{}, len(obs))
	for _, o := range obs {
		alive[o.Entity] = struct{}{}
	}
	for ent := range j.views {
		if _, live := alive[ent]; live {
			continue
		}
		if _, inBlob := blob.slotOf[ent]; !inBlob {
			delete(j.views, ent)
		}
	}
	refs := resolveObs(j, obs, blob)
	var win [9]CellID
	for i := range refs {
		if refs[i].slot < 0 {
			continue
		}
		n := windowInto(refs[i].rec.Cell, &win)
		j.fullPass(refs[i], blob, win[:n], true)
		j.cleanupTail(refs[i], blob)
	}
}
