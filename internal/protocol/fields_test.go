package protocol

import (
	"strings"
	"testing"
)

// assertFields — сравнение среза полей с ожиданием (вход-оракул полей
// трафик-лога).
func assertFields(t *testing.T, got, want []Field) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("полей %d (%v); want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("поле[%d] = %s=%s; want %s=%s", i, got[i].K, got[i].V, want[i].K, want[i].V)
		}
	}
}

// Quote: строки полей — в кавычках, управляющие руны экранируются \xHH
// (терминальные инъекции и срыв «одной строки на кадр» исключены).
func TestQuote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want string
	}{
		{"plain", `"plain"`},
		{"", `""`},
		{"с пробелом", `"с пробелом"`},
		{"tab\there", `"tab\x09here"`},
		{"nl\nhere", `"nl\x0Ahere"`},
		{"esc\x1b]0;x\x07", `"esc\x1B]0;x\x07"`},
		{"del\x7f", `"del\x7F"`},
		{`q"uote`, `"q\"uote"`},
		{`back\slash`, `"back\\slash"`},
	}
	for _, tt := range tests {
		if got := Quote(tt.in); got != tt.want {
			t.Errorf("Quote(%q) = %s; want %s", tt.in, got, tt.want)
		}
	}
}

// Усечение строковых полей: гигантское имя недоверенного пакета не
// разворачивает строку лога.
func TestQuotedTruncation(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("я", 300)
	got := quoted(long)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("quoted(300 рун) не усечён: %.40s…", got)
	}
	if n := len([]rune(got)); n > quoteMax+3 {
		t.Errorf("quoted = %d рун; want ≤ %d+3", n, quoteMax)
	}
}

// GameServerFrameName: имя входящего game-кадра без типизированного
// представления — имя каталога, «??(0xNN)» для неизвестного опкода,
// Ex-семейство — имя по sub или «??(0xFE:0xNNNN)»; обрезанные тела не
// читаются.
func TestGameServerFrameName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body []byte
		want string
	}{
		{"из каталога", []byte{0x1C, 0x01}, "SUNRISE"},
		{"неизвестный", []byte{0xBA, 0xAB}, "??(0xBA)"},
		{"пустое тело", nil, "??(0x??)"},
		{"Ex по sub", []byte{0xFE, 0x38, 0x00, 0x01}, "EX_SHOW_SCREEN_MESSAGE"},
		{"Ex неизвестный sub", []byte{0xFE, 0x99, 0x09, 0x01}, "??(0xFE:0x0999)"},
		{"Ex обрезанный", []byte{0xFE, 0x38}, "??(0xFE)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GameServerFrameName(tt.body); got != tt.want {
				t.Errorf("GameServerFrameName(% X) = %q; want %q", tt.body, got, tt.want)
			}
		})
	}
}

// Поля представлений — round-trip: писатель собирает кадр, представление
// читает, Fields отдаёт поля трафик-лога. Оракул — значения golden-флоу
// l2client (формат полей байт-в-байт) и явные ожидания на минимальных кадрах.
func TestViewFields(t *testing.T) {
	t.Parallel()
	modulus := make([]byte, 128)
	blowfish := make([]byte, 16)
	key := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}

	tests := []struct {
		name string
		got  func() []Field
		want []Field
	}{
		{"INIT", func() []Field {
			b := make([]byte, InitSize)
			WriteInit(b, 305419896, modulus, blowfish)
			v, _ := NewInitView(b)
			return v.Fields()
		}, []Field{
			{K: "session", V: "305419896"},
			{K: "revision", V: "0xC621"},
		}},
		{"GG_AUTH", func() []Field {
			var b [GGAuthSize]byte
			WriteGGAuth(b[:], 305419896)
			v, _ := NewGGAuthView(b[:])
			return v.Fields()
		}, []Field{{K: "response", V: "305419896"}}},
		{"LOGIN_OK", func() []Field {
			var b [LoginOkSize]byte
			WriteLoginOk(b[:], 169552957, 1298034544)
			v, _ := NewLoginOkView(b[:])
			return v.Fields()
		}, []Field{
			{K: "loginOk1", V: "169552957"},
			{K: "loginOk2", V: "1298034544"},
		}},
		{"LOGIN_FAIL", func() []Field {
			var b [LoginFailSize]byte
			WriteLoginFail(b[:], LoginFailReason(0x02))
			v, _ := NewLoginFailView(b[:])
			return v.Fields()
		}, []Field{{K: "reason", V: "0x02"}}},
		{"ACCOUNT_KICKED", func() []Field {
			var b [AccountKickedSize]byte
			WriteAccountKicked(b[:], KickDataStealer)
			v, _ := NewAccountKickedView(b[:])
			return v.Fields()
		}, []Field{{K: "reason", V: "0x01"}}},
		{"SERVER_LIST", func() []Field {
			servers := []ServerListEntry{
				{ID: 1, IP: [4]byte{127, 0, 0, 1}, Port: 7777, CurrentPlayers: 42, MaxPlayers: 500, Status: 1, ServerType: 1},
				{ID: 2, IP: [4]byte{127, 0, 0, 1}, Port: 7778, AgeLimit: 15, PvP: true, CurrentPlayers: 7, MaxPlayers: 100, Status: 1, ServerType: 4, Brackets: true},
			}
			chars := []ServerChars{
				{ServerID: 1, CharCount: 3, DeleteTimes: []int32{3600}},
				{ServerID: 2},
			}
			b := make([]byte, ServerListSize(servers, chars))
			WriteServerList(b, servers, chars, 1)
			v, _ := NewServerListView(b)
			return v.Fields()
		}, []Field{
			{K: "count", V: "2"},
			{K: "last", V: "1"},
			{K: "s1", V: "{id=1 cur=42 max=500 age=0 pvp=false status=1 type=1 brackets=false chars=3 del=3600}"},
			{K: "s2", V: "{id=2 cur=7 max=100 age=15 pvp=true status=1 type=4 brackets=true chars=0}"},
		}},
		{"PLAY_OK", func() []Field {
			var b [PlayOkSize]byte
			WritePlayOk(b[:], 287454020, 1432778632)
			v, _ := NewPlayOkView(b[:])
			return v.Fields()
		}, []Field{
			{K: "playOk1", V: "287454020"},
			{K: "playOk2", V: "1432778632"},
		}},
		{"PLAY_FAIL", func() []Field {
			var b [PlayFailSize]byte
			WritePlayFail(b[:], PlayFailReason(0x01))
			v, _ := NewPlayFailView(b[:])
			return v.Fields()
		}, []Field{{K: "reason", V: "0x01"}}},
		{"KEY_PACKET", func() []Field {
			var b [KeyPacketSize]byte
			WriteKeyPacket(b[:], 1, key[:], true, 1)
			v, _ := NewKeyPacketView(b[:])
			return v.Fields()
		}, []Field{
			{K: "result", V: "1"},
			{K: "encryption", V: "true"},
			{K: "serverID", V: "1"},
			{K: "key", V: "8"},
		}},
		{"GS_LOGIN_FAIL", func() []Field {
			var b [GSLoginFailSize]byte
			WriteGSLoginFail(b[:], GSReasonPasswordDoesNotMatchThisAccount)
			v, _ := NewGSLoginFailView(b[:])
			return v.Fields()
		}, []Field{{K: "reason", V: "0x02"}}},
		{"CHAR_SELECT_INFO", func() []Field {
			chars := []CharSelectionEntry{{
				Name: "Warrior", CharID: 268478617, Level: 10,
				CurHP: 42.5, MaxHP: 85.5, CurMP: 39.25, MaxMP: 78.5,
				SP: 100, Exp: 123456789,
			}}
			b := make([]byte, CharSelectionInfoSize(chars))
			WriteCharSelectionInfo(b, chars, 0)
			v, _ := NewCharSelectionInfoView(b)
			return v.Fields()
		}, []Field{
			{K: "count", V: "1"},
			{K: "[0]", V: `{name="Warrior" id=268478617 level=10 class=0 base=0 sex=0 race=0 hp=42.5/85.5 mp=39.25/78.5 sp=100 exp=123456789 karma=0}`},
		}},
		{"CHAR_SELECTED", func() []Field {
			d := CharSelectedData{
				Name: "Warrior", CharID: 268478617, Title: "Novice", Level: 10,
				X: -84000, Y: 247000, Z: -3700,
				CurHP: 42.5, CurMP: 39.25, SP: 100, Exp: 123456789, GameTime: 905,
			}
			b := make([]byte, CharSelectedSize(d))
			WriteCharSelected(b, d)
			v, _ := NewCharSelectedView(b)
			return v.Fields()
		}, []Field{
			{K: "name", V: `"Warrior"`},
			{K: "id", V: "268478617"},
			{K: "title", V: `"Novice"`},
			{K: "level", V: "10"},
			{K: "class", V: "0"},
			{K: "x", V: "-84000"},
			{K: "y", V: "247000"},
			{K: "z", V: "-3700"},
			{K: "hp", V: "42.5"},
			{K: "mp", V: "39.25"},
			{K: "sp", V: "100"},
			{K: "exp", V: "123456789"},
			{K: "karma", V: "0"},
			{K: "pk", V: "0"},
			{K: "gameTime", V: "905"},
		}},
		{"CHAR_TEMPLATES", func() []Field {
			templates := []CharTemplate{{Race: 0, ClassID: 0}, {Race: 1, ClassID: 10}}
			b := make([]byte, CharTemplatesSize(len(templates)))
			WriteCharTemplates(b, templates)
			v, _ := NewCharTemplatesView(b)
			return v.Fields()
		}, []Field{{K: "count", V: "2"}}},
		{"CHAR_CREATE_FAIL", func() []Field {
			var b [CharCreateFailSize]byte
			WriteCharCreateFail(b[:], CharCreateReasonNameTooLong)
			v, _ := NewCharCreateFailView(b[:])
			return v.Fields()
		}, []Field{{K: "reason", V: "0x03"}}},
		{"USER_INFO", func() []Field {
			d := UserInfoData{Name: "Botalice", ObjID: 268501522, X: -84000, Y: 247000, Z: -3700, Level: 10, Title: ""}
			b := make([]byte, UserInfoSize(d))
			WriteUserInfo(b, d)
			v, _ := NewUserInfoView(b)
			return v.Fields()
		}, []Field{
			{K: "name", V: `"Botalice"`},
			{K: "objID", V: "268501522"},
			{K: "x", V: "-84000"},
			{K: "y", V: "247000"},
			{K: "z", V: "-3700"},
			{K: "level", V: "10"},
		}},
		{"CHAR_INFO", func() []Field {
			d := CharInfoData{Name: "Botbob", ObjID: 268501523, X: -84064, Y: 247064, Z: -3700, Title: "", Heading: 1234}
			b := make([]byte, CharInfoSize(d))
			WriteCharInfo(b, d)
			v, _ := NewCharInfoView(b)
			return v.Fields()
		}, []Field{
			{K: "name", V: `"Botbob"`},
			{K: "objID", V: "268501523"},
			{K: "x", V: "-84064"},
			{K: "y", V: "247064"},
			{K: "heading", V: "1234"},
		}},
		{"DELETE_OBJECT", func() []Field {
			var b [DeleteObjectSize]byte
			WriteDeleteObject(b[:], 268501524)
			v, _ := NewDeleteObjectView(b[:])
			return v.Fields()
		}, []Field{{K: "objID", V: "268501524"}}},
		{"CHAR_MOVE_TO_LOCATION", func() []Field {
			var b [CharMoveToLocationSize]byte
			WriteCharMoveToLocation(b[:], 268501525, -84000, 247000, -3700, -84064, 247064, -3700)
			v, _ := NewCharMoveToLocationView(b[:])
			return v.Fields()
		}, []Field{
			{K: "objID", V: "268501525"},
			{K: "dstX", V: "-84000"},
			{K: "dstY", V: "247000"},
			{K: "curX", V: "-84064"},
			{K: "curY", V: "247064"},
		}},
		{"STOP_MOVE", func() []Field {
			var b [StopMoveSize]byte
			WriteStopMove(b[:], 268501526, -84000, 247000, -3700, 4096)
			v, _ := NewStopMoveView(b[:])
			return v.Fields()
		}, []Field{
			{K: "objID", V: "268501526"},
			{K: "x", V: "-84000"},
			{K: "y", V: "247000"},
			{K: "heading", V: "4096"},
		}},
		{"VALIDATE_LOCATION", func() []Field {
			var b [ValidateLocationSize]byte
			WriteValidateLocation(b[:], 268501527, -84000, 247000, -3700, 8192)
			v, _ := NewValidateLocationView(b[:])
			return v.Fields()
		}, []Field{
			{K: "objID", V: "268501527"},
			{K: "x", V: "-84000"},
			{K: "y", V: "247000"},
			{K: "heading", V: "8192"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertFields(t, tt.got(), tt.want)
		})
	}
}
