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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
// с тем же значением и тем же именем (Name* — строковые константы каталога
// для трафик-лога; пустое имя — константа ещё не родилась).
func TestConstantsMatchCatalog(t *testing.T) {
	t.Parallel()
	consts := []struct {
		name      string
		value     byte
		cat       string
		nameConst string
	}{
		{"ATTACK", attack, "GameServer→C", ""},
		{"INIT", OpInit, "LoginServer→C", NameInit},
		{"LOGIN_OK", OpLoginOk, "LoginServer→C", NameLoginOk},
		{"LOGIN_FAIL", OpLoginFail, "LoginServer→C", NameLoginFail},
		{"ACCOUNT_KICKED", OpAccountKicked, "LoginServer→C", NameAccountKicked},
		{"SERVER_LIST", OpServerList, "LoginServer→C", NameServerList},
		{"PLAY_OK", OpPlayOk, "LoginServer→C", NamePlayOk},
		{"PLAY_FAIL", OpPlayFail, "LoginServer→C", NamePlayFail},
		{"GG_AUTH", OpGGAuth, "LoginServer→C", NameGGAuth},
		{"REQUEST_AUTH_LOGIN", OpRequestAuthLogin, "C→LoginServer", NameRequestAuthLogin},
		{"REQUEST_SERVER_LIST", OpRequestServerList, "C→LoginServer", NameRequestServerList},
		{"REQUEST_SERVER_LOGIN", OpRequestServerLogin, "C→LoginServer", NameRequestServerLogin},
		{"AUTH_GAME_GUARD", OpAuthGameGuard, "C→LoginServer", NameAuthGameGuard},
		{"PROTOCOL_VERSION", OpProtocolVersion, "C→GameServer", NameProtocolVersion},
		{"AUTH_LOGIN", OpAuthLogin, "C→GameServer", NameAuthLogin},
		{"LOGOUT", OpLogout, "C→GameServer", NameLogout},
		{"CHARACTER_SELECT", OpCharacterSelect, "C→GameServer", NameCharacterSelect},
		{"KEY_PACKET", OpKeyPacket, "GameServer→C", NameKeyPacket},
		{"CHAR_SELECT_INFO", OpCharSelectInfo, "GameServer→C", NameCharSelectInfo},
		{"LOGIN_FAIL", OpGSLoginFail, "GameServer→C", NameLoginFail},
		{"CHAR_SELECTED", OpCharSelected, "GameServer→C", NameCharSelected},
		// Пакеты мира P3.5 (константы при пакетах, стиль attack).
		{"MOVE_TO_LOCATION", moveToLocation, "C→GameServer", ""},
		{"ENTER_WORLD", enterWorld, "C→GameServer", ""},
		{"SAY2", say2, "C→GameServer", ""},
		{"CHARACTER_CREATE", characterCreate, "C→GameServer", ""},
		{"CHARACTER_DELETE", characterDelete, "C→GameServer", ""},
		{"NEW_CHARACTER", newCharacter, "C→GameServer", ""},
		{"CANNOT_MOVE_ANYMORE", cannotMoveAnymore, "C→GameServer", ""},
		{"VALIDATE_POSITION", validatePosition, "C→GameServer", NameValidatePosition},
		{"REQUEST_RESTART", requestRestart, "C→GameServer", NameRequestRestart},
		{"CHAR_MOVE_TO_LOCATION", charMoveToLocation, "GameServer→C", NameCharMoveToLocation},
		{"CHAR_INFO", charInfo, "GameServer→C", NameCharInfo},
		{"USER_INFO", userInfo, "GameServer→C", NameUserInfo},
		{"DELETE_OBJECT", deleteObject, "GameServer→C", NameDeleteObject},
		{"CHAR_TEMPLATES", charTemplates, "GameServer→C", ""},
		{"CHAR_CREATE_OK", charCreateOk, "GameServer→C", ""},
		{"CHAR_CREATE_FAIL", charCreateFail, "GameServer→C", ""},
		{"ITEM_LIST", itemList, "GameServer→C", ""},
		{"TELEPORT_TO_LOCATION", teleportToLocation, "GameServer→C", ""},
		{"ACTION_FAIL", actionFail, "GameServer→C", ""},
		{"STOP_MOVE", stopMove, "GameServer→C", NameStopMove},
		{"CREATURE_SAY", creatureSay, "GameServer→C", ""},
		{"SKILL_LIST", skillList, "GameServer→C", ""},
		{"VALIDATE_LOCATION", validateLocation, "GameServer→C", NameValidateLocation},
		{"SYSTEM_MESSAGE", systemMessage, "GameServer→C", ""},
		{"SHORT_CUT_INIT", shortCutInit, "GameServer→C", ""},
		{"NPC_INFO", npcInfo, "GameServer→C", ""},
		{"CHAR_DELETE_FAIL", charDeleteFail, "GameServer→C", ""},
	}
	for _, c := range consts {
		found := false
		for _, def := range catalogGroups(t)[c.cat].table {
			if def.Name == c.name {
				found = true
				if def.Value != uint16(c.value) {
					t.Errorf("константа %s = 0x%02X; в каталоге 0x%02X", c.name, c.value, def.Value)
				}
				if c.nameConst != "" && c.nameConst != c.name {
					t.Errorf("константа %s: Name = %q; в каталоге %q", c.name, c.nameConst, c.name)
				}
			}
		}
		if !found {
			t.Errorf("константа %s отсутствует в каталоге %s", c.name, c.cat)
		}
	}
}
