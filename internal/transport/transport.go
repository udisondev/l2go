// Package transport — единый конвейер сообщений мира: конверт, адресация и
// шов ящиков (карта id→ящик, MPSC-очередь с мигрирующим читателем и водяным
// знаком, контрольный ящик региона). Здесь контракт и типы конверта;
// реализация очереди и карты появится вместе с каркасом мира.
package transport

// EntityID — вечный идентификатор: сущность, сервисная шарда или шлюз.
// ID не переиспользуются; смерть — маркер, не удаление.
type EntityID uint64

// Addr — адрес получателя: сущность по вечному ID либо слуга слота хозяина.
// Слуги не являются маршрутизируемыми сущностями; слот 0 — сама сущность.
type Addr struct {
	Entity EntityID
	Slot   uint8
}

// Class — класс доставки письма. Полный реестр типов по классам определяет
// таксономия сообщений.
type Class uint8

const (
	ClassFireAndForget Class = iota + 1 // финальный дроп молча (урон, бродкаст)
	ClassReliable                       // дроп = уведомление отправителя (аггро, XP, контроли)
	ClassTransfer                       // дроп = чистый abort (передача ресурсов)
)

// Attrs — атрибуты конверта. Полный реестр определяет таксономия сообщений.
type Attrs uint8

const (
	// AttrBound — интент привязан к позиции/моменту: резолв валидацией на
	// применении у читателя.
	AttrBound Attrs = 1 << iota
)

// Envelope — конверт письма: {to, fromID, class, attrs, payload}.
type Envelope struct {
	To      Addr
	FromID  EntityID
	Class   Class
	Attrs   Attrs
	Payload []byte
}

// Sender — отправка слепым push: lookup ящика → enqueue, без маршрутизации,
// проверок свежести адреса и ожидания. Интерфейс потребителя.
type Sender interface {
	Send(env Envelope)
}
