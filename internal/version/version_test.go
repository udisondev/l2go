package version

// Smoke-тест: у ворот CI (make check, -race) должна быть работа, а не пустой прогон.
// Заменяется реальными тестами с фазой 1 (протокол).

import "testing"

func TestVersionSmoke(t *testing.T) {
	if String() == "" {
		t.Fatal("версия не должна быть пустой строкой")
	}
}

// TestGatesTurnRed — НАМЕРЕННО СЛОМАН: доказательство, что ворота CI краснеют (P0.2).
// Будет immediately reverted; см. план/прогресс.md.
func TestGatesTurnRed(t *testing.T) {
	t.Fatal("намеренный сбой: если вы это видите в зелёном CI — ворота сломаны")
}
