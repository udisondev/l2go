//go:build embedded

package main

import (
	"fmt"

	"github.com/udisondev/l2go/internal/artifact"
	"github.com/udisondev/l2go/internal/data"
	"github.com/udisondev/l2go/internal/geo"
)

// loadStatic — источник статики embedded-сборки: пустой путь — байты,
// вшитые в бинарарь (самодостаточная поставка «make embedded → оба
// бинарника»); непустой — внешний файл (mmap LoadFile), как в dev-сборке.
// Ошибка несёт метку источника: отказ вшитых байт различим от отказа
// внешнего файла по журналу.
func loadStatic(path string) (*data.Static, *geo.Map, *artifact.Meta, error) {
	if path == "" {
		st, gm, meta, err := artifact.LoadEmbedded()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("вшитые байты бинараря: %w", err)
		}
		return st, gm, meta, nil
	}
	st, gm, meta, _, err := artifact.LoadFile(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("артефакт %s: %w", path, err)
	}
	return st, gm, meta, nil
}
