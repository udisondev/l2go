// Package conn — провода игрового процесса: TCP-коннекты клиентов, кадры,
// расшифровка входящих кадров. TCP_NODELAY и read/write deadlines на каждом
// соединении обязательны.
package conn

// ConnID — идентификатор коннекта в пределах процесса.
type ConnID uint64

// Frame — декодированный кадр (пакет протокола) без крипто-обвязки.
type Frame struct {
	Conn    ConnID
	Payload []byte
}

// Handler — потребительский шов: определяется здесь (провода ниже шлюза и не
// могут импортировать его), реализуется шлюзом; методы — по одному событию.
type Handler interface {
	OnFrame(f Frame)
	OnClose(conn ConnID)
}
