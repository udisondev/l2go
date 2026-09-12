//go:build windows

package geo

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// mapFile отображает файл только для чтения; страницы не попадают в Go-кучу.
// Слайс валиден до вызова unmap. Пустой файл — пустой слайс, unmap — no-op.
// Файл и секция закрываются сразу после отображения: view живёт до
// UnmapViewOfFile.
var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procCreateFileMappingW = kernel32.NewProc("CreateFileMappingW")
	procMapViewOfFile      = kernel32.NewProc("MapViewOfFile")
	procUnmapViewOfFile    = kernel32.NewProc("UnmapViewOfFile")
	procCloseHandle        = kernel32.NewProc("CloseHandle")
)

const (
	pageReadOnly = 0x02
	fileMapRead  = 0x0004
)

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
		return nil, nil, sizeErr(size)
	}

	h, _, callErr := procCreateFileMappingW.Call(f.Fd(), 0, pageReadOnly,
		uintptr(uint32(uint64(size)>>32)), uintptr(uint32(uint64(size)&0xFFFFFFFF)), 0)
	if h == 0 {
		return nil, nil, fmt.Errorf("geo: CreateFileMapping %s: %w", path, callErr)
	}
	base, _, callErr := procMapViewOfFile.Call(h, fileMapRead, 0, 0, 0)
	if base == 0 {
		procCloseHandle.Call(h)
		return nil, nil, fmt.Errorf("geo: MapViewOfFile %s: %w", path, callErr)
	}
	procCloseHandle.Call(h)

	// base — адрес из syscall, а не GC-управляемая память: переинтерпретация
	// через &base допустима (прямое unsafe.Pointer(base) запрещает go vet
	// вне выражения самого вызова).
	basePtr := *(*unsafe.Pointer)(unsafe.Pointer(&base))
	data := unsafe.Slice((*byte)(basePtr), size)
	unmap := func() error {
		r, _, callErr := procUnmapViewOfFile.Call(base)
		if r == 0 {
			return fmt.Errorf("geo: UnmapViewOfFile %s: %w", path, callErr)
		}
		return nil
	}
	return data, unmap, nil
}
