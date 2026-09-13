//go:build !windows

package geo

import (
	"fmt"
	"os"
	"syscall"
)

// mapFile отображает файл только для чтения; страницы не попадают в Go-кучу.
// Слайс валиден до вызова unmap. Пустой файл — пустой слайс, unmap — no-op.
// Потолок размера — обязанность вызывающего. Дескриптор файла закрывается
// сразу после mmap: отображение живёт до munmap.
func mapFile(path string) ([]byte, func() error, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("открытие %s: %w", path, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("stat %s: %w", path, err)
	}
	size := st.Size()
	if size == 0 {
		return nil, func() error { return nil }, nil
	}

	data, err := syscall.Mmap(int(f.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_PRIVATE)
	if err != nil {
		return nil, nil, fmt.Errorf("mmap %s: %w", path, err)
	}
	unmap := func() error {
		if err := syscall.Munmap(data); err != nil {
			return fmt.Errorf("munmap %s: %w", path, err)
		}
		return nil
	}
	return data, unmap, nil
}
