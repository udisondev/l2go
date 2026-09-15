package world

import (
	"context"
	"testing"
	"time"
)

// Период из Hz через float64: целочисленное деление теряло точность (Hz=15
// давало 66 мс вместо 66.67).
func TestMetronomePeriodPrecision(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Hz = 15
	m, err := NewMetronome(cfg)
	if err != nil {
		t.Fatalf("NewMetronome: %v", err)
	}
	if got, want := m.period, time.Duration(66_666_666); got < want || got > want+1 {
		t.Errorf("период при Hz=15 = %v; want ~%v", got, want)
	}
}

func TestMetronomeConfigValidation(t *testing.T) {
	bad := []struct {
		name string
		cfg  Config
	}{
		{name: "Hz=0", cfg: Config{Hz: 0, HeartbeatTicks: 10, WatchdogTicks: 30}},
		{name: "HeartbeatTicks=0", cfg: Config{Hz: 10, HeartbeatTicks: 0, WatchdogTicks: 30}},
		{name: "WatchdogTicks=0", cfg: Config{Hz: 10, HeartbeatTicks: 10, WatchdogTicks: 0}},
		{name: "CtrlBudget=0", cfg: Config{Hz: 10, HeartbeatTicks: 10, WatchdogTicks: 30, CtrlBudget: 0}},
		{name: "DrainBudget=0", cfg: Config{Hz: 10, HeartbeatTicks: 10, WatchdogTicks: 30, DrainBudget: 0}},
		{name: "PhaseBCap=0", cfg: Config{Hz: 10, HeartbeatTicks: 10, WatchdogTicks: 30, PhaseBCap: 0}},
		{name: "FreezePanics=0", cfg: Config{Hz: 10, HeartbeatTicks: 10, WatchdogTicks: 30, FreezePanics: 0}},
		{name: "LogMaxFileBytes=-1", cfg: Config{Hz: 10, HeartbeatTicks: 10, WatchdogTicks: 30, LogMaxFileBytes: -1}},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewMetronome(c.cfg); err == nil {
				t.Errorf("конфиг (%+v) прошёл валидацию; want ошибка", c.cfg)
			}
		})
	}
}

// Сервисная подписка на дверной звонок: тот же pacing, без второй ветки.
func TestMetronomeSubscribe(t *testing.T) {
	m, err := NewMetronome(DefaultConfig())
	if err != nil {
		t.Fatalf("NewMetronome: %v", err)
	}
	ctx := t.Context()
	go m.Run(ctx)
	ch, unsub := m.Subscribe()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("подписчик не получил звонка")
	}
	unsub()
	// Свидетель: пока отписанный молчит, живой подписчик продолжает получать
	// звонки — метроном точно тикал ПОСЛЕ отписки (снимок подписчиков не
	// мог остаться со «вчерашним» каналом), и только тогда молчание
	// отписанного доказательно.
	witness, wunsub := m.Subscribe()
	select {
	case <-witness:
	case <-time.After(2 * time.Second):
		t.Fatal("свидетель не получил звонка — метроном не тикал")
	}
	wunsub()
	if len(ch) != 0 { // после доказанного тика звонков отписанному нет
		t.Fatalf("отписанный канал получает звонки: %d", len(ch))
	}
}

// Вотчдог: активный регион с застывшим doneTick даёт один алерт на эпизод,
// счётчик растёт; деактивация сбрасывает эпизод.
func TestMetronomeWatchdog(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Hz = 1000
	cfg.WatchdogTicks = 5
	m, err := NewMetronome(cfg)
	if err != nil {
		t.Fatalf("NewMetronome: %v", err)
	}
	ctx := t.Context()
	go m.Run(ctx)
	r := &Region{metro: m, ringCh: make(chan struct{}, 1), fbCh: make(chan struct{}, 1)}
	m.Activate(r)
	deadline := time.After(3 * time.Second)
	for m.Alerts() == 0 {
		select {
		case <-deadline:
			t.Fatalf("вотчдог не поднял алерт по лагающему региону")
		default:
			time.Sleep(5 * time.Millisecond)
			// регион стоит в сете с doneTick=0 — звонки дропаются в его каналы
			select {
			case <-r.ringCh:
			default:
			}
			select {
			case <-r.fbCh:
			default:
			}
		}
	}
	// Свидетель реактивации: если деактивация НЕ сбросила эпизод, повторная
	// активация того же лагающего региона алерта НЕ поднимет (эпизод жив,
	// повторный алерт на эпизод не строится) — а если сбросила, алерт
	// вырастет ровно на 1. Это фальсифицирует сам сброс; окна молчания —
	// тайминг-инварианты (in-flight алерт старого снапшота), не синхронизация.
	m.Deactivate(r)
	time.Sleep(50 * time.Millisecond) // grace на in-flight алерт старого снапшота
	before := m.Alerts()
	time.Sleep(50 * time.Millisecond) // окно молчания при Hz=1000 ≈ 50 оценок
	if m.Alerts() != before {
		t.Fatalf("деактивированный регион продолжает получать алерты (эпизод не сброшен)")
	}
	m.Activate(r)
	reactivateDeadline := time.After(3 * time.Second)
	for m.Alerts() == before {
		select {
		case <-reactivateDeadline:
			t.Fatalf("реактивация лагающего региона не алертит заново: эпизод не был сброшен деактивацией")
		default:
			time.Sleep(5 * time.Millisecond)
			select {
			case <-r.ringCh:
			default:
			}
			select {
			case <-r.fbCh:
			default:
			}
		}
	}
	if got := m.Alerts() - before; got != 1 {
		t.Fatalf("после реактивации алертов +%d; want +1 (новый эпизод)", got)
	}
}

// Метрика dropped: регион в сете не читает звонок — метроном коалесинг-дропает.
func TestMetronomeDroppedMetric(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Hz = 1000
	m, err := NewMetronome(cfg)
	if err != nil {
		t.Fatalf("NewMetronome: %v", err)
	}
	r := &Region{metro: m, ringCh: make(chan struct{}, 1), fbCh: make(chan struct{}, 1)}
	m.Activate(r)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go m.Run(ctx)
	deadline := time.After(3 * time.Second)
	for r.dropped.Load() == 0 { // канал не вычитается: регион «занят», звонок в буфере
		select {
		case <-deadline:
			cancel()
			t.Fatalf("дроп звонка не посчитан")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	m.Deactivate(r)
}
