// Package admin — admin-API игрового процесса: команды оператора (лестница
// надзора: лаг → фриз мира → graceful-стоп). Пограничный лист: мир его не
// импортирует.
package admin

// Status — снимок состояния мира для оператора.
type Status struct {
	Ticks         uint64
	PlayersOnline int
}
