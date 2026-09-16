package replica

import (
	"math/rand/v2"
	"testing"

	"github.com/udisondev/l2go/internal/transport"
)

func TestJoinSetEqualityProperty(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(7, 11))
	for iter := 0; iter < 200; iter++ {
		b := NewBuilder()
		obs := rec(1, 0, 0, 0)
		if err := b.Update(obs); err != nil {
			t.Fatalf("Update: %v", err)
		}
		model := map[transport.EntityID]bool{}
		for id := transport.EntityID(2); id <= 30; id++ {
			x := int32(rng.IntN(2*int(DefaultEnterRadius)+2) - int(DefaultEnterRadius))
			y := int32(rng.IntN(2*int(DefaultEnterRadius)+2) - int(DefaultEnterRadius))
			z := int32(rng.IntN(2001)) - 1000
			if err := b.Update(rec(id, x, y, z)); err != nil {
				t.Fatalf("Update: %v", err)
			}
			d := int64(x)*int64(x) + int64(y)*int64(y) + int64(z)*int64(z)
			model[id] = d <= int64(DefaultEnterRadius)*int64(DefaultEnterRadius)
		}
		blob, diff := b.Build(uint64(iter+1), nil)
		v := NewView()
		st := &JoinStats{}
		ev := Join(blob, diff, v, obs, ModeDiff, st)
		got := map[transport.EntityID]bool{}
		for _, slot := range ev.Enters {
			got[blob.ID(slot)] = true
		}
		if len(got) != len(ev.Enters) {
			t.Fatal("дубли вводов")
		}
		for id, want := range model {
			if id == 1 {
				continue
			}
			if got[id] != want {
				t.Fatalf("итер %d: id=%d ввод=%v; want %v (наблюдений %d)", iter, id, got[id], want, len(got))
			}
		}
		if got[1] {
			t.Fatal("self-пара введена")
		}
	}
}

func TestJoinBoundaryRadius(t *testing.T) {
	t.Parallel()
	// Ровно на границе enter: d == enterRadius → ввод (≤); +1 → нет.
	obs := rec(1, 0, 0, 0)
	cases := []struct {
		name   string
		x      int32
		wantIn bool
	}{
		{"enter", DefaultEnterRadius, true},
		{"enter+1", DefaultEnterRadius + 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBuilder()
			if err := b.Update(obs); err != nil {
				t.Fatalf("Update: %v", err)
			}
			if err := b.Update(rec(2, tc.x, 0, 0)); err != nil {
				t.Fatalf("Update: %v", err)
			}
			blob, diff := b.Build(1, nil)
			ev := Join(blob, diff, NewView(), obs, ModeDiff, &JoinStats{})
			if in := len(ev.Enters) == 1; in != tc.wantIn {
				t.Fatalf("%s: ввод=%v; want %v", tc.name, in, tc.wantIn)
			}
		})
	}
}

func TestJoinHysteresisArcZeroChurn(t *testing.T) {
	t.Parallel()
	obs := rec(1, 0, 0, 0)
	b := NewBuilder()
	if err := b.Update(obs); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// Житель вводится внутри enter, затем ходит по дуге радиуса
	// (enter+exit)/2 — зоне удержания гистерезиса.
	arc := int32((DefaultEnterRadius + DefaultExitRadius) / 2)
	if err := b.Update(rec(2, 100, 0, 0)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	blob, diff := b.Build(1, nil)
	v := NewView()
	ev := Join(blob, diff, v, obs, ModeDiff, &JoinStats{})
	ev.Apply(v, blob)
	if len(ev.Enters) != 1 || len(ev.Exits) != 0 {
		t.Fatalf("ввод дуги: enters=%d exits=%d; want 1/0", len(ev.Enters), len(ev.Exits))
	}

	churn := 0
	for step := 1; step <= 100; step++ {
		angle := int32(step) // дуга по компонентам, |p| остаётся ~arc
		x, y := int32(int64(arc)*cos100(angle%100)/10000), int32(int64(arc)*sin100(angle%100)/10000)
		if err := b.Update(rec(2, x, y, 0)); err != nil {
			t.Fatalf("Update: %v", err)
		}
		nb, nd := b.Build(uint64(step+1), blob)
		ev := Join(nb, nd, v, obs, ModeDiff, &JoinStats{}) // цель dirty → её пары
		churn += len(ev.Enters) + len(ev.Exits)
		ev.Apply(v, nb)
		blob = nb
	}
	if churn != 0 {
		t.Fatalf("осцилляция на дуге: churn=%d; want 0", churn)
	}

	// За exit — ровно одно удаление; возврат в зону удержания — ничего;
	// возврат внутрь enter — ровно один ввод.
	if err := b.Update(rec(2, DefaultExitRadius+1, 0, 0)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	nb, nd := b.Build(200, blob)
	ev = Join(nb, nd, v, obs, ModeDiff, &JoinStats{})
	if len(ev.Exits) != 1 || len(ev.Enters) != 0 {
		t.Fatalf("за exit: exits=%d enters=%d; want 1/0", len(ev.Exits), len(ev.Enters))
	}
	ev.Apply(v, nb)
	blob = nb

	if err := b.Update(rec(2, (DefaultEnterRadius+DefaultExitRadius)/2, 0, 0)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	nb, nd = b.Build(201, blob)
	ev = Join(nb, nd, v, obs, ModeDiff, &JoinStats{})
	if len(ev.Exits) != 0 || len(ev.Enters) != 0 {
		t.Fatal("зона удержания: события обязаны отсутствовать")
	}
	ev.Apply(v, nb)
	blob = nb

	if err := b.Update(rec(2, DefaultEnterRadius, 0, 0)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	nb, nd = b.Build(202, blob)
	ev = Join(nb, nd, v, obs, ModeDiff, &JoinStats{})
	if len(ev.Enters) != 1 || len(ev.Exits) != 0 {
		t.Fatalf("повторный вход: enters=%d; want 1", len(ev.Enters))
	}
}

func TestJoinDistanceIncludesZAxis(t *testing.T) {
	t.Parallel()
	obs := rec(1, 0, 0, 0)
	b := NewBuilder()
	if err := b.Update(obs); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// 2D-дистанция == enter (x=enter), Z-сдвиг выталкивает 3D за границу.
	if err := b.Update(rec(2, DefaultEnterRadius, 0, 5)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// x чуть меньше enter: 3D внутри при z=0, но с z — за границей.
	if err := b.Update(rec(3, DefaultEnterRadius-5, 0, 135)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// Полностью внутри с ненулевым Z.
	if err := b.Update(rec(4, 100, 100, 100)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	blob, diff := b.Build(1, nil)
	ev := Join(blob, diff, NewView(), obs, ModeDiff, &JoinStats{})
	got := map[transport.EntityID]bool{}
	for _, s := range ev.Enters {
		got[blob.ID(s)] = true
	}
	if got[2] || got[3] {
		t.Fatalf("3D за границей введён: %v", got)
	}
	if !got[4] {
		t.Fatal("3D внутри не введён")
	}
}

func TestJoinIdempotentSameView(t *testing.T) {
	t.Parallel()
	obs := rec(1, 0, 0, 0)
	b := NewBuilder()
	if err := b.Update(obs); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := b.Update(rec(2, 10, 10, 10)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	blob, diff := b.Build(1, nil)
	v := NewView()
	ev1 := Join(blob, diff, v, obs, ModeDiff, &JoinStats{})
	if len(ev1.Enters) != 1 {
		t.Fatal("первый вызов обязан ввести")
	}
	ev1.Apply(v, blob)
	// Повторный вызов с тем же view — даже с тем же непустым диффом.
	ev2 := Join(blob, diff, v, obs, ModeDiff, &JoinStats{})
	if len(ev2.Enters)+len(ev2.Exits) != 0 {
		t.Fatalf("идемпотентность: enters=%d exits=%d; want 0/0", len(ev2.Enters), len(ev2.Exits))
	}
}

func TestJoinEmptyDiffNoPairsNoPayloadReads(t *testing.T) {
	t.Parallel()
	obs := rec(1, 0, 0, 0)
	b := NewBuilder()
	if err := b.Update(obs); err != nil {
		t.Fatalf("Update: %v", err)
	}
	for id := transport.EntityID(2); id <= 20; id++ {
		if err := b.Update(rec(id, 100, 100, 100)); err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
	blob, diff := b.Build(1, nil)
	v := NewView()
	Join(blob, diff, v, obs, ModeDiff, &JoinStats{}).Apply(v, blob)

	// idle: тот же блоб (новая публикация без изменений), дифф пуст.
	_, empty := b.Build(2, blob)
	st := &JoinStats{}
	ev := Join(blob, empty, v, obs, ModeDiff, st)
	if len(ev.Enters)+len(ev.Exits) != 0 {
		t.Fatal("idle: события порождены")
	}
	if st.Pairs != 0 {
		t.Fatalf("idle: join-пар %d; want 0", st.Pairs)
	}
	if st.PayloadReads != 0 {
		t.Fatalf("idle: чтений пейлоадов %d; want 0 (скан битмапов)", st.PayloadReads)
	}
}

func TestJoinObserverDirtyRecomputesPairs(t *testing.T) {
	t.Parallel()
	obs := rec(1, 0, 0, 0)
	b := NewBuilder()
	if err := b.Update(obs); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := b.Update(rec(2, DefaultEnterRadius+500, 0, 0)); err != nil {
		t.Fatalf("Update: %v", err) // вне enter
	}
	if err := b.Update(rec(3, 50, 0, 0)); err != nil {
		t.Fatalf("Update: %v", err) // внутри
	}
	blob, diff := b.Build(1, nil)
	v := NewView()
	ev := Join(blob, diff, v, obs, ModeDiff, &JoinStats{})
	ev.Apply(v, blob)

	// Наблюдатель сместился к цели 2 — дифф пуст, но obsDirty.
	moved := rec(1, DefaultEnterRadius-100, 0, 0)
	st := &JoinStats{}
	ev2 := Join(blob, diff, v, moved, ModeObsDirty, st)
	if st.Pairs == 0 {
		t.Fatal("obsDirty: пары не пересчитаны (счётчик write-only?)")
	}
	entered := map[transport.EntityID]bool{}
	for _, s := range ev2.Enters {
		entered[blob.ID(s)] = true
	}
	if !entered[2] {
		t.Fatal("цель 2 не введена после сближения наблюдателя")
	}
}

func TestJoinSlotSwapAbsoluteRemoval(t *testing.T) {
	t.Parallel()
	obs := rec(1, 0, 0, 0)
	b := NewBuilder()
	if err := b.Update(obs); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := b.Update(rec(2, 10, 0, 0)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	blob, diff := b.Build(1, nil)
	v := NewView()
	Join(blob, diff, v, obs, ModeDiff, &JoinStats{}).Apply(v, blob)

	// Swap: тот же слот занят другой записью (present→present).
	if err := b.Remove(2); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := b.Update(rec(9, 20, 0, 0)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	nb, nd := b.Build(2, blob)
	ev := Join(nb, nd, v, obs, ModeDiff, &JoinStats{})
	var sawOld, sawNew bool
	for _, ex := range ev.Exits {
		if ex.ID == 2 {
			sawOld = true
		}
	}
	for _, s := range ev.Enters {
		if nb.ID(s) == 9 {
			sawNew = true
		}
	}
	if !sawOld || !sawNew {
		t.Fatalf("swap: удаление старого=%v ввод нового=%v; want true/true", sawOld, sawNew)
	}
}

func TestJoinRemovalWithoutMarkerImmediateDelete(t *testing.T) {
	t.Parallel()
	obs := rec(1, 0, 0, 0)
	b := NewBuilder()
	if err := b.Update(obs); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := b.Update(rec(2, 10, 0, 0)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	blob, diff := b.Build(1, nil)
	v := NewView()
	Join(blob, diff, v, obs, ModeDiff, &JoinStats{}).Apply(v, blob)

	if err := b.Remove(2); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	nb, nd := b.Build(2, blob)
	ev := Join(nb, nd, v, obs, ModeDiff, &JoinStats{})
	if len(ev.Exits) != 1 {
		t.Fatalf("уход без маркера: exits=%d; want ровно 1 (немедленно)", len(ev.Exits))
	}
	if len(ev.Enters) != 0 {
		t.Fatal("уход породил вводы")
	}
}

func TestVisiblePredicateFlagsTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		obs, tgt uint32
		want     bool
	}{
		{"чистые", 0, 0, true},
		{"флаг наблюдателя", 1, 0, true},
		{"флаг цели скрывает", 0, 1, false},
		{"оба", 1, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Visible(tc.obs, tc.tgt); got != tc.want {
				t.Fatalf("Visible(%d, %d) = %v; want %v", tc.obs, tc.tgt, got, tc.want)
			}
		})
	}
}

func TestJoinFlagsChangeReevaluatesAllPairs(t *testing.T) {
	t.Parallel()
	obs := rec(1, 0, 0, 0)
	b := NewBuilder()
	if err := b.Update(obs); err != nil {
		t.Fatalf("Update: %v", err)
	}
	known := rec(2, 10, 0, 0)
	if err := b.Update(known); err != nil {
		t.Fatalf("Update: %v", err)
	}
	blob, diff := b.Build(1, nil)
	v := NewView()
	Join(blob, diff, v, obs, ModeDiff, &JoinStats{}).Apply(v, blob)

	// Флаг цели изменился (dirty), дистанция та же — удаление.
	hidden := known
	hidden.Flags = 1
	if err := b.Update(hidden); err != nil {
		t.Fatalf("Update: %v", err)
	}
	nb, nd := b.Build(2, blob)
	ev := Join(nb, nd, v, obs, ModeDiff, &JoinStats{})
	if len(ev.Exits) != 1 || len(ev.Enters) != 0 {
		t.Fatalf("переоценка: exits=%d enters=%d; want 1/0", len(ev.Exits), len(ev.Enters))
	}
	ev.Apply(v, nb)

	// Снятие флага — ввод той же цели.
	if err := b.Update(known); err != nil {
		t.Fatalf("Update: %v", err)
	}
	nb2, nd2 := b.Build(3, nb)
	ev2 := Join(nb2, nd2, v, obs, ModeDiff, &JoinStats{})
	if len(ev2.Enters) != 1 || len(ev2.Exits) != 0 {
		t.Fatalf("снятие флага: enters=%d; want 1", len(ev2.Enters))
	}
}

func TestJoinDistanceSquaresInt64(t *testing.T) {
	t.Parallel()
	const maxI32 = int64(2147483647)
	obs := rec(1, int32(-maxI32/2), 0, 0)
	b := NewBuilder()
	if err := b.Update(obs); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := b.Update(rec(2, int32(maxI32/2), 0, 0)); err != nil {
		t.Fatalf("Update: %v", err) // dx ~ maxI32 — за границей
	}
	blob, diff := b.Build(1, nil)
	v := NewView()
	st := &JoinStats{}
	ev := Join(blob, diff, v, obs, ModeDiff, st) // не паникует
	if len(ev.Enters) != 0 {
		t.Fatal("переполнение дало ложный ввод")
	}
}

func cos100(a int32) int64 {
	table := [10]int64{10000, 9980, 9921, 9822, 9685, 9510, 9297, 9050, 8768, 8454}
	return table[a%10]
}

func sin100(a int32) int64 {
	table := [10]int64{0, 627, 1253, 1873, 2486, 3090, 3681, 4257, 4817, 5358}
	return table[a%10]
}

func TestJoinBoundaryExitStays(t *testing.T) {
	t.Parallel()
	// Ровно на exit — остаётся известным (удаление строго за границей).
	obs := rec(1, 0, 0, 0)
	b := NewBuilder()
	if err := b.Update(obs); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := b.Update(rec(2, DefaultEnterRadius-10, 0, 0)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	blob, diff := b.Build(1, nil)
	v := NewView()
	Join(blob, diff, v, obs, ModeDiff, &JoinStats{}).Apply(v, blob)

	if err := b.Update(rec(2, DefaultExitRadius, 0, 0)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	nb, nd := b.Build(2, blob)
	ev := Join(nb, nd, v, obs, ModeDiff, &JoinStats{})
	if len(ev.Exits) != 0 {
		t.Fatalf("d == exit: удалений %d; want 0 (граница удерживает)", len(ev.Exits))
	}
	ev.Apply(v, nb)
	if err := b.Update(rec(2, DefaultExitRadius+1, 0, 0)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	nb2, nd2 := b.Build(3, nb)
	ev2 := Join(nb2, nd2, v, obs, ModeDiff, &JoinStats{})
	if len(ev2.Exits) != 1 {
		t.Fatalf("d == exit+1: удалений %d; want 1", len(ev2.Exits))
	}
}
