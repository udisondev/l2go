package replica

import (
	"fmt"
	"testing"

	"github.com/udisondev/l2go/internal/transport"
)

// benchPop — синтетическое население N записей вокруг центра (кластер в
// enter-радиусе + фон до exit), M наблюдателей. Координаты в пределах пары
// клеток сетки benchGrid — вырожденный до P4.1 профиль стоимости скана.
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

// benchGrid — сетка бенчей: то же начало (−8192, −8192), benchPop в 2×2 клеток.
var benchGrid = NewGrid(-8192, -8192, 13)

// mustJoinB — конструирование join в бенчах (сетка бенчей заведомо проходит
// фабрик-инвариант; паника = ошибка конфигурации бенча).
func mustJoinB(g Grid, cfg JoinConfig) *Join {
	j, err := NewJoin(g, cfg)
	if err != nil {
		panic("бенч: NewJoin: " + err.Error())
	}
	return j
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
	p := NewPublisher(benchGrid)
	j := mustJoinB(benchGrid, cfg)
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
	p := NewPublisher(benchGrid)
	j := mustJoinB(benchGrid, cfg)
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
			p := NewPublisher(benchGrid)
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
			p := NewPublisher(benchGrid)
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

// BenchmarkJoinStepIsolatedFromCrowd — фальсификатор независимости от толпы
// вне окна (ADR-0004 «Проверка»): изолированный Step одного наблюдателя при
// толпе 10⁵ вне окна, раскладка толпы по клеткам 1/10²/10⁴. Build линеен по
// населению и выведен из метрики (только Step+Apply); допустимый рост —
// логарифмический по числу клеток (бинарный поиск сегментов). Наклон по
// слотам толпы = возврат квадрат-скана (граница ~5000 движущихся, P3.10).
func BenchmarkJoinStepIsolatedFromCrowd(b *testing.B) {
	const crowd = 100_000
	for _, cells := range []int{1, 100, 10_000} {
		b.Run(fmt.Sprintf("cells=%d", cells), func(b *testing.B) {
			recs := []Record{
				{Entity: 1, X: -100, Y: -100, Kind: RecordKindPlayer, Name: "obs"},
				{Entity: 2, X: 100, Y: -100, Kind: RecordKindPlayer, Name: "member"},
			}
			for i := range crowd {
				x := int32(65536 + (i%cells)*8192 + i%97)
				y := int32(65536 + (i/cells)%cells*8192 + i%89)
				recs = append(recs, Record{Entity: transport.EntityID(1000 + i), X: x, Y: y, Kind: RecordKindPlayer})
			}
			obs := []Observer{{Entity: 1}}
			p := NewPublisher(benchGrid)
			j := mustJoinB(benchGrid, JoinConfig{Enter: 3500, Exit: 4200})
			blob := p.Build(recs)
			j.Step(obs, blob) // ввод члена
			j.Apply()
			p.Commit(blob)
			moved := append([]Record(nil), recs...)
			moved[1].X += 10 // стационарный dirty члена: пара жива каждый шаг
			blob = p.Build(moved)
			p.Commit(blob)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				// Build линеен по населению — ВНЕ метрики (один и тот же блоб:
				// dirty-бит члена статичен, Step пересчитывает события каждый вызов).
				// Apply не зовём: appliedGen остаётся равным base — каждый вызов
				// идёт событийным dirty-путём (не примирением)
				events := j.Step(obs, blob)
				if len(events) == 0 {
					b.Fatalf("живость: стрим пары потерян (мёртвый бенч)")
				}
			}
		})
	}
}

// BenchmarkPublishBlobMultiCell — плотная укладка: стоимость Build+Commit
// не зависит от числа клеток (1 клетка vs 100 при том же населении).
func BenchmarkPublishBlobMultiCell(b *testing.B) {
	const n = 1000
	for _, cells := range []int{1, 100} {
		b.Run(fmt.Sprintf("cells=%d", cells), func(b *testing.B) {
			recs := make([]Record, n)
			perAxis := 1 // клеток ровно cells: perAxis²
			for perAxis*perAxis < cells {
				perAxis++
			}
			for i := range recs {
				x, y := int32(-100), int32(-100)
				if cells > 1 {
					x = int32(i%perAxis)*8192 + 100
					y = int32(i/perAxis%perAxis)*8192 + 100
				}
				recs[i] = Record{Entity: transport.EntityID(i + 1), X: x, Y: y, Kind: RecordKindPlayer}
			}
			p := NewPublisher(benchGrid)
			p.Commit(p.Build(recs))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				p.Commit(p.Build(recs))
			}
		})
	}
}

// BenchmarkAdvisoryReadMultiCell — чтение по сегменту клетки: стоимость
// точечного чтения падает с рассредоточением населения (линейна по сегменту,
// не по населению).
func BenchmarkAdvisoryReadMultiCell(b *testing.B) {
	const n = 10_000
	for _, cells := range []int{1, 100} {
		b.Run(fmt.Sprintf("cells=%d", cells), func(b *testing.B) {
			recs := make([]Record, n)
			perAxis := 1 // клеток ровно cells: perAxis²
			for perAxis*perAxis < cells {
				perAxis++
			}
			for i := range recs {
				x, y := int32(-100+i%50*10), int32(-100+i/50*10)
				if cells > 1 {
					x = int32(i%perAxis)*8192 + 100
					y = int32(i/perAxis%perAxis)*8192 + 100
				}
				recs[i] = Record{Entity: transport.EntityID(i + 1), X: x, Y: y, Kind: RecordKindPlayer}
			}
			p := NewPublisher(benchGrid)
			p.Commit(p.Build(recs))
			mid := recs[n/2]
			midCell := benchGrid.CellOf(mid.X, mid.Y)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, ok := p.Read(midCell, mid.Entity); !ok {
					b.Fatalf("Read потерял запись %d", mid.Entity)
				}
			}
		})
	}
}
