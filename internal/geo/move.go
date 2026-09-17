package geo

import "fmt"

// Пороги канона (порт констант GeoEngine канона Mobius Interlude).
const (
	heightIncreaseLimit     = 40   // HEIGHT_INCREASE_LIMIT
	maxSeeOverHeight        = 48   // MAX_SEE_OVER_HEIGHT
	elevatedSeeOverDistance = 2    // ELEVATED_SEE_OVER_DISTANCE
	pathContinuityTolerance = 16   // PATH_CONTINUITY_TOLERANCE
	layerDropLimit          = 1000 // правило MultilayerBlock.getNearestZ
)

// Loc — точка в мировых координатах. Z — высота; допустимы значения
// клиентского диапазона (int32): вне высот мира используется как есть,
// экстремальные значения вне диапазона не поддерживаются.
type Loc struct {
	X, Y, Z int
}

// geoOf переводит мировую точку в гео-ячейку. Точка обязана лежать в сетке
// мира — программный контракт вызывающего (недоверенные координаты
// валидируются до вызова); нарушение — паника с диагностикой, как у
// Region.CellAt. Прецедент канона: GeoEngine.getRegion за границами сетки
// бросает исключение. Точки в полосе до cellSize за западной/северной
// границей целочисленным усечением отображаются в краевую ячейку — так же,
// как в каноне (целочисленное деление Java).
func geoOf(x, y int) (int, int) {
	gx, gy := WorldToGeoX(x), WorldToGeoY(y)
	if uint(gx) >= regionsX*regionCells || uint(gy) >= regionsY*regionCells {
		panic(fmt.Sprintf("geo: мировая точка (%d, %d) вне сетки мира", x, y))
	}
	return gx, gy
}

// InWorld сообщает, лежит ли мировая точка в сетке мира. Обратна паник-условию
// geoOf (та же арифметика WorldToGeoX/Y и та же граница): потребитель обязан
// проверять недоверенные координаты до контракта geoOf — иначе злой вход на
// кромке сетки уронит шаг региона паникой (порт границы GeoEngine.getRegion).
func InWorld(x, y int) bool {
	gx, gy := WorldToGeoX(x), WorldToGeoY(y)
	return uint(gx) < regionsX*regionCells && uint(gy) < regionsY*regionCells
}

// hasGeo сообщает, есть ли геодата у ячейки (соседи за кромкой сетки —
// нет гео; порт GeoEngine.hasGeoPos).
func (m *Map) hasGeo(gx, gy int) bool {
	return m.RegionAt(gx, gy) != nil
}

// nearestZ — высота слоя, ближайшего к z, с правилом 1000; ячейка без
// гео — сам z (порт GeoEngine.getNearestZ, NullRegion).
func (m *Map) nearestZ(gx, gy, z int) int {
	r := m.RegionAt(gx, gy)
	if r == nil {
		return z
	}
	return nearestZLimited(r.CellAt(gx, gy), z)
}

// nearestZLimited — правило 1000 живёт только в multilayer-ячейках (порт
// MultilayerBlock.getNearestZ: ближайший слой выше z более чем на 1000 —
// взять слой ниже); flat/complex — единственный слой без правила.
func nearestZLimited(c Cell, z int) int {
	h, _ := c.Nearest(z)
	if c.BlockType() == BlockMultilayer && h-z > layerDropLimit {
		return c.LowerZ(z)
	}
	return h
}

// higherZ — слой не ниже z; без гео — сам z (порт
// GeoEngine.getNextHigherZ, NullRegion).
func (m *Map) higherZ(gx, gy, z int) int {
	if r := m.RegionAt(gx, gy); r != nil {
		return r.CellAt(gx, gy).HigherZ(z)
	}
	return z
}

// checkNSWE — разрешён ли переход из ячейки в направлении dir с высоты z:
// флаги ближайшего к z слоя без правила 1000 (порт
// GeoEngine.checkNearestNswe → MultilayerBlock.getNearestNSWE). Ячейка без
// гео — разрешён (NullRegion).
func (m *Map) checkNSWE(gx, gy, z int, dir NSWE) bool {
	r := m.RegionAt(gx, gy)
	if r == nil {
		return true
	}
	_, nswe := r.CellAt(gx, gy).Nearest(z)
	return nswe&dir == dir
}

// computeNswe — направление перехода между ячейками (порт
// GeoUtils.computeNswe: восток — x+1, юг — y+1; диагональ — составное
// направление).
func computeNswe(px, py, cx, cy int) NSWE {
	var nswe NSWE
	if cx > px {
		nswe |= East
	} else if cx < px {
		nswe |= West
	}
	if cy > py {
		nswe |= South
	} else if cy < py {
		nswe |= North
	}
	return nswe
}

// checkNSWEAntiCornerCut — движение из ячейки в направлении dir с учётом
// анти-среза диагонали (порт GeoEngine.checkNearestNsweAntiCornerCut):
// составное направление требует оба ортогональных флага самой ячейки и оба
// переходных флага ячеек угла.
func (m *Map) checkNSWEAntiCornerCut(gx, gy, z int, dir NSWE) bool {
	switch dir {
	case North | East:
		if !m.checkNSWE(gx, gy-1, z, East) || !m.checkNSWE(gx+1, gy, z, North) {
			return false
		}
	case North | West:
		if !m.checkNSWE(gx, gy-1, z, West) || !m.checkNSWE(gx-1, gy, z, North) {
			return false
		}
	case South | East:
		if !m.checkNSWE(gx, gy+1, z, East) || !m.checkNSWE(gx+1, gy, z, South) {
			return false
		}
	case South | West:
		if !m.checkNSWE(gx, gy+1, z, West) || !m.checkNSWE(gx-1, gy, z, South) {
			return false
		}
	}
	return m.checkNSWE(gx, gy, z, dir)
}

// hasNeighbourLayerNear — есть ли у одного из четырёх NSWE-соседей ячейки
// слой в пределах tol от z (порт GeoEngine.hasNeighbourLayerNear; соседи
// без гео не считаются, сосед предыдущей ячейки учитывается).
func (m *Map) hasNeighbourLayerNear(gx, gy, z, tol int) bool {
	for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
		nx, ny := gx+d[0], gy+d[1]
		if m.hasGeo(nx, ny) && abs(m.nearestZ(nx, ny, z)-z) <= tol {
			return true
		}
	}
	return false
}

// losGeoZ — гео-высота для проверки LOS: переход prev→cur открыт — ближайший
// слой, закрыт — слой выше (порт GeoEngine.getLosGeoZ).
func (m *Map) losGeoZ(px, py, pz, cx, cy int, dir NSWE) int {
	if m.checkNSWEAntiCornerCut(px, py, pz, dir) {
		return m.nearestZ(cx, cy, pz)
	}
	return m.higherZ(cx, cy, pz)
}

// cellCenter — Loc центра гео-ячейки.
func cellCenter(gx, gy, z int) Loc {
	return Loc{X: GeoToWorldX(gx), Y: GeoToWorldY(gy), Z: z}
}

// NearestZ возвращает высоту слоя, ближайшего к z точки: в multilayer — с
// правилом 1000 (слой больше чем на 1000 выше — взять слой ниже); без
// геодаты — сам z. Порт GeoEngine.getNearestZ.
func (m *Map) NearestZ(at Loc) int {
	gx, gy := geoOf(at.X, at.Y)
	return m.nearestZ(gx, gy, at.Z)
}

// ValidLocation возвращает дальнюю достижимую точку от from к to и признак
// прибытия: ok ⇒ res.XY == to.XY и слой цели совпал; отказ — кламп в центр
// последней валидной ячейки либо исходная точка при несовпадении слоя цели
// (в том же cell different-layer — ok=false при res.XY == to.XY). Порт
// GeoEngine.getValidLocation (двери и заборы вне задачи). Обе точки — в
// сетке мира (контракт geoOf).
func (m *Map) ValidLocation(from, to Loc) (Loc, bool) {
	gx, gy := geoOf(from.X, from.Y)
	tx, ty := geoOf(to.X, to.Y)
	fromZ := m.nearestZ(gx, gy, from.Z)
	toZ := m.nearestZ(tx, ty, to.Z)

	st := newStepper(from, to, 0, 0)
	prevX, prevY, prevZ := gx, gy, fromZ
	for {
		cx, cy, _, kind, ok := st.next()
		if !ok {
			break
		}
		if kind == touchX || kind == touchY {
			// Угловое касание: высотная проверка касания; NSWE обоих
			// переходов покрывается композицией диагональной ячейки
			// (checkNSWEAntiCornerCut). Позиция обхода не двигается —
			// кламп остаётся у основания пучка.
			if rawZ := m.nearestZ(cx, cy, prevZ); m.heightWall(cx, cy, prevZ, rawZ) {
				return cellCenter(prevX, prevY, prevZ), false
			}
			continue
		}
		curZ := m.nearestZ(cx, cy, prevZ)
		if curZ-prevZ > heightIncreaseLimit {
			// Одноячеечный разрыв слоя: непрерывность через соседа
			// (порт GeoEngine.getValidLocation, ветка >40).
			if m.heightWall(cx, cy, prevZ, curZ) {
				return cellCenter(prevX, prevY, prevZ), false
			}
			curZ = prevZ
		}
		if m.hasGeo(prevX, prevY) {
			if !m.checkNSWEAntiCornerCut(prevX, prevY, prevZ, computeNswe(prevX, prevY, cx, cy)) {
				return cellCenter(prevX, prevY, prevZ), false
			}
		}
		prevX, prevY, prevZ = cx, cy, curZ
	}
	if m.hasGeo(prevX, prevY) && prevZ != toZ {
		return Loc{X: from.X, Y: from.Y, Z: fromZ}, false
	}
	return Loc{X: to.X, Y: to.Y, Z: toZ}, true
}

// heightWall — резкий подъём rawZ без непрерывности: у ячейки нет слоя в
// пределах pathContinuityTolerance ни у одного NSWE-соседа.
func (m *Map) heightWall(gx, gy, prevZ, rawZ int) bool {
	if rawZ-prevZ <= heightIncreaseLimit {
		return false
	}
	return !m.hasNeighbourLayerNear(gx, gy, prevZ, pathContinuityTolerance)
}

// CanSee проверяет линию видимости между точками: обход луча по ячейкам,
// рельеф выше линии луча (с допуском maxSeeOverHeight) перекрывает.
// Порт GeoEngine.canSeeTarget (двери и заборы вне задачи; обмен концов —
// источник выше; первые elevatedSeeOverDistance точек видны от высоты
// источника). Обе точки — в сетке мира (контракт geoOf).
func (m *Map) CanSee(from, to Loc) bool {
	gx, gy := geoOf(from.X, from.Y)
	tx, ty := geoOf(to.X, to.Y)
	fromZ := m.nearestZ(gx, gy, from.Z)
	toZ := m.nearestZ(tx, ty, to.Z)

	// Быстрый путь той же ячейки (порт canSeeTarget).
	if gx == tx && gy == ty {
		return !m.hasGeo(tx, ty) || fromZ == toZ
	}
	// Источник выше: обмен концов.
	if toZ > fromZ {
		from, to = to, from
		gx, gy, tx, ty = tx, ty, gx, gy
		fromZ, toZ = toZ, fromZ
	}

	st := newStepper(from, to, fromZ, toZ)
	prevX, prevY, prevZ := gx, gy, fromZ
	pointIndex := 0
	for {
		cx, cy, zRay, kind, ok := st.next()
		if !ok {
			break
		}
		curZ := prevZ
		if m.hasGeo(cx, cy) {
			dir := computeNswe(prevX, prevY, cx, cy)
			curZ = m.losGeoZ(prevX, prevY, prevZ, cx, cy, dir)
			maxHeight := zRay + maxSeeOverHeight
			if pointIndex < elevatedSeeOverDistance {
				maxHeight = fromZ + maxSeeOverHeight
			}
			if !m.canSeeThrough(prevX, prevY, prevZ, curZ, dir, zRay, maxHeight) {
				return false
			}
		}
		if kind == touchX || kind == touchY {
			// Касание угла — проверенная, но не пройденная точка: позиция
			// и z обзора не двигаются; окно высоты источника не
			// расходуется (у обхода канона точек-касаний нет — счётчик
			// точек растёт только на пройденных ячейках).
			continue
		}
		prevX, prevY, prevZ = cx, cy, curZ
		pointIndex++
	}
	return true
}

// canSeeThrough — перекрывает ли ячейка линию: собственная гео-высота
// (curZ) и, на диагональном переходе, обе касательные ячейки угла с их
// переходными гейтами (порт GeoEngine.canSeeTarget, ветки NE/NW/SE/SW).
func (m *Map) canSeeThrough(px, py, pz, curZ int, dir NSWE, zRay, maxHeight int) bool {
	if curZ > maxHeight {
		return false
	}
	var c1x, c1y, c2x, c2y int
	var gate1, gate2 NSWE
	switch dir {
	case North | East:
		c1x, c1y, gate1 = px, py-1, East
		c2x, c2y, gate2 = px+1, py, North
	case North | West:
		c1x, c1y, gate1 = px, py-1, West
		c2x, c2y, gate2 = px-1, py, North
	case South | East:
		c1x, c1y, gate1 = px, py+1, East
		c2x, c2y, gate2 = px+1, py, South
	case South | West:
		c1x, c1y, gate1 = px, py+1, West
		c2x, c2y, gate2 = px-1, py, South
	default:
		return true
	}
	z1 := m.losGeoZ(px, py, pz, c1x, c1y, gate1)
	z2 := m.losGeoZ(px, py, pz, c2x, c2y, gate2)
	return z1 <= maxHeight && z2 <= maxHeight &&
		z1 <= m.nearestZ(c1x, c1y, zRay) && z2 <= m.nearestZ(c2x, c2y, zRay)
}
