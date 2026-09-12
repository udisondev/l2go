// l2data — проверка статики: загрузка датапака с полным отчётом валидации.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/udisondev/l2go/internal/data"
)

func main() {
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() != 2 || flag.Arg(0) != "check" {
		usage()
		os.Exit(2)
	}
	_, rep, err := data.Load(os.DirFS(flag.Arg(1)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "l2data: %v\n", err)
		os.Exit(1)
	}
	printReport(rep)
	if rep.HasErrors() {
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "использование: l2data check <корень данных>")
}

func printReport(rep *data.Report) {
	fmt.Printf("файлов: %d, предметов: %d, манифест: %x\n", rep.Files, rep.Items, rep.Manifest)
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
		fmt.Printf("дубликаты ключей внутри предмета: %d\n", rep.DupKeys)
	}
	if rep.UnnamedSets > 0 {
		fmt.Printf("set без имени: %d\n", rep.UnnamedSets)
	}
	if rep.StatNoType > 0 {
		fmt.Printf("stat без типа: %d\n", rep.StatNoType)
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
