// Package tap — сбор и разбор живого трафика: TCP-прокси с бинарным журналом
// (режим capture) и декодер журнала (режим decode). Служебный инструмент вне
// конвейера мира: единственный писатель журнала под мьютексом — сознательный
// компромисс (медленный писатель блокирует все соединения; пропускная
// способность фиксируется бенчмарком).
//
// Журнал содержит чувствительные данные сессии (чат, имена, шифротекст
// учётных данных) — хранить как секрет; в репозиторий не помещать.
package tap

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
)

// journalMagic открывает журнал; вторая версия формата — новый magic.
const journalMagic = "L2TAP\x01"

// Типы записей журнала.
const (
	recConnOpen  byte = 1
	recData      byte = 2 // байты, фактически отправленные в ногу
	recOriginal  byte = 3 // кадр до перезаписи rewrite-веткой
	recConnClose byte = 4
	recDirCtoS   byte = 1
	recDirStoC   byte = 2
)

// Направления ноги.
const (
	DirCtoS = recDirCtoS
	DirStoC = recDirStoC
)

// journalWriter — единственный писатель журнала: все записи под мьютексом,
// первая ошибка залипает (capture обязан остановиться, а не молчать). Без
// буферизации: ошибка писателя видна сразу, а не на flush.
type journalWriter struct {
	mu  sync.Mutex
	w   io.Writer
	err error
}

func newJournalWriter(w io.Writer) *journalWriter {
	j := &journalWriter{w: w}
	if _, err := io.WriteString(w, journalMagic); err != nil {
		j.err = fmt.Errorf("tap: заголовок журнала: %w", err)
	}
	return j
}

// flush — точка завершения capture; при прямой записи новых данных нет.
func (j *journalWriter) flush() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.err
}

// writeRecord — [тип u8][длина u32 LE][тело].
func (j *journalWriter) writeRecord(typ byte, body []byte) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.err != nil {
		return j.err
	}
	var head [5]byte
	head[0] = typ
	binary.LittleEndian.PutUint32(head[1:], uint32(len(body)))
	if _, err := j.w.Write(head[:]); err != nil {
		j.err = fmt.Errorf("tap: запись журнала: %w", err)
		return j.err
	}
	if _, err := j.w.Write(body); err != nil {
		j.err = fmt.Errorf("tap: запись журнала: %w", err)
		return j.err
	}
	return nil
}

func (j *journalWriter) connOpen(id uint64, listen, upstream string, openedAt int64) error {
	body := make([]byte, 0, 20+len(listen)+len(upstream))
	var num [8]byte
	binary.LittleEndian.PutUint64(num[:], id)
	body = append(body, num[:]...)
	binary.LittleEndian.PutUint64(num[:], uint64(openedAt))
	body = append(body, num[:]...)
	var l [4]byte
	binary.LittleEndian.PutUint32(l[:], uint32(len(listen)))
	body = append(body, l[:]...)
	body = append(body, listen...)
	binary.LittleEndian.PutUint32(l[:], uint32(len(upstream)))
	body = append(body, l[:]...)
	body = append(body, upstream...)
	return j.writeRecord(recConnOpen, body)
}

func (j *journalWriter) data(typ byte, id uint64, dir byte, ts int64, b []byte) error {
	body := make([]byte, 0, 21+len(b))
	var num [8]byte
	binary.LittleEndian.PutUint64(num[:], id)
	body = append(body, num[:]...)
	body = append(body, dir)
	binary.LittleEndian.PutUint64(num[:], uint64(ts))
	body = append(body, num[:]...)
	var l [4]byte
	binary.LittleEndian.PutUint32(l[:], uint32(len(b)))
	body = append(body, l[:]...)
	body = append(body, b...)
	return j.writeRecord(typ, body)
}

func (j *journalWriter) connClose(id uint64, connErr error) error {
	var msg []byte
	if connErr != nil {
		msg = []byte(connErr.Error())
	}
	body := make([]byte, 0, 12+len(msg))
	var num [8]byte
	binary.LittleEndian.PutUint64(num[:], id)
	body = append(body, num[:]...)
	var l [4]byte
	binary.LittleEndian.PutUint32(l[:], uint32(len(msg)))
	body = append(body, l[:]...)
	body = append(body, msg...)
	return j.writeRecord(recConnClose, body)
}

// Record — одна запись журнала.
type Record struct {
	Type             byte
	ConnID           uint64
	OpenedAt         int64
	Dir              byte
	TS               int64
	Bytes            []byte
	Listen, Upstream string
	Err              string
}

// errBadHeader — чужой заголовок журнала (не наш файл вовсе).
var errBadHeader = errors.New("чужой заголовок")

// journalReader — последовательное чтение журнала с валидацией формата.
type journalReader struct {
	r       *bufio.Reader
	started bool
}

func newJournalReader(r io.Reader) *journalReader {
	return &journalReader{r: bufio.NewReader(r)}
}

// Next возвращает следующую запись; io.EOF — чистый конец журнала.
func (j *journalReader) Next() (Record, error) {
	if !j.started {
		magic := make([]byte, len(journalMagic))
		if _, err := io.ReadFull(j.r, magic); err != nil {
			return Record{}, fmt.Errorf("tap: чтение заголовка журнала: %w", err)
		}
		if string(magic) != journalMagic {
			return Record{}, fmt.Errorf("tap: чужой заголовок журнала %q: %w", magic, errBadHeader)
		}
		j.started = true
	}
	var head [5]byte
	if _, err := io.ReadFull(j.r, head[:]); err != nil {
		if err == io.EOF {
			return Record{}, io.EOF
		}
		return Record{}, fmt.Errorf("tap: чтение шапки записи: %w", err)
	}
	typ := head[0]
	size := binary.LittleEndian.Uint32(head[1:])
	if size > 1<<24 {
		return Record{}, fmt.Errorf("tap: запись типа %d длиной %d байт — за пределами формата", typ, size)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(j.r, body); err != nil {
		return Record{}, fmt.Errorf("tap: тело записи типа %d: %w", typ, err)
	}
	return parseRecord(typ, body)
}

func parseRecord(typ byte, body []byte) (Record, error) {
	rec := Record{Type: typ}
	switch typ {
	case recConnOpen:
		if len(body) < 20 {
			return rec, fmt.Errorf("tap: connOpen: тело %d байт < 20", len(body))
		}
		rec.ConnID = binary.LittleEndian.Uint64(body)
		rec.OpenedAt = int64(binary.LittleEndian.Uint64(body[8:]))
		l1 := binary.LittleEndian.Uint32(body[16:])
		if int(20+l1) > len(body) {
			return rec, fmt.Errorf("tap: connOpen: listen длиной %d не помещается в тело", l1)
		}
		rec.Listen = string(body[20 : 20+l1])
		rest := body[20+l1:]
		if len(rest) < 4 {
			return rec, fmt.Errorf("tap: connOpen: нет длины upstream")
		}
		l2 := binary.LittleEndian.Uint32(rest)
		if int(4+l2) > len(rest) {
			return rec, fmt.Errorf("tap: connOpen: upstream длиной %d не помещается в тело", l2)
		}
		rec.Upstream = string(rest[4 : 4+l2])
	case recData, recOriginal:
		if len(body) < 21 {
			return rec, fmt.Errorf("tap: data: тело %d байт < 21", len(body))
		}
		rec.ConnID = binary.LittleEndian.Uint64(body)
		rec.Dir = body[8]
		if rec.Dir != recDirCtoS && rec.Dir != recDirStoC {
			return rec, fmt.Errorf("tap: data: направление %d вне {1,2}", rec.Dir)
		}
		rec.TS = int64(binary.LittleEndian.Uint64(body[9:]))
		n := binary.LittleEndian.Uint32(body[17:])
		if int(21+n) != len(body) {
			return rec, fmt.Errorf("tap: data: заявлено %d байт, в записи %d", n, len(body)-21)
		}
		rec.Bytes = body[21:]
	case recConnClose:
		if len(body) < 12 {
			return rec, fmt.Errorf("tap: connClose: тело %d байт < 12", len(body))
		}
		rec.ConnID = binary.LittleEndian.Uint64(body)
		n := binary.LittleEndian.Uint32(body[8:])
		if int(12+n) != len(body) {
			return rec, fmt.Errorf("tap: connClose: длина ошибки не сходится с телом")
		}
		rec.Err = string(body[12:])
	default:
		return rec, fmt.Errorf("tap: неизвестный тип записи %d", typ)
	}
	return rec, nil
}
