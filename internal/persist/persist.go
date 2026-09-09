// Package persist — типы данных выходного потока тика (один из трёх выходных
// потребителей: пакеты игрокам, бухгалтерия на диск, публикация снапшота):
// WAL transferID journal-before-apply + write-behind снапшоты; пишет только
// подтверждённый владелец. Интерфейс потребителя объявит код владельца.
package persist

import "github.com/udisondev/l2go/internal/transport"

// Record — запись исходящего потока тика (формат WAL/снапшотов — при
// реализации).
type Record struct {
	Tick uint64
	Envs []transport.Envelope
}
