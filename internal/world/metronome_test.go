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
	bad := []Config{
		{Hz: 0, HeartbeatTicks: 10, WatchdogTicks: 30},
		{Hz: 10, HeartbeatTicks: 0, WatchdogTicks: 30},
		{Hz: 10, HeartbeatTicks: 10, WatchdogTicks: 0},
		{Hz: 10, HeartbeatTicks: 10, WatchdogTicks: 30, CtrlBudget: 0},
	}
	for i, cfg := range bad {
		if _, err := NewMetronome(cfg); err == nil {
			t.Errorf("конфиг %d (%+v) прошёл валидацию; want ошибка", i, cfg)
		}
	}
}

// Сервисная подписка на дверной звонок: тот же pacing, без второй ветки.
func TestMetronomeSubscribe(t *testing.T) {
	m, err := NewMetronome(DefaultConfig())
	if err != nil {
		t.Fatalf("NewMetronome: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	ch, unsub := m.Subscribe()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("подписчик не получил звонка")
	}
	unsub()
	// после отписки звонки подписчику не идут (канал не переполняется и не блокирует метроном)
	for i := 0; i < 100 && len(ch) < cap(ch); i++ {
		time.Sleep(time.Millisecond)
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	r := &Region{metro: m, ringCh: make(chan struct{}, 1), fbCh: make(chan struct{}, 1)}
	m.Activate(r)
	deadline := time.After(3 * time.Second)
	for m.Alerts() == 0 {
		select {
		case <-deadline:
			t.Fatalf("вотчдог не поднял алерт по лагающему региону")
		default:
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
	m.Deactivate(r)
	time.Sleep(50 * time.Millisecond)
	before := m.Alerts()
	time.Sleep(50 * time.Millisecond)
	if m.Alerts() != before {
		t.Fatalf("деактивированный регион продолжает получать алерты (эпизод не сброшен)")
	}
}
