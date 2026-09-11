package tap

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

// Раундтрип всех типов записей: запись → чтение → те же поля.
func TestJournalRoundtrip(t *testing.T) {
	var buf bytes.Buffer
	jw := newJournalWriter(&buf)
	if err := jw.connOpen(7, "127.0.0.1:2106", "37.228.91.208:7777", 12345); err != nil {
		t.Fatalf("connOpen: %v", err)
	}
	if err := jw.data(recData, 7, DirCtoS, 1, []byte{1, 2, 3}); err != nil {
		t.Fatalf("data: %v", err)
	}
	if err := jw.data(recData, 7, DirStoC, 2, bytes.Repeat([]byte{9}, 300)); err != nil {
		t.Fatalf("data big: %v", err)
	}
	if err := jw.data(recOriginal, 7, DirStoC, 3, []byte{4}); err != nil {
		t.Fatalf("original: %v", err)
	}
	if err := jw.connClose(7, errors.New("обрыв ноги")); err != nil {
		t.Fatalf("connClose: %v", err)
	}
	if err := jw.connClose(8, nil); err != nil {
		t.Fatalf("connClose nil: %v", err)
	}
	if err := jw.flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	jr := newJournalReader(bytes.NewReader(buf.Bytes()))
	var got []Record
	for {
		rec, err := jr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		got = append(got, rec)
	}
	if len(got) != 6 {
		t.Fatalf("записей %d, want 6", len(got))
	}
	r := got[0]
	if r.Type != recConnOpen || r.ConnID != 7 || r.Listen != "127.0.0.1:2106" ||
		r.Upstream != "37.228.91.208:7777" || r.OpenedAt != 12345 {
		t.Fatalf("connOpen: %+v", r)
	}
	r = got[1]
	if r.Type != recData || r.Dir != DirCtoS || r.TS != 1 || !bytes.Equal(r.Bytes, []byte{1, 2, 3}) {
		t.Fatalf("data: %+v", r)
	}
	if r := got[2]; len(r.Bytes) != 300 {
		t.Fatalf("большая data: %d байт", len(r.Bytes))
	}
	if r := got[3]; r.Type != recOriginal || !bytes.Equal(r.Bytes, []byte{4}) {
		t.Fatalf("original: %+v", r)
	}
	r = got[4]
	if r.Type != recConnClose || r.ConnID != 7 || r.Err != "обрыв ноги" {
		t.Fatalf("connClose: %+v", r)
	}
	if r := got[5]; r.Err != "" {
		t.Fatalf("connClose nil-ошибка: %q", r.Err)
	}
}

// Таблица злых журналов: мусорный magic, неизвестный тип, dir вне {1,2},
// len-несогласованность, обрыв тела — детерминированные ошибки, не паники.
func TestJournalEvilInputs(t *testing.T) {
	valid := func() []byte {
		var buf bytes.Buffer
		jw := newJournalWriter(&buf)
		_ = jw.connOpen(1, "a:1", "b:2", 0)
		_ = jw.data(recData, 1, DirCtoS, 0, []byte{1})
		_ = jw.flush()
		return buf.Bytes()
	}
	cases := []struct {
		name string
		raw  func() []byte
	}{
		{"мусорный magic", func() []byte { return []byte("GARBAGE!") }},
		{"обрыв magic", func() []byte { return []byte("L2TA") }},
		{"обрыв записи посередине", func() []byte {
			b := valid()
			return b[:len(b)-1]
		}},
		{"неизвестный тип записи", func() []byte {
			b := valid()
			b[len(journalMagic)] = 0x7F
			return b
		}},
		{"dir вне {1,2}", func() []byte {
			var buf bytes.Buffer
			jw := newJournalWriter(&buf)
			_ = jw.connOpen(1, "a", "b", 0)
			_ = jw.data(recData, 1, 7, 0, []byte{1})
			_ = jw.flush()
			return buf.Bytes()
		}},
		{"len-несогласованность data", func() []byte {
			var buf bytes.Buffer
			buf.WriteString(journalMagic)
			var head [5]byte
			head[0] = recData
			body := make([]byte, 25) // заявим длину больше полей
			binary.LittleEndian.PutUint32(head[1:], uint32(len(body)+1))
			buf.Write(head[:])
			buf.Write(body)
			return buf.Bytes()
		}},
		{"len за пределами файла", func() []byte {
			var buf bytes.Buffer
			buf.WriteString(journalMagic)
			var head [5]byte
			head[0] = recData
			binary.LittleEndian.PutUint32(head[1:], 1<<30)
			buf.Write(head[:])
			return buf.Bytes()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("паника: %v", r)
				}
			}()
			jr := newJournalReader(bytes.NewReader(tc.raw()))
			for {
				_, err := jr.Next()
				if errors.Is(err, io.EOF) {
					t.Fatal("ожидалась ошибка формата, получен чистый EOF")
				}
				if err != nil {
					if err == nil || !strings.Contains(err.Error(), "tap:") {
						t.Fatalf("ошибка без контекста: %v", err)
					}
					return
				}
			}
		})
	}
}

// Ошибка писателя залипает: capture обязан остановиться, а не молчать.
func TestJournalWriterStickyError(t *testing.T) {
	fail := &failWriter{}
	jw := newJournalWriter(fail)
	if err := jw.connOpen(1, "a", "b", 0); err == nil {
		t.Fatal("хотели ошибку")
	}
	if err := jw.data(recData, 1, DirCtoS, 0, []byte{1}); err == nil {
		t.Fatal("залипшая ошибка не возвращена")
	}
	if err := jw.connClose(1, nil); err == nil {
		t.Fatal("залипшая ошибка не возвращена")
	}
}

type failWriter struct{}

func (*failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
