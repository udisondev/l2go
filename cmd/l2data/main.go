// l2data — статики: проверка датапака и геодаты, сборка компилированного
// артефакта и загрузка из него.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/udisondev/l2go/internal/artifact"
	"github.com/udisondev/l2go/internal/data"
	"github.com/udisondev/l2go/internal/geo"
)

// defaultArtifact — путь артефакта по умолчанию (embed-каталог); относителен
// корню модуля — при запуске из другого каталога используй -o.
var defaultArtifact = filepath.Join("internal", "artifact", "embedded", "artifact.l2a")

func main() {
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() < 1 {
		usage()
		os.Exit(2)
	}
	switch flag.Arg(0) {
	case "check":
		if flag.NArg() != 2 {
			usage()
			os.Exit(2)
		}
		check(flag.Arg(1))
	case "geo":
		if flag.NArg() != 2 {
			usage()
			os.Exit(2)
		}
		checkGeo(flag.Arg(1))
	case "build":
		cmdBuild(flag.Args()[1:])
	case "load":
		if flag.NArg() != 2 {
			usage()
			os.Exit(2)
		}
		cmdLoad(flag.Arg(1))
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
	fmt.Fprintln(os.Stderr, "использование: l2data check <корень данных> | l2data geo <каталог геодаты> |")
	fmt.Fprintln(os.Stderr, "           l2data build <корень данных> [каталог геодаты] [-o артефакт] | l2data load <артефакт>")
}

// cmdBuild собирает артефакт статики: парсинг и валидация исходников (красные
// — выход 1, артефакт не трогается), затем атомарная запись (идентичный
// существующему — «актуален», без записи).
func cmdBuild(args []string) {
	out := defaultArtifact
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-o" {
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "l2data: -o требует путь")
				os.Exit(2)
			}
			out = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	if len(rest) < 1 || len(rest) > 2 {
		usage()
		os.Exit(2)
	}
	root, geoDir := rest[0], ""
	if len(rest) == 2 {
		geoDir = rest[1]
	}

	t0 := time.Now()
	st, drep, err := data.Load(os.DirFS(root))
	if err != nil {
		fmt.Fprintf(os.Stderr, "l2data: %v\n", err)
		os.Exit(1)
	}
	printReport(drep)
	if drep.HasErrors() {
		os.Exit(1)
	}
	var m *geo.Map
	var grep *geo.Report
	if geoDir != "" {
		m, grep, err = geo.LoadDir(geoDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "l2data: %v\n", err)
			os.Exit(1)
		}
		printGeoReport(grep)
		if grep.HasErrors() || grep.Regions == 0 {
			if !grep.HasErrors() {
				fmt.Println("регионы не загружены — проверьте каталог и формат (нужен L2J .l2j)")
			}
			os.Exit(1)
		}
	} else {
		fmt.Println("геодата не задана: регионов: 0")
	}
	parseDur := time.Since(t0)

	res, err := artifact.Build(out, st, drep, m, grep)
	if err != nil {
		fmt.Fprintf(os.Stderr, "l2data: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("манифест данных: %x\nманифест гео: %x\n", res.Meta.DataManifest, res.Meta.GeoManifest)
	fmt.Printf("секция данных: %d байт, гео: %d байт, регионов: %d\n",
		res.Meta.DataLen, res.Meta.GeoLen, res.Meta.Regions)
	fmt.Printf("фазы: parse=%s encode=%s write=%s\n", parseDur, res.Encode, res.Write)
	if res.Fresh {
		fmt.Printf("артефакт записан: %s\n", out)
	} else {
		fmt.Printf("артефакт актуален, запись не требуется: %s\n", out)
	}
}

// cmdLoad загружает артефакт (mmap + проверка + декодирование) и печатает
// манифесты, счётчики и времена фаз.
func cmdLoad(path string) {
	_, _, meta, ph, err := artifact.LoadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "l2data: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("манифест данных: %x\nманифест гео: %x\n", meta.DataManifest, meta.GeoManifest)
	fmt.Printf("файлов: %d, предметов: %d, NPC: %d, спавнов: %d, территорий: %d, зон: %d, скиллов: %d (уровней: %d), регионов: %d\n",
		meta.Files, meta.Items, meta.Npcs, meta.Spawns, meta.Territories, meta.Zones, meta.Skills, meta.SkillLevels, meta.Regions)
	fmt.Printf("секция данных: %d байт, гео: %d байт\n", meta.DataLen, meta.GeoLen)
	fmt.Printf("фазы: verify=%s decode-data=%s decode-geo=%s\n", ph.Verify, ph.DecodeData, ph.DecodeGeo)
}

func printReport(rep *data.Report) {
	fmt.Printf("файлов: %d, предметов: %d, NPC: %d, спавнов: %d, территорий: %d, дроп-предметов: %d, манифест: %x\n",
		rep.Files, rep.Items, rep.Npcs, rep.Spawns, rep.Territories, rep.DropItems, rep.Manifest)
	if rep.Zones > 0 {
		fmt.Printf("зон: %d (спавн-точек: %d, рас-точек: %d, с явным id: %d)\n",
			rep.Zones, rep.ZoneSpawns, rep.ZoneRacePoints, rep.ZonesWithID)
	}
	if rep.Skills > 0 {
		fmt.Printf("скиллов: %d (уровней: %d, с энчантами: %d, таблиц: %d)\n",
			rep.Skills, rep.SkillLevels, rep.EnchantedSkills, rep.SkillTables)
	}
	if rep.MinZOverMaxZ > 0 {
		fmt.Printf("minZ>maxZ (канон нормализует): %d\n", rep.MinZOverMaxZ)
	}
	if rep.MinEqMaxZ > 0 {
		fmt.Printf("тонкослойные зоны (minZ==maxZ): %d\n", rep.MinEqMaxZ)
	}
	if rep.DupZoneNames > 0 {
		fmt.Printf("дублирующиеся имена зон: %d\n", rep.DupZoneNames)
	}
	if rep.DupAdjacentNodes > 0 {
		fmt.Printf("повторы соседних узлов территорий: %d\n", rep.DupAdjacentNodes)
	}
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
		fmt.Printf("отключённые файлы (спавны, зоны): %d\n", rep.DisabledFiles)
	}
	if rep.TerrOwnName > 0 {
		fmt.Printf("собственные имена территорий (не читаются): %d\n", rep.TerrOwnName)
	}
	if rep.DeepSkips > 0 {
		fmt.Printf("элементы глубже потолка пути: %d\n", rep.DeepSkips)
	}
	if rep.MissingTargetType > 0 {
		fmt.Printf("скиллы без targetType (дефолт SELF): %d\n", rep.MissingTargetType)
	}
	if rep.OrphanEnchants > 0 {
		fmt.Printf("мёртвые override энчантов без маршрута: %d\n", rep.OrphanEnchants)
	}
	if rep.NestedDirect > 0 {
		fmt.Printf("вложенные прямые элементы скиллов: %d\n", rep.NestedDirect)
	}
	if rep.DupTables > 0 {
		fmt.Printf("дубликаты таблиц скиллов: %d\n", rep.DupTables)
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
		// Блок/офсет имеют смысл только у структурных кодов; файл-уровневые
		// (dup/range/io/size) печатаются без них.
		switch e.Code {
		case geo.CodeTrunc, geo.CodeBlockType, geo.CodeLayers, geo.CodeTail:
			fmt.Printf("ошибка %s %s блок %d офсет %d: %s\n", e.Code, e.File, e.Block, e.Offset, e.Message)
		default:
			fmt.Printf("ошибка %s %s: %s\n", e.Code, e.File, e.Message)
		}
	}
}
