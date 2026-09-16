package world

import (
	"bytes"
	"math/rand/v2"
	"testing"

	"github.com/udisondev/l2go/internal/transport"
)

// stepRNG — сид RNG региона: свой на каждый шаг, от (regionID, tick) (D2).
func stepRNG(region RegionID, tick Tick) *rand.Rand {
	return rand.New(rand.NewPCG(uint64(region), uint64(tick)))
}

func foldPortions(t Tick, envs ...transport.Envelope) []Portion {
	return []Portion{{Region: 1, Tick: t, Envs: envs}}
}

func synthEntities(n int) []*Entity {
	ents := make([]*Entity, 0, n)
	for i := 1; i <= n; i++ {
		ents = append(ents, &Entity{ID: transport.EntityID(i), Owner: 1, HP: int32(100 + i)})
	}
	return ents
}

// Два прогона одной последовательности порций через Fold — бит-в-бит одинаковые
// дампы (детерминизм: RNG-потребление, счётчики, порядок обхода).
func TestFoldDeterministicBitExact(t *testing.T) {
	run := func() []byte {
		st := &State{}
		ents := synthEntities(3)
		ticks := []Tick{10, 11, 11, 13}
		portions := map[Tick][]Portion{
			10: foldPortions(10,
				transport.Envelope{To: transport.Addr{Entity: 1}, FromID: 9, Kind: transport.KindAggro},
				transport.Envelope{To: transport.Addr{Entity: 2}, FromID: 9, Kind: transport.KindClientFrame},
			),
			11: foldPortions(11, transport.Envelope{FromID: 7, Kind: transport.KindXP}),
			13: nil,
		}
		for _, n := range ticks {
			Fold(n, 1, stepRNG(1, n), st, ents, portions[n], nil, testRules())
		}
		return st.Dump(ents)
	}
	first, second := run(), run()
	if !bytes.Equal(first, second) {
		t.Fatalf("два прогона одной последовательности дали разные дампы")
	}
}

// Другой сид (regionID, tick) — дамп отличается: RNG реально сеется и
// потребляется (Noise), состояние чувствительно.
func TestFoldSeedSensitivity(t *testing.T) {
	st1, ents1 := &State{}, synthEntities(2)
	st2, ents2 := &State{}, synthEntities(2)
	Fold(100, 1, stepRNG(1, 100), st1, ents1, nil, nil, testRules())
	Fold(100, 1, stepRNG(2, 100), st2, ents2, nil, nil, testRules())
	if bytes.Equal(st1.Dump(ents1), st2.Dump(ents2)) {
		t.Fatalf("другой (regionID, tick) дал тот же дамп: RNG не влияет на состояние")
	}
	// тот же регион, другой тик — тоже отличается
	st3, ents3 := &State{}, synthEntities(2)
	Fold(101, 1, stepRNG(1, 101), st3, ents3, nil, nil, testRules())
	if bytes.Equal(st1.Dump(ents1), st3.Dump(ents3)) {
		t.Fatalf("другой tick дал тот же дамп")
	}
}

// Повторный шаг того же тика (немедленное пробуждение): delta=0, Noise не
// самопогашается (wrapping-add), Beat/Steps растут на каждый шаг.
func TestFoldRepeatedTickDeltaZero(t *testing.T) {
	st, ents := &State{}, synthEntities(1)
	Fold(50, 1, stepRNG(1, 50), st, ents, nil, nil, testRules())
	one := st.Noise
	Fold(50, 0, stepRNG(1, 50), st, ents, nil, nil, testRules())
	if st.Steps != 2 {
		t.Errorf("Steps = %d; want 2", st.Steps)
	}
	if st.LastDelta != 0 {
		t.Errorf("LastDelta = %d; want 0", st.LastDelta)
	}
	if st.Noise == one {
		t.Errorf("Noise не изменился на повторном шаге тика (самопогашение)")
	}
	if ents[0].Beat != 50 {
		t.Errorf("Beat = %d; want 50", ents[0].Beat)
	}
}

// Счётчики: письма по Kind, heartbeat всего населения, StepResult фазы 3 пуст.
func TestFoldCountsAndHeartbeat(t *testing.T) {
	st, ents := &State{}, synthEntities(3)
	res := Fold(7, 2, stepRNG(3, 7), st, ents, foldPortions(7,
		transport.Envelope{Kind: transport.KindAggro},
		transport.Envelope{Kind: transport.KindAggro},
		transport.Envelope{Kind: transport.KindEnterWorld},
	), []AdvisoryIn{{Cell: 5, Entity: 11}}, testRules())
	if st.Letters != 3 {
		t.Errorf("Letters = %d; want 3", st.Letters)
	}
	if st.KindCounts[transport.KindAggro-1] != 2 {
		t.Errorf("KindCounts[Aggro] = %d; want 2", st.KindCounts[transport.KindAggro-1])
	}
	if st.KindCounts[transport.KindEnterWorld-1] != 1 {
		t.Errorf("KindCounts[EnterWorld] = %d; want 1", st.KindCounts[transport.KindEnterWorld-1])
	}
	if st.LastDelta != 2 {
		t.Errorf("LastDelta = %d; want 2", st.LastDelta)
	}
	for i, e := range ents {
		if e.Beat != 7 {
			t.Errorf("Beat[%d] = %d; want 7", i, e.Beat)
		}
	}
	// EnterWorld с пустым payload — dead-letter (валидация на применении),
	// эффектов нет.
	if len(res.Out) != 0 || len(res.Births) != 0 || len(res.Retires) != 0 {
		t.Errorf("StepResult не пуст на мусорном письме: %+v", res)
	}
	if st.DeadLetters != 1 {
		t.Errorf("DeadLetters = %d; want 1", st.DeadLetters)
	}
}

// Dump детерминирован по отсортированному населению: перестановка входного
// слайса меняет дамп (порядок обхода — часть состояния свёртки).
func TestStateDumpOrderSensitive(t *testing.T) {
	ents := synthEntities(3)
	st := &State{}
	reversed := []*Entity{ents[2], ents[1], ents[0]}
	if bytes.Equal(st.Dump(ents), st.Dump(reversed)) {
		t.Fatalf("дамп не чувствителен к порядку населения")
	}
}
