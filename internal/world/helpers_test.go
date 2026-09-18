package world

import (
	"github.com/udisondev/l2go/internal/data"
	"github.com/udisondev/l2go/internal/geo"
)

// nullPusher — заглушка пушера для тестов без стейджа.
type nullPusher struct{}

func (nullPusher) Push(uint64, []byte, bool) {}

// testRules — правила свёртки для юнит-тестов (период канона 10 Гц).
func testRules() Rules {
	return Rules{GraceTicks: 50, SaveRetryTicks: 10, PeriodNS: 100_000_000, Persist: 900, Gateway: 901, From: 1}
}

// testEnv — окружение свёртки юнит-тестов (пустая гео, опциональная статика).
func testEnv(static *data.Static) Env {
	return Env{Region: 1, Rules: testRules(), GM: emptyGeo, Static: static}
}

// testEnvGM — окружение свёртки с заданной гео-картой.
func testEnvGM(gm *geo.Map) Env {
	return Env{Region: 1, Rules: testRules(), GM: gm, Static: nil}
}

// emptyGeo — карта без гео-регионов: NullRegion-семантика (всё проходимо);
// тесты без гео-сценариев.
var emptyGeo = func() *geo.Map {
	m, err := geo.NewMapFromRegions(nil)
	if err != nil {
		panic("тест: пустая карта гео: " + err.Error())
	}
	return m
}()
