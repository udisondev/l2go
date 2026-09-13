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

// BenchmarkTerritoryContains: территория спавна 3 узла (53% дистрибутива —
// 4 узла, 5 — 940, хвост до 16; здесь 5 узлов — середина распределения).
func BenchmarkTerritoryContains(b *testing.B) {
	t := &Territory{MinZ: -3800, MaxZ: -3400,
		Nodes: [][2]int32{{70780, 125060}, {71852, 124640}, {72660, 125432}, {71500, 125800}, {70500, 125500}}}
	t.MinX, t.MaxX, t.MinY, t.MaxY = polyBounds(t.Nodes)
	b.ReportAllocs()
	for b.Loop() {
		benchSink = t.Contains(71764, 125044, -3600)
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
// пространственного индекса; ротация точек трёх классов (z-промах; внутри
// bbox, но мимо полигона — платит кроссинг; полный промах) — одна
// константная точка мерила бы везение бранч-предиктора.
func BenchmarkZonesScan(b *testing.B) {
	zones := scanZones()
	if len(zones) < 1900 {
		b.Fatalf("скан-набор = %d зон; want ~2000", len(zones))
	}
	pts := [][3]int32{
		{0, 0, 99999},                             // z-промах
		{-300000, -300000, 0},                     // полный промах
		{150001, 150001, 99999},                   // z-промах вдали
		{zones[0].MinX + 1, zones[0].MaxY - 1, 0}, // в bbox первой зоны (верхняя кромка — мимо полигона, платит кроссинг)
		{zones[3].MinX + 1, zones[3].MinY + 1, 0}, // внутри bbox четвёртой зоны
	}
	b.ReportAllocs()
	for b.Loop() {
		hit := false
		for i := range zones {
			p := pts[i%len(pts)]
			if zones[i].Contains(p[0], p[1], p[2]) {
				hit = true
			}
		}
		benchSink = hit
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
