// Package transport — транспорт мира: карта вечных адресов id→ящик, MPSC-ящики
// с мигрирующим читателем и водяным знаком (ADR-0003). Отправка — слепой push:
// lookup ящика и enqueue — смежные операции, без маршрутизации, проверок свежести
// адреса и ожидания; указатель ящика между отправками не кешируется.
package transport

// EntityID — вечный идентификатор: сущность, сервисная шарда, регион или шлюз.
// ID не переиспользуются; смерть — маркер, не удаление. Ноль зарезервирован
// как невалидный (нулевое значение Addr не адресует никого).
type EntityID uint64

// Slot — слот адресата: SlotSelf адресует саму сущность, остальные значения —
// слуг хозяина.
type Slot int8

const (
	SlotSelf  Slot = -1 // сама сущность (адрес без слуги)
	SlotPet   Slot = 0  // позиционный пет/саммон
	SlotCube1 Slot = 1  // кубики
	SlotCube2 Slot = 2
	SlotCube3 Slot = 3
)

// Addr — адрес получателя: сущность по вечному ID либо слуга слота хозяина.
// Слуги не являются маршрутизируемыми сущностями: письмо падает в ящик
// хозяина.
type Addr struct {
	Entity EntityID
	Slot   Slot
}

// Class — класс доставки письма. Полный реестр типов по классам определяет
// таксономия сообщений.
type Class uint8

const (
	ClassFireAndForget Class = iota + 1 // финальный дроп молча (урон, бродкаст)
	ClassReliable                       // дроп = уведомление владельцу отправителя (аггро, XP, контроли)
	ClassTransfer                       // дроп = чистый abort (передача ресурсов)
)

// Attrs — атрибуты конверта. Полный реестр определяет таксономия сообщений.
type Attrs uint8

const (
	// AttrBound — интент привязан к позиции/моменту: резолв валидацией на
	// применении у читателя.
	AttrBound Attrs = 1 << iota
)

// Envelope — конверт письма: {to, fromID, kind, attrs, payload}. Тип письма
// едет с конвертом: класс доставки и принадлежность контрольному разбору —
// производные Kind. Payload — байты со смыслом по Kind; enqueue передаёт
// владение байтами отправителя, после изъятия байты принадлежат читателю.
type Envelope struct {
	To      Addr
	FromID  EntityID
	Kind    Kind
	Attrs   Attrs
	Payload []byte
}

// Kind — тип письма таксономии. Реестр закрыт: новый тип — правка таксономии
// в задаче-потребителе вместе с тестом полноты.
type Kind uint16

const (
	// Действия над сущностью: применяет владелец цели, валидация на применении.
	KindApplyDamage    Kind = iota + 1 // урон: наступательная часть от атакующего
	KindAggro                          // аггро
	KindKillCredit                     // зачёт убийства атакующему
	KindXP                             // опыт
	KindControlEffect                  // контроли (стан/рут и т.п.)
	KindBroadcastState                 // рассылка состояния (огонь/флаги)

	// Передача ресурсов: reserve → commit/abort, журнал до применения.
	KindReserve    // резервирование предмета/денег
	KindCommit     // подтверждение передачи
	KindAbort      // откат передачи
	KindLootPickup // подбор лута с земли
	KindSpoil      // спойл трупа
	KindSweep      // свип

	// Сервисные письма доменов без географии.
	KindMemberStatus // статус члена пати получателю
	KindServiceMsg   // прочие письма доменов (инвайты, переписка)

	// Контрольные письма региону: ящик региона, приоритетный разбор.
	KindSuitcase   // чемодан переезда
	KindInstallAck // подтверждение установки черновика
	KindConfirmAck // подтверждение забвения копии источника
	KindRetire     // гашение черновика/устаревшей попытки
	KindSeed       // затравка известности при переезде наблюдателя

	// Клиентские кадры: payload — байты расшифрованного кадра (опкод + тело),
	// fromID — шлюз; декодирование представления — у читателя-владельца.
	KindClientFrame

	// Персист-актор: запросы и ответы файлового писателя.
	KindPersistRequest
	KindPersistReply

	// Контрольные письма фазы 3: рождение и смерть связи игрок-коннект.
	KindEnterWorld // вход в мир: {connID, account, снимок персонажа} региону
	KindLinkDead   // обрыв коннекта региону
	KindConnClose  // регион→шлюз «закрыть коннект»

	kindSentinel // маркер конца реестра (не тип письма)
)

// KindCount — мощность реестра Kind: типы плотны в [1, KindCount], дыр нет
// (тест полноты). Счётчики свёртки мира индексируются по Kind без выхода за
// границы.
const KindCount = int(kindSentinel) - 1

// Class возвращает класс доставки типа. Неизвестный тип — нулевой класс:
// валидация на применении такое отклоняет.
func (k Kind) Class() Class {
	switch k {
	case KindApplyDamage, KindBroadcastState, KindClientFrame:
		return ClassFireAndForget
	case KindAggro, KindKillCredit, KindXP, KindControlEffect,
		KindMemberStatus, KindServiceMsg, KindInstallAck, KindConfirmAck,
		KindRetire, KindSeed, KindPersistRequest, KindPersistReply,
		KindEnterWorld, KindLinkDead, KindConnClose:
		return ClassReliable
	case KindReserve, KindCommit, KindAbort, KindLootPickup, KindSpoil,
		KindSweep, KindSuitcase:
		return ClassTransfer
	}
	return 0
}

// Regional сообщает, что письмо контрольное: адресат — регион как таковой,
// разбор — в приоритетной фазе дрена.
func (k Kind) Regional() bool {
	switch k {
	case KindSuitcase, KindInstallAck, KindConfirmAck, KindRetire, KindSeed,
		KindEnterWorld, KindLinkDead:
		return true
	}
	return false
}

// Service сообщает, что письмо принадлежит домену без географии.
func (k Kind) Service() bool {
	return k == KindMemberStatus || k == KindServiceMsg
}

// Domain — домен адресата для валидации на применении: письмо применяется,
// только если домен разрешает тип; по умолчанию — запрет. Сервисные адресаты
// (шлюз, persist-актор, регион) вне доменной валидации — их читатель
// диспетчеризует по Kind.
type Domain uint8

const (
	DomainWorld  Domain = iota + 1 // сущность мира: применяет регион-владелец
	DomainParty                    // пати
	DomainChat                     // чат-каналы
	DomainClan                     // клан
	DomainMarket                   // маркет/аукцион
)

// Allows — полный whitelist домена; всё вне перечисленного запрещено.
func (d Domain) Allows(k Kind) bool {
	switch d {
	case DomainWorld:
		return !k.Service()
	case DomainParty:
		return k.Service()
	case DomainChat, DomainClan, DomainMarket:
		return k == KindServiceMsg
	}
	return false
}
