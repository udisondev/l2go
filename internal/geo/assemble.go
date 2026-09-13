package geo

import "fmt"

// MapFile отображает файл только для чтения в память, вне Go-кучи. Слайс
// валиден до вызова unmap; владелец отображения — вызывающий. Потолок размера
// — обязанность вызывающего: LoadDir проверяет потолок файла региона до
// отображения, артефакт статики гвардит заголовком против длины отображения.
func MapFile(path string) ([]byte, func() error, error) {
	return mapFile(path)
}

// NewMapFromRegions собирает карту из декодированных регионов. Повтор слота
// региона — ошибка. Карта после возврата не мутируется: чтение из любой
// горутины без синхронизации.
func NewMapFromRegions(regions []*Region) (*Map, error) {
	m := &Map{}
	for _, r := range regions {
		if r == nil {
			return nil, fmt.Errorf("geo: нулевой регион в списке сборки")
		}
		if uint(r.rx) >= regionsX || uint(r.ry) >= regionsY {
			return nil, fmt.Errorf("geo: регион (%d, %d) вне сетки %d×%d", r.rx, r.ry, regionsX, regionsY)
		}
		idx := r.rx*regionsY + r.ry
		if m.regions[idx] != nil {
			return nil, fmt.Errorf("geo: регион (%d, %d) уже установлен", r.rx, r.ry)
		}
		m.regions[idx] = r
	}
	return m, nil
}

// EachRegion обходит установленные регионы в детерминированном порядке
// (возрастание rx, затем ry); raw — исходные байты региона. Возврат false из
// fn прекращает обход.
func (m *Map) EachRegion(fn func(rx, ry int, raw []byte) bool) {
	for idx := 0; idx < len(m.regions); idx++ {
		r := m.regions[idx]
		if r == nil {
			continue
		}
		if !fn(r.rx, r.ry, r.data) {
			return
		}
	}
}
