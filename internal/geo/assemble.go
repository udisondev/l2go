package geo

import "errors"

// MapFile отображает файл в память только для чтения. Возвращённый слайс живёт
// до вызова unmap; потолок размера — забота вызывающего (LoadDir проверяет
// потолок файла региона до отображения, артефакт статики гвардит заголовком).
func MapFile(path string) ([]byte, func() error, error) {
	return nil, nil, errors.New("geo: MapFile не реализован")
}

// NewMapFromRegions собирает карту из декодированных регионов. Повтор слота
// региона — ошибка. Собранная карта после публикации не мутируется.
func NewMapFromRegions(regions []*Region) (*Map, error) {
	return nil, errors.New("geo: NewMapFromRegions не реализован")
}

// EachRegion обходит установленные регионы в детерминированном порядке
// (возрастание координат); raw — исходные байты региона. Возврат false из fn
// прекращает обход.
func (m *Map) EachRegion(fn func(rx, ry int, raw []byte) bool) {}
