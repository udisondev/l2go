package world

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"sync/atomic"

	"github.com/udisondev/l2go/internal/encode"
	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/replica"
	"github.com/udisondev/l2go/internal/transport"
)

// Фазы шага — порядок drain → fold → эффекти населения → log → AoI → B →
// publish → ack; маркер текущей фазы едет в лог при панике.
const (
	phaseDrain byte = iota + 1
	phaseFold
	phaseEffects
	phaseLog
	phaseAoI
	phaseB
	phasePublish
	phaseAck
)

// outboxAlertThreshold — порог глубины outbox для алерта декларации очереди
// (троттлинг compose — фаза 4; предел отсутствует: исходящие региона —
// надёжный класс, живому не дропаются).
const outboxAlertThreshold = 4096

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
// FramePusher — стейдж исходящих кадров клиента (реализация — encode.Stage,
// удовлетворяет структурно; интерфейс у потребителя по codestyle §2).
type FramePusher interface {
	Push(id uint64, frame []byte, crypt bool)
}

type Region struct {
	id      RegionID
	cfg     Config
	metro   *Metronome
	reg     *transport.Registry
	log     *PortionLog
	pusher  FramePusher
	rules   Rules
	started atomic.Bool

	pub       *replica.Publisher
	join      *replica.Join
	adv       *logAdviser
	advWindow bool // окно advisory-чтений: от начала шага до LogStep

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
	forcePanic   atomic.Uint32 // инъекция сбоя для тестов recover-политики; 0 — выключена (тест-горутина пишет при живом Run — атомик)
	outbox       []transport.Envelope
	backlogNoted bool

	pendingPushes []FramePush // пуши шага (исполнение — фазой B раньше писем)

	// Фаза AoI: блоб шага, кадры join и курсор доставки (долговечный
	// хвост: недоставленное переносится в голову следующего шага); события —
	// слайс-вид стадинга Join, мир между шагами не хранит.
	nextBlob   *replica.Blob
	joinPushes []FramePush
	pushCursor int
	aoiRecs    []replica.Record
	aoiObs     []replica.Observer

	// forcePanicPostApply — тестовый шов окна [Apply, merge] (recover-политика;
	// атомик: пишется тест-горутиной при живом Run).
	forcePanicPostApply atomic.Bool

	npcIntroduceSkipped atomic.Uint64

	// Буферы дрена — поля региона, переиспользуемые между шагами.
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
	phAoI     atomic.Uint64
	phB       atomic.Uint64
	phPublish atomic.Uint64
	phAck     atomic.Uint64
}

// NewRegion создаёт регион: контрольный ящик регистрируется в реестре и
// клеймится (токен = uint64(ctrlID), ID монотонные с 1). Регион рождается
// спящим: первый шаг — delta=0.
func NewRegion(metro *Metronome, reg *transport.Registry, id RegionID, cfg Config, log *PortionLog, pusher FramePusher) (*Region, error) {
	if metro == nil || reg == nil || log == nil || pusher == nil {
		return nil, fmt.Errorf("world: NewRegion(%d): метроном, реестр, лог и пушер кадров обязательны", id)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("world: NewRegion(%d): %w", id, err)
	}
	r := &Region{
		id:     id,
		cfg:    cfg,
		metro:  metro,
		reg:    reg,
		log:    log,
		pusher: pusher,
		state:  newState(),
		slept:  true,
		pub:    replica.NewPublisher(),
		join:   replica.NewJoin(replica.CanonJoinConfig()),
	}
	r.adv = &logAdviser{src: r.pub, r: r}
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

// CtrlID — адрес контрольного ящика региона (получатели контрольных писем).
func (r *Region) CtrlID() transport.EntityID { return r.ctrlID }

// Wire — адресаты контрольных писем свёртки (шлюз, персист). Вызов обязателен
// до старта Run: регион рождается раньше шлюза, адрес при New неизвестен.
func (r *Region) Wire(gateway, pers transport.EntityID) error {
	if r.started.Load() {
		return fmt.Errorf("world: Wire после старта Run")
	}
	r.rules = Rules{
		GraceTicks:     r.cfg.GraceTicks,
		SaveRetryTicks: r.cfg.SaveRetryTicks,
		Persist:        pers,
		Gateway:        gateway,
		From:           r.ctrlID,
	}
	return nil
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
		PhaseAoI:     r.phAoI.Load(),
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
	PhaseAoI     uint64
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
	r.started.Store(true)
	defer r.shutdown()
	defer r.closeLog()
	defer r.finalSave()
	if !r.rules.valid() {
		slog.Error("world: регион без Wire — шаги не исполняются", "region", r.id)
		return
	}
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

// finalSave — финальный сохранитель (T3, ADR-0003 §9): живым игрокам и
// зависшим SaveQ — письма OpSaveChar; дорабатывает персист своим drain-ом,
// ответы мёртвому региону безвредны. Исполняется в горутине региона
// (single-writer) на выходе Run; порядок ключей — сортированный.
func (r *Region) finalSave() {
	if r.frozenFlag.Load() {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			slog.Error("world: паника финального сохранителя", "region", r.id, "panic", p)
		}
	}()
	saved := 0
	for _, res := range r.residents {
		if res.ent.Player == nil {
			continue
		}
		rec := res.ent.Player.Rec
		rec.X, rec.Y, rec.Z = int(res.ent.Pos.X), int(res.ent.Pos.Y), int(res.ent.Pos.Z)
		r.sendSave(rec, res.ent.ID)
		saved++
	}
	for _, acc := range sortedSaveKeys(r.state) {
		q := r.state.SaveQ[acc]
		r.sendSave(q.Char, q.Entity)
		saved++
	}
	if saved > 0 {
		slog.Info("world: финальные сохранения отправлены", "region", r.id, "chars", saved)
	}
}

// sendSave — письмо OpSaveChar от горутины региона (сохранитель).
func (r *Region) sendSave(rec persist.CharRecord, corr transport.EntityID) {
	body, err := persist.EncodeRequest(persist.Request{
		Op: persist.OpSaveChar, Corr: uint64(corr), Account: rec.Account, Char: rec,
	})
	if err != nil {
		slog.Error("world: кодирование финального OpSaveChar", "account", rec.Account, "err", err)
		return
	}
	r.reg.Send(transport.Envelope{
		To: transport.Addr{Entity: r.rules.Persist}, FromID: r.ctrlID,
		Kind: transport.KindPersistRequest, Payload: body,
	})
}

// userInfoOf — CharRecord → данные UserInfo (производные статы — плейсхолдеры
// P3.5 до появления формул).
func userInfoOf(p *Player) protocol.UserInfoData {
	r := &p.Rec
	return protocol.UserInfoData{
		X: int32(r.X), Y: int32(r.Y), Z: int32(r.Z),
		Name:      r.Name,
		Race:      int32(r.Race),
		Female:    r.Sex == 1,
		BaseClass: int32(r.ClassID),
		Level:     int32(r.Level),
		Exp:       r.Exp,
		Str:       int32(persist.HumanFighter.Str),
		Dex:       int32(persist.HumanFighter.Dex),
		Con:       int32(persist.HumanFighter.Con),
		Int:       int32(persist.HumanFighter.Int),
		Wit:       int32(persist.HumanFighter.Wit),
		Men:       int32(persist.HumanFighter.Men),
		MaxHp:     int32(r.HP), CurHp: int32(r.HP),
		MaxMp: int32(r.MP), CurMp: int32(r.MP),
		Sp: 0,
	}
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
	r.advWindow = false
	if err := r.log.LogPanic(r.stepTick, phase); err != nil {
		slog.Error("world: маркер паники не записан", "region", r.id, "err", err)
	}
	// немедленная доставка выжившего хвоста кадров (оптимизация латентности;
	// гарантия — перенос resetDrain): под recover — неудача оставляет остаток
	// живым по курсору, процесс не падает мимо freeze-политики
	func() {
		defer func() {
			if p := recover(); p != nil {
				slog.Error("world: паника доставки хвоста в recovered", "region", r.id, "panic", p)
			}
		}()
		for i := r.pushCursor; i < len(r.pendingPushes); i++ {
			p := r.pendingPushes[i]
			r.pusher.Push(p.Client, p.Frame, p.Crypt)
			r.pushCursor = i + 1
		}
	}()
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
	r.advWindow = true // advisory-окно: чтения допустимы до LogStep этого шага
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
	res := Fold(n, delta, rng, r.state, r.entsProj(), r.portions, r.adviseBuf, r.rules)
	r.pendingPushes = append(r.pendingPushes, res.Pushes...)
	// письма свёртки — в outbox сразу после Fold: переживают панику любой
	// позднейшей фазы (надёжный класс, доставляются phaseB/backlog-ом)
	r.outbox = append(r.outbox, res.Out...)
	r.phFold.Add(1)

	r.curPhase = phaseEffects
	r.injectPanic(phaseEffects)
	births := r.applyEffects(res, n)
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
	r.advWindow = false // чтения после LogStep — паника шва (не своя порция)

	r.curPhase = phaseAoI
	r.injectPanic(phaseAoI)
	r.aoiStep()
	r.phAoI.Add(1)

	r.curPhase = phaseB
	r.injectPanic(phaseB)
	r.phaseB()
	r.phB.Add(1)

	r.curPhase = phasePublish
	r.injectPanic(phasePublish)
	r.pub.Commit(r.nextBlob)
	r.phPublish.Add(1)

	r.curPhase = phaseAck
	r.injectPanic(phaseAck)
	r.lastStep = n
	r.doneTick.Store(uint64(n))
	r.phAck.Add(1)
	r.panicStreak = 0

	r.syncMembership()
}

// aoiStep — фаза AoI: Build (издатель не мутируется) → Step (стадинг
// диффов) → компоновка кадров → Apply (view-мутации, appliedGen первой
// операцией) → merge (слив ТОЛЬКО применённого стадинга). Паника до merge ⇒
// joinPushes шага дропаются (курсор их не видит), события пере-выведутся
// манифестом/примирением следующего шага.
func (r *Region) aoiStep() {
	r.aoiRecs = r.aoiRecs[:0]
	for _, res := range r.residents {
		r.aoiRecs = append(r.aoiRecs, recordOf(res.ent))
	}
	r.nextBlob = r.pub.Build(r.aoiRecs)
	r.aoiObs = r.aoiObs[:0]
	for _, res := range r.residents {
		if res.ent.Player == nil || res.ent.Player.EnterLeaving || leavingEntity(r.state, res.ent.ID) {
			continue // наблюдатели — только живые игроки (Leaving кадры некому доставлять)
		}
		r.aoiObs = append(r.aoiObs, replica.Observer{Entity: res.ent.ID, ConnID: res.ent.Player.ConnID})
	}
	events := r.join.Step(r.aoiObs, r.nextBlob)
	r.joinPushes = r.composeJoin(events)
	r.join.Apply()
	if r.forcePanicPostApply.Load() {
		panic("world: инъекция сбоя между Apply и merge фазы AoI")
	}
	r.pendingPushes = append(r.pendingPushes, r.joinPushes...)
}

// injectPanic — тестовый шов инъекции сбоя (критерий «паника в фазе B ⇒
// счётчики A выросли, B/publish/ack — нет»); в поставке выключен.
func (r *Region) injectPanic(phase byte) {
	if fp := r.forcePanic.Load(); fp != 0 && byte(fp) == phase {
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
	// долговечный хвост: недоставленные кадры переносятся в голову (сброс
	// слайса без переноса запрещён — потеря = вечный фантом/невидимость)
	if r.pushCursor > 0 {
		n := copy(r.pendingPushes, r.pendingPushes[r.pushCursor:])
		r.pendingPushes = r.pendingPushes[:n]
		r.pushCursor = 0
	}
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
// освобождение записи. Рождение игрока: тени аккаунта/коннекта, бинд-письмо
// шлюзу и слиток входа (композиция здесь — ObjectID требует присвоенный ID;
// P3.6-F42: пушер стационарных кадров — регион). EnterLeaving — «вошёл и
// оборвался»: без бинда и слитка, сразу в grace.
func (r *Region) applyEffects(res StepResult, tick Tick) []AppliedBirth {
	births := make([]AppliedBirth, 0, len(res.Births))
	for _, b := range res.Births {
		ent := b.Ent
		if ent.Player != nil && ent.Player.DisplacedSameStep {
			// Вытеснено повторным входом той же пачки: сущность не рождается
			// ВООБЩЕ — ни ящика, ни резидента, ни финального сохранения
			// (проверка строго до Spawn: рождённый призрак жил бы вечно).
			continue
		}
		id, err := r.Spawn(ent)
		if err != nil {
			slog.Error("world: рождение жителя не удалось", "region", r.id, "err", err)
			continue
		}
		ent.ID = id
		births = append(births, AppliedBirth{ID: id, Ent: &ent})
		if ent.Player == nil {
			continue
		}
		r.state.ResolveBirth(ent.Player.Rec.Account, ent.Player.ConnID, id)
		if ent.Player.EnterLeaving {
			addLeaving(r.state, leaveState{Entity: id, Deadline: tick + Tick(r.cfg.GraceTicks)})
			continue
		}
		r.sendBind(ent.Player.ConnID, id)
		r.composeEnterWorld(id, ent.Player, tick)
	}
	for _, rt := range res.Retires {
		r.Remove(rt.ID)
	}
	return births
}

// sendBind — KindConnBind шлюзу (производитель — регион, P3.7-F1).
func (r *Region) sendBind(conn uint64, id transport.EntityID) {
	body, err := transport.EncodeLetter(transport.ConnBindMsg{Conn: conn, Entity: id})
	if err != nil {
		slog.Error("world: кодирование бинда", "err", err)
		return
	}
	r.reg.Send(transport.Envelope{
		To: transport.Addr{Entity: r.rules.Gateway}, FromID: r.ctrlID,
		Kind: transport.KindConnBind, Payload: body,
	})
}

// composeEnterWorld — слиток входа в пуши шага (кадры — encode-обёрткой;
// gameTime — из тика метронома, IG-сутки 4 реальных часа).
func (r *Region) composeEnterWorld(id transport.EntityID, p *Player, tick Tick) {
	hz := r.cfg.Hz
	frames := encode.ComposeEnterWorld(encode.EnterWorldData{
		Entity:          uint64(id),
		User:            userInfoOf(p),
		Heading:         int32(p.Rec.Heading),
		GameTimeMinutes: encode.GameTimeMinutes(uint64(tick), hz),
	})
	for _, f := range frames {
		r.pendingPushes = append(r.pendingPushes, FramePush{Client: p.ConnID, Frame: f, Crypt: true})
	}
}

// phaseB — capped-отправка исходящих. Пуши кадров — СТРОГО раньше писем
// шага (happens-before «LeaveWorld в стейдж → ConnClose шлюзу»: close-after-
// flush стейджа выдаёт кадр до разрыва). Письма: сначала backlog, затем
// свежие (порядок отправителя); кап на шаг, излишек переносится.
func (r *Region) phaseB() {
	for i := r.pushCursor; i < len(r.pendingPushes); i++ {
		p := r.pendingPushes[i]
		r.pusher.Push(p.Client, p.Frame, p.Crypt)
		r.pushCursor = i + 1
	}
	if r.pushCursor == len(r.pendingPushes) {
		r.pendingPushes = r.pendingPushes[:0]
		r.pushCursor = 0
	}
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
// жители ИЛИ висят несохранённые персонажи (SaveQ: IO-ретраи живы, S6-мажор);
// иначе последний logout усыплял бы регион с мёртвыми ретраями. Пустой и без
// SaveQ — деактивация (сон: первый шаг после сна — delta=0). Только горутина
// региона.
func (r *Region) syncMembership() {
	if len(r.residents) > 0 || len(r.state.SaveQ) > 0 {
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
