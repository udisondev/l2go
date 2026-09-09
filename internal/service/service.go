// Package service — несетевые сервисы-акторы (пати/чат/клан/маркет):
// шардированы по entity-id, живут на том же транспорте — пишут письма в ящики
// сущностей и принимают свои; fromID сервиса — в конверте. Детализация
// шардирования — при реализации.
package service

import "github.com/udisondev/l2go/internal/transport"

// Kind — вид сервиса.
type Kind uint8

const (
	KindParty  Kind = iota + 1 // пати: членство, MemberStatus-письма получателям
	KindChat                   // чат-каналы
	KindClan                   // кланы
	KindMarket                 // маркет/аукцион
)

// Service — идентификатор сервисной шарды как отправителя (fromID).
type Service struct {
	Kind Kind
	ID   transport.EntityID
}
