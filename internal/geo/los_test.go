package geo

import "testing"

// LOS-кейсы: порты GeoEngine.canSeeTarget / getLosGeoZ (атрибуция в
// комментариях кейсов).

func TestCanSee(t *testing.T) {
	tests := []struct {
		name  string
		world func(w *cellWorld)
		from  Loc
		to    Loc
		want  bool
	}{
		{
			name: "ровная плоскость 20 ячеек",
			from: at(0, 0, 0),
			to:   at(20, 0, 0),
			want: true,
		},
		{
			// losGeoZ поднятой ячейки (200) выше потолка zЛуча+48 —
			// порт GeoEngine.canSeeTarget: перекрытие при строгом >.
			name:  "стена перекрывает",
			world: func(w *cellWorld) { w.set(geoX(10), geoY(0), 200, NSWEAll) },
			from:  at(0, 0, 0),
			to:    at(20, 0, 0),
		},
		{
			// Граница обзора: рост +48 к лучу виден (<=), +49 скрыт —
			// высота 48 кратна кванту complex; 49 — flat-блок (int16).
			// Цель — за блоком на грунте, иначе слой цели разворачивает
			// луч обменом концов.
			name:  "рост +48 виден",
			world: func(w *cellWorld) { w.setFlatBlock(geoX(8), geoY(0), 48) },
			from:  at(0, 0, 0),
			to:    at(16, 0, 0),
			want:  true,
		},
		{
			name:  "рост +49 скрыт",
			world: func(w *cellWorld) { w.setFlatBlock(geoX(8), geoY(0), 49) },
			from:  at(0, 0, 0),
			to:    at(16, 0, 0),
			want:  false,
		},
		{
			// «С высоты источника»: первые elevatedSeeOverDistance точек
			// видны от nearestFromZ+48 — шип 1024 у первой точки виден
			// (без окна был бы скрыт: zЛуча(1,0)+48 < 1024).
			name: "шип у наблюдателя виден из окна высоты источника",
			world: func(w *cellWorld) {
				w.set(geoX(0), geoY(0), 1000, NSWEAll)
				w.set(geoX(1), geoY(0), 1024, NSWEAll)
			},
			from: at(0, 0, 1000),
			to:   at(12, 0, 0),
			want: true,
		},
		{
			// Вне окна потолок = zЛуча+48: шип 896 у третьей точки скрыт.
			name: "шип вне окна скрыт",
			world: func(w *cellWorld) {
				w.set(geoX(0), geoY(0), 1000, NSWEAll)
				w.set(geoX(3), geoY(0), 896, NSWEAll)
			},
			from: at(0, 0, 1000),
			to:   at(12, 0, 0),
			want: false,
		},
		{
			// Диагональная ветка corner-пучка (порт canSeeTarget, ветки
			// NE/NW/SE/SW): флаг A.South закрыт → eastGeoZ резолвится
			// через getNextHigherZ угловой ячейки (слой 56 > 48;
			// контроль: ближайший слой −8) — перекрытие.
			name: "corner-луч: перпендикулярный флаг закрыт — скрыто",
			world: func(w *cellWorld) {
				w.set(geoX(0), geoY(0), 0, NSWEAll&^South)
				w.setML(geoX(1), geoY(0), layer(-8, NSWEAll), layer(56, NSWEAll))
			},
			from: at(0, 0, 0),
			to:   at(10, 10, 0),
			want: false,
		},
		{
			// Контроль: флаг открыт — eastGeoZ через getNearestZ (−8).
			name: "corner-луч: флаг открыт — видно",
			world: func(w *cellWorld) {
				w.setML(geoX(1), geoY(0), layer(-8, NSWEAll), layer(56, NSWEAll))
			},
			from: at(0, 0, 0),
			to:   at(10, 10, 0),
			want: true,
		},
		{
			// Same-cell: быстрый путь — слой не совпал.
			name:  "same-cell different-layer",
			world: func(w *cellWorld) { w.setML(geoX(5), geoY(5), layer(0, NSWEAll), layer(1000, NSWEAll)) },
			from:  at(5, 5, 0),
			to:    at(5, 5, 1000),
			want:  false,
		},
		{
			name:  "same-cell same-layer",
			world: func(w *cellWorld) { w.setML(geoX(5), geoY(5), layer(0, NSWEAll), layer(1000, NSWEAll)) },
			from:  at(5, 5, 0),
			to:    at(5, 5, 0),
			want:  true,
		},
		{
			// Обмен концов «источник выше»: наблюдатель снизу, цель на
			// комплексной ячейке 1000 — луч разворачивается сверху вниз.
			name:  "обмен концов — снизу вверх видно",
			world: func(w *cellWorld) { w.set(geoX(12), geoY(0), 1000, NSWEAll) },
			from:  at(0, 0, 0),
			to:    at(12, 0, 1000),
			want:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newCellWorld(testRX, testRY)
			if tt.world != nil {
				tt.world(w)
			}
			m := buildCellMap(t, w)
			if got := m.CanSee(tt.from, tt.to); got != tt.want {
				t.Errorf("CanSee(%v, %v) = %v; want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}

// TestCanSeeVoidMiddle — LOS через nil-регион: безгео-точки переносят
// текущий losGeoZ (порт canSeeTarget: инкремент счётчика точек — по всем
// XY-точкам, включая безгео).
func TestCanSeeVoidMiddle(t *testing.T) {
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
	if !m.CanSee(from, to) {
		t.Errorf("CanSee(%v, %v) = false; пустота в середине не перекрывает", from, to)
	}
}

// TestCanSeeReflexivity — CanSee(from, from) == true на любых мирах
// (быстрый путь той же ячейки канона).
func TestCanSeeReflexivity(t *testing.T) {
	worlds := []func(w *cellWorld){
		nil,
		func(w *cellWorld) { w.set(geoX(4), geoY(0), 200, NSWEAll) },
		func(w *cellWorld) { w.setML(geoX(5), geoY(5), layer(0, NSWEAll), layer(1000, NSWEAll)) },
	}
	for i, paint := range worlds {
		w := newCellWorld(testRX, testRY)
		if paint != nil {
			paint(w)
		}
		m := buildCellMap(t, w)
		for _, p := range []Loc{at(3, 3, 0), at(3, 3, 500), at(10, 2, -100)} {
			if !m.CanSee(p, p) {
				t.Errorf("мир %d: CanSee(%v, %v) = false; want true", i, p, p)
			}
		}
	}
}

// TestCanSeeElevatedWindowDiagonal — окно высоты источника на точном
// диагональном луче: первые пройденные ячейки видны от высоты источника,
// касания пучка окно не расходуют (у обхода канона точек-касаний нет).
func TestCanSeeElevatedWindowDiagonal(t *testing.T) {
	w := newCellWorld(testRX, testRY)
	w.set(geoX(0), geoY(0), 1000, NSWEAll)
	w.set(geoX(1), geoY(1), 1016, NSWEAll)
	m := buildCellMap(t, w)
	if !m.CanSee(at(0, 0, 1000), at(10, 10, 0)) {
		t.Errorf("шип 1016 у первой диагональной ячейки скрыт; want виден (окно fromZ+48)")
	}

	w2 := newCellWorld(testRX, testRY)
	w2.set(geoX(0), geoY(0), 1000, NSWEAll)
	w2.set(geoX(3), geoY(3), 800, NSWEAll)
	m2 := buildCellMap(t, w2)
	if m2.CanSee(at(0, 0, 1000), at(10, 10, 0)) {
		t.Errorf("шип 800 у четвёртой диагональной ячейки виден; want скрыт (окно исчерпано)")
	}
}

// TestCanSeeCornerQuadrants — диагональная ветка LOS во всех четырёх
// квадрантах: перпендикулярный флаг источника закрыт — касательная ячейка
// резолвится через getNextHigherZ и перекрывает; флаг открыт — видно.
func TestCanSeeCornerQuadrants(t *testing.T) {
	for name, q := range map[string][2]int{"SE": {1, 1}, "NE": {1, -1}, "SW": {-1, 1}, "NW": {-1, -1}} {
		sx, sy := q[0], q[1]
		t.Run(name, func(t *testing.T) {
			ax, ay := geoX(20), geoY(20)
			from := at(20, 20, 0)
			to := atGeo(ax+10*sx, ay+10*sy, 0)
			// Флаг источника к B=(20+sx,20): S/N-семейство.
			flagY := South
			if sy < 0 {
				flagY = North
			}
			// Флаг источника к D=(20,20+sy): E/W-семейство.
			flagX := East
			if sx < 0 {
				flagX = West
			}
			// Угловая ячейка: нижний слой −8 (ближайший), верхний 56.
			raise := func(w *cellWorld, corner [2]int) {
				w.setML(corner[0], corner[1], layer(-8, NSWEAll), layer(56, NSWEAll))
			}
			cases := []struct {
				name   string
				closed *NSWE
				corner [2]int
				want   bool
			}{
				{"флаг к B закрыт (S/N)", &flagY, [2]int{ax + sx, ay}, false},
				{"флаг к D закрыт (E/W)", &flagX, [2]int{ax, ay + sy}, false},
				{"флаги открыты", nil, [2]int{ax + sx, ay}, true},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					w := newCellWorld(testRX, testRY)
					if tc.closed != nil {
						w.set(ax, ay, 0, NSWEAll&^*tc.closed)
					}
					raise(w, tc.corner)
					m := buildCellMap(t, w)
					if got := m.CanSee(from, to); got != tc.want {
						t.Errorf("квадрант %s %s: CanSee = %v; want %v", name, tc.name, got, tc.want)
					}
				})
			}
		})
	}
}
