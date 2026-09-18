// Трафик-лог: детерминированные строки. Представление и сборка полей
// типизированных пакетов — protocol (Field, Fields-методы представлений,
// GameServerFrameName); здесь — только писатель строк и форматтеры
// аргументов исходящих команд.

package l2client

import (
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/udisondev/l2go/internal/protocol"
)

// Field — поле типизированного пакета трафик-лога; тип и сборка — protocol.
type Field = protocol.Field

func num(v int64) string   { return strconv.FormatInt(v, 10) }
func num32(v int32) string { return num(int64(v)) }
func hexs(b []byte) string { return hex.EncodeToString(b) }

// hexDumpMax — длина hex-дампа фолбэка: поток мусорных 0xFFFF-кадров не
// заливает лог мегабайтами текста.
const hexDumpMax = 64

// HexBytes — hex-дамп байтов: целиком до 64 Б, дальше первые 64 Б и маркер
// остатка.
func HexBytes(b []byte) string {
	if len(b) <= hexDumpMax {
		return hexs(b)
	}
	return hexs(b[:hexDumpMax]) + fmt.Sprintf(" [+%dБ]", len(b)-hexDumpMax)
}

// LogRecv пишет строку входящего кадра с полями типизированного пакета.
func LogRecv(w io.Writer, name string, fields ...Field) {
	logLine(w, "←", name, fields)
}

// LogSend пишет строку исходящего кадра.
func LogSend(w io.Writer, name string, fields ...Field) {
	logLine(w, "→", name, fields)
}

// LogRecvHex пишет строку входящего кадра hex-фолбэком (нетипизированный или
// нераскрывшийся пакет).
func LogRecvHex(w io.Writer, name string, body []byte) {
	logLine(w, "←", name, []Field{{K: "hex", V: HexBytes(body)}})
}

// Ошибка записи лога не прерывает клиента: трафик-лог — наблюдаемость, а не
// поток управления.
func logLine(w io.Writer, dir, name string, fields []Field) {
	var b strings.Builder
	b.WriteString(dir)
	b.WriteByte(' ')
	b.WriteString(name)
	for _, f := range fields {
		b.WriteByte(' ')
		b.WriteString(f.K)
		b.WriteByte('=')
		b.WriteString(f.V)
	}
	b.WriteByte('\n')
	_, _ = w.Write([]byte(b.String()))
}
