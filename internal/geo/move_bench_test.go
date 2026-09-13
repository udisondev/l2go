package geo

import (
	"sync"
	"testing"
)

// Фикстуры бенчей движения/LOS строятся лениво один раз: плоский flat-регион, стена на 15-й ячейке, стена LOS на
// 10-й, двухслойный мост, ML с закрытым East (дорогая ветка losGeoZ —
// резолв через HigherZ).
var (
	moveBenchOnce sync.Once
	mbFlat        *Map
	mbWall        *Map
	mbSeeBlocked  *Map
	mbBridge      *Map
	mbML          *Map

	sinkLoc  Loc
	sinkBool bool
)

// flatWorldMap — регион целиком из flat-блоков нулевой высоты.
func flatWorldMap() *Map {
	b := newRegionBuilder()
	for b.n < regionBlocks {
		b.addFlat(0)
	}
	reg, _, err := decodeRegion(testRX, testRY, b.build())
	if err != nil {
		panic(err)
	}
	m := &Map{}
	m.regions[testRX*regionsY+testRY] = reg
	return m
}

func buildMoveBench() {
	mbFlat = flatWorldMap()

	wall := newCellWorld(testRX, testRY)
	wall.set(geoX(15), geoY(0), 0, NSWEAll&^East)
	mbWall = worldMap(wall)

	see := newCellWorld(testRX, testRY)
	see.set(geoX(10), geoY(0), 200, NSWEAll)
	mbSeeBlocked = worldMap(see)

	bridge := newCellWorld(testRX, testRY)
	for lx := 0; lx <= 20; lx++ {
		for ly := 0; ly <= 2; ly++ {
			bridge.setML(geoX(lx), geoY(ly), layer(0, NSWEAll), layer(200, NSWEAll))
		}
	}
	mbBridge = worldMap(bridge)

	ml := newCellWorld(testRX, testRY)
	for lx := 0; lx <= 20; lx++ {
		for ly := 0; ly <= 2; ly++ {
			ml.setML(geoX(lx), geoY(ly),
				layer(-16, NSWEAll&^East), layer(0, NSWEAll&^East), layer(16, NSWEAll&^East))
		}
	}
	mbML = worldMap(ml)
}

func moveBenchReady() {
	moveBenchOnce.Do(buildMoveBench)
}

func BenchmarkValidLocationFlat3(b *testing.B) {
	moveBenchReady()
	b.ReportAllocs()
	from, to := at(0, 0, 0), at(3, 0, 0)
	for b.Loop() {
		sinkLoc, sinkBool = mbFlat.ValidLocation(from, to)
	}
}

func BenchmarkValidLocationWall20(b *testing.B) {
	moveBenchReady()
	b.ReportAllocs()
	from, to := at(0, 0, 0), at(20, 0, 0)
	for b.Loop() {
		sinkLoc, sinkBool = mbWall.ValidLocation(from, to)
	}
}

func BenchmarkValidLocationBridgeML(b *testing.B) {
	moveBenchReady()
	b.ReportAllocs()
	from, to := at(0, 0, 0), at(20, 0, 0)
	for b.Loop() {
		sinkLoc, sinkBool = mbBridge.ValidLocation(from, to)
	}
}

// BenchmarkValidLocationDiag45 — worst case supercover: corner-пучок на
// каждом шаге (плата до 3× эмитов против Брезенхама).
func BenchmarkValidLocationDiag45(b *testing.B) {
	moveBenchReady()
	b.ReportAllocs()
	from, to := at(0, 0, 0), at(20, 20, 0)
	for b.Loop() {
		sinkLoc, sinkBool = mbFlat.ValidLocation(from, to)
	}
}

// BenchmarkValidLocationVoid — луч 36 ячеек: 23 с гео региона-якоря, 13 —
// nil-хвост соседнего региона, цель в пустоте (мир с частичным покрытием:
// цена итерации по пустоте и финал без гео; серединная пустота —
// юнит-тестами, здесь короче из-за кратности региона).
func BenchmarkValidLocationVoid(b *testing.B) {
	moveBenchReady()
	b.ReportAllocs()
	from := at(2024, 0, 0)
	to := atGeo(testRX*regionCells+2060, geoY(0), 0)
	for b.Loop() {
		sinkLoc, sinkBool = mbFlat.ValidLocation(from, to)
	}
}

func BenchmarkCanSeeOpen20(b *testing.B) {
	moveBenchReady()
	b.ReportAllocs()
	from, to := at(0, 0, 0), at(20, 0, 0)
	for b.Loop() {
		sinkBool = mbFlat.CanSee(from, to)
	}
}

// BenchmarkCanSeeBlocked10 — перекрытие на 10-й из 20 ячеек.
func BenchmarkCanSeeBlocked10(b *testing.B) {
	moveBenchReady()
	b.ReportAllocs()
	from, to := at(0, 0, 0), at(20, 0, 0)
	for b.Loop() {
		sinkBool = mbSeeBlocked.CanSee(from, to)
	}
}

// BenchmarkCanSeeMLClosed — дорогая ветка losGeoZ: multilayer + закрытый
// NSWE (резолв через HigherZ на каждой ячейке).
func BenchmarkCanSeeMLClosed(b *testing.B) {
	moveBenchReady()
	b.ReportAllocs()
	from, to := at(0, 0, 0), at(20, 0, 0)
	for b.Loop() {
		sinkBool = mbML.CanSee(from, to)
	}
}

func BenchmarkCanSeeDiag45(b *testing.B) {
	moveBenchReady()
	b.ReportAllocs()
	from, to := at(0, 0, 0), at(20, 20, 0)
	for b.Loop() {
		sinkBool = mbFlat.CanSee(from, to)
	}
}

func BenchmarkNearestZFlat(b *testing.B) {
	moveBenchReady()
	b.ReportAllocs()
	p := at(5, 5, 100)
	for b.Loop() {
		sinkInt = mbFlat.NearestZ(p)
	}
}

func BenchmarkNearestZML(b *testing.B) {
	moveBenchReady()
	b.ReportAllocs()
	p := at(5, 0, 0)
	for b.Loop() {
		sinkInt = mbML.NearestZ(p)
	}
}
