package world

// FuzzFoldSay2 — фазз-цель ветви свёртки чата (F15 реестра P3.11): любые
// байты тела Say2 не паникуют в свёртке (критерий приёмки «битый UTF-16 —
// дроп с метрикой, не паника»); кадр обязан начинаться с опкода — первые
// байты добавляет harness, фаззит само переменно-структурное тело.

import (
	"math/rand/v2"
	"testing"

	"github.com/udisondev/l2go/internal/protocol"
)

func FuzzFoldSay2(f *testing.F) {
	// Семена: легитимные реплики писателем + злые тела (мусорный тип, битый
	// суррогат, обрезанное, \b, сверх-длина).
	for _, s := range []string{
		"hello",
		"\x00\x00\x00\x00\x00\x00",
		"\xe7\x03\x00\x00",
		"\x00\xd8\x41\x00\x00\x00\x00\x00\x00\x00\x00",
		"spam\x08ID=1\x00\x00\x00\x00",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" +
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		st := newState()
		ent := chatEnt(1, 7, syncPos.X, syncPos.Y, syncPos.Z)
		witness := chatEnt(2, 8, syncPos.X+10, syncPos.Y, syncPos.Z)
		ents := []*Entity{ent, witness}
		payload := append([]byte{protocol.OpCSay2}, body...)
		_ = Fold(10, 0, rand.New(rand.NewPCG(1, 10)), st, ents,
			portion(sayRawLetter(1, payload)), nil, testEnv(nil))
		// Инвариант классификации: письмо либо разорвало коннект (Retires),
		// либо попало ровно в один класс (доставлено/дроп/игнор/флуд/
		// структурный дроп) — считаем по кадрам и счётчикам.
		delivered := st.ChatDropped + st.ChatFlooded + st.ChatIgnored + st.DroppedFrames
		if delivered > 1 {
			t.Fatalf("письмо попало в %d классов (dropped=%d flooded=%d ignored=%d frames=%d)",
				delivered, st.ChatDropped, st.ChatFlooded, st.ChatIgnored, st.DroppedFrames)
		}
	})
}
