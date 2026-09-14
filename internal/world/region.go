package world

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"sync/atomic"

	"github.com/udisondev/l2go/internal/transport"
)

// Фазы шага — порядок drain → fold → эффекты населения → B → publish → ack;
// маркер текущей фазы едет в лог при панике.
const (
	phaseDrain byte = iota + 1
	phaseFold
	phaseEffects
	phaseLog
	phaseB
	phasePublish
	phaseAck
)

// outboxAlertThreshold — порог глубины outbox для алерта декларации очереди
// (троттлинг compose — фаза 4; предел отсутствует: исходящие региона —
// надёжный класс, живому не дропаются).
const outboxAlertThreshold = 4096

// snapshot — заготовка снапшота региона: шов replica (публикация после шага,
// атомарный свап). SoA-блоб — задача репликации (фаза 3.8); фаза 3 публикует
// заготовку с тиком шага.
type snapshot struct {
	tick Tick
}

// resident — запись сущности у владельца: заголовок ящика встраивается в
// запись (кеш-локальность опроса тика). Слайс значений невозможен: карта
// адресов держит &box навечно.
type resident struct {
	ent   *Entity
	box   transport.Mailbox
	token uint64
}

// Region — горутина-актор региона: мутации состояния — только здесь
// (single-writer). Шаг: drain → fold → эффекты населения → фаза B → publish →
// ack; номер шага — Now() на старте (монотонен, регресса нет); dt — тиками
// метронома, настенных часов в шаге нет (D1).
type Region struct {
	id    RegionID
	cfg   Config
	metro *Metronome
	reg   *transport.Registry
	log   *PortionLog

	ctrl      transport.Mailbox
	ctrlID    transport.EntityID
	ctrlToken uint64

	ringCh chan struct{} // дверной звонок метронома (cap-1)
	fbCh   chan struct{} // heartbeat-фолбэк (cap-1)

	doneTick     atomic.Uint64
	dropped      atomic.Uint64
	failed       atomic.Uint64
	frozenFlag   atomic.Bool
	resCount     atomic.Int64
	backlogLen   atomic.Int64
	drainOverrun atomic.Int64 // кумулятивные письма сверх drainBudget (пачка неделима)

	// Только горутина региона:
	residents    []*resident // сортированный по ent.ID слайс (обход — D3)
	ents         []*Entity   // кэш проекции для fold
	popVersion   uint64      // поколение населения: инкремент на Spawn/Remove
	entsVer      uint64      // поколение, на котором построен кэш
	stepTick     Tick        // номер текущего шага (для маркера паники)
	state        *State
	lastStep     Tick
	slept        bool // регион деактивирован: первый шаг после сна — delta=0
	activeFlag   bool
	panicStreak  int
	curPhase     byte
	forcePanic   byte // инъекция сбоя для тестов recover-политики; 0 — выключена
	outbox       []transport.Envelope
	backlogNoted bool

	// Буферы дрена — поля региона, переиспользуются между шагами.
	ctrlBatch []transport.Envelope
	prioBuf   []transport.Envelope
	restBuf   []transport.Envelope
	rereadBuf []transport.Envelope
	entityBuf []transport.Envelope
	portions  []Portion
	records   []PortionRecord
	adviseBuf []AdvisoryIn

	phDrain   atomic.Uint64
	phFold    atomic.Uint64
	phEffects atomic.Uint64
	phB       atomic.Uint64
	phPublish atomic.Uint64
	phAck     atomic.Uint64

	snapPtr atomic.Pointer[snapshot]
}

// NewRegion создаёт регион: контрольный ящик регистрируется в реестре и
// клеймится (токен = uint64(ctrlID), ID монотонные с 1). Регион рождается
// спящим: первый шаг — delta=0.
func NewRegion(metro *Metronome, reg *transport.Registry, id RegionID, cfg Config, log *PortionLog) (*Region, error) {
	if metro == nil || reg == nil || log == nil {
		return nil, fmt.Errorf("world: NewRegion(%d): метроном, реестр и лог порций обязательны", id)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("world: NewRegion(%d): %w", id, err)
	}
	r := &Region{
		id:    id,
		cfg:   cfg,
		metro: metro,
		reg:   reg,
		log:   log,
		state: &State{},
		slept: true,
	}
	r.ringCh = make(chan struct{}, 1)
	r.fbCh = make(chan struct{}, 1)
	r.ctrlID = reg.Register(&r.ctrl)
	r.ctrlToken = uint64(r.ctrlID)
	if err := r.ctrl.Claim(r.ctrlToken); err != nil {
		return nil, fmt.Errorf("world: NewRegion(%d): контрольный ящик: %w", id, err)
	}
	metro.register(r)
	return r, nil
}

// Stats — снимок метрик региона (атомики; состояние свёртки не входит — оно
// принадлежит горутине региона, наружу — через Dump после остановки).
func (r *Region) Stats() RegionStats {
	return RegionStats{
		DoneTick:     Tick(r.doneTick.Load()),
		Dropped:      r.dropped.Load(),
		Failed:       r.failed.Load(),
		Frozen:       r.frozenFlag.Load(),
		Residents:    int(r.resCount.Load()),
		Backlog:      int(r.backlogLen.Load()),
		DrainOverrun: uint64(r.drainOverrun.Load()),
		PhaseDrain:   r.phDrain.Load(),
		PhaseFold:    r.phFold.Load(),
		PhaseEffects: r.phEffects.Load(),
		PhaseB:       r.phB.Load(),
		PhasePublish: r.phPublish.Load(),
		PhaseAck:     r.phAck.Load(),
	}
}

// RegionStats — снимок метрик региона: dropped/doneTick читаемы, фазовые
// счётчики наблюдаемы после шага.
type RegionStats struct {
	DoneTick     Tick
	Dropped      uint64
	Failed       uint64
	Frozen       bool
	Residents    int
	Backlog      int
	DrainOverrun uint64 // кумулятивные письма сверх drainBudget (пачка неделима)
	PhaseDrain   uint64
	PhaseFold    uint64
	PhaseEffects uint64
	PhaseB       uint64
	PhasePublish uint64
	PhaseAck     uint64
}

// Spawn рождает жителя: аллокация ящика (Register + Claim, токен = ID),
// вставка в отсортированный слайс. Только горутина региона (или до старта
// Run); контрольные письма входа в мир сведутся к Births свёртки.
func (r *Region) Spawn(ent Entity) (transport.EntityID, error) {
	res := &resident{}
	res.ent = &ent
	res.ent.Owner = r.id
	res.ent.ID = r.reg.Register(&res.box)
	res.token = uint64(res.ent.ID)
	if err := res.box.Claim(res.token); err != nil {
		return 0, fmt.Errorf("world: Spawn: ящик жителя %d: %w", res.ent.ID, err)
	}
	idx := sort.Search(len(r.residents), func(i int) bool { return r.residents[i].ent.ID >= res.ent.ID })
	r.residents = append(r.residents, nil)
	copy(r.residents[idx+1:], r.residents[idx:])
	r.residents[idx] = res
	r.resCount.Add(1)
	r.popVersion++
	r.syncMembership()
	return res.ent.ID, nil
}

// Remove удаляет жителя: Despawn → Retire → освобождение записи (контракт
// транспорта). Только горутина региона (или до старта Run).
func (r *Region) Remove(id transport.EntityID) {
	idx := sort.Search(len(r.residents), func(i int) bool { return r.residents[i].ent.ID >= id })
	if idx >= len(r.residents) || r.residents[idx].ent.ID != id {
		return
	}
	res := r.residents[idx]
	res.box.Despawn(res.token)
	r.reg.Retire(id)
	r.residents = append(r.residents[:idx], r.residents[idx+1:]...)
	r.resCount.Add(-1)
	r.popVersion++
	r.syncMembership()
}

// Run — горутина региона: select по ctx, notify-токену контрольного ящика,
// дверному звонку и фолбэку. Контрольное письмо будит немедленным шагом
// (волны коалесятся cap-1 токеном). Активный регион фолбэк игнорирует (окно
// закрыто тик-звонком), замороженный не шагает. На выходе по ctx горутина
// сама закрывает лог порций.
func (r *Region) Run(ctx context.Context) {
	defer r.shutdown()
	defer r.closeLog()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.ctrl.Notify():
			if r.frozenFlag.Load() {
				continue
			}
			r.safeStep()
		case <-r.ringCh:
			if r.frozenFlag.Load() {
				continue
			}
			r.safeStep()
		case <-r.fbCh:
			if r.frozenFlag.Load() || r.activeFlag || r.ctrl.Depth() == 0 {
				continue
			}
			r.safeStep()
		}
	}
}

// shutdown — выход региона из сетов метронома (мёртвый регион не звонит
// вотчдогу и не удерживается всеми-сетом).
func (r *Region) shutdown() {
	if r.activeFlag {
		r.activeFlag = false
		r.metro.Deactivate(r)
	}
	r.metro.unregister(r)
}

// closeLog — закрытие лога на выходе; recover со slog: паника Close не уронит
// выход молча.
func (r *Region) closeLog() {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("world: паника закрытия лога порций", "region", r.id, "panic", p)
		}
	}()
	if err := r.log.Close(); err != nil {
		slog.Error("world: закрытие лога порций", "region", r.id, "err", err)
	}
}

func (r *Region) safeStep() {
	if r.frozenFlag.Load() {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			r.recovered(p)
		}
	}()
	r.step()
}

// recovered — recover-политика: тик провален (doneTick не пишется, failed
// растёт), неприменённый остаток изъятого классово дропнут (консервативно —
// все пачки шага: прогресс применения после паники не специфицируем),
// маркер сбойного шага в лог; серия порога — заморозка. Успешный шаг
// обнуляет серию.
func (r *Region) recovered(p any) {
	r.failed.Add(1)
	r.panicStreak++
	phase := r.curPhase
	// Консервативный классовый дроп пачек шага (учёт — на контрольном ящике
	// региона): до фазы эффектов применение могло быть частичным. Поздние
	// фазы (эффекты/B/publish) письма уже применили — дроп дал бы ложный
	// reliable-инцидент.
	if phase <= phaseFold {
		// дизъюнктный покров шага: ctrlBatch — вся первая волна (приоритет ≤K и
		// излишек), rereadBuf — волна перечита AckNotify; restBuf не трогаем:
		// его излишек — копии ctrlBatch (двойной счёт инцидентов)
		r.ctrl.DropBatch(r.ctrlBatch)
		r.ctrl.DropBatch(r.entityBuf)
		r.ctrl.DropBatch(r.rereadBuf)
	}
	r.adviseBuf = r.adviseBuf[:0]
	if err := r.log.LogPanic(r.stepTick, phase); err != nil {
		slog.Error("world: маркер паники не записан", "region", r.id, "err", err)
	}
	slog.Error("world: паника в шаге региона — тик провален",
		"region", r.id, "phase", phase, "panic", p, "streak", r.panicStreak)
	if r.panicStreak >= r.cfg.FreezePanics {
		r.freeze("серия паник шага")
		return
	}
	// ящики, не дренированные из-за бюджета, не тронуты — следующим шагам
	r.ctrlBatch = r.ctrlBatch[:0]
	r.entityBuf = r.entityBuf[:0]
	r.portions = r.portions[:0]
	r.records = r.records[:0]
}

// freeze — заморозка региона: шаги не исполняются, письма копятся; разморозка
// вне фазы 3 (рестарт процесса — надзор let-it-crash).
func (r *Region) freeze(reason string) {
	if r.frozenFlag.Swap(true) {
		return
	}
	if r.activeFlag {
		r.activeFlag = false
		r.metro.Deactivate(r)
		r.slept = true
	}
	slog.Error("world: регион заморожен (алерт recover-политики)", "region", r.id, "reason", reason)
}

// step — дисциплина шага: drain → fold → эффекты населения → фаза B →
// publish → ack. Номер шага — Now() на старте; накопившиеся звонки
// сбрасываются (non-blocking перечит).
func (r *Region) step() {
	n := r.metro.Now()
	r.stepTick = n
	select {
	case <-r.ringCh:
	default:
	}
	var delta uint64
	if r.slept {
		r.slept = false // шаг после сна: симуляционное время региона не тёкло
	} else if uint64(n) > uint64(r.lastStep) {
		delta = uint64(n) - uint64(r.lastStep)
	}

	r.curPhase = phaseDrain
	r.resetDrain()
	r.injectPanic(phaseDrain)
	r.drain(n)
	r.phDrain.Add(1)

	r.curPhase = phaseFold
	r.injectPanic(phaseFold)
	rng := rand.New(rand.NewPCG(uint64(r.id), uint64(n)))
	res := Fold(n, delta, rng, r.state, r.entsProj(), r.portions, r.adviseBuf)
	r.phFold.Add(1)

	r.curPhase = phaseEffects
	r.injectPanic(phaseEffects)
	births := r.applyEffects(res)
	r.phEffects.Add(1)

	r.curPhase = phaseLog
	r.injectPanic(phaseLog)
	if err := r.log.LogStep(StepInput{
		Tick: n, Delta: delta, Births: births, Retires: res.Retires,
		Portions: r.records, Advisory: r.adviseBuf,
	}); err != nil {
		r.freeze("ошибка записи лога порций")
		return
	}
	r.adviseBuf = r.adviseBuf[:0]

	r.curPhase = phaseB
	r.injectPanic(phaseB)
	r.outbox = append(r.outbox, res.Out...)
	r.phaseB()
	r.phB.Add(1)

	r.curPhase = phasePublish
	r.injectPanic(phasePublish)
	r.snapPtr.Store(&snapshot{tick: n})
	r.phPublish.Add(1)

	r.curPhase = phaseAck
	r.injectPanic(phaseAck)
	r.lastStep = n
	r.doneTick.Store(uint64(n))
	r.phAck.Add(1)
	r.panicStreak = 0

	r.syncMembership()
}

// injectPanic — тестовый шов инъекции сбоя (критерий «паника в фазе B ⇒
// счётчики A выросли, B/publish/ack — нет»); в поставке выключен.
func (r *Region) injectPanic(phase byte) {
	if r.forcePanic != 0 && r.forcePanic == phase {
		panic(fmt.Sprintf("world: инъекция сбоя в фазе %d", phase))
	}
}

// drain — изъятие порций: контрольный ящик первым (≤K контрольных в
// приоритете, излишек и не-контрольные — общий список после сущностных
// ящиков), затем сущностные ящики по сортированному слайсу с кольцевым
// стартом tick mod len под бюджетом drainBudget (остаток ≥1 ⇒ пачка целиком,
// перерасход метится стопом обхода). После дрена — AckNotify и перечит
// (протокол читателя P3.1): письмо в окне дрена обязано дать следующий шаг.
// resetDrain — сброс буферов дрена на старте шага: паника фазы дрена не
// дропает пачки прошлого успешного шага.
func (r *Region) resetDrain() {
	r.portions = r.portions[:0]
	r.records = r.records[:0]
	r.ctrlBatch = r.ctrlBatch[:0]
	r.prioBuf = r.prioBuf[:0]
	r.restBuf = r.restBuf[:0]
	r.rereadBuf = r.rereadBuf[:0]
	r.entityBuf = r.entityBuf[:0]
}

func (r *Region) drain(n Tick) {
	// контрольный ящик — первым
	r.ctrlBatch = r.ctrl.ExtractInto(r.ctrlToken, r.ctrlBatch[:0])
	r.prioBuf = r.prioBuf[:0]
	r.restBuf = r.restBuf[:0]
	k := 0
	for _, env := range r.ctrlBatch {
		if env.Kind.Regional() && k < r.cfg.CtrlBudget {
			r.prioBuf = append(r.prioBuf, env)
			k++
		} else {
			r.restBuf = append(r.restBuf, env)
		}
	}
	if len(r.prioBuf) > 0 {
		r.portions = append(r.portions, Portion{Region: r.id, Tick: n, Envs: r.prioBuf})
		r.records = append(r.records, PortionRecord{Box: r.ctrlID, Mark: r.ctrl.Mark(), Envs: r.prioBuf})
	}

	// сущностные ящики: кольцевой старт, бюджет писем
	budget := r.cfg.DrainBudget
	if len(r.residents) > 0 {
		start := int(uint64(n) % uint64(len(r.residents)))
		for i := 0; i < len(r.residents) && budget > 0; i++ {
			res := r.residents[(start+i)%len(r.residents)]
			from := len(r.entityBuf)
			r.entityBuf = res.box.ExtractAppend(res.token, r.entityBuf) // дописывание: пачки соседей не перезаписываются
			took := len(r.entityBuf) - from
			if took == 0 {
				continue
			}
			if took > budget {
				r.drainOverrun.Add(int64(took - budget)) // перерасход метрится: пачка неделима
			}
			budget -= took
			envs := r.entityBuf[from:]
			r.portions = append(r.portions, Portion{Region: r.id, Tick: n, Envs: envs})
			r.records = append(r.records, PortionRecord{Box: res.ent.ID, Mark: res.box.Mark(), Envs: envs})
		}
	}

	// излишек контрольных + перечит после AckNotify — общий список, последней порцией
	r.ctrl.AckNotify()
	r.rereadBuf = r.ctrl.ExtractInto(r.ctrlToken, r.rereadBuf[:0])
	r.restBuf = append(r.restBuf, r.rereadBuf...)
	if len(r.restBuf) > 0 {
		r.portions = append(r.portions, Portion{Region: r.id, Tick: n, Envs: r.restBuf})
		r.records = append(r.records, PortionRecord{Box: r.ctrlID, Mark: r.ctrl.Mark(), Envs: r.restBuf})
	}
}

// entsProj — кэш проекции residents для fold: перестраивается при изменении
// населения (0 аллокаций на стабильном населении).
func (r *Region) entsProj() []*Entity {
	if r.entsVer != r.popVersion || len(r.ents) != len(r.residents) { // поколение, не длина: равные Births+Retires меняют состав
		r.ents = make([]*Entity, len(r.residents))
		for i, res := range r.residents {
			r.ents[i] = res.ent
		}
		r.entsVer = r.popVersion
	}
	return r.ents
}

// applyEffects — применение эффектов населения после дрена (обход под
// мутацией слайса исключён): рождения — Register + Claim + вставка
// (присвоенные ID возвращаются для лога), удаления — Despawn → Retire →
// освобождение записи.
func (r *Region) applyEffects(res StepResult) []AppliedBirth {
	births := make([]AppliedBirth, 0, len(res.Births))
	for _, b := range res.Births {
		ent := b.Ent
		id, err := r.Spawn(ent)
		if err != nil {
			slog.Error("world: рождение жителя не удалось", "region", r.id, "err", err)
			continue
		}
		ent.ID = id
		births = append(births, AppliedBirth{ID: id, Ent: &ent})
	}
	for _, rt := range res.Retires {
		r.Remove(rt.ID)
	}
	return births
}

// phaseB — capped-отправка исходящих: сначала backlog, затем свежие письма
// шага (порядок отправителя); кап на шаг, излишек переносится.
func (r *Region) phaseB() {
	sent := 0
	for sent < len(r.outbox) && sent < r.cfg.PhaseBCap {
		r.reg.Send(r.outbox[sent])
		sent++
	}
	if sent == len(r.outbox) {
		r.outbox = r.outbox[:0]
	} else {
		n := copy(r.outbox, r.outbox[sent:])
		r.outbox = r.outbox[:n]
	}
	r.backlogLen.Store(int64(len(r.outbox)))
	if len(r.outbox) > outboxAlertThreshold {
		if !r.backlogNoted {
			r.backlogNoted = true
			slog.Error("world: глубина outbox превысила порог (алерт декларации)",
				"region", r.id, "backlog", len(r.outbox), "threshold", outboxAlertThreshold)
		}
	} else if len(r.outbox) == 0 {
		r.backlogNoted = false
	}
}

// syncMembership — активность через общий механизм сета: активен, пока есть
// жители; пустой — деактивация (сон: первый шаг после сна — delta=0).
// Только горутина региона.
func (r *Region) syncMembership() {
	if len(r.residents) > 0 {
		if !r.activeFlag {
			r.activeFlag = true
			r.metro.Activate(r)
		}
		return
	}
	if r.activeFlag {
		r.activeFlag = false
		r.metro.Deactivate(r)
		r.slept = true
	}
}
