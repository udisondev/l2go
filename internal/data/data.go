// Package data загружает статические данные мира (датапак) из файлового дерева
// в типизированные иммутабельные структуры. Данные после загрузки не меняются:
// все структуры публикуются только для чтения, мутация полей — нарушение
// контракта пакета. Ошибки целостности (дубликат ID, битая структура, кривое
// число) не прерывают обход: Load читает всё дерево и возвращает полный отчёт.
package data

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// Static — загруженная статика мира. Иммутабельна после загрузки: поля
// публикуются только для чтения.
type Static struct {
	Items map[ItemID]Item
}

// Load читает статику из fsys и возвращает её вместе с отчётом валидации.
// Возвращённая ошибка — только фатальные условия самой файловой системы
// (корень не читается); ошибки данных — записи отчёта: Load доходит до конца
// и собирает полный перечень. Static пригоден к использованию только когда
// Report.HasErrors вернула false.
func Load(fsys fs.FS) (*Static, *Report, error) {
	ctx := newLoadCtx()
	for _, l := range loaders {
		if ctx.fatalErr != nil {
			break
		}
		l.run(fsys, ctx)
	}
	if ctx.fatalErr != nil {
		return nil, nil, ctx.fatalErr
	}
	ctx.rep.Manifest = manifestOf(ctx.inputs)
	return &Static{Items: ctx.items}, ctx.rep, nil
}

// Dump возвращает канонический текстовый вид статики: записи по возрастанию
// ID, поля в фиксированном порядке, ключи параметров отсортированы. Формат
// стабилен и служит золотым сравнением и формой сверки эквивалентности
// скомпилированного артефакта статики.
func (s *Static) Dump() string {
	if s == nil {
		return ""
	}
	ids := make([]int64, 0, len(s.Items))
	for id := range s.Items {
		ids = append(ids, int64(id))
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var sb strings.Builder
	for _, idv := range ids {
		it := s.Items[ItemID(idv)]
		fmt.Fprintf(&sb,
			"id=%d name=%q type=%q weight=%d price=%d stackable=%t crystal_type=%q crystal_count=%d material=%q bodypart=%q",
			it.ID, it.Name, it.Type, it.Weight, it.Price, it.Stackable,
			it.CrystalType, it.CrystalCount, it.Material, it.BodyPart)
		keys := make([]string, 0, len(it.set))
		for k := range it.set {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sb.WriteString(" sets=[")
		for i, k := range keys {
			if i > 0 {
				sb.WriteByte(' ')
			}
			sb.WriteString(k)
			sb.WriteByte('=')
			sb.WriteString(it.set[k])
		}
		sb.WriteString("]\n")
	}
	return sb.String()
}
