// Кодеки контрольных писем фазы 3 (шлюз↔регион). Живут в транспорте: обе
// стороны импортируют его, доменных зависимостей нет — персонаж едет
// json.RawMessage (тип записи знает persist, его импортируют отправитель и
// читатель по свою сторону).
package transport

import (
	"encoding/json"
	"fmt"
)

// EnterWorldMsg — контрольное письмо входа в мир (шлюз→регион, KindEnterWorld).
type EnterWorldMsg struct {
	Conn    uint64          `json:"conn"`
	Account string          `json:"account"`
	Char    json.RawMessage `json:"char"` // persist.CharRecord
}

// ConnBindMsg — регион→шлюз (KindConnBind): игрок вошёл, адрес ящика.
type ConnBindMsg struct {
	Conn   uint64   `json:"conn"`
	Entity EntityID `json:"entity"`
}

// ConnRefMsg — ссылка на коннект (KindLinkDead/KindConnClose).
type ConnRefMsg struct {
	Conn uint64 `json:"conn"`
}

// NPCDeployMsg — контрольное письмо разворачивания NPC-населения
// (KindDeployNPCs): срез спавнов статики — центр и радиус.
type NPCDeployMsg struct {
	CenterX int32 `json:"cx"`
	CenterY int32 `json:"cy"`
	Radius  int32 `json:"radius"`
}

// EncodeLetter кодирует контрольное письмо в байты конверта.
func EncodeLetter(v any) ([]byte, error) {
	return json.Marshal(v)
}

// DecodeLetter разбирает байты конверта в контрольное письмо; битый вход —
// ошибка с причиной (валидация на применении у читателя), не паника.
func DecodeLetter[T any](payload []byte) (T, error) {
	var v T
	if err := json.Unmarshal(payload, &v); err != nil {
		return v, fmt.Errorf("%w: %v", ErrBadLetter, err)
	}
	return v, nil
}

// ErrBadLetter — неразборчивый payload контрольного письма.
var ErrBadLetter = errBadLetter{}

type errBadLetter struct{}

func (errBadLetter) Error() string {
	return "transport: битое контрольное письмо"
}
