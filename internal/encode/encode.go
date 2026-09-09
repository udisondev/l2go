// Package encode — стейтлес-стадия энкода исходящего: сборка per-client FIFO
// кадров в слэбы вызывающего; пакеты-писатели protocol пишут прямо в слэб.
// Работает вне тика; криптоблок — батч кадра.
package encode

// Frame — исходящий кадр клиента в пер-клиентском FIFO (payload уже собран
// пакетом-писателем).
type Frame struct {
	ClientID uint32
	Payload  []byte
}
