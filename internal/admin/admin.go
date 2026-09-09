// Package admin — admin-API game-процесса: gRPC-стенд (фаза 3+) для команд
// оператора — лестница надзора T2/T3 (ADR-0003 §9). Пограничный лист: мир
// его не импортирует (карта ADR-0005).
package admin

// Status — снимок состояния мира для оператора (состав — фаза 3+).
type Status struct {
	Ticks         uint64
	PlayersOnline int
}
