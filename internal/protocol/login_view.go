// Представления пакетов логин-флоу (обе стороны протокола): конструктор
// проверяет минимальную длину один раз, геттеры читают по офсетам без копий;
// навигация переменных секций — с ok-семантикой. Нулевое значение
// недействительно, доступ — только после успешного конструктора.

package protocol

import "encoding/binary"

func (b viewBuf) d(off int) int32 { return int32(binary.LittleEndian.Uint32(b[off:])) }

// viewBuf — общий тип-надстройка представлений над буфером пакета.
type viewBuf []byte

// InitView — представление пакета Init (LS→C).
type InitView viewBuf

// NewInitView проверяет длину InitSize и возвращает представление.
func NewInitView(b []byte) (InitView, bool) {
	if len(b) < InitSize {
		return nil, false
	}
	return InitView(b), true
}

// SessionID возвращает идентификатор сессии логина.
func (v InitView) SessionID() int32 { return viewBuf(v).d(1) }

// Revision возвращает ревизию протокола логина (Interlude: 0x0000C621).
func (v InitView) Revision() int32 { return viewBuf(v).d(5) }

// Modulus возвращает скрэмблированный RSA-модуль (128 Б, непрозрачные байты).
func (v InitView) Modulus() []byte { return v[9:137] }

// BlowfishKey возвращает динамический ключ Blowfish (16 Б).
func (v InitView) BlowfishKey() []byte { return v[153:169] }

// LoginOkView — представление пакета LoginOk (LS→C).
type LoginOkView viewBuf

// NewLoginOkView проверяет длину LoginOkSize и возвращает представление.
func NewLoginOkView(b []byte) (LoginOkView, bool) {
	if len(b) < LoginOkSize {
		return nil, false
	}
	return LoginOkView(b), true
}

// LoginOkID1 возвращает первую часть ключа сессии логина.
func (v LoginOkView) LoginOkID1() int32 { return viewBuf(v).d(1) }

// LoginOkID2 возвращает вторую часть ключа сессии логина.
func (v LoginOkView) LoginOkID2() int32 { return viewBuf(v).d(5) }

// LoginFailView — представление пакета LoginFail (LS→C).
type LoginFailView viewBuf

// NewLoginFailView проверяет длину LoginFailSize и возвращает представление.
func NewLoginFailView(b []byte) (LoginFailView, bool) {
	if len(b) < LoginFailSize {
		return nil, false
	}
	return LoginFailView(b), true
}

// Reason возвращает код причины отказа.
func (v LoginFailView) Reason() LoginFailReason { return LoginFailReason(v[1]) }

// AccountKickedView — представление пакета AccountKicked (LS→C).
type AccountKickedView viewBuf

// NewAccountKickedView проверяет длину AccountKickedSize и возвращает
// представление.
func NewAccountKickedView(b []byte) (AccountKickedView, bool) {
	if len(b) < AccountKickedSize {
		return nil, false
	}
	return AccountKickedView(b), true
}

// Reason возвращает код причины исключения аккаунта.
func (v AccountKickedView) Reason() KickReason { return KickReason(viewBuf(v).d(1)) }

// PlayOkView — представление пакета PlayOk (LS→C).
type PlayOkView viewBuf

// NewPlayOkView проверяет длину PlayOkSize и возвращает представление.
func NewPlayOkView(b []byte) (PlayOkView, bool) {
	if len(b) < PlayOkSize {
		return nil, false
	}
	return PlayOkView(b), true
}

// PlayOkID1 возвращает первую часть ключа игровой сессии.
func (v PlayOkView) PlayOkID1() int32 { return viewBuf(v).d(1) }

// PlayOkID2 возвращает вторую часть ключа игровой сессии.
func (v PlayOkView) PlayOkID2() int32 { return viewBuf(v).d(5) }

// PlayFailView — представление пакета PlayFail (LS→C).
type PlayFailView viewBuf

// NewPlayFailView проверяет длину PlayFailSize и возвращает представление.
func NewPlayFailView(b []byte) (PlayFailView, bool) {
	if len(b) < PlayFailSize {
		return nil, false
	}
	return PlayFailView(b), true
}

// Reason возвращает код причины отказа.
func (v PlayFailView) Reason() PlayFailReason { return PlayFailReason(v[1]) }

// GGAuthView — представление пакета GGAuth (LS→C).
type GGAuthView viewBuf

// NewGGAuthView проверяет длину GGAuthSize и возвращает представление.
func NewGGAuthView(b []byte) (GGAuthView, bool) {
	if len(b) < GGAuthSize {
		return nil, false
	}
	return GGAuthView(b), true
}

// Response возвращает код ответа на пробу GameGuard (обычно sessionID).
func (v GGAuthView) Response() int32 { return viewBuf(v).d(1) }

// ServerListView — представление пакета ServerList (LS→C). Конструктор
// проверяет только заголовок; записи навигационными геттерами с ok=false на
// усечении (частичный разбор лучше отказа — диспетчер показывает hex-дамп).
type ServerListView viewBuf

// NewServerListView проверяет длину заголовка (3 Б) и возвращает
// представление.
func NewServerListView(b []byte) (ServerListView, bool) {
	if len(b) < 3 {
		return nil, false
	}
	return ServerListView(b), true
}

// Count возвращает число записей серверов первой секции (байт заголовка).
func (v ServerListView) Count() int { return int(v[1]) }

// LastServer возвращает идентификатор последнего выбранного сервера.
func (v ServerListView) LastServer() byte { return v[2] }

// Server возвращает запись первой секции (фиксированный шаг 21 Б). ok=false
// при выходе индекса за счётчик или усечении буфера.
func (v ServerListView) Server(i int) (ServerListEntry, bool) {
	off := 3 + 21*i
	if i < 0 || i >= v.Count() || 21 > len(v)-off {
		return ServerListEntry{}, false
	}
	var e ServerListEntry
	e.ID = v[off]
	copy(e.IP[:], v[off+1:])
	e.Port = viewBuf(v).d(off + 5)
	e.AgeLimit = v[off+9]
	e.PvP = v[off+10] != 0
	e.CurrentPlayers = int16(binary.LittleEndian.Uint16(v[off+11:]))
	e.MaxPlayers = int16(binary.LittleEndian.Uint16(v[off+13:]))
	e.Status = v[off+15]
	e.ServerType = viewBuf(v).d(off + 16)
	e.Brackets = v[off+20] != 0
	return e, true
}

// charsOff возвращает офсет счётчика второй секции; ok=false при усечении.
func (v ServerListView) charsOff() (int, bool) {
	off := 3 + 21*v.Count() + 2
	if off >= len(v) {
		return 0, false
	}
	return off, true
}

// CharsCount возвращает счётчик второй секции (счётчики персонажей).
func (v ServerListView) CharsCount() (int, bool) {
	off, ok := v.charsOff()
	if !ok {
		return 0, false
	}
	return int(v[off]), true
}

// Chars возвращает запись второй секции (serverID, счётчик персонажей,
// времена удаления). ok=false при усечении.
func (v ServerListView) Chars(i int) (ServerChars, bool) {
	off, ok := v.charsOff()
	if !ok || i < 0 || i >= int(v[off]) {
		return ServerChars{}, false
	}
	off++ // записи переменной длины — проход до i-й
	for j := 0; j < i; j++ {
		if 3 > len(v)-off {
			return ServerChars{}, false
		}
		off += 3 + 4*int(v[off+2])
	}
	if 3 > len(v)-off {
		return ServerChars{}, false
	}
	e := ServerChars{ServerID: v[off], CharCount: v[off+1]}
	off += 3
	n := int(v[off-1])
	if 4*n > len(v)-off {
		return ServerChars{}, false
	}
	if n > 0 {
		e.DeleteTimes = make([]int32, n)
		for k := range e.DeleteTimes {
			e.DeleteTimes[k] = viewBuf(v).d(off + 4*k)
		}
	}
	return e, true
}

// RequestAuthLoginView — представление wire-пакета RequestAuthLogin (C→LS)
// над [опкод||RSA-блоб]. New-method (256 Б) не типизируется.
type RequestAuthLoginView viewBuf

// NewRequestAuthLoginView проверяет минимальную длину 129 (опкод + блок) и
// возвращает представление.
func NewRequestAuthLoginView(b []byte) (RequestAuthLoginView, bool) {
	if len(b) < RequestAuthLoginSize {
		return nil, false
	}
	return RequestAuthLoginView(b), true
}

// RSABlock возвращает 128-байтовый RSA-шифротекст (непрозрачные байты;
// расшифровка — пакет crypto).
func (v RequestAuthLoginView) RSABlock() []byte { return v[1:129] }

// AuthLoginPlainView — представление расшифрованного 128-байтового блока
// учётных данных RequestAuthLogin (блок без опкода).
type AuthLoginPlainView viewBuf

// NewAuthLoginPlainView проверяет минимальную длину 124 (0x6C+16 — поле
// пароля до конца) и возвращает представление.
func NewAuthLoginPlainView(block []byte) (AuthLoginPlainView, bool) {
	if len(block) < authLoginPassOffset+authLoginPassLen {
		return nil, false
	}
	return AuthLoginPlainView(block), true
}

// User возвращает логин: поле обрезается от ≤ U+0020 с обоих концов —
// семантика Java String.trim() канона.
func (v AuthLoginPlainView) User() string {
	return trimField(v[authLoginUserOffset : authLoginUserOffset+authLoginUserLen])
}

// Password возвращает пароль с той же обрезкой; пробелы в середине сохраняются.
func (v AuthLoginPlainView) Password() string {
	return trimField(v[authLoginPassOffset : authLoginPassOffset+authLoginPassLen])
}

func trimField(b []byte) string {
	for len(b) > 0 && b[0] <= 0x20 {
		b = b[1:]
	}
	for len(b) > 0 && b[len(b)-1] <= 0x20 {
		b = b[:len(b)-1]
	}
	return string(b)
}

// RequestServerListView — представление пакета RequestServerList (C→LS).
type RequestServerListView viewBuf

// NewRequestServerListView проверяет длину RequestServerListSize и возвращает
// представление.
func NewRequestServerListView(b []byte) (RequestServerListView, bool) {
	if len(b) < RequestServerListSize {
		return nil, false
	}
	return RequestServerListView(b), true
}

// LoginOkID1 возвращает первую часть ключа сессии логина.
func (v RequestServerListView) LoginOkID1() int32 { return viewBuf(v).d(1) }

// LoginOkID2 возвращает вторую часть ключа сессии логина.
func (v RequestServerListView) LoginOkID2() int32 { return viewBuf(v).d(5) }

// RequestServerLoginView — представление пакета RequestServerLogin (C→LS).
type RequestServerLoginView viewBuf

// NewRequestServerLoginView проверяет длину RequestServerLoginSize и
// возвращает представление.
func NewRequestServerLoginView(b []byte) (RequestServerLoginView, bool) {
	if len(b) < RequestServerLoginSize {
		return nil, false
	}
	return RequestServerLoginView(b), true
}

// LoginOkID1 возвращает первую часть ключа сессии логина.
func (v RequestServerLoginView) LoginOkID1() int32 { return viewBuf(v).d(1) }

// LoginOkID2 возвращает вторую часть ключа сессии логина.
func (v RequestServerLoginView) LoginOkID2() int32 { return viewBuf(v).d(5) }

// ServerID возвращает выбранный сервер.
func (v RequestServerLoginView) ServerID() byte { return v[9] }

// AuthGameGuardView — представление пробы GameGuard (C→LS); 16 резервных
// байт после sessionID не читаются.
type AuthGameGuardView viewBuf

// NewAuthGameGuardView проверяет минимальную длину 5 (опкод + sessionID) и
// возвращает представление.
func NewAuthGameGuardView(b []byte) (AuthGameGuardView, bool) {
	if len(b) < 5 {
		return nil, false
	}
	return AuthGameGuardView(b), true
}

// SessionID возвращает идентификатор сессии пробы.
func (v AuthGameGuardView) SessionID() int32 { return viewBuf(v).d(1) }
