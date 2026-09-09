package version

// Smoke-тест: у ворот CI (make check, -race) должна быть работа, а не пустой прогон.
// Заменяется реальными тестами протокола.

import "testing"

func TestVersionSmoke(t *testing.T) {
	if String() == "" {
		t.Fatal("версия не должна быть пустой строкой")
	}
}
