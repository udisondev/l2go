// Поле пакета трафик-лога: ключ и готовое строковое значение. Сборка полей
// типизированного пакета — метод Fields представления (значения —
// детерминированные строки: числа без экспоненты, строки в кавычках с
// экранированием управляющих рун); писатель строк — потребитель (l2client).

package protocol

import (
	"fmt"
	"strconv"
	"strings"
)

// Field — поле типизированного пакета трафик-лога: ключ и готовое значение.
type Field struct {
	K, V string
}

func num(v int64) string    { return strconv.FormatInt(v, 10) }
func num32(v int32) string  { return num(int64(v)) }
func flt(v float64) string  { return strconv.FormatFloat(v, 'g', -1, 64) }
func boolean(v bool) string { return strconv.FormatBool(v) }

// Quote оформляет строковое поле: кавычки; управляющие руны (< 0x20, 0x7F) —
// \xHH, кавычка и обратный слэш — заэскейплены.
func Quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7F:
			fmt.Fprintf(&b, "\\x%02X", r)
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// quoteMax — потолок строкового поля лога: гигантские имена недоверенных
// пакетов не разворачивают строку в сотни килобайт.
const quoteMax = 128

// quoted — строковое поле лога: Quote с усечением сверх quoteMax рун.
func quoted(s string) string {
	r := []rune(s)
	if len(r) > quoteMax {
		return Quote(string(r[:quoteMax])) + "…"
	}
	return Quote(s)
}
