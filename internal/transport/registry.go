package transport

import (
	"log/slog"
	"sync"
	"sync/atomic"
)

const (
	// defaultFAFCap — кап fire-and-forget писем на ящик по умолчанию
	// (злой клиент не раздувает ящик; reliable/transfer живому не дропаются).
	defaultFAFCap = 1024

	// shardCount — число шардов карты адресов (степень двойки). Запись редка
	// (рождения/деспавны), чтение — без блокировок отправителя дольше, чем
	// лок сегмента: RLock/RUnlock.
	shardCount = 64
)

// Registry — карта вечных адресов id→ящик. Рождение адресата (сущность или
// сервисный: шлюз, persist-актор, регион) — Register; ID монотонные из одного
// счётчика, не переиспользуются, ноль зарезервирован. Смерть — Retire: свап
// записи на синглтон «мёртв» этого реестра (поздние отправители получают
// классовый дроп с метрикой, запись карты не удаляется). Отправка — Send,
// слепой push: lookup и enqueue смежные операции.
type Registry struct {
	fafCap int
	lastID atomic.Uint64
	shards [shardCount]shard
	miss   atomic.Uint64
	retire atomic.Uint64

	deadBox Mailbox // синглтон «мёртв»: адресат всех retired id
}

type shard struct {
	mu sync.RWMutex
	m  map[EntityID]*Mailbox
}

// DeadDrops — агрегат финальных классовых дропов синглтона «мёртв» (посылки
// в retired id): наблюдаемость страгглеров без per-entity атрибуции.
func (r *Registry) DeadDrops() (faf, reliable int64) {
	st := r.deadBox.Stats()
	return st.FinalFireAndForget, st.FinalReliable
}

// NewRegistry создаёт реестр с капом FAF на ящик (<=0 — дефолт).
func NewRegistry(fafCap int) *Registry {
	r := &Registry{fafCap: fafCap}
	if fafCap <= 0 {
		r.fafCap = defaultFAFCap
	}
	r.deadBox.init(r.fafCap)
	r.deadBox.mu.Lock()
	r.deadBox.dead = true
	r.deadBox.mu.Unlock()
	return r
}

// Register рождает адресата: инициализирует ящик и записывает в карту под
// свежим монотонным ID.
func (r *Registry) Register(box *Mailbox) EntityID {
	id := EntityID(r.lastID.Add(1)) // счётчик с 1: ноль зарезервирован
	box.init(r.fafCap)
	box.regID.Store(uint64(id))
	sh := &r.shards[uint64(id)&(shardCount-1)]
	sh.mu.Lock()
	if sh.m == nil {
		sh.m = make(map[EntityID]*Mailbox)
	}
	sh.m[id] = box
	sh.mu.Unlock()
	return id
}

// Send — слепой push: lookup адресата и enqueue. Miss по неизвестному id —
// не тихо: метрика и жёсткий журнал. Retired id адресует синглтон «мёртв» —
// классовый дроп с метрикой.
func (r *Registry) Send(env Envelope) {
	sh := &r.shards[uint64(env.To.Entity)&(shardCount-1)]
	sh.mu.RLock()
	box := sh.m[env.To.Entity]
	sh.mu.RUnlock()
	if box == nil {
		r.miss.Add(1)
		slog.Error("transport: письмо в неизвестный адрес (lookup-miss)",
			"to", env.To.Entity, "kind", env.Kind, "from", env.FromID)
		return
	}
	box.enqueue(env)
}

// Retire свапает запись карты на синглтон «мёртв» (бюджет компакции — метрика
// retire). Вызывается после Despawn ящика и до освобождения записи сущности.
func (r *Registry) Retire(id EntityID) {
	sh := &r.shards[uint64(id)&(shardCount-1)]
	sh.mu.Lock()
	sh.m[id] = &r.deadBox
	sh.mu.Unlock()
	r.retire.Add(1)
}

// RegistryStats — метрики реестра: рождения, свапы, мисс-отправки.
type RegistryStats struct {
	Births  uint64
	Retires uint64
	Misses  uint64
}

// Stats — снимок метрик реестра.
func (r *Registry) Stats() RegistryStats {
	return RegistryStats{
		Births:  r.lastID.Load(),
		Retires: r.retire.Load(),
		Misses:  r.miss.Load(),
	}
}
