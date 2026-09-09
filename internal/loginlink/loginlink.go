// Package loginlink — клиент стыка login↔game в game-процессе (r4):
// LoginServer — один процесс на все миры, GameServer — процесс на мир, стык
// всегда сетевой. Транспорт — gRPC с фазы 3 (семантика перенесена из pkg/rpc
// interlude); пограничный лист: пакеты мира этот пакет не импортируют
// (карта ADR-0005).
package loginlink

import "context"

// Client — семантика стыка login↔game (gRPC-реализация — фаза 3).
type Client interface {
	// RegisterSession регистрирует ключ сессии после успешного логина.
	RegisterSession(ctx context.Context, key, account string) error
	// Kick отключает аккаунт (запрос логина во всех мирах).
	Kick(ctx context.Context, account string) error
	// OnlineReport отправляет онлайн-статистику мира.
	OnlineReport(ctx context.Context, players int) error
}
