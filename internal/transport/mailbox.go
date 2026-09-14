package transport

import (
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
)

//segCap — ёмкость сегмента очереди писем.

const segCap = 64

// Ошибки претензии на читательство.
var (
	// ErrBusy — у ящика уже есть читатель: вытеснения не существует.
	ErrBusy = errors.New("transport: у ящика уже есть читателя")
	// ErrZeroToken — нулевой токен не идентифицирует читателя.
	ErrZeroToken = errors.New("transport: нулевой токен читателя")
)

// segment — звено очереди: массив конвертов и атомарный счётчик опубликованных
// писем. Продюсеры пишут envs/cnt под мьютексом ящика, публикуя cnt атомарно;
// читатель проходит цепочку next без блокировок — видимость envs гарантирует
// пару «store cnt → load cnt».
type segment struct {
	envs     [segCap]Envelope
	cnt      atomic.Int32
	firstSeq uint64
	next     atomic.Pointer[segment]
}

// Mailbox — личная очередь адресата: MPSC FIFO этапа-1 (мьютекс внутри enqueue,
// читателя и отправителей на чтение не блокирующий) с водяным знаком. Заголовок
// встраивается в запись сущности у владельца (кеш-локальность тика): поля
// отправителей и поля читателя разведены паддингом по разным кеш-линиям.
// Первый сегмент очереди ленивый: рождается с первым письмом — пустые ящики
// не платят за сегмент (бюджет памяти заголовков ADR-0003 §11). Готовность
// к отправке даёт Register: он инициализирует ящик.
type Mailbox struct {
	// Поля отправителей: мьютекс, хвост публикации, деспавн-флаг (под mu).
	mu      sync.Mutex
	tail    *segment
	dead    bool
	fafCap  int
	lastSeq uint64 // под mu: seq последнего опубликованного письма

	// Счётчики и токен пробуждения — атомики (читаются без mu).
	notify    chan struct{}           // cap-1; создаётся при init
	regID     atomic.Uint64           // EntityID из карты (заполняет Register)
	start     atomic.Pointer[segment] // первый сегмент (публикация читателю)
	length    atomic.Int64
	fafDepth  atomic.Int64
	highWater atomic.Int64
	drops     boxDrops

	_ [64]byte // разделение кеш-линий: ниже — только поля читателя

	mark    atomic.Uint64 // водяной знак: seq последнего изъятого письма
	owner   atomic.Uint64 // токен подтверждённого читателя; 0 — читателя нет
	head    *segment      // позиция сбора (только читатель; nil до первого письма)
	headOff int           // смещение внутри head (только читатель)
}

// boxDrops — счётчики дропов по классам (метрики единой декларации очереди).
type boxDrops struct {
	droppedFAF    atomic.Int64 // дропы новых FAF по капу
	finalFAF      atomic.Int64 // финальные дропы FAF (мёртвому)
	finalReliable atomic.Int64 // финальные дропы reliable (инцидент)
	finalTransfer atomic.Int64 // финальные дропы transfer (abort)
}

// BoxStats — снимок метрик ящика (глубина, максимум, дропы по классам).
type BoxStats struct {
	Depth              int64
	HighWater          int64
	DroppedFAF         int64
	FinalFireAndForget int64
	FinalReliable      int64
	FinalTransfer      int64
	Dead               bool
}

// init инициализирует ящик (идемпотентно; вызывает Register): кап и токен
// пробуждения. Сегменты — лениво, с первым письмом.
func (m *Mailbox) init(fafCap int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.notify != nil {
		return
	}
	m.fafCap = fafCap
	m.notify = make(chan struct{}, 1)
}

// enqueue — слепой push одного письма. Передаёт владение payload: отправитель
// не сохраняет и не переиспользует байты после отправки. Классовая политика:
// FAF дропается по капу (дроп нового, O(1)); reliable/transfer живому копятся;
// мёртвому — финальный классовый дроп с метрикой.
func (m *Mailbox) enqueue(env Envelope) {
	class := env.Kind.Class()
	m.mu.Lock()
	if m.notify == nil {
		m.fafCap = defaultFAFCap
		m.notify = make(chan struct{}, 1)
	}
	if m.dead {
		m.mu.Unlock()
		m.dropFinal(env, class)
		return
	}
	if class == ClassFireAndForget && m.fafCap > 0 && m.fafDepth.Load() >= int64(m.fafCap) {
		m.mu.Unlock()
		m.drops.droppedFAF.Add(1)
		return
	}
	if m.tail == nil {
		ns := &segment{firstSeq: m.lastSeq + 1}
		ns.envs[0] = env
		ns.cnt.Store(1)
		m.start.Store(ns)
		m.tail = ns
	} else {
		seg := m.tail
		idx := int(seg.cnt.Load())
		if idx == segCap {
			ns := &segment{firstSeq: m.lastSeq + 1}
			ns.envs[0] = env
			ns.cnt.Store(1)
			seg.next.Store(ns)
			m.tail = ns
		} else {
			seg.envs[idx] = env
			seg.cnt.Store(int32(idx + 1))
		}
	}
	m.lastSeq++
	m.mu.Unlock()

	if class == ClassFireAndForget {
		m.fafDepth.Add(1)
	}
	n := m.length.Add(1)
	if n == 1 {
		m.wake() // переход пусто→непусто будит читателя
	}
	for {
		h := m.highWater.Load()
		if n <= h || m.highWater.CompareAndSwap(h, n) {
			break
		}
	}
}

// dropFinal — классовый дроп письма в мёртвый ящик. Надёжные классы —
// инцидент: slog-алерт (уведомление отправителя письмом — фаза 4).
func (m *Mailbox) dropFinal(env Envelope, class Class) {
	switch class {
	case ClassFireAndForget:
		m.drops.finalFAF.Add(1)
	case ClassReliable:
		m.drops.finalReliable.Add(1)
		slog.Error("transport: финальный дроп reliable в мёртвый ящик (инцидент)",
			"to", env.To.Entity, "kind", env.Kind, "from", env.FromID)
	case ClassTransfer:
		m.drops.finalTransfer.Add(1)
		slog.Error("transport: финальный дроп transfer в мёртвом ящике — abort",
			"to", env.To.Entity, "kind", env.Kind, "from", env.FromID)
	}
}

// DropBatch — классовый дроп изъятой, но неприменённой части пачки (деспавн
// посреди применения, недообработанный остаток). Пачка уже вне счётчиков
// очереди.
func (m *Mailbox) DropBatch(envs []Envelope) {
	var faf, rel, xfer int64
	for _, env := range envs {
		switch env.Kind.Class() {
		case ClassFireAndForget:
			faf++
		case ClassReliable:
			rel++
		case ClassTransfer:
			xfer++
		}
	}
	if faf > 0 {
		m.drops.finalFAF.Add(faf)
	}
	if rel > 0 {
		m.drops.finalReliable.Add(rel)
		slog.Error("transport: классовый дроп неприменённого остатка reliable (инцидент)",
			"count", rel)
	}
	if xfer > 0 {
		m.drops.finalTransfer.Add(xfer)
		slog.Error("transport: классовый дроп неприменённого остатка transfer — abort",
			"count", xfer)
	}
}

// wake — неблокирующий токен пробуждения (cap-1); spurious-токен безвреден.
func (m *Mailbox) wake() {
	if m.notify == nil {
		return
	}
	select {
	case m.notify <- struct{}{}:
	default:
	}
}

// Notify возвращает канал пробуждения читателя. Токен приходит на переходе
// пусто→непусто и при деспавне; сбрасывается AckNotify после дрена.
func (m *Mailbox) Notify() <-chan struct{} { return m.notify }

// AckNotify сбрасывает токен после дрена. Протокол читателя: select{Notify,
// тик} → Extract → применение → AckNotify → Extract (перечит после сброса —
// закрывает гонку письмо-в-окне-дрена) → сон.
func (m *Mailbox) AckNotify() {
	select {
	case <-m.notify:
	default:
	}
}

// Claim — претензия на читательство (CAS по owner). Возвращает ErrBusy, если
// читатель есть; нулевой токен — ошибка контракта.
func (m *Mailbox) Claim(token uint64) error {
	if token == 0 {
		return ErrZeroToken
	}
	for {
		cur := m.owner.Load()
		if cur != 0 {
			return ErrBusy
		}
		if m.owner.CompareAndSwap(0, token) {
			return nil // acquire: позиция предшественника видна
		}
	}
}

// Release освобождает читательство: ящик остаётся без читателя (окно переезда),
// письма копятся. Позиция потребления сохраняется для преемника.
func (m *Mailbox) Release(token uint64) {
	m.checkReader(token)
	m.owner.Store(0) // release: head/headOff видны преемнику через Claim
}

// checkReader — контракт читателя: операции изъятия только у текущего
// претендента. Нарушение — паника контракта, не гонка.
func (m *Mailbox) checkReader(token uint64) {
	if m.owner.Load() != token {
		panic("transport: операция читателя без претензии (контракт ящика)")
	}
}

// Extract изымает порцию до снапшота хвоста: один атомарный store водяного
// знака, применение — после. Возвращает слайс пачки (аллокация — только
// передача пачки). Повторное изъятие тех же писем невозможно.
func (m *Mailbox) Extract(token uint64) []Envelope {
	return m.extractInto(token, nil)
}

// ExtractInto — Extract с переиспользуемым буфером пачки (нулевые аллокации
// на опросе пустых ящиков).
func (m *Mailbox) ExtractInto(token uint64, buf []Envelope) []Envelope {
	return m.extractInto(token, buf[:0])
}

func (m *Mailbox) extractInto(token uint64, buf []Envelope) []Envelope {
	m.checkReader(token)
	seg := m.readerStart()
	if seg == nil {
		return buf // пусто с рождения
	}
	var lastSeq uint64
	var took, fafTook int64
	have := false
	for seg != nil {
		n := int(seg.cnt.Load())
		if m.headOff < n {
			for i := m.headOff; i < n; i++ {
				env := seg.envs[i]
				buf = append(buf, env)
				if env.Kind.Class() == ClassFireAndForget {
					fafTook++
				}
			}
			lastSeq = seg.firstSeq + uint64(n-1)
			took += int64(n - m.headOff)
			m.headOff = n
			have = true
		}
		nxt := seg.next.Load()
		if nxt == nil {
			break // снапшот хвоста достигнут
		}
		if m.headOff < segCap {
			// Снимок cnt устарел: следующее звено публикуется только у
			// заполненного сегмента — перечитать и дочитать тот же сегмент.
			continue
		}
		// сбор сегментов — строго ниже водяного знака
		m.head = nxt
		m.headOff = 0
		seg = nxt
	}
	if !have {
		return buf
	}
	m.mark.Store(lastSeq) // изъятие = один атомарный store знака
	m.length.Add(-took)
	if fafTook > 0 {
		m.fafDepth.Add(-fafTook)
	}
	return buf
}

// readerStart — стартовая позиция обхода читателя: с текущей головы или,
// до первого письма, с опубликованного первого сегмента. Вызывает только
// текущий читатель.
func (m *Mailbox) readerStart() *segment {
	if m.head != nil {
		return m.head
	}
	seg := m.start.Load()
	if seg != nil {
		m.head = seg
	}
	return seg
}

// Despawn убивает ящик: установка dead-флага и финальный дрен по классам —
// одна критическая секция мьютекса enqueue (окно «флаг поставлен, письмо
// влетело» закрыто). Вызывает только текущий читатель. Поздние страгглеры
// получают классовый дроп с метрикой. Свап карты адресов — Registry.Retire
// после Despawn (до освобождения записи сущности).
func (m *Mailbox) Despawn(token uint64) {
	m.checkReader(token)
	m.mu.Lock()
	m.dead = true
	var faf, rel, xfer int64
	var took int64
	for seg := m.readerStart(); seg != nil; {
		n := int(seg.cnt.Load())
		if m.headOff < n {
			for i := m.headOff; i < n; i++ {
				switch seg.envs[i].Kind.Class() {
				case ClassFireAndForget:
					faf++
				case ClassReliable:
					rel++
				case ClassTransfer:
					xfer++
				}
			}
			m.mark.Store(seg.firstSeq + uint64(n-1))
			took += int64(n - m.headOff)
			m.headOff = n
		}
		nxt := seg.next.Load()
		if nxt == nil {
			break
		}
		if m.headOff < segCap {
			continue // симметрично extractInto (под mu снимок не стареет — не срабатывает)
		}
		m.head = nxt
		m.headOff = 0
		seg = nxt
	}
	m.mu.Unlock()

	if faf > 0 {
		m.drops.finalFAF.Add(faf)
	}
	if rel > 0 {
		m.drops.finalReliable.Add(rel)
		slog.Error("transport: финальный дрен reliable при деспавне (инцидент)",
			"to", m.ID(), "count", rel)
	}
	if xfer > 0 {
		m.drops.finalTransfer.Add(xfer)
		slog.Error("transport: финальный дрен transfer при деспавне — abort",
			"to", m.ID(), "count", xfer)
	}
	if took > 0 {
		m.length.Store(0)
		m.fafDepth.Store(0)
	}
	m.wake() // деспавн будит наблюдающего читателя
}

// ID — адрес ящика в карте (заполняется Register; 0 до регистрации).
func (m *Mailbox) ID() EntityID { return EntityID(m.regID.Load()) }

// Depth — текущая глубина очереди (метрика декларации).
func (m *Mailbox) Depth() int64 { return m.length.Load() }

// Stats — снимок метрик ящика.
func (m *Mailbox) Stats() BoxStats {
	m.mu.Lock()
	dead := m.dead
	m.mu.Unlock()
	return BoxStats{
		Depth:              m.length.Load(),
		HighWater:          m.highWater.Load(),
		DroppedFAF:         m.drops.droppedFAF.Load(),
		FinalFireAndForget: m.drops.finalFAF.Load(),
		FinalReliable:      m.drops.finalReliable.Load(),
		FinalTransfer:      m.drops.finalTransfer.Load(),
		Dead:               dead,
	}
}
