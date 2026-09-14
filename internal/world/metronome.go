package world

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Config — конфигурация каркаса мира: метроном, регион, лог порций. Дефолты —
// стартовая калибровка; тюнинг — фаза 4 по измерениям.
type Config struct {
	Hz              int  // 10 — канон мира (r3 §6): выше растёт плата за тик без выигрыша в наблюдаемости
	CtrlBudget      int  // 16: контрольные — редкие события владения; K упорядочивает (приоритет), не ограничивает
	DrainBudget     int  // 4096 писем/шаг: 40k/с на регион при 10 Гц — порядок перегрузки
	PhaseBCap       int  // 128: исходящие шага; пересмотр с первым производителем конвертов
	WatchdogTicks   int  // 30 тиков (3 с при 10 Гц): короче — шум на разовых пиках
	HeartbeatTicks  int  // 10 тиков (секунда): окно потерянного пробуждения заметно человеком лишь выше
	FreezePanics    int  // 3 подряд: короткая серия — уже сигнал систематического бага
	LogPayloads     bool // payload-байты в логе порций: заголовки — всегда
	LogMaxFileBytes int64
}

// DefaultConfig — дефолты с аргументацией в комментариях полей.
func DefaultConfig() Config {
	return Config{
		Hz:              10,
		CtrlBudget:      16,
		DrainBudget:     4096,
		PhaseBCap:       128,
		WatchdogTicks:   30,
		HeartbeatTicks:  10,
		FreezePanics:    3,
		LogPayloads:     false,
		LogMaxFileBytes: defaultMaxFileBytes,
	}
}

func (c Config) validate() error {
	for _, v := range []struct {
		name string
		n    int
	}{
		{"Hz", c.Hz},
		{"CtrlBudget", c.CtrlBudget},
		{"DrainBudget", c.DrainBudget},
		{"PhaseBCap", c.PhaseBCap},
		{"WatchdogTicks", c.WatchdogTicks},
		{"HeartbeatTicks", c.HeartbeatTicks},
		{"FreezePanics", c.FreezePanics},
	} {
		if v.n <= 0 {
			return fmt.Errorf("world: %s = %d; want > 0", v.name, v.n)
		}
	}
	if c.LogMaxFileBytes < 0 {
		return fmt.Errorf("world: LogMaxFileBytes = %d; want ≥ 0", c.LogMaxFileBytes)
	}
	return nil
}

// Period — период метронома из Hz. Деление через float64: целочисленное
// теряет точность на некратных Hz (Hz=15 давало 66 мс вместо 66.67).
func (c Config) Period() time.Duration {
	return time.Duration(float64(time.Second) / float64(c.Hz))
}

// Metronome — диспетчер тиков без барьера (ADR-0002): одна горутина, тикер
// периода Period(), активный сет, дверной звонок в cap-1 канал каждого
// активного региона (провал — дроп + per-region метрика, очередь не копится).
// Глобальный номер тика — атомарный счётчик: регионы берут номер шага сами
// (Now()), буферизованный номер не может устареть. Второй сет — все регионы:
// heartbeat-фолбэк пробуждения каждые HeartbeatTicks тиков (страховка окна
// «письмо в очереди без токена» для спящих). Сервисные подписчики (шлюз) —
// тот же звонок, без второй ветки pacing'а.
type Metronome struct {
	cfg    Config
	period time.Duration

	tick atomic.Uint64

	mu        sync.Mutex // записи сетов и подписок (редкие)
	activePtr atomic.Pointer[[]*Region]
	allPtr    atomic.Pointer[[]*Region]
	subsPtr   atomic.Pointer[[]chan struct{}]

	alerts atomic.Uint64 // счётчик алертов вотчдога
}

// NewMetronome создаёт метроном; конфигурация валидируется (недоверенный вход
// — флаги/env — ошибкой, не паникой).
func NewMetronome(cfg Config) (*Metronome, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	m := &Metronome{cfg: cfg, period: cfg.Period()}
	var emptyRegions []*Region
	var emptySubs []chan struct{}
	m.activePtr.Store(&emptyRegions)
	m.allPtr.Store(&emptyRegions)
	m.subsPtr.Store(&emptySubs)
	return m, nil
}

// Now — текущий глобальный номер тика (монотонен).
func (m *Metronome) Now() Tick { return Tick(m.tick.Load()) }

// Alerts — счётчик алертов вотчдога (один на эпизод лага).
func (m *Metronome) Alerts() uint64 { return m.alerts.Load() }

// Run — горутина-диспетчер: выход по ctx, тикер останавливается.
func (m *Metronome) Run(ctx context.Context) {
	ticker := time.NewTicker(m.period)
	defer ticker.Stop()
	episodes := make(map[*Region]bool) // состояние эпизодов лага — собственность этой горутины
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n := m.tick.Add(1)
			active := *m.activePtr.Load()
			for _, r := range active {
				select {
				case r.ringCh <- struct{}{}:
				default:
					r.dropped.Add(1) // коалесинг: звонок дропнут, регион в прошлом шаге
				}
			}
			if int(n)%m.cfg.HeartbeatTicks == 0 {
				for _, r := range *m.allPtr.Load() {
					select {
					case r.fbCh <- struct{}{}:
					default:
					}
				}
			}
			for _, ch := range *m.subsPtr.Load() {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
			m.watchdog(Tick(n), active, episodes)
		}
	}
}

// watchdog — наблюдатель, не исполнитель: лаг активного региона свыше
// WatchdogTicks тиков — slog-алерт одноразово на эпизод; никто не блокируется.
// Эпизоды деактивированных регионов сбрасываются (реактивация в лаге алертит
// заново, «залипших» эпизодов нет).
func (m *Metronome) watchdog(n Tick, active []*Region, episodes map[*Region]bool) {
	// 0 аллокаций на такт: эпизоды — единственный map (живёт весь Run),
	// принадлежность активным — линейный поиск (регионов десятки)
	for _, r := range active {
		lag := uint64(n) - r.doneTick.Load()
		if lag > uint64(m.cfg.WatchdogTicks) {
			if !episodes[r] {
				episodes[r] = true
				m.alerts.Add(1)
				slog.Error("world: регион отстаёт от метронома свыше порога вотчдога",
					"region", r.id, "tick", uint64(n), "doneTick", r.doneTick.Load(), "lag", lag)
			}
		} else {
			episodes[r] = false
		}
	}
	for r := range episodes {
		if !containsRegion(active, r) {
			delete(episodes, r) // деактивация сбрасывает эпизод: реактивация алертит заново
		}
	}
}

func containsRegion(rs []*Region, r *Region) bool {
	for _, x := range rs {
		if x == r {
			return true
		}
	}
	return false
}

// Activate включает регион в активный сет (no-op при повторной активации).
// Вызывает горутина региона.
func (m *Metronome) Activate(r *Region) {
	m.addSet(&m.activePtr, r)
}

// Deactivate выводит регион из активного сета (сброс эпизода лага — в
// вотчдоге по отсутствию в сете). Вызывает горутина региона.
func (m *Metronome) Deactivate(r *Region) {
	m.removeSet(&m.activePtr, r)
}

func (m *Metronome) register(r *Region) {
	m.addSet(&m.allPtr, r)
}

// unregister выводит регион из все-сета (фолбэк больше не звонит мёртвому).
func (m *Metronome) unregister(r *Region) {
	m.removeSet(&m.allPtr, r)
}

func (m *Metronome) addSet(ptr *atomic.Pointer[[]*Region], r *Region) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := *ptr.Load()
	for _, x := range cur {
		if x == r {
			return
		}
	}
	next := make([]*Region, len(cur), len(cur)+1)
	copy(next, cur)
	next = append(next, r)
	ptr.Store(&next)
}

func (m *Metronome) removeSet(ptr *atomic.Pointer[[]*Region], r *Region) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := *ptr.Load()
	idx := -1
	for i, x := range cur {
		if x == r {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	next := make([]*Region, 0, len(cur)-1)
	next = append(next, cur[:idx]...)
	next = append(next, cur[idx+1:]...)
	ptr.Store(&next)
}

// Subscribe — подписка сервисного адресата (шлюз) на дверной звонок: тот же
// pacing, что у регионов. Возврат — канал (cap-1, non-blocking send) и функция
// отписки.
func (m *Metronome) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	m.mu.Lock()
	cur := *m.subsPtr.Load()
	next := make([]chan struct{}, len(cur), len(cur)+1)
	copy(next, cur)
	next = append(next, ch)
	m.subsPtr.Store(&next)
	m.mu.Unlock()
	unsub := func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		cur := *m.subsPtr.Load()
		next := make([]chan struct{}, 0, len(cur))
		for _, x := range cur {
			if x != ch {
				next = append(next, x)
			}
		}
		m.subsPtr.Store(&next)
	}
	return ch, unsub
}
