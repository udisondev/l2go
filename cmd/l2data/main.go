// l2data — проверка статики: загрузка датапака и геодаты с полным отчётом
// валидации.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/udisondev/l2go/internal/data"
	"github.com/udisondev/l2go/internal/geo"
)

func main() {
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() != 2 {
		usage()
		os.Exit(2)
	}
	switch flag.Arg(0) {
	case "check":
		check(flag.Arg(1))
	case "geo":
		checkGeo(flag.Arg(1))
	default:
		usage()
		os.Exit(2)
	}
}

func check(root string) {
	_, rep, err := data.Load(os.DirFS(root))
	if err != nil {
		fmt.Fprintf(os.Stderr, "l2data: %v\n", err)
		os.Exit(1)
	}
	printReport(rep)
	if rep.HasErrors() {
		os.Exit(1)
	}
}

func checkGeo(dir string) {
	_, rep, err := geo.LoadDir(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "l2data: %v\n", err)
		os.Exit(1)
	}
	printGeoReport(rep)
	if rep.HasErrors() {
		os.Exit(1)
	}
	if rep.Regions == 0 {
		fmt.Println("регионы не загружены — проверьте каталог и формат (нужен L2J .l2j)")
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "использование: l2data check <корень данных> | l2data geo <каталог геодаты>")
}

func printReport(rep *data.Report) {
	fmt.Printf("файлов: %d, предметов: %d, NPC: %d, спавнов: %d, территорий: %d, дроп-предметов: %d, манифест: %x\n",
		rep.Files, rep.Items, rep.Npcs, rep.Spawns, rep.Territories, rep.DropItems, rep.Manifest)
	for _, e := range rep.Errors {
		id := ""
		if e.ID != 0 {
			id = fmt.Sprintf(" [%d]", e.ID)
		}
		fmt.Printf("ошибка %s %s:%d%s %s: %s\n", e.Category, e.File, e.Line, id, e.Code, e.Message)
	}
	printCounters("неизвестные ключи", rep.UnknownKeys)
	printCounters("неизвестные типы", rep.UnknownTypes)
	printCounters("пропущенные элементы", rep.SkippedElements)
	if rep.EmptyValues > 0 {
		fmt.Printf("пустые значения: %d\n", rep.EmptyValues)
	}
	if rep.DupKeys > 0 {
		fmt.Printf("дубликаты ключей raw-bag: %d\n", rep.DupKeys)
	}
	if rep.UnnamedSets > 0 {
		fmt.Printf("set без имени: %d\n", rep.UnnamedSets)
	}
	if rep.StatNoType > 0 {
		fmt.Printf("stat без типа: %d\n", rep.StatNoType)
	}
	if n := rep.MissingLevel + rep.MissingType + rep.MissingName + rep.MissingRace; n > 0 {
		fmt.Printf("NPC без level/type/name/race: %d/%d/%d/%d (дефолты канона)\n",
			rep.MissingLevel, rep.MissingType, rep.MissingName, rep.MissingRace)
	}
	if rep.ChanceOver100 > 0 {
		fmt.Printf("шансы дропа >100 («всегда»): %d\n", rep.ChanceOver100)
	}
	if rep.MinOverMax > 0 {
		fmt.Printf("min>max в дропе: %d\n", rep.MinOverMax)
	}
	if rep.WithoutRespawnDelay > 0 {
		fmt.Printf("спавны без respawnDelay: %d\n", rep.WithoutRespawnDelay)
	}
	if rep.WithoutHeading > 0 {
		fmt.Printf("точечные спавны без heading: %d\n", rep.WithoutHeading)
	}
	if rep.FakePlayersSkipped > 0 {
		fmt.Printf("fake-player-спавны без определения: %d\n", rep.FakePlayersSkipped)
	}
	if rep.NamedBlocks > 0 {
		fmt.Printf("именованные спавн-блоки: %d\n", rep.NamedBlocks)
	}
	if rep.DisabledFiles > 0 {
		fmt.Printf("отключённые файлы спавнов: %d\n", rep.DisabledFiles)
	}
	if rep.TerrOwnName > 0 {
		fmt.Printf("собственные имена территорий (не читаются): %d\n", rep.TerrOwnName)
	}
	if rep.DeepSkips > 0 {
		fmt.Printf("элементы глубже потолка пути: %d\n", rep.DeepSkips)
	}
	printCounters("пропущено файлов в подкаталогах", rep.SkippedDirs)
}

func printCounters(title string, m map[string]int) {
	if len(m) == 0 {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	fmt.Printf("%s:", title)
	for _, k := range keys {
		fmt.Printf(" %s=%d", k, m[k])
	}
	fmt.Println()
}

func printGeoReport(rep *geo.Report) {
	fmt.Printf("файлов: %d, регионов: %d (flat %d / complex %d / multilayer %d), слоёв: %d, макс. слоёв в ячейке: %d\n",
		rep.Files, rep.Regions, rep.BlocksFlat, rep.BlocksComplex, rep.BlocksMultilayer, rep.LayersTotal, rep.MaxLayersPerCell)
	fmt.Printf("отображено: %d байт, индексов в куче: %d байт, манифест: %x\n",
		rep.BytesMapped, rep.HeapIndexBytes, rep.Manifest)
	if rep.DupLayerZ > 0 {
		fmt.Printf("ячейки со слоями равной высоты: %d\n", rep.DupLayerZ)
	}
	if rep.ExtraTiles > 0 {
		fmt.Printf("файлы вне тайлов канона 16–26×10–25: %d\n", rep.ExtraTiles)
	}
	if rep.SkippedDirs > 0 {
		fmt.Printf("пропущено подкаталогов: %d\n", rep.SkippedDirs)
	}
	if rep.IgnoredFiles > 0 {
		fmt.Printf("игнорировано файлов вне паттерна: %d\n", rep.IgnoredFiles)
	}
	for _, e := range rep.Errors {
		fmt.Printf("ошибка %s %s блок %d офсет %d: %s\n", e.Code, e.File, e.Block, e.Offset, e.Message)
	}
}
