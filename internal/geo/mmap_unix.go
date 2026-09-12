//go:build !windows

package geo

import (
	"fmt"
	"os"
	"syscall"
)

// mapFile отображает файл только для чтения; страницы не попадают в Go-кучу.
// Слайс валиден до вызова unmap. Пустой файл — пустой слайс, unmap — no-op.
// Дескриптор файла закрывается сразу после mmap: отображение живёт до munmap.
func mapFile(path string) ([]byte, func() error, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("geo: открытие %s: %w", path, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("geo: stat %s: %w", path, err)
	}
	size := st.Size()
	if size == 0 {
		return nil, func() error { return nil }, nil
	}
	if !sizeOK(int(size)) {
		return nil, nil, &StructError{Code: CodeSize,
			Message: fmt.Sprintf("файл %d байт сверх потолка %d", size, maxRegionBytes)}
	}

	data, err := syscall.Mmap(int(f.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_PRIVATE)
	if err != nil {
		return nil, nil, fmt.Errorf("geo: mmap %s: %w", path, err)
	}
	unmap := func() error {
		if err := syscall.Munmap(data); err != nil {
			return fmt.Errorf("geo: munmap %s: %w", path, err)
		}
		return nil
	}
	return data, unmap, nil
}
