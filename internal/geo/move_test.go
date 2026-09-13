package geo

import (
	"math"
	"strings"
	"testing"
)

// Канон порогов и семантики — GeoEngine канона Mobius Interlude 43ac8878;
// атрибуция методов — в комментариях кейсов.

// Якорь тестовых миров: регион (16, 10).
const (
	testRX = 16
	testRY = 10
)

func geoX(lx int) int { return testRX*regionCells + lx }
func geoY(ly int) int { return testRY*regionCells + ly }

// at строит Loc в центре ячейки локальных координат региона (16, 10).
func at(lx, ly, z int) Loc {
	return Loc{X: GeoToWorldX(geoX(lx)), Y: GeoToWorldY(geoY(ly)), Z: z}
}

// atGeo строит Loc в центре ячейки глобальных гео-координат.
func atGeo(gx, gy, z int) Loc {
	return Loc{X: GeoToWorldX(gx), Y: GeoToWorldY(gy), Z: z}
}

// cellWorld — тестовый мир: фон flat-0, поверх — complex-ячейки, ML-ячейки
// и целиком flat-блоки (полный int16 — высоты вне кванта 8).
type cellWorld struct {
	rx, ry     int
	complexes  map[[2]int]uint16
	mls        map[[2]int][]uint16
	flatBlocks map[[2]int]int // (blockX, blockY) → высота
}

func newCellWorld(rx, ry int) *cellWorld {
	return &cellWorld{
		rx: rx, ry: ry,
		complexes:  map[[2]int]uint16{},
		mls:        map[[2]int][]uint16{},
		flatBlocks: map[[2]int]int{},
	}
}

func (w *cellWorld) local(gx, gy int) (int, int) {
	return gx - w.rx*regionCells, gy - w.ry*regionCells
}

// set задаёт complex-ячейку (высота кратна 8).
func (w *cellWorld) set(gx, gy, h int, nswe NSWE) {
	w.complexes[[2]int{gx, gy}] = encodeCellWord(h, nswe)
}

// setML задаёт ML-ячейку слоями (слова целиком).
func (w *cellWorld) setML(gx, gy int, layers ...uint16) {
	w.mls[[2]int{gx, gy}] = layers
}

// layer — слово слоя: высота (кратна 8) + флаги.
func layer(h int, nswe NSWE) uint16 { return encodeCellWord(h, nswe) }

// setFlatBlock делает блок, содержащий ячейку, flat-блоком высоты h.
func (w *cellWorld) setFlatBlock(gx, gy, h int) {
	lx, ly := w.local(gx, gy)
	w.flatBlocks[[2]int{lx >> 3, ly >> 3}] = h
}

const blockSide = 8

// defaultLayer — общий слой-заполнитель ML-блоков (билдер только читает).
var defaultLayer = []uint16{layer(0, NSWEAll)}

// build собирает байты региона: блоки с зонами — complex/ML, остальные —
// flat фона.
func (w *cellWorld) build() []byte {
	b := newRegionBuilder()
	for blk := 0; blk < regionBlocks; blk++ {
		bx, by := blk>>8, blk&0xFF
		if h, ok := w.flatBlocks[[2]int{bx, by}]; ok {
			b.addFlat(h)
			continue
		}
		var cc [blockCells]uint16
		var ml [blockCells][]uint16
		useML := false
		for i := 0; i < blockCells; i++ {
			gx := w.rx*regionCells + bx*blockSide + i/8
			gy := w.ry*regionCells + by*blockSide + i%8
			word, isComplex := w.complexes[[2]int{gx, gy}]
			if isComplex {
				cc[i] = word
			} else {
				cc[i] = layer(0, NSWEAll)
			}
			if ls, ok := w.mls[[2]int{gx, gy}]; ok {
				ml[i] = ls
				useML = true
			} else if isComplex {
				// Блок стал multilayer: complex-переопределение
				// переносится одиночным слоем.
				ml[i] = []uint16{word}
				useML = true
			}
		}
		if useML {
			for i := range ml {
				if ml[i] == nil {
					ml[i] = defaultLayer
				}
			}
			b.addMultilayer(ml)
		} else {
			b.addComplex(cc)
		}
	}
	return b.build()
}

// worldMap декодирует мир в карту с единственным регионом.
func worldMap(w *cellWorld) *Map {
	reg, _, err := decodeRegion(w.rx, w.ry, w.build())
	if err != nil {
		panic(err)
	}
	m := &Map{}
	m.regions[w.rx*regionsY+w.ry] = reg
	return m
}

func buildCellMap(t *testing.T, w *cellWorld) *Map {
	t.Helper()
	return worldMap(w)
}

func TestValidLocationGeometries(t *testing.T) {
	tests := []struct {
		name   string
		world  func(w *cellWorld)
		from   Loc
		to     Loc
		want   Loc
		wantOK bool
	}{
		{
			name:   "плоский мир 3 ячейки",
			to:     at(3, 0, 0),
			want:   Loc{at(3, 0, 0).X, at(3, 0, 0).Y, 0},
			wantOK: true,
		},
		{
			// Шаг (4,0)→(5,0): East-флаг ячейки (4,0) закрыт —
			// порт GeoEngine.checkNearestNswe (NSWE предыдущей ячейки).
			name:  "стена поперёк — кламп до стены",
			world: func(w *cellWorld) { w.set(geoX(4), geoY(0), 0, NSWEAll&^East) },
			from:  at(0, 0, 0),
			to:    at(10, 0, 0),
			want:  cellCenter(geoX(4), geoY(0), 0),
		},
		{
			// Композит диагонали SE = A.S && A.E && B.S && D.E
			// (порт GeoEngine.checkNearestNsweAntiCornerCut): закрыт
			// ОДИН ортогональный флаг A — срез угла невозможен.
			name:  "односторонний стаб у угла 45°",
			world: func(w *cellWorld) { w.set(geoX(0), geoY(0), 0, NSWEAll&^South) },
			from:  at(0, 0, 0),
			to:    at(10, 10, 0),
			want:  cellCenter(geoX(0), geoY(0), 0),
		},
		{
			// Переходный флаг угловой ячейки B=(1,0) в сторону C:
			// композит требует B.S — фальсифицирует «не ту пару» переходов.
			name:  "переходный флаг угловой ячейки",
			world: func(w *cellWorld) { w.set(geoX(1), geoY(0), 0, NSWEAll&^South) },
			from:  at(0, 0, 0),
			to:    at(10, 10, 0),
			want:  cellCenter(geoX(0), geoY(0), 0),
		},
		{
			// L-угол: обе угловые ячейки закрыты целиком — диагональный срез
			// L-стены невозможен.
			name: "L-угол",
			world: func(w *cellWorld) {
				w.set(geoX(1), geoY(0), 0, 0)
				w.set(geoX(0), geoY(1), 0, 0)
			},
			from: at(0, 0, 0),
			to:   at(10, 10, 0),
			want: cellCenter(geoX(0), geoY(0), 0),
		},
		{
			// Двухслойный мост, перепад <1000: ближайший слой держит
			// высоту (порт MultilayerBlock.getNearestLayer).
			name: "мост двухслойный <1000 — проход под мостом",
			world: func(w *cellWorld) {
				for lx := 0; lx <= 10; lx++ {
					for ly := 0; ly <= 2; ly++ {
						w.setML(geoX(lx), geoY(ly), layer(0, NSWEAll), layer(200, NSWEAll))
					}
				}
			},
			from:   at(0, 0, 0),
			to:     at(10, 0, 0),
			want:   Loc{at(10, 0, 0).X, at(10, 0, 0).Y, 0},
			wantOK: true,
		},
		{
			name: "мост двухслойный <1000 — проход по мосту",
			world: func(w *cellWorld) {
				for lx := 0; lx <= 10; lx++ {
					for ly := 0; ly <= 2; ly++ {
						w.setML(geoX(lx), geoY(ly), layer(0, NSWEAll), layer(200, NSWEAll))
					}
				}
			},
			from:   at(0, 0, 200),
			to:     at(10, 0, 200),
			want:   Loc{at(10, 0, 200).X, at(10, 0, 200).Y, 200},
			wantOK: true,
		},
		{
			// Настил 1200 над пустотой (северный ряд — соседняя строка без гео:
			// nil-регион внутри сетки): правило 1000 возвращает
			// фантомный входной z (порт MultilayerBlock.getNearestZ +
			// getNextLowerZ без нижних слоёв). У средней ячейки (6,0) все
			// соседи вне 16 — без правила была бы стена.
			name: "настил >1000 — фантомный слой правила 1000",
			world: func(w *cellWorld) {
				for lx := 5; lx <= 7; lx++ {
					for ly := 0; ly <= 1; ly++ {
						w.setML(geoX(lx), geoY(ly), layer(1200, NSWEAll))
					}
				}
			},
			from:   at(0, 0, 0),
			to:     at(10, 0, 0),
			want:   Loc{at(10, 0, 0).X, at(10, 0, 0).Y, 0},
			wantOK: true,
		},
		{
			// Граница порога 40/41: +40 — шаг вверх (не >40), слой цели
			// совпадает, успех.
			name:   "пьедестал +40 — успех",
			world:  func(w *cellWorld) { w.setFlatBlock(geoX(8), geoY(0), 40) },
			from:   at(0, 0, 0),
			to:     at(8, 0, 40),
			want:   Loc{at(8, 0, 40).X, at(8, 0, 40).Y, 40},
			wantOK: true,
		},
		{
			// +41 > 40: сосед-грунт спасает непрерывность (виртуальный
			// слой), финальная проверка слоя цели не совпала — исходная
			// точка (порт GeoEngine.getValidLocation, финальный тернар).
			name:  "пьедестал +41 — исходная точка",
			world: func(w *cellWorld) { w.setFlatBlock(geoX(8), geoY(0), 41) },
			from:  at(0, 0, 0),
			to:    at(8, 0, 41),
			want:  Loc{at(0, 0, 0).X, at(0, 0, 0).Y, 0},
		},
		{
			// Околодиагональный луч (0,0)→(4,2), плато 3×3 на (2..4, 0..2):
			// (2,1) спасается соседом-грунтом (1,1) — виртуальный слой;
			// у (3,1) все 4 соседа подняты — стена; кламп на ячейку глубже
			// с виртуальным z (зарегистрированное отклонение от канона:
			// кламп при диагональном подходе глубже, чем у Брезенхама).
			name: "плато по диагональному подходу — кламп глубже",
			world: func(w *cellWorld) {
				for lx := 2; lx <= 4; lx++ {
					for ly := 0; ly <= 2; ly++ {
						w.set(geoX(lx), geoY(ly), 200, NSWEAll)
					}
				}
			},
			from: at(0, 0, 0),
			to:   at(4, 2, 0),
			want: cellCenter(geoX(2), geoY(1), 0),
		},
		{
			// Одноячеечный разрыв: спайк +200, сосед-грунт в пределах 16 —
			// непрерывность через соседа (порт
			// GeoEngine.hasNeighbourLayerNear, PATH_CONTINUITY_TOLERANCE).
			name:   "спайк +200 — сосед в 16 спасает",
			world:  func(w *cellWorld) { w.set(geoX(5), geoY(0), 200, NSWEAll) },
			from:   at(0, 0, 0),
			to:     at(10, 0, 0),
			want:   Loc{at(10, 0, 0).X, at(10, 0, 0).Y, 0},
			wantOK: true,
		},
		{
			// Цель без гео (регион 17 не загружен): NullRegion — движение
			// в пустоту разрешено, финал возвращает цель (порт
			// GeoEngine.getValidLocation: !hasGeoPos(prev)).
			name:   "гео-пустота — цель без гео",
			from:   at(0, 0, 0),
			to:     atGeo(testRX*regionCells+3000, geoY(0), 50),
			want:   Loc{atGeo(testRX*regionCells+3000, geoY(0), 50).X, atGeo(testRX*regionCells+3000, geoY(0), 50).Y, 50},
			wantOK: true,
		},
		{
			// Same-cell different-layer: обход не стартует, финальная
			// проверка слоя цели провалена.
			name:  "same-cell different-layer",
			world: func(w *cellWorld) { w.setML(geoX(5), geoY(5), layer(0, NSWEAll), layer(1000, NSWEAll)) },
			from:  at(5, 5, 0),
			to:    at(5, 5, 1000),
			want:  Loc{at(5, 5, 0).X, at(5, 5, 0).Y, 0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newCellWorld(testRX, testRY)
			if tt.world != nil {
				tt.world(w)
			}
			m := buildCellMap(t, w)
			got, ok := m.ValidLocation(tt.from, tt.to)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("ValidLocation(%v, %v) = %v, %v; want %v, %v",
					tt.from, tt.to, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// TestValidLocationNearestZBridgeAssert — прямая фальсификация правила 1000
// и fallback getNextLowerZ: без правила Nearestz вернула бы 1200.
func TestValidLocationNearestZBridgeAssert(t *testing.T) {
	w := newCellWorld(testRX, testRY)
	for lx := 5; lx <= 7; lx++ {
		for ly := 0; ly <= 1; ly++ {
			w.setML(geoX(lx), geoY(ly), layer(1200, NSWEAll))
		}
	}
	m := buildCellMap(t, w)
	if got := m.NearestZ(at(6, 0, 0)); got != 0 {
		t.Errorf("NearestZ(настил 1200, z=0) = %d; want 0 (фантом входного z, правило 1000)", got)
	}
	if got := m.NearestZ(at(6, 0, 100)); got != 100 {
		t.Errorf("NearestZ(настил 1200, z=100) = %d; want 100 (правило 1000, нет нижних слоёв)", got)
	}
	if got := m.NearestZ(at(6, 0, 500)); got != 1200 {
		t.Errorf("NearestZ(настил 1200, z=500) = %d; want 1200 (700 ≤ 1000 — правило не работает)", got)
	}
}

// TestValidLocationQuadrants — угловые кейсы во всех четырёх квадрантах:
// зеркальные отражения геометрии и луча дают тот же исход (фальсифицирует
// перепутанные биты N/S/E/W и знаки шагов). Флаги кассируются обоих
// семейств: к D — S/N, к B — E/W.
func TestValidLocationQuadrants(t *testing.T) {
	for name, q := range map[string][2]int{"SE": {1, 1}, "NE": {1, -1}, "SW": {-1, 1}, "NW": {-1, -1}} {
		sx, sy := q[0], q[1]
		t.Run(name, func(t *testing.T) {
			ax, ay := geoX(20), geoY(20)
			from := at(20, 20, 0)
			to := atGeo(ax+10*sx, ay+10*sy, 0)

			// Флаг A к D=(20,20+sy): South при sy>0, иначе North.
			flagY := South
			if sy < 0 {
				flagY = North
			}
			// Флаг A к B=(20+sx,20): East при sx>0, иначе West.
			flagX := East
			if sx < 0 {
				flagX = West
			}

			cases := []struct {
				name  string
				paint func(w *cellWorld)
			}{
				{"односторонний к D (S/N)", func(w *cellWorld) { w.set(ax, ay, 0, NSWEAll&^flagY) }},
				{"односторонний к B (E/W)", func(w *cellWorld) { w.set(ax, ay, 0, NSWEAll&^flagX) }},
				{"переходный B к C (S/N)", func(w *cellWorld) { w.set(ax+sx, ay, 0, NSWEAll&^flagY) }},
				{"переходный D к C (E/W)", func(w *cellWorld) { w.set(ax, ay+sy, 0, NSWEAll&^flagX) }},
				{"L-угол", func(w *cellWorld) {
					w.set(ax+sx, ay, 0, 0)
					w.set(ax, ay+sy, 0, 0)
				}},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					w := newCellWorld(testRX, testRY)
					tc.paint(w)
					m := buildCellMap(t, w)
					got, ok := m.ValidLocation(from, to)
					want := cellCenter(ax, ay, 0)
					if ok || got != want {
						t.Errorf("квадрант (%d,%d) %s: ValidLocation = %v, %v; want %v, false",
							sx, sy, tc.name, got, ok, want)
					}
				})
			}
		})
	}
}

// TestValidLocationVoidMiddle — середина луча через nil-регион: регионы 16
// и 18 загружены, 17 — пустота.
func TestValidLocationVoidMiddle(t *testing.T) {
	m := &Map{}
	for _, rx := range []int{testRX, testRX + 2} {
		reg, _, err := decodeRegion(rx, testRY, newCellWorld(rx, testRY).build())
		if err != nil {
			t.Fatalf("decodeRegion(%d): %v", rx, err)
		}
		m.regions[rx*regionsY+testRY] = reg
	}
	from := at(0, 0, 0)
	to := atGeo((testRX+2)*regionCells+4, geoY(0), 0)
	got, ok := m.ValidLocation(from, to)
	want := Loc{to.X, to.Y, 0}
	if !ok || got != want {
		t.Errorf("ValidLocation через пустоту = %v, %v; want %v, true", got, ok, want)
	}
}

// isCellCenter — точка совпадает с центром некоторой гео-ячейки.
func isCellCenter(p Loc) bool {
	return (p.X-worldMinX-worldCenter)%cellSize == 0 && (p.Y-worldMinY-worldCenter)%cellSize == 0
}

// checkMoveInvariants — машинные инварианты ValidLocation:
// ok ⇒ XY цели; ok ∧ пустая цель ⇒ res == to; !ok ⇒ исходная точка или центр
// ячейки трассы; res.XY в bbox(from, to), расширенном на ячейку.
func checkMoveInvariants(t *testing.T, m *Map, from, to, res Loc, ok bool) {
	t.Helper()
	if ok && (res.X != to.X || res.Y != to.Y) {
		t.Errorf("ValidLocation(%v, %v) = %v, true; ok требует res.XY == to.XY", from, to, res)
	}
	if ok && m.RegionAt(WorldToGeoX(to.X), WorldToGeoY(to.Y)) == nil && res != to {
		t.Errorf("ValidLocation(%v, %v) = %v; пустая цель требует res == to", from, to, res)
	}
	if !ok {
		source := Loc{from.X, from.Y, m.NearestZ(from)}
		if res != source && !isCellCenter(res) {
			t.Errorf("ValidLocation(%v, %v) = %v; ожидана исходная %v или центр ячейки", from, to, res, source)
		}
	}
	loX, hiX := min(from.X, to.X)-cellSize, max(from.X, to.X)+cellSize
	loY, hiY := min(from.Y, to.Y)-cellSize, max(from.Y, to.Y)+cellSize
	if res.X < loX || res.X > hiX || res.Y < loY || res.Y > hiY {
		t.Errorf("ValidLocation(%v, %v) = %v; вне bbox с допуском ячейки", from, to, res)
	}
}

// TestValidLocationProperties — инварианты на сетке пар поверх типовых
// миров.
func TestValidLocationProperties(t *testing.T) {
	worlds := map[string]func(w *cellWorld){
		"плоский": nil,
		"стена":   func(w *cellWorld) { w.set(geoX(4), geoY(0), 0, NSWEAll&^East) },
		"плато": func(w *cellWorld) {
			for lx := 2; lx <= 4; lx++ {
				for ly := 0; ly <= 2; ly++ {
					w.set(geoX(lx), geoY(ly), 200, NSWEAll)
				}
			}
		},
		"мост": func(w *cellWorld) {
			for ly := 0; ly <= 2; ly++ {
				w.setML(geoX(5), geoY(ly), layer(0, NSWEAll), layer(200, NSWEAll))
			}
		},
		"настил": func(w *cellWorld) {
			for lx := 5; lx <= 7; lx++ {
				for ly := 0; ly <= 1; ly++ {
					w.setML(geoX(lx), geoY(ly), layer(1200, NSWEAll))
				}
			}
		},
	}
	for name, paint := range worlds {
		t.Run(name, func(t *testing.T) {
			w := newCellWorld(testRX, testRY)
			if paint != nil {
				paint(w)
			}
			m := buildCellMap(t, w)
			for _, fx := range []int{0, 5, 10, 15} {
				for _, fy := range []int{0, 5, 10} {
					for _, z := range []int{0, 200} {
						from := at(fx, fy, z)
						for _, tx := range []int{0, 5, 10, 15} {
							for _, ty := range []int{0, 5, 10} {
								to := at(tx, ty, z)
								res, ok := m.ValidLocation(from, to)
								checkMoveInvariants(t, m, from, to, res, ok)
							}
						}
					}
				}
			}
		})
	}
}

// TestDomainContractPanics — вне сетки мира: паника-контракт с диагностикой
// (программный контракт вызывающего; прецедент канона — исключение на
// REGIONS.get). В сетке пустота паникой не карается.
func TestDomainContractPanics(t *testing.T) {
	w := newCellWorld(testRX, testRY)
	m := buildCellMap(t, w)
	inGrid := at(0, 0, 0)

	evil := []Loc{
		// Точка за кромкой на целую ячейку: деление с усечением маппит
		// малые отрицательные дельты в клетку 0 (как в каноне Java) —
		// вне сетки оказываются точки от границы минус cellSize.
		{X: worldMinX - cellSize - 1, Y: 0, Z: 0},
		{X: worldMinX + regionsX*regionCells*cellSize, Y: 0, Z: 0},
		{X: 0, Y: worldMinY - cellSize - 1, Z: 0},
		{X: 0, Y: worldMinY + regionsY*regionCells*cellSize, Z: 0},
		{X: math.MinInt32, Y: math.MinInt32, Z: 0},
		{X: math.MaxInt32, Y: math.MaxInt32, Z: 0},
		{X: math.MinInt, Y: 0, Z: 0},
		{X: 0, Y: math.MaxInt, Z: 0},
	}
	calls := []struct {
		name string
		fn   func(p Loc)
	}{
		{"ValidLocation from", func(p Loc) { m.ValidLocation(p, inGrid) }},
		{"ValidLocation to", func(p Loc) { m.ValidLocation(inGrid, p) }},
		{"CanSee from", func(p Loc) { m.CanSee(p, inGrid) }},
		{"CanSee to", func(p Loc) { m.CanSee(inGrid, p) }},
		{"NearestZ", func(p Loc) { m.NearestZ(p) }},
	}
	for _, p := range evil {
		for _, c := range calls {
			panicked, msg := tryCall(func() { c.fn(p) })
			if !panicked {
				t.Errorf("%s(%v): нет паники вне сетки мира", c.name, p)
				continue
			}
			if s, ok := msg.(string); !ok || !strings.Contains(s, "вне сетки") {
				t.Errorf("%s(%v): паника без диагностики: %v", c.name, p, msg)
			}
		}
	}
	// Пустота в сетке — легальный вход (NullRegion), паники нет.
	voidTarget := atGeo(testRX*regionCells+3000, geoY(0), 0)
	m.ValidLocation(inGrid, voidTarget)
	m.CanSee(inGrid, voidTarget)
	m.NearestZ(voidTarget)
}

// tryCall выполняет fn, возвращая факт паники и её значение.
func tryCall(fn func()) (panicked bool, msg any) {
	defer func() {
		if r := recover(); r != nil {
			panicked, msg = true, r
		}
	}()
	fn()
	return false, nil
}

// worldPoint — мировая точка внутри ячейки локальных координат с офсетом
// 0..15 от её юго-западного угла (0 и 15 — границы ячейки, 8 — центр).
func worldPoint(lx, ly, ox, oy, z int) Loc {
	return Loc{
		X: worldMinX + geoX(lx)*cellSize + ox,
		Y: worldMinY + geoY(ly)*cellSize + oy,
		Z: z,
	}
}

// TestValidLocationCornerTarget — цель ровно на углу/границе ячейки:
// обход завершается шагом в целевую ячейку (регресс вечного цикла:
// corner-пучок на конце луча заносил обход за цель).
func TestValidLocationCornerTarget(t *testing.T) {
	m := buildCellMap(t, newCellWorld(testRX, testRY))
	cases := []struct {
		name string
		to   Loc
	}{
		// Диагональ 8×8 из центра (0,0): угол (1,1) — цель = C пучка.
		{"угол C (SE)", worldPoint(1, 1, 0, 0, 0)},
		// Угол между строкой 0 и столбцом 1 при луче вниз: цель = B.
		{"угол B (NE)", worldPoint(1, 0, 0, 0, 0)},
		// Симметричный D-случай: луч влево-вверх, цель = D пучка.
		{"угол D (NW)", worldPoint(0, 1, 0, 0, 0)},
		// Граница по одной оси: цель в краевой строке, луч вдоль неё.
		{"граница Y", worldPoint(4, 0, 0, 0, 0)},
		{"граница X", worldPoint(0, 4, 0, 0, 0)},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			res, ok := m.ValidLocation(worldPoint(0, 0, 8, 8, 0), tt.to)
			if !ok || res.X != tt.to.X || res.Y != tt.to.Y || res.Z != 0 {
				t.Errorf("ValidLocation(центр (0,0), %v) = %v, %v; want точку цели, true", tt.to, res, ok)
			}
		})
	}
}

// TestValidLocationCornerHeightWall — высотная стена у диагональной ячейки
// corner-пучка: кламп в центр основания пучка A (у канона диагональный шаг
// Брезенхама клампит в ту же точку — центр предыдущей ячейки; касания B/D
// спасаются соседом A виртуальным слоем).
func TestValidLocationCornerHeightWall(t *testing.T) {
	w := newCellWorld(testRX, testRY)
	for _, c := range [][2]int{{1, 0}, {0, 1}, {1, 1}, {2, 1}, {1, 2}} {
		w.set(geoX(c[0]), geoY(c[1]), 200, NSWEAll)
	}
	m := buildCellMap(t, w)
	res, ok := m.ValidLocation(at(0, 0, 0), at(10, 10, 0))
	want := cellCenter(geoX(0), geoY(0), 0)
	if ok || res != want {
		t.Errorf("ValidLocation(высотная стена corner) = %v, %v; want %v, false", res, ok, want)
	}
}

// TestValidLocationRegionSeam — шов двух загруженных смежных регионов с
// РАЗНЫМИ высотами: каждая ячейка за швом резолвится через собственный
// регион (зеркальное чтение за швом — как у канона — вернуло бы слой
// исходного региона, и финальная проверка слоя цели дала бы исходную точку).
func TestValidLocationRegionSeam(t *testing.T) {
	m := &Map{}
	for _, rx := range []int{testRX, testRX + 1} {
		w := newCellWorld(rx, testRY)
		if rx == testRX+1 {
			w.setFlatBlock(rx*regionCells+8, geoY(0), 16)
		}
		reg, _, err := decodeRegion(rx, testRY, w.build())
		if err != nil {
			t.Fatalf("decodeRegion(%d): %v", rx, err)
		}
		m.regions[rx*regionsY+testRY] = reg
	}
	from := atGeo(testRX*regionCells+2040, geoY(0), 0)
	to := atGeo((testRX+1)*regionCells+8, geoY(0), 16)
	res, ok := m.ValidLocation(from, to)
	if !ok || res.X != to.X || res.Y != to.Y || res.Z != 16 {
		t.Errorf("ValidLocation через шов регионов = %v, %v; want цель z=16, true", res, ok)
	}
}

// TestValidLocationWorldEdge — кромка мировой сетки: сосед за кромкой —
// без гео, паники нет (регион в строке 0, луч вдоль кромки).
func TestValidLocationWorldEdge(t *testing.T) {
	w := newCellWorld(testRX, 0)
	for lx := 5; lx <= 7; lx++ {
		for ly := 0; ly <= 1; ly++ {
			w.setML(geoX(lx), ly, layer(1200, NSWEAll))
		}
	}
	reg, _, err := decodeRegion(testRX, 0, w.build())
	if err != nil {
		t.Fatalf("decodeRegion: %v", err)
	}
	m := &Map{}
	m.regions[testRX*regionsY] = reg
	from := Loc{X: GeoToWorldX(geoX(0)), Y: GeoToWorldY(0), Z: 0}
	to := Loc{X: GeoToWorldX(geoX(10)), Y: GeoToWorldY(0), Z: 0}
	res, ok := m.ValidLocation(from, to)
	// Настил >1000 над кромкой: правило 1000 даёт фантомный слой 0,
	// сосед-строка за кромкой (вне сетки) гео не даёт — но правило
	// срабатывает раньше ветки соседа.
	if !ok || res.X != to.X || res.Y != to.Y || res.Z != 0 {
		t.Errorf("ValidLocation вдоль кромки = %v, %v; want цель z=0, true", res, ok)
	}
}

// TestValidLocationNonCenterPoints — нецентровые точки: кламп-точка и
// терминация не зависят от выравнивания входов по центрам ячеек.
func TestValidLocationNonCenterPoints(t *testing.T) {
	w := newCellWorld(testRX, testRY)
	w.set(geoX(4), geoY(0), 0, NSWEAll&^East)
	m := buildCellMap(t, w)
	// Стена поперёк, входы с офсетами: кламп — центр ячейки (4, 0)
	// независимо от точки входа.
	want := cellCenter(geoX(4), geoY(0), 0)
	for _, ox := range []int{0, 3, 8, 15} {
		for _, oy := range []int{0, 3, 8, 15} {
			from := worldPoint(0, 0, ox, oy, 0)
			to := worldPoint(10, 0, ox, oy, 0)
			res, ok := m.ValidLocation(from, to)
			if ok || res != want {
				t.Errorf("офсет (%d,%d): ValidLocation = %v, %v; want %v, false", ox, oy, res, ok, want)
			}
		}
	}
	// Инварианты на произвольных парах нецентровых точек.
	for _, fx := range []int{1, 7, 14} {
		for _, fy := range []int{2, 9, 13} {
			for _, tx := range []int{0, 5, 11} {
				for _, ty := range []int{4, 10, 15} {
					from := worldPoint(0, 0, fx, fy, 0)
					to := worldPoint(tx, ty, 12, 6, 0)
					res, ok := m.ValidLocation(from, to)
					checkMoveInvariants(t, m, from, to, res, ok)
				}
			}
		}
	}
}
