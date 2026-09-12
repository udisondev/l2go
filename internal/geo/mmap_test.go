package geo

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"testing"
)

func TestMapFileRoundTrip(t *testing.T) {
	data := flatZeroRegion()
	path := filepath.Join(t.TempDir(), "16_10.l2j")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	mapped, unmap, err := mapFile(path)
	if err != nil {
		t.Fatalf("mapFile: %v", err)
	}
	if !bytes.Equal(mapped, data) {
		t.Fatalf("байты отображения разошлись: len %d vs %d", len(mapped), len(data))
	}
	reg, _, err := decodeRegion(16, 10, mapped)
	if err != nil {
		t.Fatalf("decodeRegion поверх отображения: %v", err)
	}
	if _, nswe := reg.CellAt(16*regionCells, 10*regionCells).Nearest(0); nswe != NSWEAll {
		t.Errorf("flat-зонд поверх отображения: nswe = %v; want NSWEAll", nswe)
	}
	if err := unmap(); err != nil {
		t.Errorf("unmap: %v", err)
	}
}

func TestMapFileEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.l2j")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	data, unmap, err := mapFile(path)
	if err != nil {
		t.Fatalf("mapFile пустого файла: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("len = %d; want 0", len(data))
	}
	if err := unmap(); err != nil {
		t.Errorf("unmap пустого отображения (no-op): %v", err)
	}
}

func TestMapFileMissing(t *testing.T) {
	_, _, err := mapFile(filepath.Join(t.TempDir(), "нет.l2j"))
	if err == nil {
		t.Fatal("отсутствующий файл: ошибки нет")
	}
}

// TestMapFileOffHeap: байты отображения не попадают в Go-кучу — рост кучи
// ограничен известными индексами представления (HeapIndexBytes) с запасом на
// шум измерения.
// writeMLRegionFile строит multilayer-тяжёлый регион (3 слоя в каждой
// ячейке, ~29 МБ), пишет его во временный файл и возвращает путь и размер.
// Фикстура живёт в отдельной функции: буфер билдера не пересекается с окном
// измерений off-heap-теста.
func writeMLRegionFile(t *testing.T) (string, int) {
	t.Helper()
	b := newRegionBuilder()
	var cells [blockCells][]uint16
	one := [3]uint16{encodeCellWord(-16, East), encodeCellWord(0, West), encodeCellWord(16, North)}
	for i := range cells {
		cells[i] = one[:]
	}
	for b.n < regionBlocks {
		b.addMultilayer(cells)
	}
	data := b.build()
	path := filepath.Join(t.TempDir(), "16_10.l2j")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path, len(data)
}

func TestMapFileOffHeap(t *testing.T) {
	path, size := writeMLRegionFile(t)

	// debug.FreeOSMemory завершает свип: без него под параллельной
	// нагрузкой HeapAlloc держит несвёрнутый мусор и дельта скачет.
	runtime.GC()
	debug.FreeOSMemory()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	mapped, unmap, err := mapFile(path)
	if err != nil {
		t.Fatalf("mapFile: %v", err)
	}
	// Region удерживает байты и индексы живыми до второго замера — иначе
	// оракул измеряет только мусор (индексы мертвы к моменту GC).
	regHold, st, err := decodeRegion(16, 10, mapped)
	if err != nil {
		t.Fatalf("decodeRegion: %v", err)
	}
	// Финализация индексов: ёмкость равна длине — append-запас не остаётся
	// в живой куче (детерминированный оракул, без измерений).
	if cap(regHold.cells) != len(regHold.cells) || cap(regHold.blocks) != len(regHold.blocks) {
		t.Errorf("индексы не финализированы: cells cap=%d len=%d, blocks cap=%d len=%d",
			cap(regHold.cells), len(regHold.cells), cap(regHold.blocks), len(regHold.blocks))
	}

	runtime.GC()
	debug.FreeOSMemory()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(regHold)

	delta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	limit := int64(st.indexBytes) * 13 / 10
	if delta > limit {
		t.Errorf("рост кучи %d байт сверх лимита %d (HeapIndexBytes %d, файл %d байт): байты попали в кучу",
			delta, limit, st.indexBytes, size)
	}
	t.Logf("файл %d байт, HeapIndexBytes %d, рост кучи %d", size, st.indexBytes, delta)
	if err := unmap(); err != nil {
		t.Errorf("unmap: %v", err)
	}
}
