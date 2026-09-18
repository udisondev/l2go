package world

// Бенчмарк fan-out рассылки чата (F10 реестра P3.11: горячий путь — O(игроков
// в радиусе) на реплику) и фазз-цель ветви свёртки (F15: длина по raw-юнитам,
// \b, декод-отказ и бакет живут у владельца, protocol-фазз их не достигает).

import (
	"math/rand/v2"
	"strconv"
	"testing"

	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/transport"
)

// BenchmarkSay2FanOut — один Say2-шаг с N живыми получателями в радиусе
// (ReportAllocs; живость: за шаг ровно N+1 кадр CreatureSay — анти-твин
// «мёртвого бенча» P3.10-F28). N=10/100/1000.
func BenchmarkSay2FanOut(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			benchSay2FanOut(b, n)
		})
	}
}

func benchSay2FanOut(b *testing.B, n int) {
	b.Helper()
	st := newState()
	ents := make([]*Entity, 0, n+1)
	sender := chatEnt(1, 1, syncPos.X, syncPos.Y, syncPos.Z)
	ents = append(ents, sender)
	for i := range n {
		ents = append(ents, chatEnt(transport.EntityID(i+2), uint64(i+2),
			syncPos.X+int32(i%900), syncPos.Y, syncPos.Z))
	}
	letter := sayLetter(sender.ID, "benchmark hello", protocol.ChatGeneral)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sender.Player.ChatBudget = chatSayCapMS // холодный fan-out: шаг без рефилла
		res := Fold(10, 0, rand.New(rand.NewPCG(1, 10)), st, ents,
			portion(letter), nil, testEnv(nil))
		if got := len(res.Pushes); got != n+1 {
			b.Fatalf("живость: пушей = %d; want %d (мёртвый бенч)", got, n+1)
		}
		st.ChatDropped, st.ChatFlooded, st.ChatIgnored = 0, 0, 0
		st.Steps, st.Letters = 0, 0
	}
}
