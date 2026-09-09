// Package gateway — пограничный слой входа: пакеты клиентов и wall-clock
// события планировщика назначаются в порции тика; единственное место
// настенных часов. Шов назначения — инъекция порций реплей-харнессом;
// письма — только слепым push через транспорт.
package gateway

import "github.com/udisondev/l2go/internal/conn"

// Inbound — принятый кадр клиента, назначаемый в порцию тика.
type Inbound struct {
	Conn    conn.ConnID
	Tick    uint64 // тик назначения (номер тика метронома)
	Payload []byte
}
