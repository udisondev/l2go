package replica

import (
	"slices"
	"testing"

	"github.com/udisondev/l2go/internal/transport"
)

// rec — тестовая запись с позицией (остальные поля — нули канона).
func rec(id transport.EntityID, x, y, z int32) Record {
	return Record{Entity: id, X: x, Y: y, Z: z, Name: "N" + itoa(id)}
}

func itoa(id transport.EntityID) string {
	if id == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for id > 0 {
		i--
		b[i] = byte('0' + id%10)
		id /= 10
	}
	return string(b[i:])
}

// blobAllocBudget — машинный аллок-порог поколения блоба (ADR-0004 ось 3:
// числовой бэкинг + строковый + структура).
const blobAllocBudget = 4

func TestBlobBuildCopiesNoAliasing(t *testing.T) {
	t.Parallel()
	b := NewBuilder()
	for id := transport.EntityID(1); id <= 50; id++ {
		if err := b.Update(rec(id, int32(id)*10, 100, -50)); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	blob, _ := b.Build(1, nil)
	if err := b.Update(rec(7, 999, 999, 999)); err != nil {
		t.Fatalf("Update после Build: %v", err)
	}
	if err := b.Update(Record{Entity: 9, X: 1, Y: 2, Z: 3, Name: "изменён"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	slot := -1
	for i := range blob.Len() {
		if blob.ID(i) == 7 {
			slot = i
		}
	}
	if slot < 0 {
		t.Fatal("запись 7 отсутствует в блобе")
	}
	if blob.X(slot) != 70 || blob.Y(slot) != 100 || blob.Z(slot) != -50 {
		t.Fatalf("алиасинг числовых колонок: (%d,%d,%d); want (70,100,-50)",
			blob.X(slot), blob.Y(slot), blob.Z(slot))
	}
	if blob.Name(slot) != "N7" {
		t.Fatalf("алиасинг строковой колонки: %q", blob.Name(slot))
	}
}

func TestBlobArenaAllocBudget(t *testing.T) {
	// Без t.Parallel: AllocsPerRun требует последовательный тест.
	b := NewBuilder()
	for id := transport.EntityID(1); id <= 100; id++ {
		if err := b.Update(rec(id, 1, 2, 3)); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	blob, _ := b.Build(1, nil) // прогрев (первая сборка растит ёмкости)
	n := testing.AllocsPerRun(20, func() {
		_, _ = b.Build(2, blob)
	})
	if n > blobAllocBudget {
		t.Fatalf("аллокаций на поколение = %v; want ≤ %d", n, blobAllocBudget)
	}
}

func TestBlobDiffAgainstPublishedBasis(t *testing.T) {
	t.Parallel()
	b := NewBuilder()
	for id := transport.EntityID(1); id <= 10; id++ {
		if err := b.Update(rec(id, int32(id), 0, 0)); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	vs, diff := b.Build(1, nil)
	if diff == nil || !diff.Any() {
		t.Fatal("холодная сборка: дифф обязан быть непуст")
	}

	// 3 записи изменены, 1 добавлена (новый слот), 1 удалена; добавление —
	// ДО удаления, чтобы новая запись не реюзнула слот удаляемой (реюз
	// слота — dirty-swap, не Spawned; абсолютный детект swap — в Join).
	for id := transport.EntityID(2); id <= 4; id++ {
		if err := b.Update(rec(id, 1000, 0, 0)); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	if err := b.Update(rec(42, 5, 5, 5)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := b.Remove(9); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	_, d2 := b.Build(2, vs)
	if got := d2.SpawnedCount(); got != 1 {
		t.Fatalf("введённых слотов = %d; want 1", got)
	}
	if got := d2.RemovedCount(); got != 1 {
		t.Fatalf("удалённых слотов = %d; want 1", got)
	}
	if got := d2.DirtyCount(); got != 4 {
		t.Fatalf("dirty-слотов = %d; want 4 (поз. 2,3,4 + ввод 42)", got)
	}

	// Сборка того же состояния в новой генерации и дифф против неё — пуст:
	// против старой публикации накопленный дифф законно непуст (до
	// публикации он и должен оставаться видимым).
	fresh, _ := b.Build(3, vs)
	if _, d4 := b.Build(4, fresh); d4.Any() {
		t.Fatal("дифф против актуального состояния не пуст")
	}
}

func TestBuilderSlotReuseAndDensity(t *testing.T) {
	t.Parallel()
	b := NewBuilder()
	for id := transport.EntityID(1); id <= 5; id++ {
		if err := b.Update(rec(id, 0, 0, 0)); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	blob, _ := b.Build(1, nil)
	var slot3 int
	for i := range blob.Len() {
		if blob.ID(i) == 3 {
			slot3 = i
		}
	}
	if err := b.Remove(3); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := b.Update(rec(50, 7, 7, 7)); err != nil { // id 50 > 3 — в свободный слот
		t.Fatalf("Update: %v", err)
	}
	blob2, _ := b.Build(2, blob)
	if blob2.ID(slot3) != 50 {
		t.Fatalf("реюз слота: слот %d держит %d; want 50 (первый свободный)", slot3, blob2.ID(slot3))
	}
	live := 0
	for i := range blob2.Len() {
		if blob2.IsMember(i) {
			live++
		}
	}
	if live != 5 || blob2.Len() != 5 {
		t.Fatalf("плотность: live=%d len=%d; want 5/5 (слот реюзнут, дыр нет)", live, blob2.Len())
	}
	// Цикл чурна не растит слоты.
	cur := transport.EntityID(50)
	for range 100 {
		if err := b.Remove(cur); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		cur++
		if err := b.Update(rec(cur, 0, 0, 0)); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	final, d := b.Build(3, blob2)
	if final.Len() != blob2.Len() {
		t.Fatalf("чурн вырос слоты: %d → %d", blob2.Len(), final.Len())
	}
	if d.SpawnedCount() != 0 || d.RemovedCount() != 0 || d.DirtyCount() != 1 {
		// Свап в одном слоте невидим членству (present→present) — только
		// dirty; абсолютный детект реюза — обязанность Join (F19).
		t.Fatalf("чурн в одном слоте: spawned=%d removed=%d dirty=%d; want 0/0/1",
			d.SpawnedCount(), d.RemovedCount(), d.DirtyCount())
	}
}

func TestBlobEmptyGenGrows(t *testing.T) {
	t.Parallel()
	b := NewBuilder()
	g0, d0 := b.Build(1, nil)
	if g0.Len() != 0 || d0.Any() {
		t.Fatal("пустой Builder: блоб пуст, дифф пуст")
	}
	if g0.Gen() != 1 {
		t.Fatalf("Gen = %d; want 1", g0.Gen())
	}
}

// Advisory: Lookup по живому id — значение; miss и зарезервированный id=0 —
// ok=false (нулевой id — не «нулевая запись»).
func TestAdvisoryLookupValueAndMiss(t *testing.T) {
	t.Parallel()
	b := NewBuilder()
	if err := b.Update(rec(5, -100, 200, -300)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	blob, _ := b.Build(1, nil)

	snap, ok := Lookup(blob, 5)
	if !ok || snap.Entity() != 5 {
		t.Fatalf("живая запись: ok=%v id=%d", ok, snap.Entity())
	}
	if x, y, z := snap.Pos(); x != -100 || y != 200 || z != -300 {
		t.Fatalf("позиция: (%d,%d,%d)", x, y, z)
	}
	if _, ok := Lookup(blob, 404); ok {
		t.Fatal("несуществующий id найден")
	}
	if _, ok := Lookup(blob, 0); ok {
		t.Fatal("id=0 (зарезервирован) найден")
	}
	if _, ok := Lookup(nil, 5); ok {
		t.Fatal("nil-блоб вернул запись")
	}
}

// Иммутабельность через полное следующее поколение: читатель держит блоб N
// и читает его поля, пока Builder мутируется и строит N+1 (окно Build, не
// Store — опасен алиасинг, не свап). Под -race детектор ловит алиасинг;
// без -race оракул — стабильность значений читателя.
func TestBlobReaderDuringNextBuild(t *testing.T) {
	b := NewBuilder()
	for id := transport.EntityID(1); id <= 64; id++ {
		if err := b.Update(rec(id, int32(id), int32(id*2), int32(id*3))); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	blob, _ := b.Build(1, nil)

	// Горутина-читатель шлёт первую расходимость каналом (фаталить из
	// горутины нельзя); основная фейлит с полным сообщением.
	bad := make(chan int, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			for i := range blob.Len() {
				if blob.IsMember(i) {
					if blob.X(i) != int32(blob.ID(i)) || blob.Y(i) != int32(blob.ID(i))*2 {
						select {
						case bad <- i:
						default:
						}
						return
					}
				}
			}
		}
	}()
	for round := uint64(2); round <= 50; round++ {
		for id := transport.EntityID(1); id <= 64; id++ {
			if err := b.Update(rec(id, int32(id)+int32(round), int32(id*2), int32(id*3))); err != nil {
				t.Fatalf("Update: %v", err)
			}
		}
		if err := b.Remove(3); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if _, d := b.Build(round, blob); d == nil {
			t.Fatal("nil дифф")
		}
		if err := b.Update(rec(3, 1, 6, 9)); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	<-done
	select {
	case i := <-bad:
		t.Fatalf("читатель видит мусор в слоте %d: алиасинг поколений", i)
	default:
	}
}

// Базис диффа — последняя публикация: неопубликованные изменения не
// потребляются Build'ом — повторная сборка против той же публикации даёт
// поэлементно тот же дифф (до публикации дифф жив).
func TestBlobDiffNotConsumedByBuild(t *testing.T) {
	t.Parallel()
	b := NewBuilder()
	for id := transport.EntityID(1); id <= 10; id++ {
		if err := b.Update(rec(id, int32(id), 0, 0)); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	vs, _ := b.Build(1, nil)
	if err := b.Update(rec(3, 999, 0, 0)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	_, d1 := b.Build(2, vs)
	_, d2 := b.Build(3, vs)
	if !slices.Equal(d1.spawned, d2.spawned) || !slices.Equal(d1.removed, d2.removed) ||
		!slices.Equal(d1.dirty, d2.dirty) {
		t.Fatal("повторная сборка против той же публикации дала другой дифф (изменения потреблены)")
	}
	if d1.DirtyCount() == 0 {
		t.Fatal("изменение не отмечено dirty")
	}
}
