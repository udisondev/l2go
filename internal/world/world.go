// Package world — вершина зависимостей: регионы-акторы, метроном и scheduler
// wall-clock событий (ADR-0002; ADR-0003 D1–D6). Скелет границы для карты
// ADR-0005; каркас конкурентности (горутина региона, ящики, тик) — фаза 3.
package world

import (
	"time"

	"github.com/udisondev/l2go/internal/transport"
)

// RegionID идентифицирует регион грида; Tick — номер тика метронома
// (dt = тики × период, D1; настенные часы — только в шлюзе).
type (
	RegionID uint16
	Tick     uint64
)

// Portion — пачка писем, изъятая из ящиков на границе тика: единица
// детерминированной свёртки (ADR-0003 §4). Advisory-входы и лог переездов
// пополняются задачей P0.10.
type Portion struct {
	Region RegionID
	Tick   Tick
	Envs   []transport.Envelope
}

// Metronome — общий pacing без барьера (ADR-0002). Период задаётся
// конфигурацией; хардкод «100 мс» запрещён (инвариант 7 AGENTS).
type Metronome interface {
	// Period возвращает период метронома.
	Period() time.Duration
}
