package replica

import (
	"testing"

	"github.com/udisondev/l2go/internal/transport"
)

// benchPop — синтетическое население N записей вокруг центра (кластер в
// enter-радиусе + фон до exit), M наблюдателей.
func benchPop(n, m int) ([]Record, []Observer) {
	recs := make([]Record, 0, n+m)
	for i := range n {
		x := int32((i % 2000) - 1000)
		y := int32((i / 2000 % 2000) - 1000)
		recs = append(recs, Record{Entity: transport.EntityID(i + 1), X: x, Y: y, Kind: RecordKindPlayer, Name: "bench"})
	}
	obs := make([]Observer, 0, m)
	for i := range m {
		id := transport.EntityID(n + i + 1)
		recs = append(recs, Record{Entity: id, X: 0, Y: 0, Kind: RecordKindPlayer, Name: "obs"})
		obs = append(obs, Observer{Entity: id, ConnID: uint64(i)})
	}
	return recs, obs
}

// BenchmarkJoinFullPass — полный проход M×N (d²-отсечка + предикат + дифф
// view + эмит) в примирительном режиме (блоб не закоммичен — appliedGen
// рассинхронизирован с базой, каждый шаг полный проход с полным эмитом):
// это верхняя оценка пути (рождение/движение наблюдателя и пост-паник-окна).
// Оговорка: компоновка проволочных кадров (CharInfo) ВНЕ метрики; якорь
// ADR-0004 7–11 нс/пара — ПОЛНАЯ константа (скан+компоновка+выборка);
// событийный стационар — BenchmarkJoinSteadyDirty.
func BenchmarkJoinFullPass(b *testing.B) {
	const n, m = 1000, 32
	recs, obs := benchPop(n, m)
	cfg := JoinConfig{Enter: 3500, Exit: 4200}
	p := NewPublisher()
	j := NewJoin(cfg)
	blob := p.Build(recs)
	j.Step(obs, blob)
	j.Apply()
	p.Commit(blob)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		j.Step(obs, blob)
		j.Apply()
	}
}

// BenchmarkJoinSteadyDirty — стационарная цена события: k dirty-записей × M
// наблюдателей (позиции кластера меняются, членство стабильно).
func BenchmarkJoinSteadyDirty(b *testing.B) {
	const n, m, k = 1000, 32, 64
	recs, obs := benchPop(n, m)
	cfg := JoinConfig{Enter: 3500, Exit: 4200}
	p := NewPublisher()
	j := NewJoin(cfg)
	// ввод в членство
	blob := p.Build(recs)
	j.Step(obs, blob)
	j.Apply()
	p.Commit(blob)
	moved := append([]Record(nil), recs...)
	b.ReportAllocs()
	b.ResetTimer()
	flip := false
	for b.Loop() {
		// bounce: позиции возвращаются в исходные — dirty стабилен,
		// членство не дрейфует из радиуса (кластер x∈[-1000..], enter=3500)
		flip = !flip
		for i := range k {
			if flip {
				moved[i].X += 10
			} else {
				moved[i].X -= 10
			}
		}
		blob := p.Build(moved)
		j.Step(obs, blob)
		j.Apply()
		p.Commit(blob)
	}
}

// BenchmarkPublishBlob — цена публикации на тик. Состав: N вставок копии
// слот-карты + N сравнений полей (dirty) + N×sizeof(Record) копирование
// значений; лестница симметрична тиковой (100/1000/10000).
func BenchmarkPublishBlob(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(popSuffix(n), func(b *testing.B) {
			recs, _ := benchPop(n, 0)
			p := NewPublisher()
			blob := p.Build(recs)
			p.Commit(blob)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				next := p.Build(recs)
				p.Commit(next)
			}
		})
	}
}

// BenchmarkAdvisoryRead — линейный baseline точечного чтения (база будущего
// aux-индекса; наклон виден на двух точках N).
func BenchmarkAdvisoryRead(b *testing.B) {
	for _, n := range []int{100, 10000} {
		b.Run(popSuffix(n), func(b *testing.B) {
			recs, _ := benchPop(n, 0)
			p := NewPublisher()
			p.Commit(p.Build(recs))
			b.ReportAllocs()
			b.ResetTimer()
			// средний элемент сегмента: линейный скан до конца — наклон
			// стоимости по N фальсифицируем (первый элемент мерил бы O(1))
			mid := transport.EntityID(n / 2)
			for b.Loop() {
				if _, ok := p.Read(0, mid); !ok {
					b.Fatalf("Read потерял запись %d", mid)
				}
			}
		})
	}
}

func popSuffix(n int) string {
	switch {
	case n >= 10000:
		return "N10000"
	case n >= 1000:
		return "N1000"
	default:
		return "N100"
	}
}
