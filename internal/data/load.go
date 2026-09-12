package data

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"sort"
)

// loader — загрузчик одной категории статики.
type loader struct {
	name string
	run  func(fsys fs.FS, ctx *loadCtx)
}

// loaders — все категории; порядок фиксирован и влияет на порядок записей
// отчёта (манифест от порядка не зависит: сортировка путей). Ссылки между
// категориями разрешаются после всех загрузчиков — порядок на проверку
// целостности не влияет.
var loaders = []loader{
	{name: "items", run: loadItems},
	{name: "npcs", run: loadNpcs},
	{name: "spawns", run: loadSpawns},
}

// loadCtx накапливает результаты категорий, отчёт, состав манифеста входов
// и отложенные ссылки.
type loadCtx struct {
	rep         *Report
	fatalErr    error
	items       map[ItemID]Item
	npcs        map[NpcID]Npc
	territories map[string]Territory
	spawns      []NpcSpawn
	inputs      map[string][sha256.Size]byte
	links       []linkRef
	keyDict     map[string]string // интернирование путевых ключей raw-bag
}

// maxFile — потолок одного файла категории (максимум датапака ~507 КБ,
// запас больше двух порядков). Читается на байт больше, чтобы превышение
// потолка отличалось от обрыва XML.
const maxFile = 16 << 20

func newLoadCtx() *loadCtx {
	return &loadCtx{
		rep: &Report{
			UnknownKeys:     map[string]int{},
			UnknownTypes:    map[string]int{},
			SkippedElements: map[string]int{},
			SkippedDirs:     map[string]int{},
		},
		items:       map[ItemID]Item{},
		npcs:        map[NpcID]Npc{},
		territories: map[string]Territory{},
		inputs:      map[string][sha256.Size]byte{},
		keyDict:     map[string]string{},
	}
}

func (ctx *loadCtx) fatal(err error) {
	if ctx.fatalErr == nil {
		ctx.fatalErr = err
	}
}

func (ctx *loadCtx) entry(e Entry) {
	ctx.rep.Errors = append(ctx.rep.Errors, e)
}

// readFileCapped читает файл категории с потолком размера; содержимое хэшируется
// в манифест независимо от дальнейшей судьбы разбора.
func (ctx *loadCtx) readFileCapped(fsys fs.FS, path, category string) ([]byte, bool) {
	f, err := fsys.Open(path)
	if err != nil {
		ctx.fatal(fmt.Errorf("data: открытие %s: %w", path, err))
		return nil, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		ctx.fatal(fmt.Errorf("data: чтение %s: %w", path, err))
		return nil, false
	}
	if len(data) > maxFile {
		ctx.entry(Entry{Category: category, File: path, Code: CodeLimit,
			Message: fmt.Sprintf("файл %d байт превышает потолок %d", len(data), maxFile)})
		return nil, false
	}
	ctx.inputs[path] = sha256.Sum256(data)
	return data, true
}

// internKey возвращает единственный экземпляр строки ключа: путевые ключи
// raw-bag повторяются десятки тысяч раз, словарь загрузки устраняет дубликаты.
func (ctx *loadCtx) internKey(k string) string {
	if v, ok := ctx.keyDict[k]; ok {
		return v
	}
	ctx.keyDict[k] = k
	return k
}

// manifestOf сворачивает состав входов в один SHA-256: отсортированный список
// путей, длина пути малым концом, путь, хэш содержимого. Не криптографическая
// граница — ключ детерминизма и сверки версий данных.
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
