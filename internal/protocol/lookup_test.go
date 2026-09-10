package protocol

import "testing"

// Lookup-имена пакетов: выборочные значения по направлениям S→C и полнота
// против таблиц каталога (мапы строятся из тех же таблиц).
func TestPacketNames(t *testing.T) {
	tests := []struct {
		name string
		gs   bool
		op   byte
		want string
		ok   bool
	}{
		{"LS INIT", false, 0x00, "INIT", true},
		{"LS GG_AUTH", false, 0x0B, "GG_AUTH", true},
		{"LS нет REQUEST_SERVER_LIST", false, 0x05, "", false},
		{"GS KEY_PACKET", true, 0x00, "KEY_PACKET", true},
		{"GS ATTACK", true, 0x05, "ATTACK", true},
		{"GS SUNRISE", true, 0x1C, "SUNRISE", true},
		{"GS нет 0xBA", true, 0xBA, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			var ok bool
			if tt.gs {
				got, ok = GameServerPacketName(tt.op)
			} else {
				got, ok = LoginServerPacketName(tt.op)
			}
			if ok != tt.ok || got != tt.want {
				t.Errorf("имя = %q, %v; want %q, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

// Ex-семейство GS→C: sub — uint16, старший байт не теряется.
func TestGameServerExName(t *testing.T) {
	if name, ok := GameServerExName(0x38); !ok || name != "EX_SHOW_SCREEN_MESSAGE" {
		t.Errorf("GameServerExName(0x38) = %q, %v; want EX_SHOW_SCREEN_MESSAGE", name, ok)
	}
	if name, ok := GameServerExName(0x0138); ok {
		t.Errorf("GameServerExName(0x0138) = %q, %v; want \"\", false (старший байт не усекается)", name, ok)
	}
	if _, ok := GameServerExName(0); ok {
		t.Error("GameServerExName(0): ok = true; want false (sub=0 — «нет sub»)")
	}
}

// Полнота: каждая запись таблицы находится lookup'ом своего направления.
func TestPacketNamesComplete(t *testing.T) {
	for _, def := range loginServerOpcodes {
		if name, ok := LoginServerPacketName(byte(def.Value)); !ok || name != def.Name {
			t.Errorf("LS: %02X → %q, %v; want %q", def.Value, name, ok, def.Name)
		}
	}
	for _, def := range gameServerOpcodes {
		if name, ok := GameServerPacketName(byte(def.Value)); !ok || name != def.Name {
			t.Errorf("GS: %02X → %q, %v; want %q", def.Value, name, ok, def.Name)
		}
	}
	for _, def := range gameServerExOpcodes {
		if name, ok := GameServerExName(def.Value); !ok || name != def.Name {
			t.Errorf("GSEx: %X → %q, %v; want %q", def.Value, name, ok, def.Name)
		}
	}
}
