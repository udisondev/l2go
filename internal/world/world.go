// Package world — вершина зависимостей: регионы-акторы, метроном и планировщик
// wall-clock событий. Здесь каркас типов; каркас конкурентности (горутина
// региона, ящики, тик) появится вместе с реализацией мира.
package world

import (
	"time"

	"github.com/udisondev/l2go/internal/transport"
)

// RegionID идентифицирует регион грида; Tick — номер тика метронома
// (dt = тики × период; настенные часы — только во входном шлюзе).
type (
	RegionID uint16
	Tick     uint64
)

// Portion — пачка писем, изъятая из ящиков на границе тика: единица
// детерминированной свёртки. Advisory-входы и лог переездов добавит
// таксономия сообщений.
type Portion struct {
	Region RegionID
	Tick   Tick
	Envs   []transport.Envelope
}

// Metronome — общий pacing без барьера. Период задаётся конфигурацией;
// хардкод «100 мс» запрещён.
type Metronome interface {
	// Period возвращает период метронома.
	Period() time.Duration
}
