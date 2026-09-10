package protocol

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/udisondev/l2go/internal/protocol/fixture"
)

// Значения, разделяемые с генератором фикстур (testdata/login.json собран
// независимо от писателей по формату канона Mobius CT_0_Interlude@43ac8878).
const (
	tSessionID  int32 = 0x12345678
	tLoginOk1   int32 = 0x0A1B2C3D
	tLoginOk2   int32 = 0x4D5E6F70
	tPlayOk1    int32 = 0x11223344
	tPlayOk2    int32 = 0x55667788
	tLastServer byte  = 1
)

var (
	tModulus = pattern(128, 11)
	tBFKey   = pattern(16, 23)
	tRSABlk  = pattern(128, 5)
)

func pattern(n, seed int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte((i*37 + seed) % 256)
	}
	return b
}

var tServers = []ServerListEntry{
	{ID: 1, IP: [4]byte{127, 0, 0, 1}, Port: 7777, AgeLimit: 0, PvP: false,
		CurrentPlayers: 42, MaxPlayers: 500, Status: 1, ServerType: 1, Brackets: false},
	{ID: 2, IP: [4]byte{10, 0, 0, 5}, Port: 7778, AgeLimit: 15, PvP: true,
		CurrentPlayers: 7, MaxPlayers: 100, Status: 1, ServerType: 4, Brackets: true},
}

var tChars = []ServerChars{
	{ServerID: 1, CharCount: 3, DeleteTimes: []int32{3600}},
	{ServerID: 2, CharCount: 0, DeleteTimes: nil},
}

func loginFixtures(t *testing.T) map[string]fixture.Fixture {
	t.Helper()
	rows, err := fixture.Load("login")
	if err != nil {
		t.Fatalf("fixture.Load(login): %v", err)
	}
	if len(rows) != 13 {
		t.Fatalf("фикстур login: %d; want 13", len(rows))
	}
	out := make(map[string]fixture.Fixture, len(rows))
	for _, r := range rows {
		out[r.Name] = r
	}
	return out
}

// wire возвращает пакет [опкод||payload] из фикстуры.
func wire(f fixture.Fixture) []byte {
	b := make([]byte, 1+len(f.Payload))
	b[0] = byte(f.Op)
	copy(b[1:], f.Payload)
	return b
}

// checkGolden сравнивает полный байт-образ писателя с фикстурой.
func checkGolden(t *testing.T, name string, dst []byte, n int, f fixture.Fixture, op byte) {
	t.Helper()
	if n != 1+len(f.Payload) {
		t.Fatalf("%s: писатель вернул %d байт; want %d", name, n, 1+len(f.Payload))
	}
	if dst[0] != op {
		t.Errorf("%s: опкод = 0x%02X; want 0x%02X", name, dst[0], op)
	}
	if got, want := hex.EncodeToString(dst[1:n]), hex.EncodeToString(f.Payload); got != want {
		t.Errorf("%s: payload = %s; want (fixture) %s", name, got, want)
	}
}

// Машинная связка меты фикстур с константами пакета (wire-векторы).
func TestLoginFixtureMeta(t *testing.T) {
	want := []struct {
		name string
		dir  fixture.Direction
		op   byte
	}{
		{"INIT", fixture.LoginServer, loginInit},
		{"LOGIN_OK", fixture.LoginServer, loginOk},
		{"LOGIN_FAIL", fixture.LoginServer, loginFail},
		{"ACCOUNT_KICKED", fixture.LoginServer, accountKicked},
		{"SERVER_LIST", fixture.LoginServer, serverList},
		{"PLAY_OK", fixture.LoginServer, playOk},
		{"PLAY_FAIL", fixture.LoginServer, playFail},
		{"GG_AUTH", fixture.LoginServer, ggAuth},
		{"REQUEST_AUTH_LOGIN", fixture.LoginClient, requestAuthLogin},
		{"REQUEST_AUTH_LOGIN_PLAIN", fixture.LoginClient, requestAuthLogin},
		{"REQUEST_SERVER_LIST", fixture.LoginClient, requestServerList},
		{"REQUEST_SERVER_LOGIN", fixture.LoginClient, requestServerLogin},
		{"AUTH_GAME_GUARD", fixture.LoginClient, authGameGuard},
	}
	fixes := loginFixtures(t)
	for _, w := range want {
		f, ok := fixes[w.name]
		if !ok {
			t.Errorf("фикстура %s отсутствует в login.json", w.name)
			continue
		}
		if f.Dir != w.dir || f.Op != uint16(w.op) {
			t.Errorf("фикстура %s: dir/op = %s/0x%02X; want %s/0x%02X", w.name, f.Dir, f.Op, w.dir, w.op)
		}
		if f.Origin != fixture.OriginGenerated {
			t.Errorf("фикстура %s: origin = %q; want generated", w.name, f.Origin)
		}
	}
}

func TestWriteLoginPacketsGolden(t *testing.T) {
	fixes := loginFixtures(t)
	tests := []struct {
		name  string
		size  int
		op    byte
		write func(dst []byte) int
	}{
		{"INIT", InitSize, loginInit, func(dst []byte) int {
			return WriteInit(dst, tSessionID, tModulus, tBFKey)
		}},
		{"LOGIN_OK", LoginOkSize, loginOk, func(dst []byte) int {
			return WriteLoginOk(dst, tLoginOk1, tLoginOk2)
		}},
		{"LOGIN_FAIL", LoginFailSize, loginFail, func(dst []byte) int {
			return WriteLoginFail(dst, ReasonAccountInUse)
		}},
		{"ACCOUNT_KICKED", AccountKickedSize, accountKicked, func(dst []byte) int {
			return WriteAccountKicked(dst, KickDataStealer)
		}},
		{"SERVER_LIST", 58, serverList, func(dst []byte) int {
			return WriteServerList(dst, tServers, tChars, tLastServer)
		}},
		{"PLAY_OK", PlayOkSize, playOk, func(dst []byte) int {
			return WritePlayOk(dst, tPlayOk1, tPlayOk2)
		}},
		{"PLAY_FAIL", PlayFailSize, playFail, func(dst []byte) int {
			return WritePlayFail(dst, ReasonSystemErrorLoginLater)
		}},
		{"GG_AUTH", GGAuthSize, ggAuth, func(dst []byte) int {
			return WriteGGAuth(dst, tSessionID)
		}},
		{"REQUEST_AUTH_LOGIN", RequestAuthLoginSize, requestAuthLogin, func(dst []byte) int {
			return WriteRequestAuthLogin(dst, tRSABlk)
		}},
		{"REQUEST_SERVER_LIST", RequestServerListSize, requestServerList, func(dst []byte) int {
			return WriteRequestServerList(dst, tLoginOk1, tLoginOk2)
		}},
		{"REQUEST_SERVER_LOGIN", RequestServerLoginSize, requestServerLogin, func(dst []byte) int {
			return WriteRequestServerLogin(dst, tLoginOk1, tLoginOk2, 1)
		}},
		{"AUTH_GAME_GUARD", AuthGameGuardSize, authGameGuard, func(dst []byte) int {
			return WriteAuthGameGuard(dst, tSessionID)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := fixes[tt.name]
			if tt.size != 1+len(f.Payload) {
				t.Fatalf("%s: константа размера %d; фикстура даёт %d", tt.name, tt.size, 1+len(f.Payload))
			}
			dst := make([]byte, tt.size)
			checkGolden(t, tt.name, dst, tt.write(dst), f, tt.op)
		})
	}
}

// Plain-блок REQUEST_AUTH_LOGIN пишется без опкода: сравнение — payload.
func TestWriteRequestAuthLoginPlainGolden(t *testing.T) {
	f := loginFixtures(t)["REQUEST_AUTH_LOGIN_PLAIN"]
	dst := make([]byte, RequestAuthLoginPlainSize)
	if err := WriteRequestAuthLoginPlain(dst, "testuser", "secret"); err != nil {
		t.Fatalf("WriteRequestAuthLoginPlain: %v", err)
	}
	if got, want := hex.EncodeToString(dst), hex.EncodeToString(f.Payload); got != want {
		t.Errorf("plain-блок = %s; want (fixture) %s", got, want)
	}
}

func TestWriteRequestAuthLoginPlainErrors(t *testing.T) {
	dst := make([]byte, RequestAuthLoginPlainSize)
	tests := []struct {
		name       string
		user, pass string
		wantErr    bool
	}{
		{"user 14 символов", "12345678901234", "secret", false},
		{"user 15 символов", "123456789012345", "secret", true},
		{"pass 16 символов", "testuser", "1234567890123456", false},
		{"pass 17 символов", "testuser", "12345678901234567", true},
		{"пустые", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := WriteRequestAuthLoginPlain(dst, tt.user, tt.pass)
			if tt.wantErr && err == nil {
				t.Errorf("WriteRequestAuthLoginPlain(%q, %q): err = nil; want ошибка переполнения", tt.user, tt.pass)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("WriteRequestAuthLoginPlain(%q, %q) = %v; want nil", tt.user, tt.pass, err)
			}
		})
	}
}

func TestLoginViewsGolden(t *testing.T) {
	fixes := loginFixtures(t)

	t.Run("INIT", func(t *testing.T) {
		v, ok := NewInitView(wire(fixes["INIT"]))
		if !ok {
			t.Fatal("NewInitView: ok = false")
		}
		if v.SessionID() != tSessionID {
			t.Errorf("SessionID = %#x; want %#x", v.SessionID(), tSessionID)
		}
		if v.Revision() != 0x0000C621 {
			t.Errorf("Revision = %#x; want 0x0000C621", v.Revision())
		}
		if got, want := hex.EncodeToString(v.Modulus()), hex.EncodeToString(tModulus); got != want {
			t.Errorf("Modulus = %s…; want %s…", got[:16], want[:16])
		}
		if got, want := hex.EncodeToString(v.BlowfishKey()), hex.EncodeToString(tBFKey); got != want {
			t.Errorf("BlowfishKey = %s; want %s", got, want)
		}
	})
	t.Run("LOGIN_OK", func(t *testing.T) {
		v, ok := NewLoginOkView(wire(fixes["LOGIN_OK"]))
		if !ok {
			t.Fatal("NewLoginOkView: ok = false")
		}
		if v.LoginOkID1() != tLoginOk1 || v.LoginOkID2() != tLoginOk2 {
			t.Errorf("ключи = %#x/%#x; want %#x/%#x", v.LoginOkID1(), v.LoginOkID2(), tLoginOk1, tLoginOk2)
		}
	})
	t.Run("LOGIN_FAIL", func(t *testing.T) {
		v, ok := NewLoginFailView(wire(fixes["LOGIN_FAIL"]))
		if !ok {
			t.Fatal("NewLoginFailView: ok = false")
		}
		if v.Reason() != ReasonAccountInUse {
			t.Errorf("Reason = %#x; want ReasonAccountInUse", v.Reason())
		}
	})
	t.Run("ACCOUNT_KICKED", func(t *testing.T) {
		v, ok := NewAccountKickedView(wire(fixes["ACCOUNT_KICKED"]))
		if !ok {
			t.Fatal("NewAccountKickedView: ok = false")
		}
		if v.Reason() != KickDataStealer {
			t.Errorf("Reason = %#x; want KickDataStealer", v.Reason())
		}
	})
	t.Run("SERVER_LIST", func(t *testing.T) {
		v, ok := NewServerListView(wire(fixes["SERVER_LIST"]))
		if !ok {
			t.Fatal("NewServerListView: ok = false")
		}
		if v.Count() != 2 || v.LastServer() != tLastServer {
			t.Errorf("заголовок = %d/%d; want 2/1", v.Count(), v.LastServer())
		}
		for i, want := range tServers {
			got, ok := v.Server(i)
			if !ok || got != want {
				t.Errorf("Server(%d) = %+v, %v; want %+v", i, got, ok, want)
			}
		}
		if n, ok := v.CharsCount(); !ok || n != 2 {
			t.Errorf("CharsCount = %d, %v; want 2", n, ok)
		}
		for i, want := range tChars {
			got, ok := v.Chars(i)
			if !ok || got.ServerID != want.ServerID || got.CharCount != want.CharCount {
				t.Errorf("Chars(%d) = %+v, %v; want %+v", i, got, ok, want)
				continue
			}
			if len(got.DeleteTimes) != len(want.DeleteTimes) {
				t.Errorf("Chars(%d).DeleteTimes = %v; want %v", i, got.DeleteTimes, want.DeleteTimes)
				continue
			}
			for j, ts := range want.DeleteTimes {
				if got.DeleteTimes[j] != ts {
					t.Errorf("Chars(%d).DeleteTimes[%d] = %d; want %d", i, j, got.DeleteTimes[j], ts)
				}
			}
		}
		if _, ok := v.Server(2); ok {
			t.Error("Server(2): ok = true; want false")
		}
		if _, ok := v.Chars(2); ok {
			t.Error("Chars(2): ok = true; want false")
		}
	})
	t.Run("PLAY_OK", func(t *testing.T) {
		v, ok := NewPlayOkView(wire(fixes["PLAY_OK"]))
		if !ok {
			t.Fatal("NewPlayOkView: ok = false")
		}
		if v.PlayOkID1() != tPlayOk1 || v.PlayOkID2() != tPlayOk2 {
			t.Errorf("ключи = %#x/%#x; want %#x/%#x", v.PlayOkID1(), v.PlayOkID2(), tPlayOk1, tPlayOk2)
		}
	})
	t.Run("PLAY_FAIL", func(t *testing.T) {
		v, ok := NewPlayFailView(wire(fixes["PLAY_FAIL"]))
		if !ok {
			t.Fatal("NewPlayFailView: ok = false")
		}
		if v.Reason() != ReasonSystemErrorLoginLater {
			t.Errorf("Reason = %#x; want ReasonSystemErrorLoginLater", v.Reason())
		}
	})
	t.Run("GG_AUTH", func(t *testing.T) {
		v, ok := NewGGAuthView(wire(fixes["GG_AUTH"]))
		if !ok {
			t.Fatal("NewGGAuthView: ok = false")
		}
		if v.Response() != tSessionID {
			t.Errorf("Response = %#x; want %#x", v.Response(), tSessionID)
		}
	})
	t.Run("REQUEST_AUTH_LOGIN", func(t *testing.T) {
		v, ok := NewRequestAuthLoginView(wire(fixes["REQUEST_AUTH_LOGIN"]))
		if !ok {
			t.Fatal("NewRequestAuthLoginView: ok = false")
		}
		if got, want := hex.EncodeToString(v.RSABlock()), hex.EncodeToString(tRSABlk); got != want {
			t.Errorf("RSABlock = %s…; want %s…", got[:16], want[:16])
		}
	})
	t.Run("REQUEST_AUTH_LOGIN_PLAIN", func(t *testing.T) {
		v, ok := NewRequestAuthLoginPlainView(fixes["REQUEST_AUTH_LOGIN_PLAIN"].Payload)
		if !ok {
			t.Fatal("NewRequestAuthLoginPlainView: ok = false")
		}
		if v.User() != "testuser" || v.Password() != "secret" {
			t.Errorf("учётные данные = %q/%q; want testuser/secret", v.User(), v.Password())
		}
	})
	t.Run("REQUEST_SERVER_LIST", func(t *testing.T) {
		v, ok := NewRequestServerListView(wire(fixes["REQUEST_SERVER_LIST"]))
		if !ok {
			t.Fatal("NewRequestServerListView: ok = false")
		}
		if v.LoginOkID1() != tLoginOk1 || v.LoginOkID2() != tLoginOk2 {
			t.Errorf("ключи = %#x/%#x; want %#x/%#x", v.LoginOkID1(), v.LoginOkID2(), tLoginOk1, tLoginOk2)
		}
	})
	t.Run("REQUEST_SERVER_LOGIN", func(t *testing.T) {
		v, ok := NewRequestServerLoginView(wire(fixes["REQUEST_SERVER_LOGIN"]))
		if !ok {
			t.Fatal("NewRequestServerLoginView: ok = false")
		}
		if v.LoginOkID1() != tLoginOk1 || v.LoginOkID2() != tLoginOk2 || v.ServerID() != 1 {
			t.Errorf("поля = %#x/%#x/%d; want %#x/%#x/1", v.LoginOkID1(), v.LoginOkID2(), v.ServerID(), tLoginOk1, tLoginOk2)
		}
	})
	t.Run("AUTH_GAME_GUARD", func(t *testing.T) {
		v, ok := NewAuthGameGuardView(wire(fixes["AUTH_GAME_GUARD"]))
		if !ok {
			t.Fatal("NewAuthGameGuardView: ok = false")
		}
		if v.SessionID() != tSessionID {
			t.Errorf("SessionID = %#x; want %#x", v.SessionID(), tSessionID)
		}
	})
}

// Обрезка ≤ U+0020 с двух концов — семантика Java String.trim() канона.
func TestAuthLoginPlainViewTrim(t *testing.T) {
	block := make([]byte, RequestAuthLoginPlainSize)
	copy(block[0x5E:], "\tspace user \x00")
	copy(block[0x6C:], " pass word\t")
	v, ok := NewRequestAuthLoginPlainView(block)
	if !ok {
		t.Fatal("NewRequestAuthLoginPlainView: ok = false")
	}
	if v.User() != "space user" {
		t.Errorf("User = %q; want %q (trim с двух концов)", v.User(), "space user")
	}
	if v.Password() != "pass word" {
		t.Errorf("Password = %q; want %q (пробел в середине сохраняется)", v.Password(), "pass word")
	}
}

// Представления не паникуют и детерминированно отказывают на усечении.
func TestLoginViewsTruncated(t *testing.T) {
	tests := []struct {
		name string
		min  int
		ctor func(b []byte) bool
	}{
		{"INIT", InitSize, func(b []byte) bool { _, ok := NewInitView(b); return ok }},
		{"LOGIN_OK", LoginOkSize, func(b []byte) bool { _, ok := NewLoginOkView(b); return ok }},
		{"LOGIN_FAIL", LoginFailSize, func(b []byte) bool { _, ok := NewLoginFailView(b); return ok }},
		{"ACCOUNT_KICKED", AccountKickedSize, func(b []byte) bool { _, ok := NewAccountKickedView(b); return ok }},
		{"SERVER_LIST", 3, func(b []byte) bool { _, ok := NewServerListView(b); return ok }},
		{"PLAY_OK", PlayOkSize, func(b []byte) bool { _, ok := NewPlayOkView(b); return ok }},
		{"PLAY_FAIL", PlayFailSize, func(b []byte) bool { _, ok := NewPlayFailView(b); return ok }},
		{"GG_AUTH", GGAuthSize, func(b []byte) bool { _, ok := NewGGAuthView(b); return ok }},
		{"REQUEST_AUTH_LOGIN", RequestAuthLoginSize, func(b []byte) bool { _, ok := NewRequestAuthLoginView(b); return ok }},
		{"REQUEST_AUTH_LOGIN_PLAIN", 124, func(b []byte) bool { _, ok := NewRequestAuthLoginPlainView(b); return ok }},
		{"REQUEST_SERVER_LIST", RequestServerListSize, func(b []byte) bool { _, ok := NewRequestServerListView(b); return ok }},
		{"REQUEST_SERVER_LOGIN", RequestServerLoginSize, func(b []byte) bool { _, ok := NewRequestServerLoginView(b); return ok }},
		{"AUTH_GAME_GUARD", 5, func(b []byte) bool { _, ok := NewAuthGameGuardView(b); return ok }},
	}
	wireFix := loginFixtures(t)["INIT"]
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			full := make([]byte, tt.min)
			copy(full, wireFix.Payload) // мусорное заполнение — важна только длина
			if !tt.ctor(full) {
				t.Fatalf("%s: на ровно %d байт ok = false; want true", tt.name, tt.min)
			}
			for _, n := range []int{0, tt.min - 1} {
				if tt.ctor(full[:n]) {
					t.Errorf("%s: обрезано до %d байт: ok = true; want false", tt.name, n)
				}
			}
		})
	}
}

// Зона 129–256: конструктор ok, валиден только RSABlock (короткий блоб).
func TestRequestAuthLoginViewShortBlock(t *testing.T) {
	b := make([]byte, 200)
	b[0] = requestAuthLogin
	v, ok := NewRequestAuthLoginView(b)
	if !ok {
		t.Fatal("NewRequestAuthLoginView(200 Б): ok = false; want true")
	}
	if len(v.RSABlock()) != 128 {
		t.Errorf("RSABlock: %d байт; want 128", len(v.RSABlock()))
	}
}

// Пачка злых входов: конструкторы и геттеры не паникуют на недоверенных байтах.
func TestLoginViewsNoPanic(t *testing.T) {
	fixes := loginFixtures(t)
	good := [][]byte{
		wire(fixes["INIT"]),
		wire(fixes["SERVER_LIST"]),
		wire(fixes["REQUEST_AUTH_LOGIN"]),
		fixes["REQUEST_AUTH_LOGIN_PLAIN"].Payload,
	}
	evils := [][]byte{
		nil, {}, {0x00}, {0xFF, 0xFF}, pattern(2, 1), pattern(123, 2), pattern(125, 3),
		pattern(126, 4), pattern(127, 5), pattern(129, 6), pattern(255, 7),
		append([]byte{serverList, 0xFF, 0xFF, 0xFF, 0xFF}, pattern(64, 8)...),
		append([]byte{serverList, 0x02, 0x01}, pattern(30, 9)...),
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("паника на злых входах: %v", r)
		}
	}()
	for _, b := range append(evils, good...) {
		if v, ok := NewInitView(b); ok {
			_, _, _, _ = v.SessionID(), v.Revision(), v.Modulus(), v.BlowfishKey()
		}
		if v, ok := NewServerListView(b); ok {
			for i := 0; i < v.Count()+1; i++ {
				_, _ = v.Server(i)
			}
			for i := 0; i < 4; i++ {
				_, _ = v.Chars(i)
			}
			_, _ = v.CharsCount()
		}
		if v, ok := NewRequestAuthLoginView(b); ok {
			_ = v.RSABlock()
		}
		if v, ok := NewRequestAuthLoginPlainView(b); ok {
			_, _ = v.User(), v.Password()
		}
		_, _ = NewLoginOkView(b)
		_, _ = NewGGAuthView(b)
	}
}

// Фиксированные писатели — 0 аллокаций (конвенция пути «пакет-писатель»).
func TestLoginWritersZeroAllocs(t *testing.T) {
	dst := make([]byte, 256)
	allocs := testing.AllocsPerRun(100, func() {
		_ = WriteInit(dst, tSessionID, tModulus, tBFKey)
		_ = WriteLoginOk(dst, tLoginOk1, tLoginOk2)
		_ = WriteLoginFail(dst, ReasonAccountInUse)
		_ = WriteAccountKicked(dst, KickDataStealer)
		_ = WritePlayOk(dst, tPlayOk1, tPlayOk2)
		_ = WritePlayFail(dst, ReasonSystemErrorLoginLater)
		_ = WriteGGAuth(dst, tSessionID)
		_ = WriteRequestAuthLogin(dst, tRSABlk)
		_ = WriteRequestServerList(dst, tLoginOk1, tLoginOk2)
		_ = WriteRequestServerLogin(dst, tLoginOk1, tLoginOk2, 1)
		_ = WriteAuthGameGuard(dst, tSessionID)
		_ = WriteServerList(dst, tServers, tChars, tLastServer)
	})
	if allocs != 0 {
		t.Errorf("писатели login: %v аллокаций; want 0", allocs)
	}
}

// Sizing-функция сходится с результатом писателя на таблице входов.
func TestServerListSizeProperty(t *testing.T) {
	tests := []struct {
		name    string
		servers []ServerListEntry
		chars   []ServerChars
	}{
		{"пусто", nil, nil},
		{"один", tServers[:1], tChars[:1]},
		{"два", tServers, tChars},
		{"три", append(tServers, ServerListEntry{ID: 3, IP: [4]byte{192, 168, 0, 1}, Port: 7779,
			CurrentPlayers: -1, MaxPlayers: 0x7FFF, Status: 0, ServerType: 32, Brackets: true}),
			append(tChars, ServerChars{ServerID: 3, CharCount: 7,
				DeleteTimes: []int32{1, 2, 3, 4, 5}})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := ServerListSize(tt.servers, tt.chars)
			dst := make([]byte, want)
			got := WriteServerList(dst, tt.servers, tt.chars, tLastServer)
			if got != want {
				t.Errorf("WriteServerList = %d; ServerListSize = %d", got, want)
			}
		})
	}
}

func ExampleWriteInit() {
	modulus := make([]byte, 128)
	for i := range modulus {
		modulus[i] = byte((i*37 + 11) % 256)
	}
	bfKey := make([]byte, 16)
	for i := range bfKey {
		bfKey[i] = byte((i*37 + 23) % 256)
	}
	var dst [InitSize]byte
	WriteInit(dst[:], 0x12345678, modulus, bfKey)
	v, ok := NewInitView(dst[:])
	fmt.Println(ok, v.SessionID(), v.Revision())
	// Output: true 305419896 50721
}

// Грязный dst: зарезервированные поля обязаны затираться (байты 0xFF не
// должны просачиваться в пакет — пул-буферы переиспользуются грязными).
func TestLoginWritersDirtyDst(t *testing.T) {
	type tc struct {
		name  string
		size  int
		write func(dst []byte) int
	}
	tests := []tc{
		{"LoginOk", LoginOkSize, func(dst []byte) int { return WriteLoginOk(dst, 1, 2) }},
		{"GGAuth", GGAuthSize, func(dst []byte) int { return WriteGGAuth(dst, 3) }},
		{"AuthGameGuard", AuthGameGuardSize, func(dst []byte) int { return WriteAuthGameGuard(dst, 3) }},
		{"ServerList", ServerListSize(tServers, tChars), func(dst []byte) int {
			return WriteServerList(dst, tServers, tChars, tLastServer)
		}},
		{"RequestAuthLoginPlain", RequestAuthLoginPlainSize, func(dst []byte) int {
			if err := WriteRequestAuthLoginPlain(dst, "testuser", "secret"); err != nil {
				t.Errorf("WriteRequestAuthLoginPlain: %v", err)
			}
			return RequestAuthLoginPlainSize
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clean := make([]byte, tt.size)
			n := tt.write(clean)
			dirty := make([]byte, tt.size)
			for i := range dirty {
				dirty[i] = 0xFF
			}
			tt.write(dirty)
			for i := 0; i < n; i++ {
				if clean[i] != dirty[i] {
					t.Fatalf("%s: байт %d: чистый %#x, грязный %#x — затирание резервов сломано",
						tt.name, i, clean[i], dirty[i])
				}
			}
		})
	}
}

// Паник-контракты писателей: короткий dst и неверные длины аргументов-буферов
// паникуют с диагностикой (класс F6/P1.2 — программный контракт вызывающего).
func TestLoginWritersPanics(t *testing.T) {
	dst := make([]byte, 256)
	big := make([]byte, 16<<10)
	tests := []struct {
		name    string
		call    func()
		wantMsg string
	}{
		{"Init короткий dst", func() { WriteInit(dst[:InitSize-1], 1, tModulus, tBFKey) }, "WriteInit"},
		{"Init модуль", func() { WriteInit(dst, 1, tModulus[:64], tBFKey) }, "модуль"},
		{"Init bfKey", func() { WriteInit(dst, 1, tModulus, tBFKey[:8]) }, "Blowfish-ключ"},
		{"LoginOk короткий dst", func() { WriteLoginOk(dst[:LoginOkSize-1], 1, 2) }, "WriteLoginOk"},
		{"RequestAuthLogin блоб", func() { WriteRequestAuthLogin(dst, tRSABlk[:100]) }, "RSA-блоб"},
		{"RequestServerList короткий dst", func() { WriteRequestServerList(dst[:8], 1, 2) }, "WriteRequestServerList"},
		{"AuthGameGuard короткий dst", func() { WriteAuthGameGuard(dst[:20], 1) }, "WriteAuthGameGuard"},
		{"ServerList счётчик серверов", func() {
			WriteServerList(big, make([]ServerListEntry, 256), nil, 0)
		}, "вне байта"},
		{"ServerList счётчик удалений", func() {
			WriteServerList(big, tServers[:1], []ServerChars{{ServerID: 1, DeleteTimes: make([]int32, 256)}}, 0)
		}, "удалений вне байта"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("паника не поднята; want с подстрокой %q", tt.wantMsg)
				}
				if msg, ok := r.(string); !ok || !strings.Contains(msg, tt.wantMsg) {
					t.Errorf("паника = %v; want подстрока %q", r, tt.wantMsg)
				}
			}()
			tt.call()
		})
	}
}

// Счётчик — ограничитель навигации, а не только буфер: заниженный счётчик
// не отдаёт записи, целиком лежащие в буфере (обе секции).
func TestServerListViewCountLimiter(t *testing.T) {
	full := wire(loginFixtures(t)["SERVER_LIST"]) // count=2, обе записи валидны

	sec1 := append([]byte(nil), full...)
	sec1[1] = 1 // счётчик секции 1 занижен
	v, ok := NewServerListView(sec1)
	if !ok {
		t.Fatal("NewServerListView: ok = false")
	}
	if _, ok := v.Server(1); ok {
		t.Error("Server(1) при count=1: ok = true; want false (счётчик ограничивает)")
	}
	if _, ok := v.Server(0); !ok {
		t.Error("Server(0) при count=1: ok = false; want true")
	}

	sec2 := append([]byte(nil), full...)
	sec2[47] = 1 // счётчик секции 2 (офсет 3+21·2+2) занижен
	v2, ok := NewServerListView(sec2)
	if !ok {
		t.Fatal("NewServerListView: ok = false")
	}
	if _, ok := v2.Chars(1); ok {
		t.Error("Chars(1) при count2=1: ok = true; want false (счётчик ограничивает)")
	}
	if _, ok := v2.Chars(0); !ok {
		t.Error("Chars(0) при count2=1: ok = false; want true")
	}
}

// Короткий dst — паник-контракт каждого писателя пакета (диагностика с
// именем функции и размером).
func TestWritersShortDst(t *testing.T) {
	big := make([]byte, 16<<10)
	chars := []CharSelectionEntry{tChar1, tChar2}
	tests := []struct {
		name string
		size int
		call func(dst []byte)
	}{
		{"WriteInit", InitSize, func(d []byte) { WriteInit(d, 1, tModulus, tBFKey) }},
		{"WriteLoginOk", LoginOkSize, func(d []byte) { WriteLoginOk(d, 1, 2) }},
		{"WriteLoginFail", LoginFailSize, func(d []byte) { WriteLoginFail(d, ReasonAccountInUse) }},
		{"WriteAccountKicked", AccountKickedSize, func(d []byte) { WriteAccountKicked(d, KickDataStealer) }},
		{"WritePlayOk", PlayOkSize, func(d []byte) { WritePlayOk(d, 1, 2) }},
		{"WritePlayFail", PlayFailSize, func(d []byte) { WritePlayFail(d, ReasonSystemErrorLoginLater) }},
		{"WriteGGAuth", GGAuthSize, func(d []byte) { WriteGGAuth(d, 1) }},
		{"WriteServerList", ServerListSize(tServers, tChars), func(d []byte) { WriteServerList(d, tServers, tChars, 0) }},
		{"WriteRequestAuthLogin", RequestAuthLoginSize, func(d []byte) { WriteRequestAuthLogin(d, tRSABlk) }},
		{"WriteRequestAuthLoginPlain", RequestAuthLoginPlainSize, func(d []byte) {
			_ = WriteRequestAuthLoginPlain(d, "u", "p")
		}},
		{"WriteRequestServerList", RequestServerListSize, func(d []byte) { WriteRequestServerList(d, 1, 2) }},
		{"WriteRequestServerLogin", RequestServerLoginSize, func(d []byte) { WriteRequestServerLogin(d, 1, 2, 1) }},
		{"WriteAuthGameGuard", AuthGameGuardSize, func(d []byte) { WriteAuthGameGuard(d, 1) }},
		{"WriteProtocolVersion", ProtocolVersionSize, func(d []byte) { WriteProtocolVersion(d, 746) }},
		{"WriteAuthLogin", AuthLoginSize("user"), func(d []byte) { WriteAuthLogin(d, "user", 1, 2, 3, 4) }},
		{"WriteLogout", LogoutSize, func(d []byte) { WriteLogout(d) }},
		{"WriteCharacterSelect", CharacterSelectSize, func(d []byte) { WriteCharacterSelect(d, 0) }},
		{"WriteKeyPacket", KeyPacketSize, func(d []byte) { WriteKeyPacket(d, 1, tKeyPacketKey, true, 1) }},
		{"WriteGSLoginFail", GSLoginFailSize, func(d []byte) { WriteGSLoginFail(d, GSReasonNoText) }},
		{"WriteCharSelectionInfo", CharSelectionInfoSize(chars), func(d []byte) { WriteCharSelectionInfo(d, chars, 0) }},
		{"WriteCharSelected", CharSelectedSize(tCharSelected), func(d []byte) { WriteCharSelected(d, tCharSelected) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("%s: паника на dst %d байт не поднята; want контракт", tt.name, tt.size-1)
				}
				if msg, ok := r.(string); !ok || !strings.Contains(msg, tt.name) {
					t.Errorf("%s: паника = %v; want имя функции в диагностике", tt.name, r)
				}
			}()
			tt.call(big[:tt.size-1])
		})
	}
}
