package replica

import (
	"fmt"
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

// Event — исходящее событие пары. slot — слот цели в сегменте Step (поле
// пакета: применяется стадингом Apply).
type Event struct {
	Obs    Observer
	Target Record
	Kind   EventKind
	slot   int
}

// viewSet — view set наблюдателя: плотный слайс вечных id по слотам сегмента
// (id на слоте — детект реюза слота; 0 — не член). Контракт оси 3: доступ на
// пару — array-indexed по слоту.
type viewSet struct {
	ids []transport.EntityID
}

// Join — событийный join у владельца наблюдателя. Step вычисляет и стадирует
// диффы (view НЕ мутирует), Apply применяет стадинг; порядок у владельца:
// Step → компоновка кадров → Apply → merge кадров (реестр P3.8, решение 2).
// Только горутина владельца.
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

// NewJoin создаёт join с конфигурацией радиусов.
func NewJoin(cfg JoinConfig) *Join {
	return &Join{cfg: cfg, views: make(map[transport.EntityID]*viewSet)}
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
		switch ev.Kind {
		case EventIntroduce:
			cur.ids[ev.slot] = ev.Target.Entity
		case EventRemove:
			// сверка вечного id: слот мог быть реюзнут новыми жильцом (эмиты
			// Remove(старого) и Introduce(нового) в любом порядке)
			if cur.ids[ev.slot] == ev.Target.Entity {
				cur.ids[ev.slot] = 0
			}
		case EventUpdate:
			// членство не меняется; last-known не хранится — compose фазы 3
			// самодостаточен пейлоадом события (P3.9 дополнит при надобности)
		}
	}
	j.staging = j.staging[:0]
}

// obsRef — разрешение наблюдателя на время шага: слот и запись в сегменте
// вычисляются РАЗ (хеш-lookup только здесь); доступ на пару далее —
// array-indexed по слоту (контракт оси 3: хеш на пару запрещён).
type obsRef struct {
	o       Observer
	slot    int
	rec     *Record
	v       *viewSet
	covered bool // пара покрыта полным проходом — dirty-цикл пропускает
}

// resolveObs — разрешение наблюдателей шага: слот, запись и view-указатель
// вычисляются РАЗ (хеш-lookups слотов/таблицы наблюдателей — только здесь);
// доступ на пару далее — array-indexed (контракт оси 3: хеш на пару запрещён).
func resolveObs(j *Join, obs []Observer, blob *Blob) []obsRef {
	refs := make([]obsRef, len(obs))
	for i, o := range obs {
		refs[i] = obsRef{o: o, slot: -1, v: j.views[o.Entity]}
		slot, ok := blob.slots[o.Entity]
		if !ok || slot >= len(blob.seg.records) {
			continue
		}
		rec := &blob.seg.records[slot]
		if rec.Entity != o.Entity {
			continue
		}
		refs[i].slot = slot
		refs[i].rec = rec
	}
	return refs
}

// stepEvents — событийный путь (базы поколений согласованы). Наблюдатели
// новорождённые (view нет) и двинувшиеся (слот changed) покрываются ТОЛЬКО
// своим полным проходом — dirty-цикл их пропускает: каждая пара обрабатывается
// ровно один раз, дедуп-бухгалтерия не нужна.
func (j *Join) stepEvents(obs []Observer, blob *Blob) {
	seg := &blob.seg
	refs := resolveObs(j, obs, blob)
	obsSet := make(map[transport.EntityID]struct{}, len(obs))
	for i := range refs {
		obsSet[obs[i].Entity] = struct{}{}
		if refs[i].slot < 0 {
			continue // без записи в сегменте пары не разрешаются
		}
		// newborn (view нет) и двинувшийся — полный проход; сам view-set
		// создаётся ТОЛЬКО применением стадинга в Apply: паника между Step и
		// Apply не оставляет «пустого скелета», слепящего новорождённого
		// (следующий Step снова видит его новорождённым)
		if refs[i].v == nil || bitHas(seg.changed, refs[i].slot) {
			j.fullPass(refs[i], blob, false)
			refs[i].covered = true
		}
	}
	// dirty-слоты × обычные наблюдатели; порядок битов слота: gone → born →
	// changed (реюз слота: Remove прежнего жильца раньше ввода нового)
	for slot := range seg.records {
		gone := bitHas(seg.gone, slot)
		born := bitHas(seg.born, slot)
		changed := bitHas(seg.changed, slot)
		if !gone && !born && !changed {
			continue
		}
		for i := range refs {
			ref := &refs[i]
			if ref.covered || ref.slot < 0 || ref.v == nil {
				continue
			}
			v := ref.v
			if gone && slot < len(v.ids) && v.ids[slot] != 0 {
				// ушедший жилец идентифицируется вечным id из view (пейлоад
				// недоступен — DeleteObject нуждается только в id)
				j.emit(Event{Obs: ref.o, Target: Record{Entity: v.ids[slot]}, Kind: EventRemove, slot: slot})
			}
			rec := seg.records[slot]
			if rec.Entity == 0 || rec.Entity == ref.o.Entity {
				continue
			}
			member := slot < len(v.ids) && v.ids[slot] == rec.Entity
			switch {
			case (born || changed) && !member && inEnter(ref.rec, &rec, j.cfg):
				j.emit(Event{Obs: ref.o, Target: rec, Kind: EventIntroduce, slot: slot})
			case changed && member && (beyondExit(ref.rec, &rec, j.cfg) || !Visible(ref.rec.Flags, rec.Flags)):
				j.emit(Event{Obs: ref.o, Target: rec, Kind: EventRemove, slot: slot})
			case changed && member:
				j.emit(Event{Obs: ref.o, Target: rec, Kind: EventUpdate, slot: slot})
			}
		}
	}
	// дроп view наблюдателей, отсутствующих и в obs, и в блобе (умерли/ушли);
	// Leaving-наблюдатель (не в obs, жив в блобе) view удерживает до Retire
	for ent := range j.views {
		if _, live := obsSet[ent]; live {
			continue
		}
		if _, inBlob := blob.slots[ent]; !inBlob {
			delete(j.views, ent)
		}
	}
}

// fullPass — полный проход пар наблюдателя: Introduce всем в членстве
// (при fullEmit — включая уже членов: повторные вводы безвредны по канону —
// примирение), Remove всем членам, покинувшим членство или сегмент.
func (j *Join) fullPass(ref obsRef, blob *Blob, fullEmit bool) {
	if ref.slot < 0 {
		return
	}
	seg := &blob.seg
	empty := viewSet{}
	v := j.views[ref.o.Entity]
	if v == nil {
		v = &empty // newborn: членства нет — ввод всех в enter-радиусе
	}
	for slot := range seg.records {
		rec := &seg.records[slot]
		if rec.Entity == 0 || rec.Entity == ref.o.Entity {
			continue
		}
		member := slot < len(v.ids) && v.ids[slot] == rec.Entity
		switch {
		case member:
			// удержание: выход только за exit-радиус (гистерезис) или предикат
			if beyondExit(ref.rec, rec, j.cfg) || !Visible(ref.rec.Flags, rec.Flags) {
				j.emit(Event{Obs: ref.o, Target: *rec, Kind: EventRemove, slot: slot})
			} else if fullEmit {
				j.emit(Event{Obs: ref.o, Target: *rec, Kind: EventIntroduce, slot: slot}) // повторный ввод — примирение
			}
		case inEnter(ref.rec, rec, j.cfg):
			j.emit(Event{Obs: ref.o, Target: *rec, Kind: EventIntroduce, slot: slot})
		}
	}
	// члены view за пределами нового сегмента (слоты усохли/заменены) — Remove
	for slot := range v.ids {
		if v.ids[slot] == 0 {
			continue
		}
		if slot >= len(seg.records) || seg.records[slot].Entity != v.ids[slot] {
			j.emit(Event{Obs: ref.o, Target: Record{Entity: v.ids[slot]}, Kind: EventRemove, slot: slot})
		}
	}
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
// полный эмит всем наблюдателям (дубли канон-толерантны), дроп view умерших.
func (j *Join) reconcile(obs []Observer, blob *Blob) {
	alive := make(map[transport.EntityID]struct{}, len(obs))
	for _, o := range obs {
		alive[o.Entity] = struct{}{}
	}
	for ent := range j.views {
		if _, live := alive[ent]; live {
			continue
		}
		if _, inBlob := blob.slots[ent]; !inBlob {
			delete(j.views, ent)
		}
	}
	refs := resolveObs(j, obs, blob)
	for i := range refs {
		if refs[i].slot < 0 {
			continue
		}
		j.fullPass(refs[i], blob, true)
	}
}
