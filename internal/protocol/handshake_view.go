// Представления пакетов game-хендшейка (обе стороны). Фиксированные-offsets
// представления — геттеры без ok (границы гарантирует конструктор);
// переменно-строчные (AuthLogin, CharSelected) — геттеры с ok: строки ведут
// переменный хвост, скан терминатора обязан уметь отказывать.

package protocol

// ProtocolVersionView — представление пакета ProtocolVersion (C→GS).
type ProtocolVersionView viewBuf

// NewProtocolVersionView проверяет длину ProtocolVersionSize и возвращает
// представление.
func NewProtocolVersionView(b []byte) (ProtocolVersionView, bool) {
	if len(b) < ProtocolVersionSize {
		return nil, false
	}
	return ProtocolVersionView(b), true
}

// Version возвращает версию протокола клиента.
func (v ProtocolVersionView) Version() int32 { return viewBuf(v).d(1) }

// AuthLoginView — представление пакета AuthLogin (C→GS): аккаунт и четыре
// ключа сессии после него.
type AuthLoginView viewBuf

// NewAuthLoginView проверяет минимальную длину 1 (опкод) и возвращает
// представление; структурная валидность — на геттерах (ok-семантика).
func NewAuthLoginView(b []byte) (AuthLoginView, bool) {
	if len(b) < 1 {
		return nil, false
	}
	return AuthLoginView(b), true
}

// Account возвращает имя аккаунта. ok=false при отсутствии терминатора.
func (v AuthLoginView) Account() (string, bool) {
	s, _, ok := ReadS(v, 1)
	return s, ok
}

// authLoginTail возвращает офсет ключей после аккаунта.
func (v AuthLoginView) authLoginTail() (int, bool) {
	_, n, ok := ReadS(v, 1)
	if !ok || 16 > len(v)-(1+n) {
		return 0, false
	}
	return 1 + n, true
}

// PlayKey2 возвращает второй ключ игровой сессии.
func (v AuthLoginView) PlayKey2() (int32, bool) {
	off, ok := v.authLoginTail()
	if !ok {
		return 0, false
	}
	return ReadD(v, off)
}

// PlayKey1 возвращает первый ключ игровой сессии.
func (v AuthLoginView) PlayKey1() (int32, bool) {
	off, ok := v.authLoginTail()
	if !ok {
		return 0, false
	}
	return ReadD(v, off+4)
}

// LoginKey1 возвращает первый ключ сессии логина.
func (v AuthLoginView) LoginKey1() (int32, bool) {
	off, ok := v.authLoginTail()
	if !ok {
		return 0, false
	}
	return ReadD(v, off+8)
}

// LoginKey2 возвращает второй ключ сессии логина.
func (v AuthLoginView) LoginKey2() (int32, bool) {
	off, ok := v.authLoginTail()
	if !ok {
		return 0, false
	}
	return ReadD(v, off+12)
}

// LogoutView — представление маркер-пакета Logout (C→GS): полей нет.
type LogoutView viewBuf

// NewLogoutView проверяет наличие опкода и возвращает представление.
func NewLogoutView(b []byte) (LogoutView, bool) {
	if len(b) < LogoutSize {
		return nil, false
	}
	return LogoutView(b), true
}

// CharacterSelectView — представление пакета CharacterSelect (C→GS); хвост
// канона (H + 3×D после слота) не читается.
type CharacterSelectView viewBuf

// NewCharacterSelectView проверяет минимальную длину 5 (опкод + слот) и
// возвращает представление.
func NewCharacterSelectView(b []byte) (CharacterSelectView, bool) {
	if len(b) < 5 {
		return nil, false
	}
	return CharacterSelectView(b), true
}

// CharSlot возвращает индекс выбранного слота персонажа.
func (v CharacterSelectView) CharSlot() int32 { return viewBuf(v).d(1) }

// KeyPacketView — представление пакета KeyPacket (GS→C).
type KeyPacketView viewBuf

// NewKeyPacketView проверяет длину KeyPacketSize и возвращает представление.
func NewKeyPacketView(b []byte) (KeyPacketView, bool) {
	if len(b) < KeyPacketSize {
		return nil, false
	}
	return KeyPacketView(b), true
}

// Result возвращает результат приёма протокола (0 — отклонён, 1 — принят).
func (v KeyPacketView) Result() byte { return v[1] }

// Key возвращает 8 случайных байт ключа шифрования (случайная половина;
// статическую клиент добавляет сам).
func (v KeyPacketView) Key() []byte { return v[2:10] }

// Encryption возвращает признак включённого шифрования Blowfish.
func (v KeyPacketView) Encryption() bool { return viewBuf(v).d(10) != 0 }

// ServerID возвращает идентификатор игрового сервера.
func (v KeyPacketView) ServerID() int32 { return viewBuf(v).d(14) }

// GSLoginFailView — представление пакета LoginFail game-стороны (GS→C).
type GSLoginFailView viewBuf

// NewGSLoginFailView проверяет длину GSLoginFailSize и возвращает
// представление.
func NewGSLoginFailView(b []byte) (GSLoginFailView, bool) {
	if len(b) < GSLoginFailSize {
		return nil, false
	}
	return GSLoginFailView(b), true
}

// Reason возвращает int32-код причины отказа.
func (v GSLoginFailView) Reason() GSLoginFailReason { return GSLoginFailReason(viewBuf(v).d(1)) }

// CharSelectionInfoView — представление пакета CharSelectionInfo (GS→C).
// Конструктор проверяет только заголовок; записи — навигацией с ok=false на
// усечении.
type CharSelectionInfoView viewBuf

// NewCharSelectionInfoView проверяет минимальную длину 5 (опкод + счётчик D)
// и возвращает представление.
func NewCharSelectionInfoView(b []byte) (CharSelectionInfoView, bool) {
	if len(b) < 5 {
		return nil, false
	}
	return CharSelectionInfoView(b), true
}

// Count возвращает число персонажей в списке (D заголовка, может быть любым
// int32 из недоверенных байт — навигация ограничена буфером).
func (v CharSelectionInfoView) Count() int {
	n, _ := ReadD(v, 1)
	return int(n)
}

// charFixedTail — хвост записи после строки логина (до конца записи).
const charFixedTail = 293

// Char возвращает запись персонажа. ok=false при усечении или незакрытой
// строке любой из предшествующих записей.
func (v CharSelectionInfoView) Char(i int) (CharSelectionEntry, bool) {
	if i < 0 {
		return CharSelectionEntry{}, false
	}
	off := 5
	for j := 0; j < i; j++ {
		next, ok := v.skipRecord(off)
		if !ok {
			return CharSelectionEntry{}, false
		}
		off = next
	}
	return v.parseRecord(off)
}

// skipRecord проходит запись без разбора: возвращает офсет следующей.
func (v CharSelectionInfoView) skipRecord(off int) (int, bool) {
	_, n, ok := ReadS(v, off)
	if !ok {
		return 0, false
	}
	off += n + 4 // имя + CharID
	_, n, ok = ReadS(v, off)
	if !ok {
		return 0, false
	}
	if charFixedTail > len(v)-(off+n) {
		return 0, false
	}
	return off + n + charFixedTail, true
}

// parseRecord разбирает запись по офсету её начала.
func (v CharSelectionInfoView) parseRecord(off int) (CharSelectionEntry, bool) {
	var e CharSelectionEntry
	name, n, ok := ReadS(v, off)
	if !ok {
		return e, false
	}
	e.Name = name
	off += n
	if e.CharID, ok = ReadD(v, off); !ok {
		return e, false
	}
	off += 4
	login, n, ok := ReadS(v, off)
	if !ok {
		return e, false
	}
	e.LoginName = login
	off += n
	return e, v.parseTail(off, &e)
}

// parseTail читает фиксированный хвост записи после строки логина.
func (v CharSelectionInfoView) parseTail(off int, e *CharSelectionEntry) bool {
	var ok bool
	// sessionID, clanID, builder, sex, race, baseClassID, gsName, x, y, z
	for i := 0; i < 10; i++ {
		x, ok := ReadD(v, off)
		if !ok {
			return false
		}
		switch i {
		case 0:
			e.SessionID = x
		case 1:
			e.ClanID = x
		case 3:
			e.Sex = x
		case 4:
			e.Race = x
		case 5:
			e.BaseClassID = x
		}
		off += 4
	}
	f1, ok := ReadF(v, off)
	if !ok {
		return false
	}
	e.CurHP = f1
	off += 8
	f2, ok := ReadF(v, off)
	if !ok {
		return false
	}
	e.CurMP = f2
	off += 8
	if e.SP, ok = ReadD(v, off); !ok {
		return false
	}
	off += 4
	if e.Exp, ok = ReadQ(v, off); !ok {
		return false
	}
	off += 8
	if e.Level, ok = ReadD(v, off); !ok {
		return false
	}
	off += 4
	if e.Karma, ok = ReadD(v, off); !ok {
		return false
	}
	off += 4
	off += 36 // 9 зарезервированных D
	for i := range e.PaperdollObjectIDs {
		if e.PaperdollObjectIDs[i], ok = ReadD(v, off); !ok {
			return false
		}
		off += 4
	}
	for i := range e.PaperdollItemIDs {
		if e.PaperdollItemIDs[i], ok = ReadD(v, off); !ok {
			return false
		}
		off += 4
	}
	for _, dst := range []*int32{&e.HairStyle, &e.HairColor, &e.Face} {
		if *dst, ok = ReadD(v, off); !ok {
			return false
		}
		off += 4
	}
	f3, ok := ReadF(v, off)
	if !ok {
		return false
	}
	e.MaxHP = f3
	off += 8
	f4, ok := ReadF(v, off)
	if !ok {
		return false
	}
	e.MaxMP = f4
	off += 8
	if e.DeleteTime, ok = ReadD(v, off); !ok {
		return false
	}
	off += 4
	if e.ClassID, ok = ReadD(v, off); !ok {
		return false
	}
	off += 4 + 4 // ClassID + active
	if off >= len(v) {
		return false
	}
	e.Enchant = v[off]
	off++
	if e.AugmentationID, ok = ReadD(v, off); !ok {
		return false
	}
	return true
}

// CharSelectedView — представление пакета CharSelected (GS→C).
type CharSelectedView viewBuf

// NewCharSelectedView проверяет минимальную длину 1 (опкод) и возвращает
// представление.
func NewCharSelectedView(b []byte) (CharSelectedView, bool) {
	if len(b) < 1 {
		return nil, false
	}
	return CharSelectedView(b), true
}

// Name возвращает имя персонажа. ok=false при незакрытой строке.
func (v CharSelectedView) Name() (string, bool) {
	s, _, ok := ReadS(v, 1)
	return s, ok
}

// CharID возвращает ID персонажа.
func (v CharSelectedView) CharID() (int32, bool) {
	_, n, ok := ReadS(v, 1)
	if !ok {
		return 0, false
	}
	return ReadD(v, 1+n)
}

// Title возвращает титул персонажа.
func (v CharSelectedView) Title() (string, bool) {
	off, ok := v.titleOff()
	if !ok {
		return "", false
	}
	s, _, ok := ReadS(v, off)
	return s, ok
}

// titleOff возвращает офсет строки титула (имя + CharID перед ней).
func (v CharSelectedView) titleOff() (int, bool) {
	_, n, ok := ReadS(v, 1)
	if !ok {
		return 0, false
	}
	return 1 + n + 4, true
}

// tail возвращает офсет фиксированного хвоста после титула.
func (v CharSelectedView) tail() (int, bool) {
	off, ok := v.titleOff()
	if !ok {
		return 0, false
	}
	_, n, ok := ReadS(v, off)
	if !ok {
		return 0, false
	}
	return off + n, true
}

func (v CharSelectedView) field(off int) (int32, bool) {
	base, ok := v.tail()
	if !ok {
		return 0, false
	}
	return ReadD(v, base+off)
}

// SessionID возвращает идентификатор сессии.
func (v CharSelectedView) SessionID() (int32, bool) { return v.field(0) }

// ClanID возвращает идентификатор клана.
func (v CharSelectedView) ClanID() (int32, bool) { return v.field(4) }

// Sex возвращает пол персонажа.
func (v CharSelectedView) Sex() (int32, bool) { return v.field(12) }

// Race возвращает расу персонажа.
func (v CharSelectedView) Race() (int32, bool) { return v.field(16) }

// ClassID возвращает класс персонажа.
func (v CharSelectedView) ClassID() (int32, bool) { return v.field(20) }

// X возвращает X-координату.
func (v CharSelectedView) X() (int32, bool) { return v.field(28) }

// Y возвращает Y-координату.
func (v CharSelectedView) Y() (int32, bool) { return v.field(32) }

// Z возвращает Z-координату.
func (v CharSelectedView) Z() (int32, bool) { return v.field(36) }

// SP возвращает очки умений.
func (v CharSelectedView) SP() (int32, bool) { return v.field(56) }

// Exp возвращает опыт персонажа (Q канона).
func (v CharSelectedView) Exp() (int64, bool) {
	base, ok := v.tail()
	if !ok {
		return 0, false
	}
	return ReadQ(v, base+60)
}

// Level возвращает уровень персонажа.
func (v CharSelectedView) Level() (int32, bool) { return v.field(68) }

// Karma возвращает карму.
func (v CharSelectedView) Karma() (int32, bool) { return v.field(72) }

// PkKills возвращает счётчик PK.
func (v CharSelectedView) PkKills() (int32, bool) { return v.field(76) }

// INT возвращает врождённый INT.
func (v CharSelectedView) INT() (int32, bool) { return v.field(80) }

// STR возвращает врождённый STR.
func (v CharSelectedView) STR() (int32, bool) { return v.field(84) }

// CON возвращает врождённый CON.
func (v CharSelectedView) CON() (int32, bool) { return v.field(88) }

// MEN возвращает врождённый MEN.
func (v CharSelectedView) MEN() (int32, bool) { return v.field(92) }

// DEX возвращает врождённый DEX.
func (v CharSelectedView) DEX() (int32, bool) { return v.field(96) }

// WIT возвращает врождённый WIT.
func (v CharSelectedView) WIT() (int32, bool) { return v.field(100) }

// GameTime возвращает игровое время (минуты суток, поле канона gameTime%1440).
func (v CharSelectedView) GameTime() (int32, bool) { return v.field(232) }

// CurHP возвращает текущее HP (double канона).
func (v CharSelectedView) CurHP() (float64, bool) {
	base, ok := v.tail()
	if !ok {
		return 0, false
	}
	return ReadF(v, base+40)
}

// CurMP возвращает текущее MP (double канона).
func (v CharSelectedView) CurMP() (float64, bool) {
	base, ok := v.tail()
	if !ok {
		return 0, false
	}
	return ReadF(v, base+48)
}
