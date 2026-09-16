package world

// nullPusher — заглушка пушера для тестов без стейджа.
type nullPusher struct{}

func (nullPusher) Push(uint64, []byte, bool) {}

// testRules — правила свёртки для юнит-тестов.
func testRules() Rules {
	return Rules{GraceTicks: 50, SaveRetryTicks: 10, Persist: 900, Gateway: 901, From: 1}
}
