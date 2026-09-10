package protocol

import (
	"errors"
	"testing"
)

// Кадр провода: [uint16 LE длина всей записи][тело = длина−2].
func TestNextFrame(t *testing.T) {
	tests := []struct {
		name    string
		in      []byte
		want    []byte
		wantErr error
	}{
		{"пустой буфер", nil, nil, ErrFrameIncomplete},
		{"только заголовок", []byte{0x05, 0x00}, nil, ErrFrameIncomplete},
		{"часть тела", []byte{0x05, 0x00, 0x0A}, nil, ErrFrameIncomplete},
		{"полный кадр", []byte{0x04, 0x00, 0x0A, 0x0B}, []byte{0x0A, 0x0B}, nil},
		{"пустое тело", []byte{0x02, 0x00}, []byte{}, nil},
		{"два склеенных", []byte{0x03, 0x00, 0x0A, 0x03, 0x00, 0x0B}, []byte{0x0A}, nil},
		{"длина 0", []byte{0x00, 0x00}, nil, ErrFrameLength},
		{"длина 1", []byte{0x01, 0x00}, nil, ErrFrameLength},
		{"заявлено больше буфера", []byte{0xFF, 0xFF, 0x0A}, nil, ErrFrameIncomplete},
		{"максимум uint16 неполный", []byte{0xFF, 0xFF}, nil, ErrFrameIncomplete},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NextFrame(tt.in)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("NextFrame err = %v; want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NextFrame err = %v; want nil", err)
			}
			if string(got) != string(tt.want) {
				t.Errorf("body = %v; want %v", got, tt.want)
			}
			// потреблено всегда len(body)+2 — тело это срез в буфере вызывающего
			if len(got) > 0 && &got[0] != &tt.in[2] {
				t.Error("тело не срез в b[2:]")
			}
		})
	}
}

// Потребление склеенных кадров: последовательные вызовы разбирают поток.
func TestNextFrameStream(t *testing.T) {
	stream := []byte{0x03, 0x00, 0x0A, 0x02, 0x00, 0x04, 0x00, 0x0B, 0x0C}
	want := [][]byte{{0x0A}, {}, {0x0B, 0x0C}}
	for i, w := range want {
		body, err := NextFrame(stream)
		if err != nil {
			t.Fatalf("кадр %d: err = %v", i, err)
		}
		if string(body) != string(w) {
			t.Fatalf("кадр %d = %v; want %v", i, body, w)
		}
		stream = stream[len(body)+2:]
	}
	if _, err := NextFrame(stream); !errors.Is(err, ErrFrameIncomplete) {
		t.Errorf("хвост: err = %v; want ErrFrameIncomplete", err)
	}
}

// Разрез — 0 аллокаций (путь реестра ADR-0005).
func TestNextFrameZeroAllocs(t *testing.T) {
	buf := make([]byte, 4096)
	buf[0], buf[1] = 0xFF, 0x0F // заявлено 4095 — полный кадр в буфере
	allocs := testing.AllocsPerRun(100, func() {
		_, _ = NextFrame(buf)
	})
	if allocs != 0 {
		t.Errorf("NextFrame: %v аллокаций; want 0", allocs)
	}
}

func BenchmarkNextFrame(b *testing.B) {
	buf := make([]byte, 8192)
	b.SetBytes(int64(len(buf)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := NextFrame(buf); err != nil {
			b.Fatal(err)
		}
	}
}
