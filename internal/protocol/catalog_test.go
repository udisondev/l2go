package protocol

import "testing"

// Группа каталога: имя таблицы, ожидаемый счёт записей, основное ли семейство
// (основное — Value это опкод ≤0xFF; Ex — sub при опкоде семейства).
type catalogGroup struct {
	table  []opcodeDef
	want   int
	isMain bool
}

func catalogGroups(t *testing.T) map[string]catalogGroup {
	t.Helper()
	return map[string]catalogGroup{
		"C→LoginServer":  {loginClientOpcodes, 6, true},
		"LoginServer→C":  {loginServerOpcodes, 11, true},
		"C→GameServer":   {gameClientOpcodes, 161, true},
		"C→GameServerEx": {gameClientExOpcodes, 46, false},
		"GameServer→C":   {gameServerOpcodes, 198, true},
		"GameServer→CEx": {gameServerExOpcodes, 72, false},
	}
}

// Полнота: счёт записей по группам равен источнику (interlude docs/opcodes.md@34fe4c8);
// сумма 494.
func TestCatalogCounts(t *testing.T) {
	total := 0
	for name, g := range catalogGroups(t) {
		if got := len(g.table); got != g.want {
			t.Errorf("группа %s: записей %d; want %d", name, got, g.want)
		}
		total += len(g.table)
	}
	if total != 494 {
		t.Errorf("всего записей %d; want 494", total)
	}
}

// Уникальность значений и имён внутри группы; взаимная однозначность имя↔значение.
func TestCatalogUniqueWithinGroup(t *testing.T) {
	for name, g := range catalogGroups(t) {
		byValue := map[uint16]string{}
		byName := map[string]uint16{}
		for _, def := range g.table {
			if prev, dup := byValue[def.Value]; dup {
				t.Errorf("группа %s: значение 0x%02X у %q и %q", name, def.Value, prev, def.Name)
			}
			byValue[def.Value] = def.Name
			if prev, dup := byName[def.Name]; dup {
				t.Errorf("группа %s: имя %q у 0x%02X и 0x%02X", name, def.Name, prev, def.Value)
			}
			byName[def.Name] = def.Value
		}
	}
}

// Диапазон: у основных групп опкод — один байт.
func TestCatalogValueRange(t *testing.T) {
	for name, g := range catalogGroups(t) {
		if !g.isMain {
			continue
		}
		for _, def := range g.table {
			if def.Value > 0xFF {
				t.Errorf("группа %s: %q = 0x%X за пределами байта", name, def.Name, def.Value)
			}
		}
	}
}

// Константы типизируемых пакетов присутствуют в таблице своего направления
// с тем же значением (расширяется с каждым новым пакетом).
func TestConstantsMatchCatalog(t *testing.T) {
	consts := []struct {
		name  string
		value byte
		cat   string
	}{
		{"ATTACK", attack, "GameServer→C"},
		{"INIT", loginInit, "LoginServer→C"},
		{"LOGIN_OK", loginOk, "LoginServer→C"},
		{"LOGIN_FAIL", loginFail, "LoginServer→C"},
		{"ACCOUNT_KICKED", accountKicked, "LoginServer→C"},
		{"SERVER_LIST", serverList, "LoginServer→C"},
		{"PLAY_OK", playOk, "LoginServer→C"},
		{"PLAY_FAIL", playFail, "LoginServer→C"},
		{"GG_AUTH", ggAuth, "LoginServer→C"},
		{"REQUEST_AUTH_LOGIN", requestAuthLogin, "C→LoginServer"},
		{"REQUEST_SERVER_LIST", requestServerList, "C→LoginServer"},
		{"REQUEST_SERVER_LOGIN", requestServerLogin, "C→LoginServer"},
		{"AUTH_GAME_GUARD", authGameGuard, "C→LoginServer"},
		{"PROTOCOL_VERSION", protocolVersion, "C→GameServer"},
		{"AUTH_LOGIN", authLogin, "C→GameServer"},
		{"LOGOUT", logout, "C→GameServer"},
		{"CHARACTER_SELECT", characterSelect, "C→GameServer"},
		{"KEY_PACKET", keyPacket, "GameServer→C"},
		{"CHAR_SELECT_INFO", charSelectInfo, "GameServer→C"},
		{"LOGIN_FAIL", gsLoginFail, "GameServer→C"},
		{"CHAR_SELECTED", charSelected, "GameServer→C"},
	}
	for _, c := range consts {
		found := false
		for _, def := range catalogGroups(t)[c.cat].table {
			if def.Name == c.name {
				found = true
				if def.Value != uint16(c.value) {
					t.Errorf("константа %s = 0x%02X; в каталоге 0x%02X", c.name, c.value, def.Value)
				}
			}
		}
		if !found {
			t.Errorf("константа %s отсутствует в каталоге %s", c.name, c.cat)
		}
	}
}
