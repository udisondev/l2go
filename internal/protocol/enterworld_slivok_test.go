package protocol

// Golden слитка входа: каждый писатель — против ручного hex (независимая
// деривация по writeImpl канона Mobius CT_0_Interlude @43ac8878; методика
// P3.5 — двойная сверка writer↔фикстура). Параметризованные golden выведены
// второй реализацией (python struct.pack) на фиксированных значениях.

import (
	"encoding/hex"
	"testing"
)

func TestEnterworldWritersGoldenHex(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		n    int
		w    func(dst []byte) int
		want string // hex полного кадра с опкодом
	}{
		{"SendMacroList-пустой", EmptySendMacroListSize, WriteEmptySendMacroList,
			"e702000000000000"},
		{"HennaInfo-без-красок", EmptyHennaInfoSize, WriteEmptyHennaInfo,
			"e4" + "000000000000" + "03000000" + "00000000"},
		{"QuestList-пустой", EmptyQuestListSize, WriteEmptyQuestList, "800000"},
		{"EtcStatusUpdate-нейтральный", NeutralEtcStatusSize, WriteNeutralEtcStatus,
			"f3" + "00000000000000000000000000000000000000000000000000000000"},
		{"ExStorageMaxCount-канон", ExStorageMaxCountSize, WriteExStorageMaxCount,
			"fe2e005000000064000000960000000300000004000000320000003200000000000000"},
		{"FriendList-пустой", EmptyFriendListSize, WriteEmptyFriendList,
			"fa00000000"},
		{"SkillCoolTime-пустой", EmptySkillCoolTimeSize, WriteEmptySkillCoolTime,
			"c100000000"},
		{"LeaveWorld", LeaveWorldSize, WriteLeaveWorld, "7e"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dst := make([]byte, tc.n)
			got := tc.w(dst)
			if got != tc.n {
				t.Fatalf("писатель вернул %d; want %d", got, tc.n)
			}
			if hex.EncodeToString(dst) != tc.want {
				t.Fatalf("кадр:\ngot  %s\nwant %s", hex.EncodeToString(dst), tc.want)
			}
		})
	}
}

func TestRestartResponseGolden(t *testing.T) {
	t.Parallel()
	dst := make([]byte, RestartResponseSize)
	WriteRestartResponse(dst, false)
	if hex.EncodeToString(dst) != "5f00000000" {
		t.Fatalf("RestartResponse(false):\ngot  %s\nwant 5f00000000", hex.EncodeToString(dst))
	}
	WriteRestartResponse(dst, true)
	if hex.EncodeToString(dst) != "5f01000000" {
		t.Fatalf("RestartResponse(true):\ngot  %s\nwant 5f01000000", hex.EncodeToString(dst))
	}
}

// Golden ClientSetTime: деривация python struct.pack('<ii', minutes, 6).
func TestClientSetTimeGolden(t *testing.T) {
	t.Parallel()
	cases := []struct {
		minutes int32
		want    string
	}{
		{0, "ec0000000006000000"},
		{905, "ec8903000006000000"},
		{1439, "ec9f05000006000000"},
	}
	for _, tc := range cases {
		dst := make([]byte, ClientSetTimeSize)
		WriteClientSetTime(dst, tc.minutes, 6)
		if hex.EncodeToString(dst) != tc.want {
			t.Errorf("ClientSetTime(%d):\ngot  %s\nwant %s",
				tc.minutes, hex.EncodeToString(dst), tc.want)
		}
	}
}

// Опкоды/имена слитка — пин к каталогу (машинная связь констант).
func TestSlivokConstantsMatchCatalog(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		op   byte
		want string
	}{
		{sendMacroList, "SEND_MACRO_LIST"},
		{hennaInfo, "HENNA_INFO"},
		{questList, "QUEST_LIST"},
		{etcStatusUpdate, "ETC_STATUS_UPDATE"},
		{friendList, "FRIEND_LIST"},
		{skillCoolTime, "SKILL_COOL_TIME"},
		{clientSetTime, "CLIENT_SET_TIME"},
		{leaveWorld, "LEAVE_WORLD"},
		{restartResponse, "RESTART_RESPONSE"},
	} {
		name, ok := GameServerPacketName(tc.op)
		if !ok || name != tc.want {
			t.Errorf("опкод 0x%02X: каталог даёт %q (ok=%v); want %q", tc.op, name, ok, tc.want)
		}
	}
	// Ex-под и C→GS опкод слитка — своими каталогами.
	if name, ok := GameServerExName(exStorageSub); !ok || name != "EX_STORAGE_MAX_COUNT" {
		t.Errorf("Ex sub 0x%02X: %q (ok=%v); want EX_STORAGE_MAX_COUNT", exStorageSub, name, ok)
	}
	if name, ok := GameClientPacketName(requestRestart); !ok || name != "REQUEST_RESTART" {
		t.Errorf("опкод 0x%02X: %q (ok=%v); want REQUEST_RESTART", requestRestart, name, ok)
	}
}
