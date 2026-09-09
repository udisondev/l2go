// Package persist — потребитель выходного потока тика: WAL transferID
// journal-before-apply + write-behind снапшоты; пишет только подтверждённый
// владелец. Здесь контракт потребителя.
package persist

import "github.com/udisondev/l2go/internal/transport"

// Record — запись исходящего потока тика (формат WAL/снапшотов — при
// реализации).
type Record struct {
	Tick uint64
	Envs []transport.Envelope
}

// Writer — выходной потребитель тика (один из трёх: пакеты игрокам,
// бухгалтерия на диск, публикация снапшота).
type Writer interface {
	Write(rec Record) error
}
