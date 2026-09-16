package replica

import (
	"strconv"
	"testing"

	"github.com/udisondev/l2go/internal/transport"
)

// BenchmarkReplicaColdJoin — главный бет направления (ADR-0004 «Проверка»):
// холодный join — наблюдатель в центре, всё население внутри enter, view
// пуст; знаменатель — машинно посчитанные введённые пары. Якорь ~7–11 нс/пара
// — порядок величины, в журнал, не ворота; холодный путь несёт аллокации
// роста view (стационарный путь — SteadyDirty, 0 аллокаций).
func BenchmarkReplicaColdJoin(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run(scaleName(n), func(b *testing.B) {
			bl := NewBuilder()
			if err := bl.Update(rec(1, 0, 0, 0)); err != nil {
				b.Fatal(err)
			}
			for id := 2; id <= n; id++ {
				if err := bl.Update(rec(transport.EntityID(id), int32(id%1500), int32(id%800), 0)); err != nil {
					b.Fatal(err)
				}
			}
			blob, diff := bl.Build(1, nil)
			obs := rec(1, 0, 0, 0)
			b.ReportAllocs()
			b.ResetTimer()
			var pairs int
			for b.Loop() {
				v := NewView()
				st := &JoinStats{}
				ev := Join(blob, diff, v, obs, ModeDiff, st)
				pairs += len(ev.Enters)
				ev.Apply(v, blob)
			}
			b.StopTimer()
			if pairs == 0 {
				b.Fatal("вводов нет: знаменатель нс/пары нулевой")
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(pairs), "ns/pair")
		})
	}
}

// BenchmarkReplicaSteadyDirty — модель города P3.9+: полный view, все слоты
// dirty (данные для решения о сетке 3×3 — остаточный риск плана).
func BenchmarkReplicaSteadyDirty(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run(scaleName(n), func(b *testing.B) {
			bl := NewBuilder()
			if err := bl.Update(rec(1, 0, 0, 0)); err != nil {
				b.Fatal(err)
			}
			for id := 2; id <= n; id++ {
				if err := bl.Update(rec(transport.EntityID(id), int32(id%1500), int32(id%800), 0)); err != nil {
					b.Fatal(err)
				}
			}
			blob, diff := bl.Build(1, nil)
			obs := rec(1, 0, 0, 0)
			v := NewView()
			Join(blob, diff, v, obs, ModeDiff, &JoinStats{}).Apply(v, blob)
			// Все слоты dirty: сдвиг каждой записи на 1 по X.
			for id := 2; id <= n; id++ {
				if err := bl.Update(rec(transport.EntityID(id), int32(id%1600)+1, int32(id%900), 0)); err != nil {
					b.Fatal(err)
				}
			}
			dirtyBlob, dirtyDiff := bl.Build(2, blob)
			b.ReportAllocs()
			b.ResetTimer()
			var pairs int
			for b.Loop() {
				st := &JoinStats{}
				ev := Join(dirtyBlob, dirtyDiff, v, obs, ModeDiff, st)
				pairs += st.Pairs
				ev.Apply(v, dirtyBlob)
			}
			b.StopTimer()
			if pairs == 0 {
				b.Fatal("пар нет: знаменатель нулевой")
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(pairs), "ns/pair")
		})
	}
}

// BenchmarkReplicaPublishBlob — сборка+копия поколения (датчик GC
// outbound-пути; ReportAllocs обязателен).
func BenchmarkReplicaPublishBlob(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(scaleName(n), func(b *testing.B) {
			bl := NewBuilder()
			for id := 1; id <= n; id++ {
				if err := bl.Update(rec(transport.EntityID(id), int32(id), int32(id), 0)); err != nil {
					b.Fatal(err)
				}
			}
			var gen uint64
			blob, _ := bl.Build(1, nil)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				gen++
				blob, _ = bl.Build(gen, blob)
			}
		})
	}
}

func scaleName(n int) string {
	if n >= 1000 {
		return strconv.Itoa(n/1000) + "k"
	}
	return strconv.Itoa(n)
}
