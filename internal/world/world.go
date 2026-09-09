// Package world — вершина зависимостей: регионы-акторы, метроном и планировщик
// wall-clock событий. Общий pacing без барьера: период задаётся конфигурацией,
// хардкод «100 мс» запрещён. Здесь каркас типов; каркас конкурентности
// (горутина региона, ящики, тик) появится вместе с реализацией мира.
package world

import "github.com/udisondev/l2go/internal/transport"

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

// Position — точка мира (координаты слоя геоданных).
type Position struct {
	X, Y, Z int32
}

// ServantSlot — слот слуги хозяина: слот 0 — пет/саммон, 1–3 — кубики.
// Слуга — обычный двигун (follow/stand/attack); смерть — слот-флаг с
// позицией трупа.
type ServantSlot struct {
	Alive bool
	Pos   Position
}

// TransferRecord — запись transfer-журнала: путешествует со владением
// (переживает смену владельца и обрезку журнала обмена).
type TransferRecord struct {
	ID           uint64
	Phase        uint8 // 1 — reserved, 2 — committed, 3 — aborted
	Payload      []byte
	Precondition []byte
}

// Entity — состояние сущности: мутируется только горутиной региона-владельца;
// передаётся только в чемодане единственным указателем. Здесь ядро владения
// и движения; боевые и предметные поля растут вместе с фазами реализации.
type Entity struct {
	ID        transport.EntityID
	Owner     RegionID
	Pos       Position // авторитетная позиция
	Dest      Position // цель движения; бюджет скорости списывается в тике
	Moving    bool     // интент смены владельца («еду»): флаг-состояние, не лок
	Dead      bool     // смерть — маркер; деспавн — не смерть
	HP        int32
	Servants  [4]ServantSlot
	Transfers []TransferRecord
}

// Suitcase — чемодан переезда: всё, что передаётся при смене владельца.
// Черновик на стороне приёмщика пассивен до подтверждения.
type Suitcase struct {
	Entity  *Entity              // единственный указатель состояния
	Cursor  uint64               // водяной знак ящика на момент заморозки
	Pending []transport.Envelope // изъятая недообработанная пачка
	Attempt uint64               // номер попытки переезда
}
