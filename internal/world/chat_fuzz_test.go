package world

// FuzzFoldSay2 — фазз-цель ветви свёртки чата (F15 реестра P3.11): любые
// байты тела Say2 не паникуют в свёртке (критерий приёмки «битый UTF-16 —
// дроп с метрикой, не паника») и каждое письмо имеет ровно один исход:
// доставлено (кадры CreatureSay), разрыв (Retires) либо один класс
// счётчиков. Легитимные семены строит WriteSay2 — UTF-16LE: сырые
// ASCII-байты давали структурно битые кадры и не достигали ветвей
// доставки/бакета (F21 реестра); злые тела — testdata/fuzz-корпус.

import (
	"math/rand/v2"
	"testing"

	"github.com/udisondev/l2go/internal/protocol"
)

func FuzzFoldSay2(f *testing.F) {
	for _, seed := range []struct {
		text string
		typ  protocol.ChatType
	}{
		{"hello", protocol.ChatGeneral},
		{"привет, мир", protocol.ChatGeneral},
		{"", protocol.ChatGeneral},           // пустой → разрыв
		{"spam\bID=1", protocol.ChatGeneral}, // \b → контент-дроп
		{"to you", protocol.ChatWhisper},     // не-ALL → игнор
	} {
		b := make([]byte, protocol.Say2Size(seed.text, seed.typ, ""))
		protocol.WriteSay2(b, seed.text, seed.typ, "")
		f.Add(b[1:]) // тело без опкода — опкод добавляет harness
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		st := newState()
		ent := chatEnt(1, 7, syncPos.X, syncPos.Y, syncPos.Z)
		witness := chatEnt(2, 8, syncPos.X+10, syncPos.Y, syncPos.Z)
		ents := []*Entity{ent, witness}
		payload := append([]byte{protocol.OpCSay2}, body...)
		res := Fold(10, 0, rand.New(rand.NewPCG(1, 10)), st, ents,
			portion(sayRawLetter(1, payload)), nil, testEnv(nil))
		// Инвариант классификации: письмо либо оборвало коннект (Retires),
		// либо ровно один исход из {доставлено, дроп-контента, флуд, игнор,
		// структурный дроп}; вакуум (ни класса, ни кадров, ни ухода) — красный.
		classified := st.ChatDropped + st.ChatFlooded + st.ChatIgnored + st.DroppedFrames
		delivered := 0
		for _, p := range res.Pushes {
			if (p.Client == 7 || p.Client == 8) && len(p.Frame) > 0 && p.Frame[0] == opCreatureSay {
				delivered++
			}
		}
		if classified > 1 || (classified == 0 && delivered == 0 && len(res.Retires) == 0) {
			t.Fatalf("письмо исчезло или раздвоилось: классы=%d доставлено=%d retires=%d",
				classified, delivered, len(res.Retires))
		}
	})
}
