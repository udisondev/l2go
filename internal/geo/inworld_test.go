package geo

import "testing"

// TestInWorldEquivalentToPanicCondition — InWorld обязан совпадать с отрицанием
// паник-условия geoOf на всех кромках сетки: расхождение формул = злой вход,
// прошедший проверку потребителя и уронивший свёртку региона паникой. Оракул —
// реальная паника ValidLocation (recover), не дублирование формулы.
func TestInWorldEquivalentToPanicCondition(t *testing.T) {
	m, err := NewMapFromRegions(nil)
	if err != nil {
		t.Fatalf("NewMapFromRegions: %v", err)
	}
	maxX := GeoToWorldX(regionsX*regionCells-1) + cellSize // центр последней ячейки + ячейка
	maxY := GeoToWorldY(regionsY*regionCells-1) + cellSize
	points := [][2]int{
		{worldMinX, worldMinY}, {worldMinX - 1, worldMinY}, {worldMinX, worldMinY - 1},
		{maxX - 1, maxY - 1}, {maxX, maxY}, {maxX - 1, maxY}, {maxX, maxY - 1},
		{GeoToWorldX(0), GeoToWorldY(0)},
		{GeoToWorldX(0) - 1, GeoToWorldY(0)}, // полоса целочисленного усечения у кромки
		{GeoToWorldX(0), GeoToWorldY(0) - 1},
		{-1 << 30, 1 << 30}, {1 << 30, -1 << 30}, {-(1 << 31), -(1 << 31)},
	}
	for _, p := range points {
		panics := func() (yes bool) {
			defer func() { yes = recover() != nil }()
			m.ValidLocation(Loc{X: p[0], Y: p[1], Z: 0}, Loc{X: p[0], Y: p[1], Z: 0})
			return false
		}()
		if got := InWorld(p[0], p[1]); got == panics {
			t.Errorf("InWorld(%d, %d) = %v; ValidLocation паникует = %v (знаки обязаны быть обратными)",
				p[0], p[1], got, panics)
		}
	}
}
