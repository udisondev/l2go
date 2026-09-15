package gateway

import (
	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/transport"
)

// Кодеки контрольных писем шлюза: JSON по прецеденту персиста (редкие
// письма, читаемость в логе порций). Формат стабилен: KindEnterWorld и
// KindLinkDead производит шлюз, читает регион; KindConnBind и KindConnClose —
// в обратную сторону.

// enterWorldMsg — контрольное письмо входа в мир региону.
type enterWorldMsg struct {
	Conn    uint64             `json:"conn"`
	Account string             `json:"account"`
	Char    persist.CharRecord `json:"char"`
}

// connBindMsg — регион→шлюз: игрок вошёл, адрес ящика игрока.
type connBindMsg struct {
	Conn   uint64             `json:"conn"`
	Entity transport.EntityID `json:"entity"`
}

// connRefMsg — ссылка на коннект (LinkDead, ConnClose).
type connRefMsg struct {
	Conn uint64 `json:"conn"`
}
