package geo

import "fmt"

// ExampleMap_ValidLocation — движение к цели за стеной: кламп в центр
// последней проходимой ячейки (порт GeoEngine.getValidLocation).
func ExampleMap_ValidLocation() {
	w := newCellWorld(16, 10)
	w.set(geoX(4), geoY(0), 0, NSWEAll&^East)
	m := worldMap(w)
	res, ok := m.ValidLocation(at(0, 0, 0), at(10, 0, 0))
	fmt.Println(res.X, res.Y, res.Z, ok)
	// Output: -131000 -262136 0 false
}

// ExampleMap_CanSee — стена выше линии луча перекрывает обзор; бугор в
// пределах допуска обзора — нет (порт GeoEngine.canSeeTarget).
func ExampleMap_CanSee() {
	wall := newCellWorld(16, 10)
	wall.set(geoX(10), geoY(0), 200, NSWEAll)
	bump := newCellWorld(16, 10)
	bump.set(geoX(10), geoY(0), 48, NSWEAll)
	fmt.Println(worldMap(wall).CanSee(at(0, 0, 0), at(20, 0, 0)),
		worldMap(bump).CanSee(at(0, 0, 0), at(20, 0, 0)))
	// Output: false true
}
