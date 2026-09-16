package encode

// Слиток входа: канонный порядок 16 кадров, ObjectID-база, формула игрового
// времени от Гц метронома (инвариант 7 — без хардкода периода).

import (
	"encoding/binary"
	"testing"

	"github.com/udisondev/l2go/internal/protocol"
)

func slivokData() EnterWorldData {
	return EnterWorldData{
		Entity: 7,
		User: protocol.UserInfoData{
			X: -71338, Y: 258271, Z: -3104,
			Name: "Testbot", Level: 1,
			MaxHp: 80, CurHp: 80, MaxMp: 30, CurMp: 30,
		},
		Heading:         0,
		GameTimeMinutes: 905,
	}
}

// Канонный порядок опкодов слитка (Mobius EnterWorld.java): UserInfo →
// SendMacroList → ItemList → ShortcutInit → HennaInfo → QuestList →
// EtcStatusUpdate → ExStorageMaxCount → FriendList → SystemMessage(welcome)
// → SystemMessage(Seven Signs) → SkillCoolTime → SkillList →
// ValidateLocation → ActionFailed → ClientSetTime.
func TestComposeEnterWorldSixteenFramesOrder(t *testing.T) {
	t.Parallel()
	frames := ComposeEnterWorld(slivokData())
	wantOps := []byte{
		0x04, 0xE7, 0x1B, 0x45, 0xE4, 0x80, 0xF3, 0xFE, 0xFA, 0x64, 0x64,
		0xC1, 0x58, 0x61, 0x25, 0xEC,
	}
	if len(frames) != 16 {
		t.Fatalf("кадров %d; want 16", len(frames))
	}
	for i, f := range frames {
		if len(f) == 0 || f[0] != wantOps[i] {
			t.Errorf("кадр %d: опкод 0x%02X; want 0x%02X", i, first(f), wantOps[i])
		}
	}
}

func first(f []byte) byte {
	if len(f) == 0 {
		return 0
	}
	return f[0]
}

// ObjectID клиента = 268435456 + EntityID (IdManagerConfig FirstObjectId).
func TestComposeEnterWorldObjectIDBase(t *testing.T) {
	t.Parallel()
	frames := ComposeEnterWorld(slivokData())
	// UserInfo: ObjID — поле после координат/vehicleId; проверяем через
	// представление (вьюха — оракул лэйаута P3.5).
	v, ok := protocol.NewUserInfoView(frames[0])
	if !ok {
		t.Fatalf("UserInfo не разбирается вьюхой")
	}
	if got := v.ObjID(); got != int32(ObjectIDBase+7) {
		t.Errorf("UserInfo.ObjID = %d; want %d", got, int32(ObjectIDBase+7))
	}
	// ValidateLocation (кадр 13): D(ObjID) сразу за опкодом — прямой байтовый
	// оракул (вьюхи C→S ValidatePosition не подходит: это S→C кадр).
	lv := frames[13]
	if got := int32(binary.LittleEndian.Uint32(lv[1:5])); got != int32(ObjectIDBase+7) {
		t.Errorf("ValidateLocation.ObjID = %d; want %d", got, int32(ObjectIDBase+7))
	}
}

// Формула игровых минут от Гц: канон при 10 Гц (IG-сутки 14400 с, IG-минута
// 10 с) и произвольное Гц дают согласованную минуту.
func TestComposeEnterWorldGameTimeFromHz(t *testing.T) {
	t.Parallel()
	cases := []struct {
		tick uint64
		hz   int
		want int32
	}{
		{0, 10, 0},
		{1000, 10, 10},      // 100 с = 10 IG-минут
		{1439999, 10, 1439}, // последняя IG-минута суток
		{1440000, 10, 0},    // новые IG-сутки
		{0, 40, 0},
		{400, 40, 1}, // 400 тиков при 40 Гц = 10 с = 1 IG-минута
		{1439999, 40, 719},
	}
	for _, tc := range cases {
		if got := GameTimeMinutes(tc.tick, tc.hz); got != tc.want {
			t.Errorf("GameTimeMinutes(%d, %dГц) = %d; want %d", tc.tick, tc.hz, got, tc.want)
		}
	}
}
