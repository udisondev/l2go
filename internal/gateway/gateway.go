// Package gateway — пограничный слой входа: пакеты клиентов и wall-clock
// события планировщика назначаются в порции тика; единственное место
// настенных часов. Шов назначения — инъекция порций реплей-харнессом.
package gateway

import "github.com/udisondev/l2go/internal/net"

// Inbound — принятый кадр клиента, назначаемый в порцию тика.
type Inbound struct {
	Conn    net.ConnID
	Tick    uint64 // тик назначения (номер тика метронома)
	Payload []byte
}

// Gateway назначает входящие кадры и wall-clock события в порции; письма —
// только слепым push через транспорт.
type Gateway interface {
	Assign(in Inbound)
}
