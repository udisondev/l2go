//go:build !embedded

package main

import (
	"fmt"

	"github.com/udisondev/l2go/internal/artifact"
	"github.com/udisondev/l2go/internal/data"
	"github.com/udisondev/l2go/internal/geo"
)

// loadStatic — источник статики dev-сборки: только внешний артефакт
// (mmap LoadFile). Самодостаточности нет: пустой путь — ошибка, а не
// тихая деградация. Ошибка несёт источник (путь) — отказы разных
// артефактов различимы в журнале.
func loadStatic(path string) (*data.Static, *geo.Map, *artifact.Meta, error) {
	if path == "" {
		return nil, nil, nil, fmt.Errorf("артефакт статики обязателен (-artifact)")
	}
	st, gm, meta, _, err := artifact.LoadFile(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("артефакт %s: %w", path, err)
	}
	return st, gm, meta, nil
}
