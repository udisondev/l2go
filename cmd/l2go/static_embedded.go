//go:build embedded

package main

import (
	"github.com/udisondev/l2go/internal/artifact"
	"github.com/udisondev/l2go/internal/data"
	"github.com/udisondev/l2go/internal/geo"
)

// loadStatic — источник статики embedded-сборки: пустой путь — байты,
// вшитые в бинарарь (самодостаточная поставка «make embedded → оба
// бинарника»); непустой — внешний файл (mmap LoadFile), как в dev-сборке.
func loadStatic(path string) (*data.Static, *geo.Map, *artifact.Meta, error) {
	if path == "" {
		return artifact.LoadEmbedded()
	}
	st, gm, meta, _, err := artifact.LoadFile(path)
	return st, gm, meta, err
}
