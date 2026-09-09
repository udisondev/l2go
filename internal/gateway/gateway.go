// Package gateway — пограничный слой входа (глоссарий ADR-0003 «Шлюз»):
// пакеты клиентов и wall-clock события scheduler'а назначаются в порции
// тика; единственное место настенных часов (D1). Реализация — фаза 3;
// шов назначения — инъекция порций реплей-харнессом.
package gateway

import "github.com/udisondev/l2go/internal/net"

// Inbound — принятый кадр клиента, назначаемый в порцию тика.
type Inbound struct {
	Conn    net.ConnID
	Tick    uint64 // тик назначения (номер тика метронома)
	Payload []byte
}

// Gateway назначает входящие кадры и wall-clock события в порции; письма —
// только слепым push через транспорт (И12).
type Gateway interface {
	Assign(in Inbound)
}
