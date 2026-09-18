package geo

import (
	"sync"
	"testing"
)

// fuzzWorlds — фиксированный набор миров FuzzValidLocation: строится
// детерминированно один раз; фазз-вход — только пары точек (селектор мира
// отображается по модулю — мутация селектора не порождает декода).
var fuzzWorldsOnce sync.Once
var fuzzWorlds []*Map

func buildFuzzWorlds() {
	mk := func(paint func(w *cellWorld)) *Map {
		w := newCellWorld(testRX, testRY)
		if paint != nil {
			paint(w)
		}
		return worldMap(w)
	}
	fuzzWorlds = []*Map{
		mk(nil), // плоский
		mk(func(w *cellWorld) { w.set(geoX(4), geoY(0), 0, NSWEAll&^East) }),
		mk(func(w *cellWorld) {
			for lx := 2; lx <= 4; lx++ {
				for ly := 0; ly <= 2; ly++ {
					w.set(geoX(lx), geoY(ly), 200, NSWEAll)
				}
			}
		}),
		mk(func(w *cellWorld) {
			for ly := 0; ly <= 2; ly++ {
				w.setML(geoX(5), geoY(ly), layer(0, NSWEAll), layer(200, NSWEAll))
			}
		}),
		mk(func(w *cellWorld) {
			for lx := 5; lx <= 7; lx++ {
				for ly := 0; ly <= 1; ly++ {
					w.setML(geoX(lx), geoY(ly), layer(1200, NSWEAll))
				}
			}
		}),
		mk(func(w *cellWorld) {
			for lx := range 8 {
				for ly := range 8 {
					w.setML(geoX(lx), geoY(ly),
						layer(-16, NSWEAll&^East), layer(0, NSWEAll&^West), layer(16, NSWEAll&^North))
				}
			}
		}),
		mk(func(w *cellWorld) {
			w.set(geoX(0), geoY(0), 0, NSWEAll&^South)
			w.setML(geoX(1), geoY(0), layer(0, NSWEAll), layer(64, NSWEAll))
		}),
	}
	// Пустота в сетке: регион (17, 10) отсутствует — покрывает ходы
	// в nil-регион цели.
	w := newCellWorld(testRX+1, testRY)
	fuzzWorlds = append(fuzzWorlds, worldMap(w))
}

// FuzzValidLocation: машинные инварианты ValidLocation на
// детерминированных мирах; вход — координаты пары (в сетке по построению,
// точки — произвольные внутри ячеек, включая границы: офсет 0..15 от
// юго-западного угла ячейки).
func FuzzValidLocation(f *testing.F) {
	f.Add(uint8(0), int32(100), int32(200), int32(300), int32(400), int32(0), int32(0))
	f.Add(uint8(3), int32(80), int32(80), int32(2000), int32(80), int32(200), int32(0))
	f.Add(uint8(255), int32(-70000), int32(70000), int32(65535), int32(-65535), int32(-32768), int32(32767))
	f.Fuzz(func(t *testing.T, sel uint8, ax, ay, bx, by, az, bz int32) {
		fuzzWorldsOnce.Do(buildFuzzWorlds)
		m := fuzzWorlds[int(sel)%len(fuzzWorlds)]
		// Координаты сворачиваются в сетку региона-якоря и его соседа:
		// любые int32 дают in-grid точки; офсет внутри ячейки выводится
		// из других бит входа — границы и центры равноправны в корпусе.
		from := Loc{
			X: WorldMinX + (testRX*regionCells+int(uint32(ax)%regionCells))*cellSize + int(uint32(ax)>>8%cellSize),
			Y: WorldMinY + (testRY*regionCells+int(uint32(ay)%regionCells))*cellSize + int(uint32(ay)>>8%cellSize),
			Z: int(az),
		}
		to := Loc{
			X: WorldMinX + (testRX*regionCells+int(uint32(bx)%(2*regionCells)))*cellSize + int(uint32(bx)>>4%cellSize),
			Y: WorldMinY + (testRY*regionCells+int(uint32(by)%regionCells))*cellSize + int(uint32(by)>>8%cellSize),
			Z: int(bz),
		}
		res, ok := m.ValidLocation(from, to)
		checkMoveInvariants(t, m, from, to, res, ok)
		if !m.CanSee(from, from) {
			t.Errorf("CanSee(%v, %v) = false; рефлексивность нарушена", from, from)
		}
	})
}
