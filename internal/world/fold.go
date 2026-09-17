package world

import (
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"sort"

	"github.com/udisondev/l2go/internal/geo"
	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/transport"
)

// Birth — эффект рождения сущности: вычисляется свёрткой из писем шага,
// применяется актором после обхода дрена (Register + Claim + вставка в
// отсортированный слайс). Присвоенный EntityID регион дописывает в лог порций.
type Birth struct {
	Ent Entity
}

// Retire — эффект удаления сущности: актор исполняет Despawn → Retire →
// освобождение записи (контракт транспорта).
type Retire struct {
	ID transport.EntityID
}

// FramePush — исходящий кадр клиента: актор региона пушит его в encode.Stage
// в фазе B СТРОГО РАНЬШЕ писем Out шага (happens-before «LeaveWorld в стейдж →
// KindConnClose шлюзу» — close-after-flush выдаёт кадр до разрыва).
type FramePush struct {
	Client uint64
	Frame  []byte
	Crypt  bool
}

// StepResult — эффекты шага свёртки: исходящие письма, пуши кадров и
// изменения населения (Births/Retires — только через свёртку, обязательство
// P3.2). Порядок Pushes и Out — порядок применения, детерминирован.
type StepResult struct {
	Out     []transport.Envelope
	Pushes  []FramePush
	Births  []Birth
	Retires []Retire
}

// Rules — параметры поведения свёртки: тиковые окна, период метронома (тики
// переводятся во время движением; реплей подставляет из заголовка лога порций)
// и адресаты контрольных писем (актор передаёт значением из своего конфига и
// wire-up; глобалов нет).
type Rules struct {
	GraceTicks     int                // окно удержания после LinkDead
	SaveRetryTicks int                // каденс повторов сохранения
	PeriodNS       int64              // период метронома: dt = тики × период (D1)
	Persist        transport.EntityID // адрес персист-актора
	Gateway        transport.EntityID // адрес шлюза
	From           transport.EntityID // ctrl-ящик региона (отправитель)
}

func (r Rules) valid() bool {
	return r.GraceTicks > 0 && r.SaveRetryTicks > 0 && r.PeriodNS > 0 &&
		r.Persist != 0 && r.Gateway != 0 && r.From != 0
}

// shadowEntity — материализованное актором рождение игрока: тень для решений
// свёртки следующих шагов (ID присваивает Spawn; fold их не знает).
type shadowEntity struct {
	ID   transport.EntityID
	Conn uint64
}

// leaveState — удержание сущности после LinkDead (grace-окно в тиках);
// слайс всегда отсортирован по (Deadline, Entity) — детерминизм обхода.
type leaveState struct {
	Entity   transport.EntityID
	Deadline Tick
}

// saveState — попытка финального сохранения персонажа: ретраи до ok;
// валидационный отказ персиста — Dead (персонаж не сохраняем, алерт).
type saveState struct {
	Account string
	Char    persist.CharRecord
	Entity  transport.EntityID // Corr ответа (вечен и уникален)
	NextTry Tick
	Dead    bool
}

// State — счётчики и связки свёртки: детерминированная функция входов.
// Карты читаются/пишутся только свёрткой и актором региона (single-writer);
// любые обходы — по сортированным ключам (D3). Тени Accounts/Conns
// материализует актор при рождении (ResolveBirth), CleanBirth — при уходе.
type State struct {
	Steps      uint64
	Letters    uint64
	KindCounts [transport.KindCount]uint64
	Noise      uint64
	LastDelta  uint64

	Conns    map[uint64]transport.EntityID // connID → entity (ResolveBirth)
	Accounts map[string]shadowEntity       // аккаунт → тень (ResolveBirth)
	Leaving  []leaveState                  // сортировано (Deadline, Entity)
	SaveQ    map[string]*saveState         // аккаунт → попытка сохранения

	DroppedFrames   uint64 // кадры Leaving/неизвестных сущностей
	DeadLetters     uint64 // контрольные письма без адресата
	Unsavable       uint64 // валидационные отказы персиста (стоп ретраев)
	SpeedFlags      uint64 // флаги спидхака (токен-бакет ниже −SLACK)
	SnapBacks       uint64 // коррекции ValidateLocation (дрейф/спидхак/телепорт)
	CannotMoveNoops uint64 // CannotMoveAnymore вне движения: применён как стоячий поворот (метрика)
}

// newState — состояние с инициализированными картами.
func newState() *State {
	return &State{
		Conns:    make(map[uint64]transport.EntityID),
		Accounts: make(map[string]shadowEntity),
		SaveQ:    make(map[string]*saveState),
	}
}

// ResolveBirth — материализация рождения игрока актором (после Spawn):
// тени аккаунта и коннекта для решений свёртки. Перезаход перезаписывает тень
// аккаунта — старая развязывается CleanBirth при её уходе.
func (st *State) ResolveBirth(account string, conn uint64, id transport.EntityID) {
	st.Accounts[account] = shadowEntity{ID: id, Conn: conn}
	st.Conns[conn] = id
}

// CleanBirth — условная развязка тени ухода: безусловный delete стёр бы бинд
// новой сущности того же аккаунта, вошедшей этим же шагом (S6-М2).
func (st *State) CleanBirth(account string, conn uint64, id transport.EntityID) {
	if sh, ok := st.Accounts[account]; ok && sh.ID == id {
		delete(st.Accounts, account)
	}
	if st.Conns[conn] == id {
		delete(st.Conns, conn)
	}
}

// Fold — детерминированная функция шага: применяет порции к состоянию и
// населению, возвращает эффекты шага. Порядок: счётчики → письма порций (в
// порядке дрена, интенты движения — фаза B capped) → фаза A (advance по dt)
// → экспирации grace → ретраи сохранений. Письма раньше advance —
// запись-отклонение от ADR-0002 §5 (A→B) ради латентности старта: канон
// начинает движение при обработке письма, A→B добавил бы 100 мс; кредит ≤1
// тика детерминирован и одинаков в прогоне и реплее. Чистота: fold не читает
// ничего, кроме аргументов (гео — аргумент, глобал запрещён); мутация
// state/ents — владение актора.
func Fold(tick Tick, delta uint64, rng *rand.Rand, st *State, ents []*Entity, portions []Portion, adv []AdvisoryIn, rules Rules, gm *geo.Map) StepResult {
	st.Steps++
	st.LastDelta = delta
	st.Noise += rng.Uint64()
	if st.Conns == nil {
		st = newStateMaps(st)
	}
	res := StepResult{}
	for i := range portions {
		for _, env := range portions[i].Envs {
			st.Letters++
			if k := int(env.Kind); k >= 1 && k <= transport.KindCount {
				st.KindCounts[k-1]++
			}
		}
	}
	// Письма применяются в порядке дрена; рождения этого шага — локально:
	// LinkDead/повторный вход того же аккаунта в той же пачке находят своё
	// нерождённое рождение (тени Accounts материализуются актором позже).
	mov := &movement{gm: gm, budget: moveLettersCap}
	pending := newPendingEnters()
	for i := range portions {
		for j := range portions[i].Envs {
			foldLetter(tick, st, ents, &portions[i].Envs[j], rules, &res, pending, mov)
		}
	}
	foldAdvance(delta, rules, ents, &res)
	foldExpiries(tick, st, ents, rules, &res)
	foldRetries(tick, st, rules, &res)
	for _, e := range ents {
		e.Beat = tick
	}
	return res
}

func newStateMaps(st *State) *State {
	if st.Conns == nil {
		st.Conns = make(map[uint64]transport.EntityID)
	}
	if st.Accounts == nil {
		st.Accounts = make(map[string]shadowEntity)
	}
	if st.SaveQ == nil {
		st.SaveQ = make(map[string]*saveState)
	}
	return st
}

// pendingEnters — нерождённые входы шага: по конну (LinkDead) и по аккаунту
// (вытеснение повторным входом той же пачки — тень Accounts ещё не
// материализована актором).
type pendingEnters struct {
	byConn    map[uint64]int   // conn → индекс в res.Births
	byAccount map[string][]int // аккаунт → индексы
}

func newPendingEnters() *pendingEnters {
	return &pendingEnters{byConn: make(map[uint64]int), byAccount: make(map[string][]int)}
}

// foldLetter — одно письмо шага по типу. Отправитель зеркально сверяется с
// адресами Rules (валидация на применении у владельца: KindPersistReply —
// только персист, контрольные шлюза — только шлюз).
func foldLetter(tick Tick, st *State, ents []*Entity, env *transport.Envelope, rules Rules, res *StepResult, pending *pendingEnters, mov *movement) {
	switch env.Kind {
	case transport.KindEnterWorld, transport.KindLinkDead:
		if env.FromID != rules.Gateway {
			st.DeadLetters++
			return
		}
	case transport.KindPersistReply:
		if env.FromID != rules.Persist {
			st.DeadLetters++
			return
		}
	case transport.KindClientFrame:
		if env.FromID != rules.Gateway {
			st.DroppedFrames++
			return
		}
	}
	switch env.Kind {
	case transport.KindEnterWorld:
		msg, err := transport.DecodeLetter[transport.EnterWorldMsg](env.Payload)
		if err != nil {
			st.DeadLetters++
			return
		}
		foldEnterWorld(tick, st, msg, res, pending)
	case transport.KindLinkDead:
		msg, err := transport.DecodeLetter[transport.ConnRefMsg](env.Payload)
		if err != nil {
			st.DeadLetters++
			return
		}
		foldLinkDead(tick, st, msg.Conn, rules, res, pending)
	case transport.KindPersistReply:
		foldPersistReply(tick, st, rules, env.Payload)
	case transport.KindClientFrame:
		foldClientFrame(tick, st, ents, env, rules, res, mov)
	default:
		// Прочие типы — вне свёртки входа/выхода (фазы 4+); счёт учтён.
	}
}

// foldEnterWorld — вход: вытеснение живой сущности аккаунта (без её
// персиста — снимок перезахода свеже́е), рождение новой, очистка SaveQ
// аккаунта (ретраи старого снимка не переживают перезаход).
// foldEnterWorld — вход: валидация записи на применении (аккаунт нормализован
// и совпадает с конвертом, имя в домене), вытеснение живой сущности аккаунта
// (без её персиста — снимок перезахода свеже́е) — материализованной тенью ИЛИ
// нерождённым входом той же пачки, очистка SaveQ аккаунта, рождение новой.
func foldEnterWorld(tick Tick, st *State, msg transport.EnterWorldMsg, res *StepResult, pending *pendingEnters) {
	var rec persist.CharRecord
	if err := json.Unmarshal(msg.Char, &rec); err != nil {
		st.DeadLetters++
		return
	}
	if norm, err := persist.NormalizeLogin(msg.Account); err != nil || norm != msg.Account ||
		rec.Account != msg.Account || !persist.ValidName(rec.Name) {
		st.DeadLetters++
		return
	}
	// Позиция записи — за trust-границей (персист-файл): сетка мира
	// проверяется до сужения до int32 (значение 2^32+k заворачивается кастом).
	if !geo.InWorld(rec.X, rec.Y) {
		st.DeadLetters++
		return
	}
	delete(st.SaveQ, msg.Account)
	if sh, ok := st.Accounts[msg.Account]; ok {
		res.Retires = append(res.Retires, Retire{ID: sh.ID}) // без персиста
		removeLeaving(st, sh.ID)
		st.CleanBirth(msg.Account, sh.Conn, sh.ID)
	}
	// Вытеснение нерождённого входа той же пачки: тень аккаунта ещё не
	// материализована актором, повторный вход гасит первое рождение (без спавна).
	for _, idx := range pending.byAccount[msg.Account] {
		if b := &res.Births[idx]; b.Ent.Player != nil {
			b.Ent.Player.DisplacedSameStep = true
		}
	}
	res.Births = append(res.Births, Birth{Ent: Entity{
		Pos:     Position{X: int32(rec.X), Y: int32(rec.Y), Z: int32(rec.Z)},
		Heading: int32(rec.Heading) & 0xFFFF, // запись за trust-границей — домен [0,65536)
		HP:      int32(rec.HP),
		Player:  &Player{Rec: rec, ConnID: msg.Conn, PendingTeleport: true, SpeedBudget: speedCAP},
	}})
	pending.byConn[msg.Conn] = len(res.Births) - 1
	pending.byAccount[msg.Account] = append(pending.byAccount[msg.Account], len(res.Births)-1)
}

// foldLinkDead — обрыв коннекта: живому — grace-удержание; входу этого шага —
// пометка «вошёл и оборвался» (актор не пошлёт слиток/бинд, сущность сразу в
// grace); неизвестному — dead-letter.
func foldLinkDead(tick Tick, st *State, conn uint64, rules Rules, res *StepResult, pending *pendingEnters) {
	if idx, ok := pending.byConn[conn]; ok {
		if b := &res.Births[idx]; b.Ent.Player != nil {
			b.Ent.Player.EnterLeaving = true
		}
		return
	}
	if id, ok := st.Conns[conn]; ok {
		addLeaving(st, leaveState{Entity: id, Deadline: tick + Tick(rules.GraceTicks)})
		return
	}
	st.DeadLetters++
}

// foldClientFrame — стационарный кадр из ящика сущности: Logout — полный
// выход; RequestRestart — отказ канона; движение — интент/сверка/упор (P3.9);
// чат — P3.11; Leaving/неизвестным — классовый дроп с метрикой (кадр не
// применяется).
func foldClientFrame(tick Tick, st *State, ents []*Entity, env *transport.Envelope, rules Rules, res *StepResult, mov *movement) {
	if len(env.Payload) == 0 {
		st.DroppedFrames++
		return
	}
	id := env.To.Entity
	idx := sort.Search(len(ents), func(i int) bool { return ents[i].ID >= id })
	if idx >= len(ents) || ents[idx].ID != id {
		st.DroppedFrames++
		return
	}
	ent := ents[idx]
	if ent.Player == nil {
		st.DroppedFrames++
		return
	}
	if leavingEntity(st, id) {
		st.DroppedFrames++
		return
	}
	switch env.Payload[0] {
	case protocol.OpLogout:
		foldLogout(tick, st, ent, rules, res)
	case protocol.OpCRequestRestart:
		pushFrame(res, ent.Player.ConnID, protocol.RestartResponseSize,
			func(dst []byte) int { return protocol.WriteRestartResponse(dst, false) })
		pushFrame(res, ent.Player.ConnID, protocol.ActionFailedSize, protocol.WriteActionFailed)
	case protocol.OpCMoveToLocation:
		foldMoveToLocation(st, ent, env, mov, res)
	case protocol.OpCValidatePosition:
		foldValidatePosition(st, ent, env, res)
	case protocol.OpCCannotMoveAnymore:
		foldCannotMoveAnymore(st, ent, env, res)
	default:
		// Чат — потребитель P3.11; кадр валиден, применения в фазе 3 нет.
	}
}

// foldLogout — полный выход: остановка движения, финальный персист (Corr =
// EntityID), Retire, LeaveWorld клиенту, ConnClose шлюзу, развязка.
func foldLogout(tick Tick, st *State, ent *Entity, rules Rules, res *StepResult) {
	stopSegment(ent)
	queueSave(tick, st, ent, rules, res)
	res.Retires = append(res.Retires, Retire{ID: ent.ID})
	pushFrame(res, ent.Player.ConnID, protocol.LeaveWorldSize, protocol.WriteLeaveWorld)
	sendLetter(res, transport.KindConnClose, rules, transport.ConnRefMsg{Conn: ent.Player.ConnID})
	removeLeaving(st, ent.ID)
	st.CleanBirth(ent.Player.Rec.Account, ent.Player.ConnID, ent.ID)
}

// foldExpiries — истёкшие grace-удержания: путь логаута БЕЗ LeaveWorld и
// ConnClose (сокет мёртв); порядок — по (Deadline, Entity). Guard владения:
// сущность, вытесненная перезаходом (тень аккаунта указывает на другую),
// не сохраняется — её снимок устарел относительно новой сессии.
func foldExpiries(tick Tick, st *State, ents []*Entity, rules Rules, res *StepResult) {
	i := 0
	for i < len(st.Leaving) && st.Leaving[i].Deadline <= tick {
		id := st.Leaving[i].Entity
		idx := sort.Search(len(ents), func(k int) bool { return ents[k].ID >= id })
		if idx < len(ents) && ents[idx].ID == id && ents[idx].Player != nil {
			ent := ents[idx]
			stopSegment(ent)
			acc := ent.Player.Rec.Account
			if sh, ok := st.Accounts[acc]; !ok || sh.ID == id {
				queueSave(tick, st, ent, rules, res)
			}
			res.Retires = append(res.Retires, Retire{ID: id})
			st.CleanBirth(acc, ent.Player.ConnID, id)
		}
		i++
	}
	st.Leaving = st.Leaving[i:]
}

// foldRetries — повторы сохранений: безответные/IO — по каденсу; Dead —
// никогда (валидационный отказ персиста бессмысленно повторять).
func foldRetries(tick Tick, st *State, rules Rules, res *StepResult) {
	for _, acc := range sortedSaveKeys(st) {
		q := st.SaveQ[acc]
		if q.Dead || q.NextTry > tick {
			continue
		}
		sendSave(res, rules, q)
		q.NextTry = tick + Tick(rules.SaveRetryTicks)
	}
}

// saveSnapshot — единая точка снимка: запись персиста получает текущую
// позицию и живой heading сущности (обе точки сохранения — queueSave свёртки
// и finalSave актора).
func saveSnapshot(rec persist.CharRecord, e *Entity) persist.CharRecord {
	rec.X, rec.Y, rec.Z = int(e.Pos.X), int(e.Pos.Y), int(e.Pos.Z)
	rec.Heading = int(e.Heading)
	return rec
}

// queueSave — постановка финального сохранения (снимок saveSnapshot: позиция
// и живой heading) с первой отправкой.
func queueSave(tick Tick, st *State, ent *Entity, rules Rules, res *StepResult) {
	rec := saveSnapshot(ent.Player.Rec, ent)
	q := &saveState{Account: rec.Account, Char: rec, Entity: ent.ID,
		NextTry: tick + Tick(rules.SaveRetryTicks)}
	st.SaveQ[rec.Account] = q
	sendSave(res, rules, q)
}

// sendSave — письмо OpSaveChar персисту (Corr = EntityID).
func sendSave(res *StepResult, rules Rules, q *saveState) {
	body, err := persist.EncodeRequest(persist.Request{
		Op: persist.OpSaveChar, Corr: uint64(q.Entity), Account: q.Account, Char: q.Char,
	})
	if err != nil {
		slog.Error("world: кодирование OpSaveChar", "account", q.Account, "err", err)
		return
	}
	res.Out = append(res.Out, transport.Envelope{
		To: transport.Addr{Entity: rules.Persist}, FromID: rules.From,
		Kind: transport.KindPersistRequest, Payload: body,
	})
}

// foldPersistReply — ответ персиста: ok гасит попытку; валидационный отказ —
// Dead + счётчик Unsavable; IO — повтор по каденсу (сам повтор — foldRetries).
func foldPersistReply(tick Tick, st *State, rules Rules, payload []byte) {
	reply, err := persist.DecodeReply(payload)
	if err != nil {
		st.DeadLetters++
		return
	}
	for _, acc := range sortedSaveKeys(st) {
		q := st.SaveQ[acc]
		if q.Entity != transport.EntityID(reply.Corr) {
			continue
		}
		switch {
		case reply.OK:
			delete(st.SaveQ, acc)
		case reply.Code == persist.CodeIO:
			q.NextTry = tick + Tick(rules.SaveRetryTicks) // каденс решения 6
		default:
			if !q.Dead {
				q.Dead = true
				st.Unsavable++
				slog.Error("world: персист отверг сохранение (валидация) — ретраи остановлены",
					"account", acc, "code", reply.Code, "err", reply.Err)
			}
		}
		return
	}
	// Поздний ответ без ожидания (перезаход очистил SaveQ) — no-op.
}

func addLeaving(st *State, l leaveState) {
	i := sort.Search(len(st.Leaving), func(i int) bool {
		if st.Leaving[i].Deadline != l.Deadline {
			return st.Leaving[i].Deadline > l.Deadline
		}
		return st.Leaving[i].Entity >= l.Entity
	})
	st.Leaving = append(st.Leaving, leaveState{})
	copy(st.Leaving[i+1:], st.Leaving[i:])
	st.Leaving[i] = l
}

func removeLeaving(st *State, id transport.EntityID) {
	for i := range st.Leaving {
		if st.Leaving[i].Entity == id {
			st.Leaving = append(st.Leaving[:i], st.Leaving[i+1:]...)
			return
		}
	}
}

func leavingEntity(st *State, id transport.EntityID) bool {
	for i := range st.Leaving {
		if st.Leaving[i].Entity == id {
			return true
		}
	}
	return false
}

func sortedAccountKeys(st *State) []string {
	keys := make([]string, 0, len(st.Accounts))
	for k := range st.Accounts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedSaveKeys(st *State) []string {
	keys := make([]string, 0, len(st.SaveQ))
	for k := range st.SaveQ {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func pushFrame(res *StepResult, client uint64, n int, w func([]byte) int) {
	dst := make([]byte, n)
	w(dst)
	res.Pushes = append(res.Pushes, FramePush{Client: client, Frame: dst, Crypt: true})
}

func sendLetter(res *StepResult, kind transport.Kind, rules Rules, msg any) {
	body, err := transport.EncodeLetter(msg)
	if err != nil {
		slog.Error("world: кодирование контрольного письма", "kind", kind, "err", err)
		return
	}
	res.Out = append(res.Out, transport.Envelope{
		To: transport.Addr{Entity: rules.Gateway}, FromID: rules.From, Kind: kind, Payload: body,
	})
}

// Dump — детерминированная сериализация состояния и населения: бит-в-бит
// сравнение в тестах, потребитель — реплей-гейт. Карты — по сортированным
// ключам; население — в порядке обхода (сортированный слайс).
func (st *State) Dump(ents []*Entity) []byte {
	buf := make([]byte, 0, 48+40*len(ents))
	buf = binary.AppendUvarint(buf, st.Steps)
	buf = binary.AppendUvarint(buf, st.Letters)
	for k := range st.KindCounts {
		buf = binary.AppendUvarint(buf, st.KindCounts[k])
	}
	buf = binary.AppendUvarint(buf, st.Noise)
	buf = binary.AppendUvarint(buf, st.LastDelta)
	buf = binary.AppendUvarint(buf, st.DroppedFrames)
	buf = binary.AppendUvarint(buf, st.DeadLetters)
	buf = binary.AppendUvarint(buf, st.Unsavable)
	buf = binary.AppendUvarint(buf, st.SpeedFlags)
	buf = binary.AppendUvarint(buf, st.SnapBacks)
	buf = binary.AppendUvarint(buf, st.CannotMoveNoops)
	buf = binary.AppendUvarint(buf, uint64(len(ents)))
	for _, e := range ents {
		buf = appendEntity(buf, e)
	}
	buf = binary.AppendUvarint(buf, uint64(len(st.Accounts)))
	for _, acc := range sortedAccountKeys(st) {
		sh := st.Accounts[acc]
		buf = binary.AppendUvarint(buf, uint64(len(acc)))
		buf = append(buf, acc...)
		buf = binary.AppendUvarint(buf, uint64(sh.ID))
		buf = binary.AppendUvarint(buf, sh.Conn)
	}
	buf = binary.AppendUvarint(buf, uint64(len(st.Conns)))
	for _, conn := range sortedConnKeys(st) {
		buf = binary.AppendUvarint(buf, conn)
		buf = binary.AppendUvarint(buf, uint64(st.Conns[conn]))
	}
	buf = binary.AppendUvarint(buf, uint64(len(st.Leaving)))
	for _, l := range st.Leaving {
		buf = binary.AppendUvarint(buf, uint64(l.Entity))
		buf = binary.AppendUvarint(buf, uint64(l.Deadline))
	}
	buf = binary.AppendUvarint(buf, uint64(len(st.SaveQ)))
	for _, acc := range sortedSaveKeys(st) {
		q := st.SaveQ[acc]
		buf = binary.AppendUvarint(buf, uint64(len(acc)))
		buf = append(buf, acc...)
		buf = binary.AppendUvarint(buf, uint64(q.Entity))
		buf = binary.AppendUvarint(buf, uint64(q.NextTry))
		if q.Dead {
			buf = append(buf, 1)
		} else {
			buf = append(buf, 0)
		}
	}
	return buf
}

func sortedConnKeys(st *State) []uint64 {
	keys := make([]uint64, 0, len(st.Conns))
	for k := range st.Conns {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

// appendEntity — детерминированная сериализация сущности (поля по убыванию
// значимости; отрезок движения и Servants, Transfers и Player — полностью).
func appendEntity(buf []byte, e *Entity) []byte {
	buf = binary.AppendUvarint(buf, uint64(e.ID))
	buf = binary.AppendUvarint(buf, uint64(e.Owner))
	buf = binary.AppendUvarint(buf, uint64(e.Pos.X))
	buf = binary.AppendUvarint(buf, uint64(e.Pos.Y))
	buf = binary.AppendUvarint(buf, uint64(e.Pos.Z))
	buf = binary.AppendUvarint(buf, uint64(e.Dest.X))
	buf = binary.AppendUvarint(buf, uint64(e.Dest.Y))
	buf = binary.AppendUvarint(buf, uint64(e.Dest.Z))
	buf = binary.AppendUvarint(buf, uint64(e.Heading))
	buf = binary.AppendUvarint(buf, uint64(e.MoveFrom.X))
	buf = binary.AppendUvarint(buf, uint64(e.MoveFrom.Y))
	buf = binary.AppendUvarint(buf, uint64(e.MoveFrom.Z))
	buf = binary.AppendUvarint(buf, uint64(e.MoveDist))
	buf = binary.AppendUvarint(buf, uint64(e.MoveDone))
	if e.Moving {
		buf = append(buf, 1)
	} else {
		buf = append(buf, 0)
	}
	if e.Dead {
		buf = append(buf, 1)
	} else {
		buf = append(buf, 0)
	}
	buf = binary.AppendUvarint(buf, uint64(e.HP))
	buf = binary.AppendUvarint(buf, uint64(e.Beat))
	for i := range e.Servants {
		if e.Servants[i].Alive {
			buf = append(buf, 1)
		} else {
			buf = append(buf, 0)
		}
		buf = binary.AppendUvarint(buf, uint64(e.Servants[i].Pos.X))
		buf = binary.AppendUvarint(buf, uint64(e.Servants[i].Pos.Y))
		buf = binary.AppendUvarint(buf, uint64(e.Servants[i].Pos.Z))
	}
	buf = binary.AppendUvarint(buf, uint64(len(e.Transfers)))
	for i := range e.Transfers {
		tr := &e.Transfers[i]
		buf = binary.AppendUvarint(buf, tr.ID)
		buf = append(buf, tr.Phase)
		buf = binary.AppendUvarint(buf, uint64(len(tr.Payload)))
		buf = append(buf, tr.Payload...)
		buf = binary.AppendUvarint(buf, uint64(len(tr.Precondition)))
		buf = append(buf, tr.Precondition...)
	}
	if e.Player == nil {
		buf = append(buf, 0)
		return buf
	}
	buf = append(buf, 1)
	buf = appendPlayer(buf, e.Player)
	return buf
}

// appendPlayer — сериализация игрока: int64-поля записи — varint (uvarint
// отрицательного разваливается), строки — длиной + байтами.
func appendPlayer(buf []byte, p *Player) []byte {
	r := &p.Rec
	buf = appendStr(buf, r.Account)
	buf = appendStr(buf, r.Name)
	buf = binary.AppendUvarint(buf, uint64(r.Slot))
	buf = binary.AppendUvarint(buf, uint64(r.ClassID))
	buf = binary.AppendUvarint(buf, uint64(r.Race))
	buf = binary.AppendUvarint(buf, uint64(r.Sex))
	buf = binary.AppendUvarint(buf, uint64(r.HairStyle))
	buf = binary.AppendUvarint(buf, uint64(r.HairColor))
	buf = binary.AppendUvarint(buf, uint64(r.Face))
	buf = binary.AppendVarint(buf, int64(r.X))
	buf = binary.AppendVarint(buf, int64(r.Y))
	buf = binary.AppendVarint(buf, int64(r.Z))
	buf = binary.AppendVarint(buf, int64(r.Heading))
	buf = binary.AppendUvarint(buf, uint64(r.Level))
	buf = binary.AppendVarint(buf, r.Exp)
	buf = binary.AppendUvarint(buf, uint64(r.HP))
	buf = binary.AppendUvarint(buf, uint64(r.MP))
	buf = binary.AppendVarint(buf, r.CreatedUnix)
	buf = binary.AppendVarint(buf, r.LastSeenUnix)
	buf = binary.AppendUvarint(buf, p.ConnID)
	buf = binary.AppendVarint(buf, p.SpeedBudget) // бакет бывает отрицательным (ниже −SLACK)
	if p.SpeedFlagged {
		buf = append(buf, 1)
	} else {
		buf = append(buf, 0)
	}
	if p.PendingTeleport {
		buf = append(buf, 1)
	} else {
		buf = append(buf, 0)
	}
	if p.EnterLeaving {
		buf = append(buf, 1)
	} else {
		buf = append(buf, 0)
	}
	return buf
}

func appendStr(buf []byte, s string) []byte {
	buf = binary.AppendUvarint(buf, uint64(len(s)))
	return append(buf, s...)
}
