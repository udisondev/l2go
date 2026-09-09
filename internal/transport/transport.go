// Package transport — единый конвейер сообщений мира: конверт, адресация и
// шов ящиков (ADR-0003 §16: карта id→ящик с COW-записью, MPSC с мигрирующим
// читателем и водяным знаком, контрольный ящик региона). Реализация — фаза 3;
// здесь контракт и типы конверта v10.
package transport

// EntityID — вечный идентификатор: сущность, сервисная шарда или шлюз.
// ID не переиспользуются, смерть = маркер (И1).
type EntityID uint64

// Addr — адрес получателя: сущность по вечному ID либо слуга слота хозяина.
// Слуги не являются маршрутизируемыми сущностями (И11); слот 0 — сама сущность.
type Addr struct {
	Entity EntityID
	Slot   uint8
}

// Class — класс доставки письма (ADR-0003 §7; полный реестр типов по классам — P0.10).
type Class uint8

const (
	ClassFireAndForget Class = iota + 1 // финальный дроп молча (урон, бродкаст)
	ClassReliable                       // дроп = уведомление отправителя (аггро, XP, контроли)
	ClassTransfer                       // дроп = чистый abort (Reserve/Commit — И9)
)

// Attrs — атрибуты конверта (полный реестр — P0.10).
type Attrs uint8

const (
	// AttrBound — интент привязан к позиции/моменту: резолв валидацией на
	// применении у читателя (ADR-0003 §3).
	AttrBound Attrs = 1 << iota
)

// Envelope — конверт письма v10: {to, fromID, class, attrs, payload}.
type Envelope struct {
	To      Addr
	FromID  EntityID
	Class   Class
	Attrs   Attrs
	Payload []byte
}

// Sender — отправка слепым push: lookup ящика → enqueue, без маршрутизации,
// проверок свежести адреса и ожидания (И12). Интерфейс потребителя.
type Sender interface {
	Send(env Envelope)
}
