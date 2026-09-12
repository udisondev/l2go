package geo

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Коды записей отчёта геодаты.
const (
	CodeTrunc     = "trunc"      // данные кончились внутри структуры
	CodeBlockType = "block_type" // байт типа блока вне {0,1,2}
	CodeLayers    = "layers"     // счётчик слоёв ячейки вне 1..125
	CodeTail      = "tail"       // байты после 65536-го блока
	CodeDup       = "dup"        // повторный файл на те же координаты региона
	CodeRange     = "range"      // координаты региона из имени вне сетки 32×32
	CodeSize      = "size"       // файл сверх потолка
	CodeIO        = "io"         // чтение или отображение файла
)

// Entry — запись об ошибке загрузки геодаты.
type Entry struct {
	File    string
	Code    string
	Block   int
	Offset  int
	Message string
}

// Report — итог загрузки геодаты: счётчики, ошибки и манифест состава.
// Единственный источник численности.
type Report struct {
	Files            int
	Regions          int
	BlocksFlat       int
	BlocksComplex    int
	BlocksMultilayer int
	LayersTotal      int // слои multilayer-ячеек
	MaxLayersPerCell int
	DupLayerZ        int // ячейки со слоями равной высоты (широта, не ошибка)

	// Тайлы канона Interlude — 16..26 × 10..25; файлы сетки вне этого
	// диапазона — счётчик широты.
	ExtraTiles   int
	SkippedDirs  int
	IgnoredFiles int

	BytesMapped    int // байты установленных регионов (вне Go-кучи)
	HeapIndexBytes int // производные индексы представления в куче

	Errors   []Entry
	Manifest [sha256.Size]byte
}

// HasErrors сообщает, есть ли ошибки целостности.
func (r *Report) HasErrors() bool { return len(r.Errors) > 0 }

// LoadDir загружает каталог геодаты (файлы «NN_NN.l2j» в корне; подпапки и
// прочие имена не читаются, но видны в счётчиках). Отображения установленных
// регионов принадлежат Map на всё время жизни процесса (см. doc пакета).
// error — только фатальное (каталог не читается); ошибки данных — в отчёте.
func LoadDir(dir string) (*Map, *Report, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("geo: чтение каталога %s: %w", dir, err)
	}

	m := &Map{}
	rep := &Report{}
	inputs := map[string][sha256.Size]byte{}
	for _, e := range entries {
		if e.IsDir() {
			rep.SkippedDirs++
			continue
		}
		rx, ry, ok := parseRegionName(e.Name())
		if !ok {
			rep.IgnoredFiles++
			continue
		}
		if rx >= regionsX || ry >= regionsY {
			rep.err(Entry{File: e.Name(), Code: CodeRange,
				Message: fmt.Sprintf("координаты региона (%d, %d) вне сетки %d×%d", rx, ry, regionsX, regionsY)})
			continue
		}
		if m.regions[rx*regionsY+ry] != nil {
			rep.err(Entry{File: e.Name(), Code: CodeDup,
				Message: fmt.Sprintf("регион (%d, %d) уже установлен другим файлом", rx, ry)})
			continue
		}

		data, unmap, err := mapFile(filepath.Join(dir, e.Name()))
		if err != nil {
			rep.err(structEntry(e.Name(), err))
			continue
		}
		reg, st, err := decodeRegion(rx, ry, data)
		if err != nil {
			rep.err(structEntry(e.Name(), err))
			if err := unmap(); err != nil {
				rep.err(Entry{File: e.Name(), Code: CodeIO, Message: "unmap: " + err.Error()})
			}
			continue
		}
		// Регион установлен: отображение живёт до конца процесса, unmap
		// не вызывается (контракт doc пакета).
		m.regions[rx*regionsY+ry] = reg
		rep.Files++
		rep.Regions++
		rep.BlocksFlat += st.blocksFlat
		rep.BlocksComplex += st.blocksComplex
		rep.BlocksMultilayer += st.blocksMulti
		rep.LayersTotal += st.layers
		if st.maxLayers > rep.MaxLayersPerCell {
			rep.MaxLayersPerCell = st.maxLayers
		}
		rep.DupLayerZ += st.dupLayerZ
		if rx < tileXMin || rx > tileXMax || ry < tileYMin || ry > tileYMax {
			rep.ExtraTiles++
		}
		rep.BytesMapped += len(data)
		rep.HeapIndexBytes += st.indexBytes
		inputs[e.Name()] = sha256.Sum256(data)
	}
	rep.Manifest = manifestOf(inputs)
	return m, rep, nil
}

// Тайлы канона Interlude (World.TILE_* канона Mobius).
const (
	tileXMin, tileXMax = 16, 26
	tileYMin, tileYMax = 10, 25
)

func (r *Report) err(e Entry) {
	r.Errors = append(r.Errors, e)
}

// structEntry превращает ошибку декода в запись отчёта, добавляя имя файла.
func structEntry(file string, err error) Entry {
	var se *StructError
	if errors.As(err, &se) {
		return Entry{File: file, Code: se.Code, Block: se.Block, Offset: se.Offset, Message: se.Message}
	}
	return Entry{File: file, Code: CodeIO, Message: err.Error()}
}

var regionNameRe = regexp.MustCompile(`^(\d+)_(\d+)\.l2j$`)

// parseRegionName разбирает имя файла региона; регистр расширения не важен.
// Переполнение числа — координата вне сетки (отловится range-проверкой).
func parseRegionName(name string) (rx, ry int, ok bool) {
	m := regionNameRe.FindStringSubmatch(strings.ToLower(name))
	if m == nil {
		return 0, 0, false
	}
	x, err := strconv.ParseUint(m[1], 10, 31)
	if err != nil {
		x = 1 << 30
	}
	y, err := strconv.ParseUint(m[2], 10, 31)
	if err != nil {
		y = 1 << 30
	}
	return int(x), int(y), true
}

// manifestOf сворачивает состав в один SHA-256: отсортированные имена, длина
// имени малым концом, имя, хэш содержимого. Фрейминг единый с датапаком.
func manifestOf(inputs map[string][sha256.Size]byte) [sha256.Size]byte {
	paths := make([]string, 0, len(inputs))
	for p := range inputs {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	var lenBuf [8]byte
	for _, p := range paths {
		binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(p)))
		h.Write(lenBuf[:])
		h.Write([]byte(p))
		sum := inputs[p]
		h.Write(sum[:])
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}
