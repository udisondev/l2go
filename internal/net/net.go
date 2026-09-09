// Package net — провода game-процесса: TCP-коннекты клиентов, кадры, обвязка
// крипты (TCP_NODELAY и read/write deadlines обязательны — docs/perf.md §3).
// Перенос фрейминга interlude — фаза 1; серверные коннекты — фаза 3.
package net

// ConnID — идентификатор коннекта в пределах процесса.
type ConnID uint64

// Frame — декодированный кадр (пакет протокола) без крипто-обвязки.
type Frame struct {
	Conn    ConnID
	Payload []byte
}

// Handler — потребитель кадров; реализуется gateway. OnClose обязателен:
// жизненный цикл горутины соединения конечен (codestyle §9).
type Handler interface {
	OnFrame(f Frame)
	OnClose(conn ConnID)
}
