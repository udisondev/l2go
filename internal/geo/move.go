package geo

// Loc — точка в мировых координатах.
type Loc struct {
	X, Y, Z int
}

// NearestZ возвращает высоту слоя, ближайшего к z точки (заглушка красной
// фазы).
func (m *Map) NearestZ(at Loc) int { return 0 }

// ValidLocation возвращает дальнюю достижимую точку и признак прибытия
// (заглушка красной фазы).
func (m *Map) ValidLocation(from, to Loc) (Loc, bool) { return Loc{}, false }

// CanSee проверяет линию видимости (заглушка красной фазы).
func (m *Map) CanSee(from, to Loc) bool { return false }

// cellCenter возвращает Loc центра гео-ячейки (заглушка красной фазы).
func cellCenter(gx, gy, z int) Loc { return Loc{} }
