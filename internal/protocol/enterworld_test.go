package protocol

import (
	"encoding/hex"
	"testing"

	"github.com/udisondev/l2go/internal/protocol/fixture"
)

// Golden EnterWorld (C→GS): фиксированный 105-байтовый кадр (104 payload);
// серверные поля (hwinfo/tracert) не потребляются — представление проверяет
// только длину. Формат — Mobius CT_0_Interlude clientpackets/EnterWorld.java.
func TestEnterWorldGolden(t *testing.T) {
	t.Parallel()
	f := enterworldFixture(t, "ENTER_WORLD")
	if len(f.Payload) != 104 {
		t.Fatalf("payload фикстуры = %d Б; want 104", len(f.Payload))
	}
	var dst [EnterWorldSize]byte
	n := WriteEnterWorld(dst[:])
	if n != len(dst) || dst[0] != byte(enterWorld) {
		t.Fatalf("WriteEnterWorld = %d, op 0x%02X; want %d, 0x%02X", n, dst[0], len(dst), byte(enterWorld))
	}
	v, ok := NewEnterWorldView(wire(f))
	if !ok {
		t.Fatal("NewEnterWorldView: ok = false")
	}
	if len(v) != EnterWorldSize {
		t.Errorf("len(view) = %d; want %d", len(v), EnterWorldSize)
	}
	if _, ok := NewEnterWorldView(wire(f)[:EnterWorldSize-1]); ok {
		t.Error("обрезанный EnterWorld: ok = true; want false")
	}
	// Писатель l2client пишет нулевые hwinfo/tracert: кадр структурно тот же,
	// серверной семантики в байтах нет.
	if dst[1] != 0 || dst[104] != 0 {
		t.Error("WriteEnterWorld: не нулевые непотребляемые поля")
	}
}

// Golden пустых списков и ActionFailed (GS→C) против независимых векторов:
// ItemList — showWindow=0 канона входа (EnterWorld шлёт ItemList(player,
// false)), SkillList/ShortCutInit — нулевые счётчики, ActionFailed — маркер
// из одного опкода.
func TestEnterWorldBurstGolden(t *testing.T) {
	t.Parallel()
	fixes := enterworldFixtures(t)

	il := fixes["ITEM_LIST"]
	var ilDst [EmptyItemListSize]byte
	n := WriteEmptyItemList(ilDst[:])
	if n != len(ilDst) || ilDst[0] != byte(itemList) ||
		hex.EncodeToString(ilDst[1:]) != hex.EncodeToString(il.Payload) {
		t.Errorf("EmptyItemList = %d байт, payload % x; want %d, %s", n, ilDst[1:], len(ilDst), il.Payload)
	}
	ilv, ok := NewItemListView(wire(il))
	if !ok || ilv.ShowWindow() != 0 || ilv.Count() != 0 {
		t.Errorf("ItemListView = %d/%d, %v; want 0/0, true", ilv.ShowWindow(), ilv.Count(), ok)
	}

	sl := fixes["SKILL_LIST"]
	var slDst [EmptySkillListSize]byte
	n = WriteEmptySkillList(slDst[:])
	if n != len(slDst) || slDst[0] != byte(skillList) ||
		hex.EncodeToString(slDst[1:]) != hex.EncodeToString(sl.Payload) {
		t.Errorf("EmptySkillList = %d байт, payload % x; want %d, %s", n, slDst[1:], len(slDst), sl.Payload)
	}
	slv, ok := NewSkillListView(wire(sl))
	if !ok || slv.Count() != 0 {
		t.Errorf("SkillListView count = %d, %v; want 0, true", slv.Count(), ok)
	}

	sc := fixes["SHORT_CUT_INIT"]
	var scDst [EmptyShortCutInitSize]byte
	n = WriteEmptyShortCutInit(scDst[:])
	if n != len(scDst) || scDst[0] != byte(shortCutInit) ||
		hex.EncodeToString(scDst[1:]) != hex.EncodeToString(sc.Payload) {
		t.Errorf("EmptyShortCutInit = %d байт, payload % x; want %d, %s", n, scDst[1:], len(scDst), sc.Payload)
	}
	scv, ok := NewShortCutInitView(wire(sc))
	if !ok || scv.Count() != 0 {
		t.Errorf("ShortCutInitView count = %d, %v; want 0, true", scv.Count(), ok)
	}

	af := fixes["ACTION_FAIL"]
	var afDst [ActionFailedSize]byte
	n = WriteActionFailed(afDst[:])
	if n != len(afDst) || afDst[0] != byte(actionFail) || len(af.Payload) != 0 {
		t.Errorf("ActionFailed = %d байт, op 0x%02X, fixture %d Б; want 1, 0x%02X, 0",
			n, afDst[0], len(af.Payload), byte(actionFail))
	}
	if _, ok := NewActionFailedView(afDst[:]); !ok {
		t.Error("NewActionFailedView: ok = false; want true")
	}
}

// enterworldFixture — единичный вектор группы enterworld.
func enterworldFixture(t *testing.T, name string) fixture.Fixture {
	t.Helper()
	fixes := enterworldFixtures(t)
	f, ok := fixes[name]
	if !ok {
		t.Fatalf("фикстура enterworld/%s не найдена", name)
	}
	return f
}

// enterworldFixtures — все векторы группы enterworld по имени.
// Байты — ручной hex по канону (независимая деривация); проверено живым клиентом на КТ-3.
func enterworldFixtures(t *testing.T) map[string]fixture.Fixture {
	t.Helper()
	rows, err := fixture.Load("enterworld")
	if err != nil {
		t.Fatalf("fixture.Load(enterworld): %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("фикстур enterworld: %d; want 5", len(rows))
	}
	out := make(map[string]fixture.Fixture, len(rows))
	for _, r := range rows {
		out[r.Name] = r
	}
	return out
}
