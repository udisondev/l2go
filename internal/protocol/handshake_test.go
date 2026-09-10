package protocol

import (
	"testing"

	"github.com/udisondev/l2go/internal/protocol/fixture"
)

// Значения, разделяемые с генератором фикстур testdata/handshake.json.
var (
	tKeyPacketKey = pattern(8, 31)

	tChar1 = CharSelectionEntry{
		Name: "Warrior", CharID: 268478617, LoginName: "testuser",
		SessionID: tSessionID, ClanID: 0, Sex: 0, Race: 0, BaseClassID: 0,
		CurHP: 42.5, CurMP: 39.25, SP: 100, Exp: 123456789, Level: 10, Karma: 0,
		HairStyle: 0, HairColor: 0, Face: 0, MaxHP: 85.5, MaxMP: 78.5,
		DeleteTime: 0, ClassID: 0, Enchant: 0, AugmentationID: 0,
	}
	tChar2 = CharSelectionEntry{
		Name: "Маг", CharID: 268478618, LoginName: "testuser",
		SessionID: tSessionID, ClanID: 0, Sex: 1, Race: 2, BaseClassID: 10,
		CurHP: 25.5, CurMP: 100.125, SP: 32767, Exp: 9876543210, Level: 1, Karma: 0,
		HairStyle: 2, HairColor: 2, Face: 0, MaxHP: 51.0, MaxMP: 100.5,
		DeleteTime: 0, ClassID: 10, Enchant: 0, AugmentationID: 0,
	}

	tCharSelected = CharSelectedData{
		Name: "Warrior", CharID: 268478617, Title: "Novice", SessionID: tSessionID,
		ClanID: 0, Sex: 0, Race: 0, ClassID: 0,
		X: -84000, Y: 247000, Z: -3700, CurHP: 42.5, CurMP: 39.25,
		SP: 100, Exp: 123456789, Level: 10, Karma: 0, PkKills: 0,
		INT: 36, STR: 36, CON: 36, MEN: 36, DEX: 36, WIT: 36,
		GameTime: 905,
	}
)

func handshakeFixtures(t *testing.T) map[string]fixture.Fixture {
	t.Helper()
	rows, err := fixture.Load("handshake")
	if err != nil {
		t.Fatalf("fixture.Load(handshake): %v", err)
	}
	if len(rows) != 8 {
		t.Fatalf("фикстур handshake: %d; want 8", len(rows))
	}
	out := make(map[string]fixture.Fixture, len(rows))
	for _, r := range rows {
		out[r.Name] = r
	}
	return out
}

// Машинная связка меты фикстур с константами пакета.
func TestHandshakeFixtureMeta(t *testing.T) {
	want := []struct {
		name string
		dir  fixture.Direction
		op   byte
	}{
		{"KEY_PACKET", fixture.GameServer, keyPacket},
		{"CHAR_SELECT_INFO", fixture.GameServer, charSelectInfo},
		{"LOGIN_FAIL", fixture.GameServer, gsLoginFail},
		{"CHAR_SELECTED", fixture.GameServer, charSelected},
		{"PROTOCOL_VERSION", fixture.GameClient, protocolVersion},
		{"AUTH_LOGIN", fixture.GameClient, authLogin},
		{"LOGOUT", fixture.GameClient, logout},
		{"CHARACTER_SELECT", fixture.GameClient, characterSelect},
	}
	fixes := handshakeFixtures(t)
	for _, w := range want {
		f, ok := fixes[w.name]
		if !ok {
			t.Errorf("фикстура %s отсутствует в handshake.json", w.name)
			continue
		}
		if f.Dir != w.dir || f.Op != uint16(w.op) {
			t.Errorf("фикстура %s: dir/op = %s/0x%02X; want %s/0x%02X", w.name, f.Dir, f.Op, w.dir, w.op)
		}
	}
}

func TestWriteHandshakePacketsGolden(t *testing.T) {
	fixes := handshakeFixtures(t)
	tests := []struct {
		name  string
		size  int
		op    byte
		write func(dst []byte) int
	}{
		{"KEY_PACKET", KeyPacketSize, keyPacket, func(dst []byte) int {
			return WriteKeyPacket(dst, 1, tKeyPacketKey, true, 1)
		}},
		{"GS LOGIN_FAIL", GSLoginFailSize, gsLoginFail, func(dst []byte) int {
			return WriteGSLoginFail(dst, GSReasonPasswordDoesNotMatchThisAccount)
		}},
		{"CHAR_SELECT_INFO", 659, charSelectInfo, func(dst []byte) int {
			return WriteCharSelectionInfo(dst, []CharSelectionEntry{tChar1, tChar2}, 0)
		}},
		{"CHAR_SELECTED", 327, charSelected, func(dst []byte) int {
			return WriteCharSelected(dst, tCharSelected)
		}},
		{"PROTOCOL_VERSION", ProtocolVersionSize, protocolVersion, func(dst []byte) int {
			return WriteProtocolVersion(dst, ProtocolVersionInterlude)
		}},
		{"AUTH_LOGIN", 35, authLogin, func(dst []byte) int {
			return WriteAuthLogin(dst, "ТестЮзер", tPlayOk2, tPlayOk1, tLoginOk1, tLoginOk2)
		}},
		{"LOGOUT", LogoutSize, logout, func(dst []byte) int {
			return WriteLogout(dst)
		}},
		{"CHARACTER_SELECT", CharacterSelectSize, characterSelect, func(dst []byte) int {
			return WriteCharacterSelect(dst, 1)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixName := map[string]string{"GS LOGIN_FAIL": "LOGIN_FAIL"}[tt.name]
			if fixName == "" {
				fixName = tt.name
			}
			f := fixes[fixName]
			if tt.size != 1+len(f.Payload) {
				t.Fatalf("%s: константа размера %d; фикстура даёт %d", tt.name, tt.size, 1+len(f.Payload))
			}
			dst := make([]byte, tt.size)
			checkGolden(t, tt.name, dst, tt.write(dst), f, tt.op)
		})
	}
}

func TestHandshakeViewsGolden(t *testing.T) {
	fixes := handshakeFixtures(t)

	t.Run("KEY_PACKET", func(t *testing.T) {
		v, ok := NewKeyPacketView(wire(fixes["KEY_PACKET"]))
		if !ok {
			t.Fatal("NewKeyPacketView: ok = false")
		}
		if v.Result() != 1 || !v.Encryption() || v.ServerID() != 1 {
			t.Errorf("поля = %d/%v/%d; want 1/true/1", v.Result(), v.Encryption(), v.ServerID())
		}
		for i, b := range v.Key() {
			if b != tKeyPacketKey[i] {
				t.Errorf("Key[%d] = %#x; want %#x", i, b, tKeyPacketKey[i])
			}
		}
	})
	t.Run("GS LOGIN_FAIL", func(t *testing.T) {
		v, ok := NewGSLoginFailView(wire(fixes["LOGIN_FAIL"]))
		if !ok {
			t.Fatal("NewGSLoginFailView: ok = false")
		}
		if v.Reason() != GSReasonPasswordDoesNotMatchThisAccount {
			t.Errorf("Reason = %d; want GSReasonPasswordDoesNotMatchThisAccount", v.Reason())
		}
	})
	t.Run("CHAR_SELECT_INFO", func(t *testing.T) {
		v, ok := NewCharSelectionInfoView(wire(fixes["CHAR_SELECT_INFO"]))
		if !ok {
			t.Fatal("NewCharSelectionInfoView: ok = false")
		}
		if v.Count() != 2 {
			t.Fatalf("Count = %d; want 2", v.Count())
		}
		got1, ok := v.Char(0)
		if !ok || got1 != tChar1 {
			t.Errorf("Char(0) = %+v, %v; want %+v", got1, ok, tChar1)
		}
		got2, ok := v.Char(1)
		if !ok || got2 != tChar2 {
			t.Errorf("Char(1) = %+v, %v; want %+v", got2, ok, tChar2)
		}
		if _, ok := v.Char(2); ok {
			t.Error("Char(2): ok = true; want false")
		}
	})
	t.Run("CHAR_SELECTED", func(t *testing.T) {
		v, ok := NewCharSelectedView(wire(fixes["CHAR_SELECTED"]))
		if !ok {
			t.Fatal("NewCharSelectedView: ok = false")
		}
		checks := []struct {
			name string
			got  any
			want any
		}{
			{"Name", must(v.Name())["v"], tCharSelected.Name},
			{"Title", must(v.Title())["v"], tCharSelected.Title},
			{"CharID", must(v.CharID())["v"], tCharSelected.CharID},
			{"SessionID", must(v.SessionID())["v"], tCharSelected.SessionID},
			{"X", must(v.X())["v"], tCharSelected.X},
			{"Y", must(v.Y())["v"], tCharSelected.Y},
			{"Z", must(v.Z())["v"], tCharSelected.Z},
			{"CurHP", must(v.CurHP())["v"], tCharSelected.CurHP},
			{"CurMP", must(v.CurMP())["v"], tCharSelected.CurMP},
			{"SP", must(v.SP())["v"], tCharSelected.SP},
			{"Exp", must(v.Exp())["v"], tCharSelected.Exp},
			{"Level", must(v.Level())["v"], tCharSelected.Level},
			{"Karma", must(v.Karma())["v"], tCharSelected.Karma},
			{"PkKills", must(v.PkKills())["v"], tCharSelected.PkKills},
			{"INT", must(v.INT())["v"], tCharSelected.INT},
			{"STR", must(v.STR())["v"], tCharSelected.STR},
			{"CON", must(v.CON())["v"], tCharSelected.CON},
			{"MEN", must(v.MEN())["v"], tCharSelected.MEN},
			{"DEX", must(v.DEX())["v"], tCharSelected.DEX},
			{"WIT", must(v.WIT())["v"], tCharSelected.WIT},
			{"GameTime", must(v.GameTime())["v"], int32(905)},
		}
		for _, c := range checks {
			if c.got != c.want {
				t.Errorf("%s = %v; want %v", c.name, c.got, c.want)
			}
		}
	})
	t.Run("PROTOCOL_VERSION", func(t *testing.T) {
		v, ok := NewProtocolVersionView(wire(fixes["PROTOCOL_VERSION"]))
		if !ok {
			t.Fatal("NewProtocolVersionView: ok = false")
		}
		if v.Version() != 746 {
			t.Errorf("Version = %d; want 746", v.Version())
		}
	})
	t.Run("AUTH_LOGIN", func(t *testing.T) {
		v, ok := NewAuthLoginView(wire(fixes["AUTH_LOGIN"]))
		if !ok {
			t.Fatal("NewAuthLoginView: ok = false")
		}
		if acc, ok := v.Account(); !ok || acc != "ТестЮзер" {
			t.Errorf("Account = %q, %v; want ТестЮзер", acc, ok)
		}
		if k, ok := v.PlayKey2(); !ok || k != tPlayOk2 {
			t.Errorf("PlayKey2 = %#x, %v; want %#x", k, ok, tPlayOk2)
		}
		if k, ok := v.PlayKey1(); !ok || k != tPlayOk1 {
			t.Errorf("PlayKey1 = %#x, %v; want %#x", k, ok, tPlayOk1)
		}
		if k, ok := v.LoginKey1(); !ok || k != tLoginOk1 {
			t.Errorf("LoginKey1 = %#x, %v; want %#x", k, ok, tLoginOk1)
		}
		if k, ok := v.LoginKey2(); !ok || k != tLoginOk2 {
			t.Errorf("LoginKey2 = %#x, %v; want %#x", k, ok, tLoginOk2)
		}
	})
	t.Run("LOGOUT", func(t *testing.T) {
		if _, ok := NewLogoutView(wire(fixes["LOGOUT"])); !ok {
			t.Error("NewLogoutView: ok = false; want true (маркер-пакет)")
		}
	})
	t.Run("CHARACTER_SELECT", func(t *testing.T) {
		v, ok := NewCharacterSelectView(wire(fixes["CHARACTER_SELECT"]))
		if !ok {
			t.Fatal("NewCharacterSelectView: ok = false")
		}
		if v.CharSlot() != 1 {
			t.Errorf("CharSlot = %d; want 1", v.CharSlot())
		}
	})
}

// must разворачивает (значение, ok) геттера в карту для табличной сверки;
// ok=false здесь — ошибка теста.
func must[T any](v T, ok bool) map[string]any {
	if !ok {
		panic("getter вернул ok=false над golden-байтами")
	}
	return map[string]any{"v": v}
}

// Обрезанные входы и злые счётчики: детерминированный отказ, не паника.
func TestHandshakeViewsTruncated(t *testing.T) {
	tests := []struct {
		name string
		min  int
		ctor func(b []byte) bool
	}{
		{"KEY_PACKET", KeyPacketSize, func(b []byte) bool { _, ok := NewKeyPacketView(b); return ok }},
		{"GS LOGIN_FAIL", GSLoginFailSize, func(b []byte) bool { _, ok := NewGSLoginFailView(b); return ok }},
		{"CHAR_SELECT_INFO", 5, func(b []byte) bool { _, ok := NewCharSelectionInfoView(b); return ok }},
		{"PROTOCOL_VERSION", ProtocolVersionSize, func(b []byte) bool { _, ok := NewProtocolVersionView(b); return ok }},
		{"AUTH_LOGIN", 1, func(b []byte) bool { _, ok := NewAuthLoginView(b); return ok }},
		{"LOGOUT", LogoutSize, func(b []byte) bool { _, ok := NewLogoutView(b); return ok }},
		{"CHARACTER_SELECT", 5, func(b []byte) bool { _, ok := NewCharacterSelectView(b); return ok }},
	}
	fill := pattern(64, 3)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			full := append([]byte{0x00}, fill[:tt.min-1]...)
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

// Злые счётчики и строки без терминатора: навигация отдаёт ok=false, не паникуя.
func TestHandshakeViewsEvil(t *testing.T) {
	t.Run("CHAR_SELECT_INFO count=-1", func(t *testing.T) {
		b := []byte{charSelectInfo, 0xFF, 0xFF, 0xFF, 0xFF}
		v, ok := NewCharSelectionInfoView(b)
		if !ok {
			t.Fatal("конструктор: ok = false; want true (заголовок валиден)")
		}
		if _, ok := v.Char(0); ok {
			t.Error("Char(0) при count=-1: ok = true; want false")
		}
	})
	t.Run("CHAR_SELECT_INFO count=MaxInt32", func(t *testing.T) {
		b := []byte{charSelectInfo, 0xFF, 0xFF, 0xFF, 0x7F}
		v, ok := NewCharSelectionInfoView(b)
		if !ok {
			t.Fatal("конструктор: ok = false; want true")
		}
		if _, ok := v.Char(0); ok {
			t.Error("Char(0) при count=MaxInt32: ok = true; want false")
		}
	})
	t.Run("AUTH_LOGIN без терминатора", func(t *testing.T) {
		b := append([]byte{authLogin}, []byte{0x41, 0x00, 0x42, 0x00, 0x43}...) // «A B C» и непарный хвост
		v, ok := NewAuthLoginView(b)
		if !ok {
			t.Fatal("конструктор: ok = false; want true (min 1)")
		}
		if _, ok := v.Account(); ok {
			t.Error("Account без терминатора: ok = true; want false")
		}
		if _, ok := v.PlayKey2(); ok {
			t.Error("PlayKey2 при незакрытой строке: ok = true; want false")
		}
	})
	t.Run("CHAR_SELECTED строка имени не закрыта", func(t *testing.T) {
		b := append([]byte{charSelected}, pattern(20, 7)...)
		v, ok := NewCharSelectedView(b)
		if !ok {
			t.Fatal("конструктор: ok = false; want true")
		}
		if _, ok := v.Name(); ok {
			t.Error("Name без терминатора: ok = true; want false")
		}
		if _, ok := v.Level(); ok {
			t.Error("Level при незакрытом имени: ok = true; want false")
		}
	})
}

// Пачка злых входов: конструкторы и геттеры не паникуют.
func TestHandshakeViewsNoPanic(t *testing.T) {
	fixes := handshakeFixtures(t)
	evils := [][]byte{
		nil, {}, {0x00}, {keyPacket}, pattern(4, 1), pattern(22, 2), pattern(23, 3),
		pattern(50, 4), pattern(200, 5),
		append([]byte{charSelectInfo, 0x02, 0x00, 0x00, 0x00}, pattern(16, 6)...),
		append([]byte{charSelected}, pattern(300, 7)...),
		append([]byte{authLogin}, pattern(30, 8)...),
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("паника на злых входах: %v", r)
		}
	}()
	for _, b := range append(evils, wire(fixes["CHAR_SELECT_INFO"]), wire(fixes["CHAR_SELECTED"]), wire(fixes["AUTH_LOGIN"])) {
		if v, ok := NewKeyPacketView(b); ok {
			_, _, _, _ = v.Result(), v.Key(), v.Encryption(), v.ServerID()
		}
		if v, ok := NewCharSelectionInfoView(b); ok {
			for i := 0; i < v.Count()+1 && i < 300; i++ {
				_, _ = v.Char(i)
			}
		}
		if v, ok := NewCharSelectedView(b); ok {
			_, _ = v.Name()
			_, _ = v.Title()
			_, _ = v.Level()
			_, _ = v.Exp()
		}
		if v, ok := NewAuthLoginView(b); ok {
			_, _ = v.Account()
			_, _ = v.PlayKey2()
			_, _ = v.PlayKey1()
			_, _ = v.LoginKey1()
			_, _ = v.LoginKey2()
		}
		_, _ = NewProtocolVersionView(b)
		_, _ = NewCharacterSelectView(b)
	}
}

// Фиксированные писатели хендшейка — 0 аллокаций.
func TestHandshakeWritersZeroAllocs(t *testing.T) {
	dst := make([]byte, 1024)
	chars := []CharSelectionEntry{tChar1, tChar2}
	allocs := testing.AllocsPerRun(100, func() {
		_ = WriteKeyPacket(dst, 1, tKeyPacketKey, true, 1)
		_ = WriteGSLoginFail(dst, GSReasonPasswordDoesNotMatchThisAccount)
		_ = WriteProtocolVersion(dst, ProtocolVersionInterlude)
		_ = WriteAuthLogin(dst, "ТестЮзер", 1, 2, 3, 4)
		_ = WriteLogout(dst)
		_ = WriteCharacterSelect(dst, 1)
		_ = WriteCharSelectionInfo(dst, chars, 0)
		_ = WriteCharSelected(dst, tCharSelected)
	})
	if allocs != 0 {
		t.Errorf("писатели handshake: %v аллокаций; want 0", allocs)
	}
}

// Sizing-функции сходятся с результатом писателя на таблице входов
// (строчные длины — за пределами фикс-вектора).
func TestHandshakeSizingProperty(t *testing.T) {
	accounts := []string{"", "a", "ТестЮзер", "🧙"}
	for _, acc := range accounts {
		want := AuthLoginSize(acc)
		dst := make([]byte, want)
		if got := WriteAuthLogin(dst, acc, 1, 2, 3, 4); got != want {
			t.Errorf("AuthLoginSize(%q) = %d; писатель = %d", acc, want, got)
		}
	}
	for _, d := range []CharSelectedData{tCharSelected,
		{Name: "", Title: "", GameTime: 5000},
		{Name: "🧙‍♂️", Title: "очень длинный титул с эмодзи 🎖"},
	} {
		want := CharSelectedSize(d)
		dst := make([]byte, want)
		if got := WriteCharSelected(dst, d); got != want {
			t.Errorf("CharSelectedSize(%q) = %d; писатель = %d", d.Name, want, got)
		}
	}
	for _, chars := range [][]CharSelectionEntry{nil, {tChar1}, {tChar1, tChar2},
		{{Name: "🧙", LoginName: "x"}},
	} {
		want := CharSelectionInfoSize(chars)
		dst := make([]byte, want)
		if got := WriteCharSelectionInfo(dst, chars, 0); got != want {
			t.Errorf("CharSelectionInfoSize(%d записей) = %d; писатель = %d", len(chars), want, got)
		}
	}
}
