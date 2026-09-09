// Package persist — потребитель выходного потока тика (ADR-0003 §8):
// WAL transferID journal-before-apply + write-behind снапшоты; пишет только
// подтверждённый владелец. Реализация — фаза 5; здесь контракт потребителя.
package persist

import "github.com/udisondev/l2go/internal/transport"

// Record — запись исходящего потока тика (формат WAL/снапшотов — фаза 5).
type Record struct {
	Tick uint64
	Envs []transport.Envelope
}

// Writer — выходной потребитель тика (один из трёх: пакеты игрокам,
// бухгалтерия на диск, публикация снапшота — ADR-0003 §2).
type Writer interface {
	Write(rec Record) error
}
