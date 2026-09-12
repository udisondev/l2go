package geo

import "fmt"

// ExampleValidLocation — движение к цели за стеной: кламп в центр последней
// проходимой ячейки (порт GeoEngine.getValidLocation).
func ExampleMap_ValidLocation() {
	w := newCellWorld(16, 10)
	w.set(geoX(4), geoY(0), 0, NSWEAll&^East)
	m := worldMap(w)
	res, ok := m.ValidLocation(at(0, 0, 0), at(10, 0, 0))
	fmt.Println(res.X, res.Y, res.Z, ok)
	// Output: -131000 -262136 0 false
}

// ExampleCanSee — препятствие выше линии луча перекрывает обзор; с высоты
// препятствия видно (порт GeoEngine.canSeeTarget).
func ExampleMap_CanSee() {
	w := newCellWorld(16, 10)
	w.set(geoX(0), geoY(0), 200, NSWEAll)
	w.set(geoX(10), geoY(0), 200, NSWEAll)
	w.set(geoX(20), geoY(0), 200, NSWEAll)
	m := worldMap(w)
	fmt.Println(m.CanSee(at(0, 0, 0), at(20, 0, 0)), m.CanSee(at(0, 0, 200), at(20, 0, 200)))
	// Output: false true
}
