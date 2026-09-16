// Персист-актор game-процесса: единственная горутина-писатель каталога
// персонажей. Запросы и ответы — письма через транспорт; пробуждение —
// notify-токен ящика плюс дверной звонок метронома (тик-фолбэк против
// остаточного окна «письмо без токена»).
package persist

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/udisondev/l2go/internal/transport"
)

// Config — конфигурация актора; валидируется в New, не паникует.
type Config struct {
	// Dir — корень персиста (подкаталог chars рождается здесь).
	Dir string
	// DrainTimeout — бюджет финального дрена при выходе по ctx (T3).
	DrainTimeout time.Duration
	// PanicLimit — серия паник обработки подряд до let-it-crash.
	PanicLimit int
	// Senders — whitelist FromID отправителей запросов (шлюз, регион).
	// Доливается AllowSender до старта Run: актор рождается раньше шлюза и
	// региона, их адреса при New неизвестны. Run с пустым списком запрещён.
	Senders []transport.EntityID
}

func (c Config) validate() error {
	if c.Dir == "" {
		return fmt.Errorf("persist: каталог персиста пуст")
	}

	if c.DrainTimeout <= 0 {
		return fmt.Errorf("persist: DrainTimeout <= 0")
	}
	if c.PanicLimit <= 0 {
		return fmt.Errorf("persist: PanicLimit <= 0")
	}
	return nil
}

// Actor — персист-актор персонажей.
type Actor struct {
	cfg      Config
	reg      *transport.Registry
	box      transport.Mailbox
	id       transport.EntityID
	token    uint64
	doorbell <-chan struct{}
	chars    *charStore
	batch    []transport.Envelope
	done     chan struct{}

	panicSeries int
	testPanicOp string
	now         func() time.Time // шов времени финального дрена; дефолт — time.Now

	handled   atomic.Uint64
	replies   atomic.Uint64
	writeErrs atomic.Uint64
	panics    atomic.Uint64
	started   atomic.Bool
}

// New валидирует конфиг, подготавливает корень персиста, открывает каталог
// персонажей (стартовый скан строит индекс имён) и регистрирует ящик актора
// в реестре. Doorbell — подписка на дверной звонок метронома (инъекция из
// cmd): тик-фолбэк обязателен.
func New(cfg Config, reg *transport.Registry, doorbell <-chan struct{}) (*Actor, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if reg == nil {
		return nil, fmt.Errorf("persist: реестр транспорта nil")
	}
	if doorbell == nil {
		return nil, fmt.Errorf("persist: дверной звонок nil — тик-фолбэк обязателен")
	}
	if err := ensureRoot(cfg.Dir); err != nil {
		return nil, err
	}
	chars, err := openCharStore(charsDir(cfg.Dir))
	if err != nil {
		return nil, err
	}
	a := &Actor{
		cfg:      cfg,
		reg:      reg,
		doorbell: doorbell,
		chars:    chars,
		done:     make(chan struct{}),
		now:      time.Now,
	}
	a.id = reg.Register(&a.box)
	a.token = uint64(a.id)
	return a, nil
}

// ID возвращает адрес актора в реестре (адресат запросов).
func (a *Actor) ID() transport.EntityID { return a.id }

// Done закрывается после финального дрена при выходе по ctx: wire-up ждёт
// его перед завершением процесса.
func (a *Actor) Done() <-chan struct{} { return a.done }

// ActorStats — снимок метрик актора.
type ActorStats struct {
	Handled   uint64
	Replies   uint64
	WriteErrs uint64
	Panics    uint64
	Depth     int64
}

// Stats возвращает снимок метрик.
func (a *Actor) Stats() ActorStats {
	return ActorStats{
		Handled:   a.handled.Load(),
		Replies:   a.replies.Load(),
		WriteErrs: a.writeErrs.Load(),
		Panics:    a.panics.Load(),
		Depth:     a.box.Depth(),
	}
}

// Run — горутина актора. Выход по ctx: финальный дрен до пустоты в бюджет
// DrainTimeout, остаток — классовый дроп с инцидент-алертом, затем Done.
func (a *Actor) Run(ctx context.Context) {
	defer close(a.done)
	a.started.Store(true)
	if len(a.cfg.Senders) == 0 {
		slog.Error("persist: Run с пустым whitelist отправителей — актор не работает")
		return
	}
	if err := a.box.Claim(a.token); err != nil {
		// Свежий ящик: претензия не может быть занята; отказ — контрактная
		// ошибка, актор не работает, процесс узнает по тишине ответов.
		slog.Error("persist: претензия читателя не удалась", "err", err)
		return
	}
	for {
		// Отмена предпочитается токену: по ctx.Done — финальный дрен, без
		// гонки с pendинг-токеном пробуждения.
		select {
		case <-ctx.Done():
			a.shutdownDrain()
			return
		default:
		}
		select {
		case <-ctx.Done():
			a.shutdownDrain()
			return
		case <-a.box.Notify():
			a.drain()
		case <-a.doorbell:
			a.drain()
		}
	}
}

// drain — протокол читателя P3.1: изъятие пачки, AckNotify-перечит, обработка.
func (a *Actor) drain() {
	a.batch = a.box.ExtractInto(a.token, a.batch)
	a.box.AckNotify()
	if len(a.batch) == 0 {
		return
	}
	a.processBatch(a.batch)
	a.batch = a.batch[:0]
}

// shutdownDrain — финальный дрен: надёжный класс обязан быть обработан
// (T3); по исчерпании таймаута остаток классово дропается с алертом — без
// «ещё одной пачки за счёт бюджета».
func (a *Actor) shutdownDrain() {
	deadline := a.now().Add(a.cfg.DrainTimeout)
	for {
		a.batch = a.box.ExtractInto(a.token, a.batch)
		if len(a.batch) == 0 {
			return
		}
		if a.now().After(deadline) {
			a.box.DropBatch(a.batch)
			slog.Error("persist: таймаут финального дрена — остаток классово дропнут (инцидент)",
				"letters", len(a.batch))
			a.batch = a.batch[:0]
			return
		}
		a.box.AckNotify()
		a.processBatch(a.batch)
		a.batch = a.batch[:0]
	}
}

// processBatch обрабатывает пачку. Паника письма: recover, неприменённый
// остаток пачки — классовый дроп с алертом; серия PanicLimit подряд —
// паника наружу (let-it-crash, надзор — рестарт процесса).
func (a *Actor) processBatch(envs []transport.Envelope) {
	for i := range envs {
		if a.processLetter(&envs[i]) {
			continue
		}
		rest := envs[i:]
		a.box.DropBatch(rest)
		slog.Error("persist: остаток пачки после паники классово дропнут (инцидент)",
			"letters", len(rest))
		return
	}
	a.panicSeries = 0
}

// processLetter обрабатывает одно письмо; false — паника поймана.
func (a *Actor) processLetter(env *transport.Envelope) (ok bool) {
	a.handled.Add(1)
	defer func() {
		if r := recover(); r != nil {
			ok = false
			a.panics.Add(1)
			a.panicSeries++
			slog.Error("persist: паника обработки письма", "from", env.FromID, "panic", r)
			if a.panicSeries >= a.cfg.PanicLimit {
				panic(r) // серия систематического бага — процесс падает
			}
		}
	}()
	// Сервисный адресат диспетчеризует по Kind: письмо чужого типа — ошибка
	// отправителя, ответа нет.
	if env.Kind != transport.KindPersistRequest {
		slog.Error("persist: письмо чужого Kind отброшено", "from", env.FromID, "kind", env.Kind)
		return true
	}
	if !a.senderAllowed(env.FromID) {
		slog.Error("persist: отправитель вне whitelist отброшен", "from", env.FromID)
		return true
	}
	req, err := DecodeRequest(env.Payload)
	if err != nil {
		// Синтаксический мусор: журнал, ответа нет (таймаут отправителя
		// разрулит); паника запрещена недоверенным входом.
		slog.Error("persist: неразобранный запрос отброшен", "from", env.FromID, "err", err)
		return true
	}
	if a.testPanicOp != "" && req.Op == a.testPanicOp {
		panic("persist: тестовая паника обработки")
	}
	reply := a.handle(req)
	payload, err := EncodeReply(reply)
	if err != nil {
		slog.Error("persist: ответ не закодирован", "op", req.Op, "err", err)
		return true
	}
	a.reg.Send(transport.Envelope{
		To:      transport.Addr{Entity: env.FromID},
		FromID:  a.id,
		Kind:    transport.KindPersistReply,
		Payload: payload,
	})
	a.replies.Add(1)
	return true
}

// AllowSender дополняет whitelist отправителей; вызов после старта Run —
// ошибка (список читает горутина актора). Wire-up зовёт после создания
// шлюза и региона.
func (a *Actor) AllowSender(ids ...transport.EntityID) error {
	if a.started.Load() {
		return fmt.Errorf("persist: AllowSender после старта Run")
	}
	// copy-on-write: горутина актора читает слайс без лока.
	next := make([]transport.EntityID, len(a.cfg.Senders), len(a.cfg.Senders)+len(ids))
	copy(next, a.cfg.Senders)
	a.cfg.Senders = append(next, ids...)
	return nil
}

// senderAllowed — отправитель запроса в whitelist конфига.
func (a *Actor) senderAllowed(from transport.EntityID) bool {
	for _, id := range a.cfg.Senders {
		if id == from {
			return true
		}
	}
	return false
}

// fail — конструктор отказа: эхо операции и корреляции, код + причина.
func (r Request) fail(code string, err error) Reply {
	return Reply{Op: r.Op, Corr: r.Corr, Code: code, Err: err.Error()}
}

// failMsg — fail со строковой причиной (домены канона без error-объекта).
func (r Request) failMsg(code, msg string) Reply {
	return Reply{Op: r.Op, Corr: r.Corr, Code: code, Err: msg}
}

// handle исполняет запрос; ошибки — ответом ok=false (retry решает
// отправитель), не паникой и не блокировкой мира.
func (a *Actor) handle(req Request) Reply {
	switch req.Op {
	case OpCharList:
		return a.handleCharList(req)
	case OpCreateChar:
		return a.handleCreateChar(req)
	case OpSaveChar:
		return a.handleSaveChar(req)
	default:
		return req.failMsg("", "неизвестная операция")
	}
}

func (a *Actor) handleCharList(req Request) Reply {
	account, err := NormalizeLogin(req.Account)
	if err != nil {
		return req.fail(CodeLogin, err)
	}
	recs, err := a.chars.list(account)
	if err != nil {
		return req.fail(CodeIO, err)
	}
	return Reply{Op: req.Op, Corr: req.Corr, OK: true, Chars: recs}
}

func (a *Actor) handleCreateChar(req Request) Reply {
	account, err := NormalizeLogin(req.Account)
	if err != nil {
		return req.fail(CodeLogin, err)
	}
	if !ValidName(req.Name) {
		return req.failMsg(CodeNameInvalid, "имя вне домена (1–16 alnum)")
	}
	if err := ValidateAppearance(req.Sex, req.HairStyle, req.HairColor, req.Face); err != nil {
		return req.fail(CodeAppearance, err)
	}
	recs, err := a.chars.list(account)
	if err != nil {
		return req.fail(CodeIO, err)
	}
	if len(recs) > maxSlot {
		return req.failMsg(CodeCharLimit, "лимит персонажей аккаунта")
	}
	if _, taken := a.chars.nameOwner(req.Name); taken {
		return req.failMsg(CodeNameTaken, "имя занято")
	}
	slot := firstFreeSlot(recs)
	now := time.Now().Unix()
	rec := CharRecord{
		Account:      account,
		Slot:         slot,
		Name:         req.Name,
		ClassID:      HumanFighter.ClassID,
		Race:         HumanFighter.Race,
		Sex:          req.Sex,
		HairStyle:    req.HairStyle,
		HairColor:    req.HairColor,
		Face:         req.Face,
		X:            HumanFighter.StartX,
		Y:            HumanFighter.StartY,
		Z:            HumanFighter.StartZ,
		Level:        1,
		HP:           HumanFighter.BaseHP,
		MP:           HumanFighter.BaseMP,
		CreatedUnix:  now,
		LastSeenUnix: now,
	}
	next := append([]CharRecord(nil), recs...)
	next = append(next, rec)
	if err := a.chars.saveFile(account, next); err != nil {
		a.writeFail(account, err)
		return req.fail(CodeIO, err)
	}
	// Индекс занимается только после успешного rename: retry после ошибки
	// записи идемпотентен.
	a.chars.resyncNames(account, next)
	return Reply{Op: req.Op, Corr: req.Corr, OK: true, Record: &rec}
}

func (a *Actor) handleSaveChar(req Request) Reply {
	account, err := NormalizeLogin(req.Account)
	if err != nil {
		return req.fail(CodeLogin, err)
	}
	ch := req.Char
	if err := validateCharRecord(ch); err != nil {
		return req.fail(CodeNameInvalid, err)
	}
	old, err := a.chars.list(account)
	if err != nil {
		return req.fail(CodeIO, err)
	}
	// upsert по слоту: заменяется запись слота, остальные (офлайн-персонажи
	// аккаунта) сохраняются как есть; CreatedUnix наследуется от записи слота;
	// подмена имени (слот живёт у другого персонажа) — аномалия отправителя.
	now := time.Now().Unix()
	ch.Account = account
	ch.LastSeenUnix = now
	ch.CreatedUnix = now
	recs := make([]CharRecord, 0, len(old)+1)
	for _, r := range old {
		if r.Slot == ch.Slot {
			if lowercaseASCII(r.Name) != lowercaseASCII(ch.Name) {
				return req.failMsg("", "имя слота принадлежит другому персонажу")
			}
			ch.CreatedUnix = r.CreatedUnix
			continue
		}
		recs = append(recs, r)
	}
	recs = append(recs, ch)
	// имена внутри аккаунта уникальны; новый взял имя соседнего слота — отказ
	for i := 1; i < len(recs); i++ {
		for j := 0; j < i; j++ {
			if lowercaseASCII(recs[j].Name) == lowercaseASCII(recs[i].Name) {
				return req.failMsg("", "дубликат имени в аккаунте")
			}
		}
	}
	// имена — против других аккаунтов (отправитель не доверяется)
	for _, r := range recs {
		if owner, ok := a.chars.nameOwner(r.Name); ok && owner != account {
			return req.failMsg("", "имя принадлежит другому аккаунту")
		}
	}
	if err := a.chars.saveFile(account, recs); err != nil {
		a.writeFail(account, err)
		return req.fail(CodeIO, err)
	}
	a.chars.resyncNames(account, recs)
	return Reply{Op: req.Op, Corr: req.Corr, OK: true}
}

// writeFail — ошибка записи: метрика + журнал (оператору), отправитель
// получает ok=false.
func (a *Actor) writeFail(account string, err error) {
	a.writeErrs.Add(1)
	slog.Error("persist: запись персонажей не удалась", "account", account, "err", err)
}

// firstFreeSlot возвращает первый свободный слот 0–maxSlot (канон: слот
// назначает сервер). Guard лимита в handleCreateChar гарантирует свободный
// слот; занятость всех — нарушение контракта вызывающего.
func firstFreeSlot(recs []CharRecord) int {
	taken := [maxSlot + 1]bool{}
	for _, r := range recs {
		if r.Slot >= 0 && r.Slot <= maxSlot {
			taken[r.Slot] = true
		}
	}
	for slot, busy := range taken {
		if !busy {
			return slot
		}
	}
	panic("persist: свободных слотов нет — нарушен guard лимита персонажей")
}

// charsDir — подкаталог персонажей корня персиста.
func charsDir(root string) string { return filepath.Join(root, "chars") }
