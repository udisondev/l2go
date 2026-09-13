package data

import (
	"math"
	"math/rand"
	"testing"
)

// benchSink — защита от свёртки вызовов компилятором (прецедент
// internal/geo/move_bench_test.go).
var benchSink bool

// BenchmarkZoneContains: изолированный Contains по формам; саббенчи NPoly
// n=4/8/16 (4 узла — 72% дистрибутива, хвост до 59) и попадание/промах.
// Координаты порядка мировых (~±100000).
func BenchmarkZoneContains(b *testing.B) {
	npoly := func(n int) Zone {
		zn := Zone{ShapeKind: ShapeNPoly, MinZ: -500, MaxZ: 500, ZLo: -500, ZHi: 500}
		zn.Nodes = make([][2]int32, n)
		for i := 0; i < n; i++ {
			a := float64(i) * 2 * 3.14159265 / float64(n)
			zn.Nodes[i] = [2]int32{100000 + int32(5000*math.Cos(a)), 100000 + int32(5000*math.Sin(a))}
		}
		zn.MinX, zn.MaxX, zn.MinY, zn.MaxY = polyBounds(zn.Nodes)
		return zn
	}
	cases := []struct {
		name string
		zn   Zone
		x, y int32
	}{
		{"npoly4-hit", npoly(4), 100000, 100000},
		{"npoly4-miss", npoly(4), 200000, 100000},
		{"npoly8-hit", npoly(8), 100000, 100000},
		{"npoly8-miss", npoly(8), 200000, 100000},
		{"npoly16-hit", npoly(16), 100000, 100000},
		{"npoly16-miss", npoly(16), 200000, 100000},
		{"cuboid-hit", cuboidBench(100000, 110000), 105000, 105000},
		{"cuboid-miss", cuboidBench(100000, 110000), -105000, 105000},
		{"cylinder-hit", cylinderBench(0, 0, 1500), 1060, 1060},
		{"cylinder-miss", cylinderBench(0, 0, 1500), 300000, 0},
	}
	for _, tt := range cases {
		b.Run(tt.name, func(b *testing.B) {
			zn := tt.zn
			b.ReportAllocs()
			for b.Loop() {
				benchSink = zn.Contains(tt.x, tt.y, 0)
			}
		})
	}
}

// BenchmarkTerritoryContains: территории 4 узла (мода распределения — 53%)
// и 5 узлов (940 из 3630), попадание и промах.
func BenchmarkTerritoryContains(b *testing.B) {
	terr4 := &Territory{MinZ: -3800, MaxZ: -3400,
		Nodes: [][2]int32{{70780, 125060}, {71852, 124640}, {72660, 125432}, {70900, 125700}}}
	terr4.MinX, terr4.MaxX, terr4.MinY, terr4.MaxY = polyBounds(terr4.Nodes)
	terr5 := &Territory{MinZ: -3800, MaxZ: -3400,
		Nodes: [][2]int32{{70780, 125060}, {71852, 124640}, {72660, 125432}, {71500, 125800}, {70500, 125500}}}
	terr5.MinX, terr5.MaxX, terr5.MinY, terr5.MaxY = polyBounds(terr5.Nodes)
	cases := []struct {
		name string
		t    *Territory
		x, y int32
	}{
		{"n4-hit", terr4, 71764, 125350},
		{"n4-miss", terr4, 0, 0},
		{"n5-hit", terr5, 71764, 125044},
		{"n5-miss", terr5, 0, 0},
	}
	for _, tt := range cases {
		b.Run(tt.name, func(b *testing.B) {
			ter := tt.t
			b.ReportAllocs()
			for b.Loop() {
				benchSink = ter.Contains(tt.x, tt.y, -3600)
			}
		})
	}
}

// scanZones — детерминированный кластеризованный набор ~2000 зон (смесь
// форм как в дистрибутиве: ~80% NPoly, ~19% Cuboid, ~0.5% Cylinder; кластеры
// центров вместо равномерного разброса — как реальные континенты/города).
// Baseline наивного перебора для будущего решения о пространственном индексе.
func scanZones() []Zone {
	rng := rand.New(rand.NewSource(42))
	var zones []Zone
	for c := 0; c < 25 && len(zones) < 2000; c++ {
		cx := int32(rng.Intn(300000) - 150000)
		cy := int32(rng.Intn(300000) - 150000)
		for k := 0; k < 80 && len(zones) < 2000; k++ {
			zx := cx + int32(rng.Intn(8000)-4000)
			zy := cy + int32(rng.Intn(8000)-4000)
			zn := Zone{MinZ: -2000, MaxZ: 2000, ZLo: -2000, ZHi: 2000}
			switch r := rng.Intn(1000); {
			case r < 800: // NPoly 4–16 узлов
				zn.ShapeKind = ShapeNPoly
				n := 4 + rng.Intn(13)
				rad := int32(200 + rng.Intn(1800))
				zn.Nodes = make([][2]int32, n)
				for i := 0; i < n; i++ {
					a := float64(i)*2*3.14159265/float64(n) + rng.Float64()*0.3
					zn.Nodes[i] = [2]int32{zx + int32(float64(rad)*math.Cos(a)), zy + int32(float64(rad)*math.Sin(a))}
				}
				zn.MinX, zn.MaxX, zn.MinY, zn.MaxY = polyBounds(zn.Nodes)
			case r < 995: // Cuboid
				zn.ShapeKind = ShapeCuboid
				zn.MinX, zn.MaxX = zx, zx+int32(200+rng.Intn(2000))
				zn.MinY, zn.MaxY = zy, zy+int32(200+rng.Intn(2000))
				zn.Nodes = [][2]int32{{zn.MinX, zn.MinY}, {zn.MaxX, zn.MaxY}}
			default: // Cylinder ~0.5%
				zn.ShapeKind = ShapeCylinder
				zn.Rad = int32(300 + rng.Intn(1200))
				zn.Nodes = [][2]int32{{zx, zy}}
			}
			zones = append(zones, zn)
		}
	}
	return zones
}

// BenchmarkZonesScan: перебор всех зон по точке — единица работы фазы 3 до
// пространственного индекса. Классы точек вычисляются от самой зоны в
// итерации (не от фикс-зон): z-промах; угол bbox (внутри bbox, обычно мимо
// полигона — платит кроссинг); центр bbox (обычно попадание); полный промах.
// Одна константная точка мерила бы везение бранч-предиктора.
func BenchmarkZonesScan(b *testing.B) {
	zones := scanZones()
	if len(zones) < 1900 {
		b.Fatalf("скан-набор = %d зон; want ~2000", len(zones))
	}
	b.ReportAllocs()
	for b.Loop() {
		hit := false
		for i := range zones {
			zn := &zones[i]
			var x, y, z int32
			switch i % 4 {
			case 0: // z-промах
				x, y, z = zn.MinX+1, zn.MinY+1, 99999
			case 1: // угол bbox: платит кроссинг, как правило мимо
				x, y = zn.MinX+1, zn.MinY+1
			case 2: // центр bbox: как правило попадание
				x, y = zn.MinX+(zn.MaxX-zn.MinX)/2, zn.MinY+(zn.MaxY-zn.MinY)/2
			default: // полный промах
				x, y = -300000, -300000
			}
			if zn.Contains(x, y, z) {
				hit = true
			}
		}
		benchSink = hit
	}
}

// TestScanClasses — фальсификация классов скана: каждый класс реально
// представлен и hit/miss по классам ненулевые (бенчмарк не выродился в
// один бранч-путь).
func TestScanClasses(t *testing.T) {
	zones := scanZones()
	classHit, classMiss := [4]int{}, [4]int{}
	for i := range zones {
		zn := &zones[i]
		var x, y, z int32
		class := i % 4
		switch class {
		case 0:
			x, y, z = zn.MinX+1, zn.MinY+1, 99999
		case 1:
			x, y = zn.MinX+1, zn.MinY+1
		case 2:
			x, y = zn.MinX+(zn.MaxX-zn.MinX)/2, zn.MinY+(zn.MaxY-zn.MinY)/2
		default:
			x, y = -300000, -300000
		}
		if zn.Contains(x, y, z) {
			classHit[class]++
		} else {
			classMiss[class]++
		}
	}
	for class := 0; class < 4; class++ {
		if classHit[class] == 0 && classMiss[class] == 0 {
			t.Errorf("класс %d не представлен", class)
		}
	}
	// Класс кроссинга (угол bbox) обязан давать и промахи (платит полный
	// кроссинг), и центр bbox — попадания.
	if classMiss[1] == 0 {
		t.Errorf("класс угла bbox без кроссинг-промахов: hit=%v", classHit[1])
	}
	if classHit[2] == 0 {
		t.Errorf("класс центра bbox без попаданий: miss=%v", classMiss[2])
	}
}

// TestZonesScanNoAlloc — скан без аллокаций.
func TestZonesScanNoAlloc(t *testing.T) {
	zones := scanZones()
	if n := testing.AllocsPerRun(10, func() {
		hit := false
		for i := range zones {
			if zones[i].Contains(0, 0, 0) {
				hit = true
			}
		}
		benchSink = hit
	}); n != 0 {
		t.Errorf("ZonesScan: %v аллокаций на проход; want 0", n)
	}
}

func cuboidBench(min, max int32) Zone {
	return Zone{ShapeKind: ShapeCuboid, MinZ: -500, MaxZ: 500, ZLo: -500, ZHi: 500,
		MinX: min, MaxX: max, MinY: min, MaxY: max,
		Nodes: [][2]int32{{min, min}, {max, max}}}
}

func cylinderBench(x, y, rad int32) Zone {
	return Zone{ShapeKind: ShapeCylinder, MinZ: -500, MaxZ: 500, Rad: rad,
		Nodes: [][2]int32{{x, y}}}
}
