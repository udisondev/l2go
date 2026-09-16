package replica

import (
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
	t.Parallel()
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
		t.Fatalf("аллокаций на поколение = %d; want ≤ %d", n, blobAllocBudget)
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

	// 3 записи изменены, 1 удалена, 1 добавлена.
	for id := transport.EntityID(2); id <= 4; id++ {
		if err := b.Update(rec(id, 1000, 0, 0)); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	if err := b.Remove(9); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := b.Update(rec(42, 5, 5, 5)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	_, d2 := b.Build(2, vs)
	if got := d2.SpawnedCount(); got != 1 {
		t.Fatalf("введённых слотов = %d; want 1", got)
	}
	if got := d2.RemovedCount(); got != 1 {
		t.Fatalf("удалённых слотов = %d; want 1", got)
	}
	if got := d2.DirtyCount(); got != 3 {
		t.Fatalf("dirty-слотов = %d; want 3 (поз. 2,3,4)", got)
	}

	// Повторная сборка против той же публикации — пустой дифф, повторно
	// против новой — тоже (изменений нет).
	fresh, d3 := b.Build(3, vs)
	if d3.Any() {
		t.Fatal("дифф против той же публикации не пуст")
	}
	if _, d4 := b.Build(4, fresh); d4.Any() {
		t.Fatal("дифф против актуальной публикации не пуст")
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
	for round := 0; round < 100; round++ {
		if err := b.Remove(50); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if err := b.Update(rec(51, 0, 0, 0)); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	if _, d := b.Build(3, blob2); d.SpawnedCount() != 1 || d.RemovedCount() != 1 {
		t.Fatal("чурн: спавн+деспавн не сошлись в один слот")
	}
}

func TestBlobEmptyGenGrows(t *testing.T) {
	t.Parallel()
	b := NewBuilder()
	g0, d0 := b.Build(1, nil)
	if g0.Len() != 0 || d0.Any() {
		t.Fatal("пустой Builder: блоб пуст, дифф пуст")
	}
	if g0.Gen != 1 {
		t.Fatalf("Gen = %d; want 1", g0.Gen)
	}
}
