// Package net — провода игрового процесса: TCP-коннекты клиентов, кадры,
// обвязка крипты. TCP_NODELAY и read/write deadlines на каждом соединении
// обязательны.
package net

// ConnID — идентификатор коннекта в пределах процесса.
type ConnID uint64

// Frame — декодированный кадр (пакет протокола) без крипто-обвязки.
type Frame struct {
	Conn    ConnID
	Payload []byte
}

// Handler — потребитель кадров; реализуется gateway. OnClose обязателен:
// жизненный цикл горутины соединения конечен.
type Handler interface {
	OnFrame(f Frame)
	OnClose(conn ConnID)
}
