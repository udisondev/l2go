// Package loginlink — клиент стыка login↔game в игровом процессе:
// LoginServer — один процесс на все миры, GameServer — процесс на мир, стык
// всегда сетевой (транспорт — gRPC). Пограничный лист: пакеты мира этот пакет
// не импортируют.
package loginlink

import "context"

// Client — семантика стыка login↔game.
type Client interface {
	// RegisterSession регистрирует ключ сессии после успешного логина.
	RegisterSession(ctx context.Context, key, account string) error
	// Kick отключает аккаунт (запрос логина во всех мирах).
	Kick(ctx context.Context, account string) error
	// OnlineReport отправляет онлайн-статистику мира.
	OnlineReport(ctx context.Context, players int) error
}
