// Трафик-лог: детерминированные строки, значения полей и имена пакетов.

package l2client

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/udisondev/l2go/internal/protocol"
)

// Field — поле типизированного пакета трафик-лога: ключ и готовое значение.
type Field struct {
	K, V string
}

func num(v int64) string    { return strconv.FormatInt(v, 10) }
func num32(v int32) string  { return num(int64(v)) }
func flt(v float64) string  { return strconv.FormatFloat(v, 'g', -1, 64) }
func boolean(v bool) string { return strconv.FormatBool(v) }
func hexs(b []byte) string  { return hex.EncodeToString(b) }

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

// hexDumpMax — длина hex-дампа фолбэка: поток мусорных 0xFFFF-кадров не
// заливает лог мегабайтами текста.
const hexDumpMax = 64

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

// unknownName — имя входящего game-кадра без типизированного представления:
// имя каталога, «??(0xNN)» для неизвестного опкода, Ex-семейство — имя по sub
// (uint16LE в байтах 1–2) или «??(0xFE:0xNNNN)»; обрезанные тела не читаются.
func unknownName(body []byte) string {
	if len(body) == 0 {
		return "??(0x??)"
	}
	if body[0] == protocol.ExGSOpcode {
		if len(body) < 3 {
			return "??(0xFE)"
		}
		sub := binary.LittleEndian.Uint16(body[1:3])
		if name, ok := protocol.GameServerExName(sub); ok {
			return name
		}
		return fmt.Sprintf("??(0xFE:0x%04X)", sub)
	}
	if name, ok := protocol.GameServerPacketName(body[0]); ok {
		return name
	}
	return fmt.Sprintf("??(0x%02X)", body[0])
}
