package replica

import "fmt"

// maxCellAxis — верх домена сетки по оси: клетки 0..0x7FFF, старший бит
// CellID зарезервирован сентинелом cellInvalid.
const maxCellAxis = 1<<15 - 1

// cellInvalid — сентинел свободного слота в сид-таблице блоба: нулевая
// клетка валидна (угол мира), «нет клетки» обязано отличаться от нуля.
const cellInvalid CellID = 1 << 31

// DefaultCellShift — сторона ячейки AoI по умолчанию: 2¹³ = 8192 ≥ Exit 4200
// канона — гистерезисное кольцо (3500, 4200] целиком помещается в окно 3×3.
// Единая точка константы для фабрик-инварианта cellSize ≥ Exit; точный
// размер — фаза 6 по метрикам.
const DefaultCellShift = 13

// Grid — глобальная выровненная сетка AoI (значение): ячейка ≥ радиуса
// обзора, подписка наблюдателя — константный стенсил 3×3. Сетка выровнена
// глобально, а не по границам регионов: окно наблюдителя не зависит от того,
// как регионы делят мир. Конструируется NewGrid.
type Grid struct {
	minX  int32
	minY  int32
	shift uint
}

// NewGrid создаёт сетку с началом (minX, minY) и стороной ячейки
// 1<<cellShift. Контракт cellShift ≤ 31: нарушение — паника (аргумент
// программиста-конструктора, недоверенным входом быть не может — сетка
// строится из констант домена один раз при старте региона).
func NewGrid(minX, minY int32, cellShift uint) Grid {
	if cellShift > 31 {
		panic(fmt.Sprintf("replica: NewGrid: cellShift %d > 31 (контракт конструктора)", cellShift))
	}
	return Grid{minX: minX, minY: minY, shift: cellShift}
}

// cellSize — сторона ячейки (int64: сравнение с Exit без переполнения).
func (g Grid) cellSize() int64 { return int64(1) << g.shift }

// CellOf — ячейка позиции. Арифметика в int64: края ±(2³¹−1) не
// переполняют вычитанием; вне домена — детерминированный кламп к краевой
// клетке оси (описанное поведение: CellOf тотален, паник/мусора нет).
func (g Grid) CellOf(x, y int32) CellID {
	gx := clampCellAxis((int64(x) - int64(g.minX)) >> g.shift)
	gy := clampCellAxis((int64(y) - int64(g.minY)) >> g.shift)
	return CellID(uint32(gx)<<16 | uint32(gy))
}

func clampCellAxis(v int64) int32 {
	switch {
	case v < 0:
		return 0
	case v > maxCellAxis:
		return maxCellAxis
	default:
		return int32(v)
	}
}

// windowInto — клетки окна 3×3 вокруг c (соседи за краем домена
// пропускаются); возвращает число записанных клеток. Стенсил константный:
// подписка наблюдателя не зависит от плотности населения.
func windowInto(c CellID, buf *[9]CellID) int {
	gx, gy := int32(c>>16), int32(c&0xFFFF)
	n := 0
	for dx := int32(-1); dx <= 1; dx++ {
		x := gx + dx
		if x < 0 || x > maxCellAxis {
			continue
		}
		for dy := int32(-1); dy <= 1; dy++ {
			y := gy + dy
			if y < 0 || y > maxCellAxis {
				continue
			}
			buf[n] = CellID(uint32(x)<<16 | uint32(y))
			n++
		}
	}
	return n
}
