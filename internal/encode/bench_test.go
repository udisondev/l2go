package encode

import (
	"bytes"
	"sync"
	"testing"
)

// Бенч-семейство реестрового пути «Энкод/композиция кадра, криптоблок»
// (ADR-0005): Push→Take раунд с возвратом слэбов после (фейкового)
// завершения записи, крипта включена/выключена, размеры 40/256/1456 Б
// (лестница прецедента P1.1), одиночный кадр и батч. Машинные бюджеты —
// TestStageAllocBudgets.

var benchKey = [8]byte{9, 8, 7, 6, 5, 4, 3, 2}

func benchRound(b *testing.B, frameSize, batchSize int, crypt bool) {
	s, err := NewStage(1 << 20)
	if err != nil {
		b.Fatal(err)
	}
	defer s.Unregister(1)
	c := s.Register(1, benchKey)
	frame := bytes.Repeat([]byte{0xAB}, frameSize)

	var batch [][]byte
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		for i := 0; i < batchSize; i++ {
			s.Push(1, frame, crypt)
		}
		// «Запись» батча имитируется немедленно: следующий Take возвращает
		// слэбы — иначе 0-аллок Push нечестно измерим (каждый push — рост).
		var err error
		batch, err = takeAll(c, batch)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func takeAll(c *client, prev [][]byte) ([][]byte, error) {
	next, closing := c.Take(prev)
	if closing {
		return nil, errBenchmarkClose
	}
	return next, nil
}

var errBenchmarkClose = &benchError{}

type benchError struct{}

func (*benchError) Error() string { return "закрытие в бенче" }

// Push→Take раунд с параллельными пушерами (контенция client.mu пушер↔Take —
// база для второго пушера стационарных кадров). Спавн 4 горутин-пушеров на
// итерацию — часть харнесса; абсолют читать как верхнюю оценку контенции.
func BenchmarkPushTakeParallelCrypt(b *testing.B) {
	s, err := NewStage(1 << 20)
	if err != nil {
		b.Fatal(err)
	}
	defer s.Unregister(1)
	c := s.Register(1, benchKey)
	frame := bytes.Repeat([]byte{0xAB}, 38)
	var batch [][]byte
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Go(func() {
				for j := 0; j < 8; j++ {
					s.Push(1, frame, true)
				}
			})
		}
		wg.Wait()
		batch, _ = c.Take(batch)
	}
}

func BenchmarkPushTake40Plain(b *testing.B)      { benchRound(b, 38, 1, false) }
func BenchmarkPushTake40Crypt(b *testing.B)      { benchRound(b, 38, 1, true) }
func BenchmarkPushTake256Crypt(b *testing.B)     { benchRound(b, 254, 1, true) }
func BenchmarkPushTake1456Crypt(b *testing.B)    { benchRound(b, 1454, 1, true) }
func BenchmarkPushTakeBatch8Crypt(b *testing.B)  { benchRound(b, 38, 8, true) }
func BenchmarkPushTakeBatch32Crypt(b *testing.B) { benchRound(b, 38, 32, true) }
func BenchmarkPushTakeBatch32Plain(b *testing.B) { benchRound(b, 38, 32, false) }

// TestStageAllocBudgets — машинные бюджеты: 0 аллокаций на Push вне роста
// очереди и на Take в стационаре (возврат слэбов обменом).
func TestStageAllocBudgets(t *testing.T) {
	s, err := NewStage(1 << 16)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Unregister(2)
	c := s.Register(2, benchKey)
	frame := bytes.Repeat([]byte{0xCD}, 38)

	// Прогрев: два слэба ping-pong + носитель батча.
	var batch [][]byte
	for i := 0; i < 4; i++ {
		s.Push(2, frame, true)
		batch, _ = c.Take(batch)
	}

	allocs := testing.AllocsPerRun(200, func() {
		s.Push(2, frame, true)
		batch, _ = c.Take(batch)
	})
	if allocs != 0 {
		t.Errorf("стационарный раунд Push→Take: %.0f аллокаций; want 0", allocs)
	}
}

// Трим-чёрн (ревью P3.7b R8): стационарный след 70 × 256-бакет ≈ 17.9 КиБ
// сверх бюджета 8 КиБ — каждая волна платит pool-раундтрип излишка
// (38 слэбов). Честная цена осознанного трейда «память ↔ цикл».
func BenchmarkPushTakeOverBudget(b *testing.B) {
	benchRound(b, 254, 70, true)
}
