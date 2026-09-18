// Package world — вершина зависимостей: регионы-акторы, метроном и планировщик
// wall-clock событий. Общий pacing без барьера: период задаётся конфигурацией,
// хардкод «100 мс» запрещён. Здесь каркас типов; каркас конкурентности
// (горутина региона, ящики, тик) появится вместе с реализацией мира.
package world

import (
	"github.com/udisondev/l2go/internal/persist"
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

// Player — состояние игрока на сущности (nil у не-игроков). Rec — запись
// персиста (единственная точка правды о персонаже); ConnID — коннект шлюза;
// PendingTeleport гасит скоростной бакет P3.9; EnterLeaving — «вошёл и
// оборвался тем же шагом» (актор не шлёт слиток/бинд, сущность сразу в grace).
type Player struct {
	Rec             persist.CharRecord
	ConnID          uint64
	PendingTeleport bool
	// SpeedBudget — токен-бакет скорости в милли-юнитах (r1: списывает
	// расхождение отчёта с authPos; при рождении = CAP).
	SpeedBudget int64
	// ChatBudget — токен-бакет спам-лимита чата в мс кредита (P3.11:
	// списание chatSayCostMS за реплику, refill dt-тиками в foldAdvance;
	// при рождении = CAP ≈ каноническому per-коннект FloodProtectors).
	ChatBudget int64
	// SpeedFlagged — сессия в состоянии спидхак-флага: slog-алерт однократен
	// на эпизод (бакет восстанавливается refill-ом), метрика SpeedFlags
	// считается на каждый кадр.
	SpeedFlagged bool
	// EnterLeaving — «вошёл и оборвался тем же шагом»: актор не шлёт
	// слиток/бинд, сущность сразу в grace.
	EnterLeaving bool
	// DisplacedSameStep — рождение вытеснено повторным входом того же
	// аккаунта той же пачки: актор не спавнит сущность вовсе.
	DisplacedSameStep bool
}

// Entity — состояние сущности: мутируется только горутиной региона-владельца;
// передаётся только в чемодане единственным указателем. Здесь ядро владения
// и движения; боевые и предметные поля растут вместе с фазами реализации.
type Entity struct {
	ID      transport.EntityID
	Owner   RegionID
	Pos     Position // авторитетная позиция
	Dest    Position // клампнутая цель движения (путь провалидирован гео)
	Heading int32    // живой heading, домен [0,65536); персист-слепок — Rec.Heading
	Moving  bool     // локомоция: отрезок MoveFrom→Dest активен (интент миграции региона — отдельное поле чемодана фазы 4)
	Dead    bool     // смерть — маркер; деспавн — не смерть
	HP      int32
	Beat    Tick // heartbeat: тик последнего шага симуляции
	// Отрезок движения: позиция пересчитывается от MoveFrom по доле
	// MoveDone/MoveDist — без накопления ошибки округления; прибытие ставит
	// Pos = Dest точно. Дистанция отрезка — 2D (скорость канона
	// горизонтальна, Z — следствие рельефа).
	MoveFrom  Position
	MoveDist  int64
	MoveDone  int64
	Servants  [4]ServantSlot
	Transfers []TransferRecord
	Player    *Player
	// Npc — скин NPC-шаблона (nil у игроков): снимок статики при рождении,
	// после не меняется — потребитель NpcInfo (P3.10).
	Npc *NpcSkin
}

// Suitcase — чемодан переезда: всё, что передаётся при смене владельца.
// Черновик на стороне приёмщика пассивен до подтверждения.
type Suitcase struct {
	Entity  *Entity              // единственный указатель состояния
	Cursor  uint64               // водяной знак ящика на момент заморозки
	Pending []transport.Envelope // изъятая недообработанная пачка
	Attempt uint64               // номер попытки переезда
}
