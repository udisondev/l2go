package protocol

import (
	"encoding/hex"
	"testing"

	"github.com/udisondev/l2go/internal/protocol/fixture"
)

// Golden Say2 (C→GS) в обе стороны: общий канал и whisper с хвостовой строкой
// адресата. Формат — Mobius CT_0_Interlude clientpackets/Say2.java: строка
// читается до типа, адресат — только при WHISPER.
func TestSay2Golden(t *testing.T) {
	fixes := chatFixtures(t)

	all := fixes["SAY2"]
	if Say2Size("Привет", ChatGeneral, "") != 1+14+4 {
		t.Errorf("Say2Size(ALL) = %d; want %d", Say2Size("Привет", ChatGeneral, ""), 19)
	}
	dst := make([]byte, Say2Size("Привет", ChatGeneral, ""))
	n := WriteSay2(dst, "Привет", ChatGeneral, "")
	if n != len(dst) || dst[0] != byte(say2) {
		t.Fatalf("WriteSay2 = %d, op 0x%02X", n, dst[0])
	}
	if hex.EncodeToString(dst[1:]) != hex.EncodeToString(all.Payload) {
		t.Errorf("payload = %s; want %s", hex.EncodeToString(dst[1:]), hex.EncodeToString(all.Payload))
	}
	v, ok := NewSay2View(wire(all))
	if !ok {
		t.Fatal("NewSay2View(ALL): ok = false")
	}
	if text, ok := v.Text(); !ok || text != "Привет" {
		t.Errorf("Text = %q, %v; want Привет", text, ok)
	}
	if v.Type() != ChatGeneral {
		t.Errorf("Type = %d; want ChatGeneral", v.Type())
	}
	if _, ok := v.Target(); ok {
		t.Error("Target при не-whisper: ok = true; want false")
	}

	whisper := fixes["SAY2_WHISPER"]
	dst = make([]byte, Say2Size("psst", ChatWhisper, "Vasya"))
	n = WriteSay2(dst, "psst", ChatWhisper, "Vasya")
	if n != len(dst) {
		t.Fatalf("WriteSay2(whisper) = %d; want %d", n, len(dst))
	}
	if hex.EncodeToString(dst[1:]) != hex.EncodeToString(whisper.Payload) {
		t.Errorf("payload = %s; want %s", hex.EncodeToString(dst[1:]), hex.EncodeToString(whisper.Payload))
	}
	v, ok = NewSay2View(wire(whisper))
	if !ok {
		t.Fatal("NewSay2View(whisper): ok = false")
	}
	if target, ok := v.Target(); !ok || target != "Vasya" {
		t.Errorf("Target = %q, %v; want Vasya", target, ok)
	}
}

// Отрицательная таблица Say2: нетерминированный текст, обрезанный тип,
// whisper без строки адресата — отказ без паники.
func TestSay2ViewEvil(t *testing.T) {
	if _, ok := NewSay2View([]byte{byte(say2)}); ok {
		t.Error("пустой кадр: ok = true; want false")
	}
	// Истинно нетерминированный текст: все юниты поля ненулевые до конца
	// буфера — терминатора нет ни в поле, ни дальше.
	noTerm := []byte{byte(say2), 'h', 0, 'i', 0, '!', 0}
	if _, ok := NewSay2View(noTerm); ok {
		t.Error("нетерминированный текст: ok = true; want false")
	}
	cutType := wire(chatFixtures(t)["SAY2"])[:15] // 1 + 12 (текст) + обрезанный D
	if _, ok := NewSay2View(cutType); ok {
		t.Error("обрезанный тип: ok = true; want false")
	}
	noTarget := []byte{byte(say2)}
	noTarget = append(noTarget, 0x70, 0, 0x73, 0, 0x74, 0, 0, 0) // "pst" + NUL
	noTarget = append(noTarget, 2, 0, 0, 0)                      // WHISPER без адресата
	v, ok := NewSay2View(noTarget)
	if !ok {
		t.Fatal("whisper без адресата: ok = false; want true (заголовок валиден)")
	}
	if _, ok := v.Target(); ok {
		t.Error("Target без строки: ok = true; want false")
	}
}

// Golden CreatureSay (GS→C): минимальный режим имя+текст канона.
func TestCreatureSayGolden(t *testing.T) {
	f := chatFixtures(t)["CREATURE_SAY"]
	if CreatureSaySize("Vasya", "Hello") != 1+12+12+4+4 {
		t.Errorf("CreatureSaySize = %d; want %d", CreatureSaySize("Vasya", "Hello"), 33)
	}
	dst := make([]byte, CreatureSaySize("Vasya", "Hello"))
	n := WriteCreatureSay(dst, 268, ChatGeneral, "Vasya", "Hello")
	if n != len(dst) || dst[0] != byte(creatureSay) {
		t.Fatalf("WriteCreatureSay = %d, op 0x%02X", n, dst[0])
	}
	if hex.EncodeToString(dst[1:]) != hex.EncodeToString(f.Payload) {
		t.Errorf("payload = %s; want %s", hex.EncodeToString(dst[1:]), hex.EncodeToString(f.Payload))
	}
	v, ok := NewCreatureSayView(wire(f))
	if !ok {
		t.Fatal("NewCreatureSayView: ok = false")
	}
	if v.SenderObjID() != 268 || v.Type() != ChatGeneral {
		t.Errorf("objID/type = %d/%d; want 268/0", v.SenderObjID(), v.Type())
	}
	if name, ok := v.SenderName(); !ok || name != "Vasya" {
		t.Errorf("SenderName = %q, %v; want Vasya", name, ok)
	}
	if text, ok := v.Text(); !ok || text != "Hello" {
		t.Errorf("Text = %q, %v; want Hello", text, ok)
	}
}

// Golden SystemMessage (GS→C): Id без параметров.
func TestSystemMessageGolden(t *testing.T) {
	f := chatFixtures(t)["SYSTEM_MESSAGE"]
	var dst [SystemMessageSize]byte
	n := WriteSystemMessage(dst[:], SystemMessageWelcomeToTheWorldOfLineageII)
	if n != len(dst) || dst[0] != byte(systemMessage) {
		t.Fatalf("WriteSystemMessage = %d, op 0x%02X", n, dst[0])
	}
	if hex.EncodeToString(dst[1:]) != hex.EncodeToString(f.Payload) {
		t.Errorf("payload = %s; want %s", hex.EncodeToString(dst[1:]), hex.EncodeToString(f.Payload))
	}
	v, ok := NewSystemMessageView(wire(f))
	if !ok {
		t.Fatal("NewSystemMessageView: ok = false")
	}
	if v.ID() != SystemMessageWelcomeToTheWorldOfLineageII || v.ParamCount() != 0 {
		t.Errorf("ID/params = %d/%d; want 34/0", v.ID(), v.ParamCount())
	}
}

// Реестр Id и таблица ChatType — выборочные значения против SystemMessageId.java
// и network/enums/ChatType.java @43ac8878 (машина против магических чисел).
func TestChatConstants(t *testing.T) {
	ids := []struct {
		id   SystemMessageID
		want int32
	}{
		{SystemMessageWelcomeToTheWorldOfLineageII, 34},
		{SystemMessageChattingIsCurrentlyProhibited, 147},
		{SystemMessageChatDisabled, 346},
	}
	for _, c := range ids {
		if c.id != SystemMessageID(c.want) {
			t.Errorf("Id = %d; want %d", c.id, c.want)
		}
	}
	types := []struct {
		typ  ChatType
		want int32
	}{
		{ChatGeneral, 0}, {ChatWhisper, 2}, {ChatShout, 1}, {ChatTrade, 8},
		{ChatParty, 3}, {ChatClan, 4}, {ChatHeroVoice, 17}, {ChatBattlefield, 20}, {ChatMPCCRoom, 21},
	}
	for _, c := range types {
		if c.typ != ChatType(c.want) {
			t.Errorf("ChatType = %d; want %d", c.typ, c.want)
		}
	}
}

// chatFixtures — все векторы группы chat по имени.
// Байты — ручной hex по канону (независимая деривация); проверено живым клиентом на КТ-3.
func chatFixtures(t *testing.T) map[string]fixture.Fixture {
	t.Helper()
	rows, err := fixture.Load("chat")
	if err != nil {
		t.Fatalf("fixture.Load(chat): %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("фикстур chat: %d; want 4", len(rows))
	}
	out := make(map[string]fixture.Fixture, len(rows))
	for _, r := range rows {
		out[r.Name] = r
	}
	return out
}
