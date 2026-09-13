package geo

// stepKind классифицирует эмиссию supercover-обхода.
type stepKind uint8

const (
	stepAxis stepKind = iota // ортогональный шаг через пересечённую границу
	touchX                   // угловое касание первой осью
	touchY                   // угловое касание второй осью
	stepDiag                 // диагональная ячейка corner-пучка
)

// stepper — supercover-обход луча по гео-ячейкам (алгоритм
// Amanatides–Woo в целочисленной форме). Эмиссия — каждый переход границы;
// луч, проходящий точно через общий угол четырёх ячеек (corner-событие,
// целочисленное равенство параметров пересечения через перекрёстное
// умножение), эмитит ОБЕ угловые ячейки и диагональную: диагональных
// переходов не возникает, обе грани угла проверяются своей стороной.
// z луча интерполируется целочисленно по параметру пересечения. Без
// аллокаций; обе точки луча обязаны лежать в сетке мира (гарантия
// вызывающего).
type stepper struct {
	cx, cy   int // текущая ячейка
	tx, ty   int // конечная ячейка
	sx, sy   int // шаги осей: −1, 0, +1 (0 — ось исчерпана)
	ex, ey   int // юниты до следующей границы по каждой оси
	adx, ady int
	zFrom    int
	dz       int

	// corner-пучок: основание, направления и z в точке пересечения.
	phase    int
	bcx, bcy int
	bsx, bsy int
	zc       int
}

func newStepper(from, to Loc, zFrom, zTo int) stepper {
	gx, gy := WorldToGeoX(from.X), WorldToGeoY(from.Y)
	s := stepper{
		cx: gx, cy: gy,
		tx: WorldToGeoX(to.X), ty: WorldToGeoY(to.Y),
		adx: abs(to.X - from.X), ady: abs(to.Y - from.Y),
		zFrom: zFrom, dz: zTo - zFrom,
	}
	if to.X > from.X {
		s.sx = 1
	} else if to.X < from.X {
		s.sx = -1
	}
	if to.Y > from.Y {
		s.sy = 1
	} else if to.Y < from.Y {
		s.sy = -1
	}
	if s.sx > 0 {
		s.ex = worldMinX + (gx+1)*cellSize - from.X
	} else if s.sx < 0 {
		s.ex = from.X - (worldMinX + gx*cellSize)
	}
	if s.sy > 0 {
		s.ey = worldMinY + (gy+1)*cellSize - from.Y
	} else if s.sy < 0 {
		s.ey = from.Y - (worldMinY + gy*cellSize)
	}
	return s
}

// next эмитит следующую ячейку обхода; ok=false после конечной ячейки.
func (s *stepper) next() (x, y, z int, kind stepKind, ok bool) {
	switch s.phase {
	case 2: // угловое касание второй осью (ячейка D)
		s.phase = 3
		return s.bcx, s.bcy + s.bsy, s.zc, touchY, true
	case 3: // диагональная ячейка пучка — продвигаем обе оси
		s.phase = 0
		s.cx, s.cy = s.bcx+s.bsx, s.bcy+s.bsy
		s.ex += cellSize
		s.ey += cellSize
		s.arrive()
		return s.cx, s.cy, s.zc, stepDiag, true
	}
	if s.cx == s.tx && s.cy == s.ty {
		return 0, 0, 0, stepAxis, false
	}
	switch {
	case s.sx == 0:
		return s.advanceY()
	case s.sy == 0:
		return s.advanceX()
	}
	// Сравнение параметров пересечений tX = ex/adx и tY = ey/ady —
	// перекрёстным умножением (int64), без float и без деления; равенство
	// — corner-событие. Произведения ≤ ~16·10¹² — запас int64 огромен.
	txNum := int64(s.ex) * int64(s.ady)
	tyNum := int64(s.ey) * int64(s.adx)
	switch {
	case txNum < tyNum:
		return s.advanceX()
	case txNum > tyNum:
		return s.advanceY()
	default:
		// Corner точно на конце луча: конечная ячейка совпадает с
		// касанием пучка — обход завершается обычным шагом в цель;
		// заходящие за конец луча касания не эмитируются (итератор
		// канона останавливается на конечной ячейке).
		if s.cx+s.sx == s.tx && s.cy == s.ty {
			return s.advanceX()
		}
		if s.cx == s.tx && s.cy+s.sy == s.ty {
			return s.advanceY()
		}
		s.bcx, s.bcy, s.bsx, s.bsy = s.cx, s.cy, s.sx, s.sy
		s.zc = s.rayZ(s.ex, s.adx)
		s.phase = 2 // B эмитится ниже; следующая эмиссия пучка — D
		return s.bcx + s.bsx, s.bcy, s.zc, touchX, true
	}
}

func (s *stepper) advanceX() (int, int, int, stepKind, bool) {
	z := s.rayZ(s.ex, s.adx)
	s.cx += s.sx
	s.ex += cellSize
	s.arrive()
	return s.cx, s.cy, z, stepAxis, true
}

func (s *stepper) advanceY() (int, int, int, stepKind, bool) {
	z := s.rayZ(s.ey, s.ady)
	s.cy += s.sy
	s.ey += cellSize
	s.arrive()
	return s.cx, s.cy, z, stepAxis, true
}

// arrive фиксирует исчерпание оси: дальнейшие шаги по ней невозможны.
func (s *stepper) arrive() {
	if s.cx == s.tx {
		s.sx = 0
	}
	if s.cy == s.ty {
		s.sy = 0
	}
}

// rayZ — z луча в точке пересечения: расстояние d по оси длиной a
// (целочисленное усечение к нулю; a > 0 — ось активна).
func (s *stepper) rayZ(d, a int) int {
	if s.dz == 0 {
		return s.zFrom
	}
	return s.zFrom + int(int64(s.dz)*int64(d)/int64(a))
}
