package protocol

import (
	"encoding/hex"
	"testing"

	"github.com/udisondev/l2go/internal/protocol/fixture"
)

// Golden CharacterCreate (C→GS) в обе стороны против независимого вектора.
// Формат — Mobius CT_0_Interlude clientpackets/CharacterCreate.java: имя и
// 12×D в порядке race, sex, classId, INT, STR, CON, MEN, DEX, WIT, внешность.
func TestCharacterCreateGolden(t *testing.T) {
	t.Parallel()
	f := charcreateFixture(t, "CHARACTER_CREATE")
	d := CharacterCreateData{
		Name: "Newbie", Race: 0, Sex: 1, ClassID: 0,
		Int: 21, Str: 40, Con: 43, Men: 25, Dex: 30, Wit: 11,
		HairStyle: 2, HairColor: 3, Face: 1,
	}
	dst := make([]byte, CharacterCreateSize(d))
	n := WriteCharacterCreate(dst, d)
	if n != len(dst) || dst[0] != byte(characterCreate) {
		t.Fatalf("WriteCharacterCreate = %d, op 0x%02X; want %d, 0x%02X", n, dst[0], len(dst), byte(characterCreate))
	}
	if hex.EncodeToString(dst[1:]) != hex.EncodeToString(f.Payload) {
		t.Errorf("payload = %s; want %s", hex.EncodeToString(dst[1:]), hex.EncodeToString(f.Payload))
	}
	v, ok := NewCharacterCreateView(wire(f))
	if !ok {
		t.Fatal("NewCharacterCreateView: ok = false")
	}
	if name, ok := v.Name(); !ok || name != "Newbie" {
		t.Errorf("Name = %q, %v; want Newbie", name, ok)
	}
	if v.Race() != 0 || v.Sex() != 1 || v.ClassID() != 0 {
		t.Errorf("race/sex/class = %d/%d/%d; want 0/1/0", v.Race(), v.Sex(), v.ClassID())
	}
	if v.Int() != 21 || v.Str() != 40 || v.Con() != 43 || v.Men() != 25 || v.Dex() != 30 || v.Wit() != 11 {
		t.Errorf("статы = %d,%d,%d,%d,%d,%d; want 21,40,43,25,30,11", v.Int(), v.Str(), v.Con(), v.Men(), v.Dex(), v.Wit())
	}
	if v.HairStyle() != 2 || v.HairColor() != 3 || v.Face() != 1 {
		t.Errorf("внешность = %d,%d,%d; want 2,3,1", v.HairStyle(), v.HairColor(), v.Face())
	}
}

// CharacterCreateView: строка без терминатора и обрезанный хвост после имени —
// детерминированный отказ.
func TestCharacterCreateViewEvil(t *testing.T) {
	t.Parallel()
	full := wire(charcreateFixture(t, "CHARACTER_CREATE"))
	// нетерминированная строка: затираем все терминаторы нулями-единицами.
	none := append([]byte{}, full...)
	for i := 1; i+1 < len(none); i += 2 {
		if none[i] == 0 && none[i+1] == 0 {
			none[i], none[i+1] = 0x41, 0
		}
	}
	if _, ok := NewCharacterCreateView(none); ok {
		t.Error("нетерминированное имя: ok = true; want false")
	}
	// имя валидно, но хвост короче 12×D.
	for cut := 1; cut <= 48; cut++ {
		if _, ok := NewCharacterCreateView(full[:len(full)-cut]); ok {
			t.Errorf("хвост короче на %d Б: ok = true; want false", cut)
		}
	}
	// Вложенный NUL: имя обрывается рано (терминатор), хвост сдвигается; пока
	// хвост влезает — частичный разбор (конвенция doc.go), не влезает — отказ.
	nul := append([]byte{}, full...)
	copy(nul[3:5], []byte{0, 0}) // NUL на второй букве имени
	vNul, ok := NewCharacterCreateView(nul)
	if !ok {
		t.Fatal("вложенный NUL с полным хвостом: ok = false; want true (частичный разбор)")
	}
	if name, ok := vNul.Name(); !ok || name != "N" {
		t.Errorf("Name при раннем терминаторе = %q, %v; want N (терминация на NUL)", name, ok)
	}
	if _, ok := NewCharacterCreateView(nul[:len(nul)-12]); ok {
		t.Error("вложенный NUL и хвост не влезает: ok = true; want false")
	}
}

// Golden CharacterDelete и NewCharacter (C→GS) — маркеры флоу создания.
func TestCharacterDeleteAndNewCharGolden(t *testing.T) {
	t.Parallel()
	fDel := charcreateFixture(t, "CHARACTER_DELETE")
	var dst [CharacterDeleteSize]byte
	n := WriteCharacterDelete(dst[:], 3)
	if n != len(dst) || dst[0] != byte(characterDelete) {
		t.Fatalf("WriteCharacterDelete = %d, op 0x%02X", n, dst[0])
	}
	if hex.EncodeToString(dst[1:]) != hex.EncodeToString(fDel.Payload) {
		t.Errorf("payload = %s; want %s", hex.EncodeToString(dst[1:]), hex.EncodeToString(fDel.Payload))
	}
	v, ok := NewCharacterDeleteView(wire(fDel))
	if !ok || v.CharSlot() != 3 {
		t.Errorf("CharSlot = %d, %v; want 3, true", v.CharSlot(), ok)
	}

	var marker [NewCharacterSize]byte
	m := WriteNewCharacter(marker[:])
	if m != 1 || marker[0] != byte(newCharacter) {
		t.Fatalf("WriteNewCharacter = %d, op 0x%02X", m, marker[0])
	}
	if _, ok := NewNewCharacterView(marker[:1]); !ok {
		t.Error("NewNewCharacterView: ok = false; want true")
	}
	if _, ok := NewNewCharacterView(nil); ok {
		t.Error("NewNewCharacterView(nil): ok = true; want false")
	}
}

// Golden CharTemplates (GS→C): один шаблон Human Fighter; числа — константы
// канона HumanFighter.xml (дубль persist.HumanFighter осознан, решение P3.3).
func TestCharTemplatesGolden(t *testing.T) {
	t.Parallel()
	f := charcreateFixture(t, "CHAR_TEMPLATES")
	templates := []CharTemplate{{
		Race: 0, ClassID: 0,
		Str: 40, Dex: 30, Con: 43, Int: 21, Wit: 11, Men: 25,
	}}
	if CharTemplatesSize(len(templates)) != 1+4+len(templates)*80 {
		t.Errorf("CharTemplatesSize(1) = %d; want %d", CharTemplatesSize(len(templates)), 85)
	}
	dst := make([]byte, CharTemplatesSize(len(templates)))
	n := WriteCharTemplates(dst, templates)
	if n != len(dst) || dst[0] != byte(charTemplates) {
		t.Fatalf("WriteCharTemplates = %d, op 0x%02X", n, dst[0])
	}
	if hex.EncodeToString(dst[1:]) != hex.EncodeToString(f.Payload) {
		t.Errorf("payload = %s; want %s", hex.EncodeToString(dst[1:]), hex.EncodeToString(f.Payload))
	}
	v, ok := NewCharTemplatesView(wire(f))
	if !ok {
		t.Fatal("NewCharTemplatesView: ok = false")
	}
	if v.Count() != 1 {
		t.Fatalf("Count = %d; want 1", v.Count())
	}
	tpl, ok := v.Template(0)
	if !ok || tpl != templates[0] {
		t.Errorf("Template(0) = %+v, %v; want %+v", tpl, ok, templates[0])
	}
	if _, ok := v.Template(1); ok {
		t.Error("Template(1): ok = true; want false")
	}
}

// Отрицательная таблица CharTemplatesView: недоверенный счётчик — знаковый
// мусор и завышенный count при коротком теле дают отказ без паники.
func TestCharTemplatesViewEvil(t *testing.T) {
	t.Parallel()
	full := wire(charcreateFixture(t, "CHAR_TEMPLATES"))
	neg := append([]byte{}, full...)
	WriteD(neg[1:], -1) // count = 0xFFFFFFFF
	if _, ok := NewCharTemplatesView(neg); ok {
		t.Error("count = -1: ok = true; want false")
	}
	big := append([]byte{}, full...)
	WriteD(big[1:], 2) // count = 2 при теле одной записи
	if _, ok := NewCharTemplatesView(big); ok {
		t.Error("count = 2 при одной записи: ok = true; want false")
	}
	for cut := 1; cut <= 80; cut += 7 { // обрезка тела записи
		if _, ok := NewCharTemplatesView(full[:len(full)-cut]); ok {
			t.Fatalf("тело короче на %d Б: ok = true; want false", cut)
		}
	}
}

// Golden ответов создания/удаления (GS→C): фиксированные D-кадры канона.
func TestCharCreateResponsesGolden(t *testing.T) {
	t.Parallel()
	okFix := charcreateFixture(t, "CHAR_CREATE_OK")
	var okDst [CharCreateOkSize]byte
	n := WriteCharCreateOk(okDst[:])
	if n != len(okDst) || okDst[0] != byte(charCreateOk) ||
		hex.EncodeToString(okDst[1:]) != hex.EncodeToString(okFix.Payload) {
		t.Errorf("CharCreateOk = %d байт, payload % x; want %d, %s", n, okDst[1:], len(okDst), okFix.Payload)
	}

	failFix := charcreateFixture(t, "CHAR_CREATE_FAIL")
	var failDst [CharCreateFailSize]byte
	n = WriteCharCreateFail(failDst[:], CharCreateReasonNameAlreadyExists)
	if n != len(failDst) || failDst[0] != byte(charCreateFail) ||
		hex.EncodeToString(failDst[1:]) != hex.EncodeToString(failFix.Payload) {
		t.Errorf("CharCreateFail = %d байт, payload % x; want %d, %s", n, failDst[1:], len(failDst), failFix.Payload)
	}
	if got := CharCreateReasonNameAlreadyExists; got != 2 {
		t.Errorf("CharCreateReasonNameAlreadyExists = %d; want 2 (канон)", got)
	}

	delFix := charcreateFixture(t, "CHAR_DELETE_FAIL")
	var delDst [CharDeleteFailSize]byte
	n = WriteCharDeleteFail(delDst[:], CharDeleteReasonDeletionFailed)
	if n != len(delDst) || delDst[0] != byte(charDeleteFail) ||
		hex.EncodeToString(delDst[1:]) != hex.EncodeToString(delFix.Payload) {
		t.Errorf("CharDeleteFail = %d байт, payload % x; want %d, %s", n, delDst[1:], len(delDst), delFix.Payload)
	}
	if got := CharDeleteReasonDeletionFailed; got != 1 {
		t.Errorf("CharDeleteReasonDeletionFailed = %d; want 1 (канон)", got)
	}

	v, ok := NewCharCreateFailView(wire(failFix))
	if !ok || v.Reason() != CharCreateReasonNameAlreadyExists {
		t.Errorf("CharCreateFailView = %d, %v; want 2, true", v.Reason(), ok)
	}
	okView, ok := NewCharCreateOkView(wire(okFix))
	if !ok || okView.Ok() != 1 {
		t.Errorf("CharCreateOkView = %d, %v; want 1, true", okView.Ok(), ok)
	}
	delView, ok := NewCharDeleteFailView(wire(delFix))
	if !ok || delView.Reason() != CharDeleteReasonDeletionFailed {
		t.Errorf("CharDeleteFailView = %d, %v; want 1, true", delView.Reason(), ok)
	}
}

// charcreateFixture — единичный вектор группы charcreate; байты — ручной hex
// по канону (независимая деривация), проверено живым клиентом на КТ-3.
func charcreateFixture(t *testing.T, name string) fixture.Fixture {
	t.Helper()
	fixes := charcreateFixtures(t)
	f, ok := fixes[name]
	if !ok {
		t.Fatalf("фикстура charcreate/%s не найдена", name)
	}
	return f
}

// charcreateFixtures — все векторы группы charcreate по имени.
func charcreateFixtures(t *testing.T) map[string]fixture.Fixture {
	t.Helper()
	rows, err := fixture.Load("charcreate")
	if err != nil {
		t.Fatalf("fixture.Load(charcreate): %v", err)
	}
	if len(rows) != 7 {
		t.Fatalf("фикстур charcreate: %d; want 7", len(rows))
	}
	out := make(map[string]fixture.Fixture, len(rows))
	for _, r := range rows {
		out[r.Name] = r
	}
	return out
}
