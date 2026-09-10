// Пакеты логин-флоу Interlude: писатели LoginServer→C и C→LoginServer.
// Порт udisondev/interlude@34fe4c86 pkg/packet/{server,client}; формат сверен
// с каноном Mobius master CT_0_Interlude (43ac8878). Расхождения источника с
// каноном закрыты в пользу канона; ключевое здесь: обрезка полей учётных
// данных — Java trim() (≤ U+0020 с двух концов), а не TrimRight NUL/пробел.
// Крипта в этом файле отсутствует: RSA-блоб и скрэмблированный модуль —
// непрозрачные байты.

package protocol

import (
	"encoding/binary"
	"fmt"
)

// Опкоды и имена каталога пакетов логин-флоу: константы живут с писателями
// и представлениями этого файла; машинная связь с каталогом —
// TestConstantsMatchCatalog (значения и имена сверяются с таблицами).
const (
	OpInit               = 0x00 // INIT, LS→C
	OpLoginFail          = 0x01 // LOGIN_FAIL, LS→C
	OpAccountKicked      = 0x02 // ACCOUNT_KICKED, LS→C
	OpLoginOk            = 0x03 // LOGIN_OK, LS→C
	OpServerList         = 0x04 // SERVER_LIST, LS→C
	OpPlayFail           = 0x06 // PLAY_FAIL, LS→C
	OpPlayOk             = 0x07 // PLAY_OK, LS→C
	OpGGAuth             = 0x0B // GG_AUTH, LS→C
	OpRequestAuthLogin   = 0x00 // REQUEST_AUTH_LOGIN, C→LS
	OpRequestServerLogin = 0x02 // REQUEST_SERVER_LOGIN, C→LS
	OpRequestServerList  = 0x05 // REQUEST_SERVER_LIST, C→LS
	OpAuthGameGuard      = 0x07 // AUTH_GAME_GUARD, C→LS

	NameInit               = "INIT"
	NameLoginFail          = "LOGIN_FAIL"
	NameAccountKicked      = "ACCOUNT_KICKED"
	NameLoginOk            = "LOGIN_OK"
	NameServerList         = "SERVER_LIST"
	NamePlayFail           = "PLAY_FAIL"
	NamePlayOk             = "PLAY_OK"
	NameGGAuth             = "GG_AUTH"
	NameRequestAuthLogin   = "REQUEST_AUTH_LOGIN"
	NameRequestServerLogin = "REQUEST_SERVER_LOGIN"
	NameRequestServerList  = "REQUEST_SERVER_LIST"
	NameAuthGameGuard      = "AUTH_GAME_GUARD"
)

// Фиксированные размеры пакетов (с опкодом).
const (
	InitSize                  = 170
	LoginFailSize             = 2
	AccountKickedSize         = 5
	LoginOkSize               = 49
	PlayFailSize              = 2
	PlayOkSize                = 9
	GGAuthSize                = 21
	RequestAuthLoginSize      = 129
	RequestAuthLoginPlainSize = 128
	RequestServerListSize     = 9
	RequestServerLoginSize    = 10
	AuthGameGuardSize         = 21
)

// Офсеты и длины полей plain-блока RequestAuthLogin (128 Б): учётные данные
// в RSA-зашифрованном блоке. New-method (256 Б, единый массив: логин
// 0x4E/50 + 0xCE/14, пароль 0xDC/16) не типизируется — потребителя нет.
const (
	authLoginUserOffset = 0x5E
	authLoginUserLen    = 14
	authLoginPassOffset = 0x6C
	authLoginPassLen    = 16
)

// Форматные константы Init: ревизия протокола логина и константы GameGuard
// (uint32 — 0x97ADB620 вне знакового диапазона; запись — битово точной
// LittleEndian-записью).
const (
	initProtocolRevision        = 0x0000C621
	ggConst1             uint32 = 0x29DD954E
	ggConst2             uint32 = 0x77C39CFC
	ggConst3             uint32 = 0x97ADB620
	ggConst4             uint32 = 0x07BDE0F7
)

// loginOkUnknown — неизвестное поле LoginOk с фиксированным значением канона.
const loginOkUnknown = 0x3EA

// Коды причин отказов LoginFail и PlayFail (LS→C, байт): канон использует
// один набор кодов в обоих пакетах (PlayFail без NOT_AUTHED), поэтому
// константы нетипизированы и годны обоим типам. Перенос Mobius master
// CT_0_Interlude LoginFailReason/PlayFailReason (43ac8878).
const (
	ReasonNoMessage                                     = 0x00
	ReasonSystemErrorLoginLater                         = 0x01
	ReasonUserOrPassWrong                               = 0x02
	ReasonAccessFailedTryAgainLater                     = 0x04
	ReasonAccountInfoIncorrectContactSupport            = 0x05
	ReasonNotAuthed                                     = 0x06
	ReasonAccountInUse                                  = 0x07
	ReasonUnder18YearsKR                                = 0x0C
	ReasonServerOverloaded                              = 0x0F
	ReasonServerMaintenance                             = 0x10
	ReasonTempPassExpired                               = 0x11
	ReasonGameTimeExpired                               = 0x12
	ReasonNoTimeLeft                                    = 0x13
	ReasonSystemError                                   = 0x14
	ReasonAccessFailed                                  = 0x15
	ReasonRestrictedIP                                  = 0x16
	ReasonWeekUsageFinished                             = 0x1E
	ReasonSecurityCardNumberInvalid                     = 0x1F
	ReasonAgeNotVerifiedCantLogBetween10PM6AM           = 0x20
	ReasonServerCannotBeAccessedByYourCoupon            = 0x21
	ReasonDualBox                                       = 0x23
	ReasonInactive                                      = 0x24
	ReasonUserAgreementRejectedOnWebsite                = 0x25
	ReasonGuardianConsentRequired                       = 0x26
	ReasonUserAgreementDeclinedOrWithdrawlRequest       = 0x27
	ReasonAccountSuspendedCall                          = 0x28
	ReasonChangePasswordAndQuizOnWebsite                = 0x29
	ReasonAlreadyLoggedInto10Accounts                   = 0x2A
	ReasonMasterAccountRestricted                       = 0x2B
	ReasonCertificationFailed                           = 0x2E
	ReasonTelephoneCertificationUnavailable             = 0x2F
	ReasonTelephoneSignalsDelayed                       = 0x30
	ReasonCertificationFailedLineBusy                   = 0x31
	ReasonCertificationServiceNumberExpiredOrIncorrect  = 0x32
	ReasonCertificationServiceCurrentlyBeingChecked     = 0x33
	ReasonCertificationServiceCantBeUsedHeavyVolume     = 0x34
	ReasonCertificationServiceExpiredGameplayBlocked    = 0x35
	ReasonCertificationFailed3TimesGameplayBlocked30Min = 0x36
	ReasonCertificationDailyUseExceeded                 = 0x37
	ReasonCertificationUnderwayTryAgainLater            = 0x38
)

// LoginFailReason — код причины отказа LoginFail (LS→C, байт).
type LoginFailReason byte

// PlayFailReason — код причины отказа PlayFail (LS→C, байт).
type PlayFailReason byte

// KickReason — код причины AccountKicked (LS→C, int32).
type KickReason int32

// Коды причин AccountKicked (перенос Mobius AccountKickedReason); префикс
// семейства отличает их от нетипизированных кодов LoginFail/PlayFail.
const (
	KickDataStealer       KickReason = 0x01
	KickGenericViolation  KickReason = 0x08
	Kick7DaysSuspended    KickReason = 0x10
	KickPermanentlyBanned KickReason = 0x20
)

// ServerListEntry — запись сервера в первой секции ServerList.
type ServerListEntry struct {
	ID             byte
	IP             [4]byte
	Port           int32
	AgeLimit       byte
	PvP            bool
	CurrentPlayers int16
	MaxPlayers     int16
	Status         byte // готовое wire-значение (0 — вниз); отображение статусов — логика сервера
	ServerType     int32
	Brackets       bool
}

// ServerChars — запись второй секции ServerList: счётчики персонажей сервера.
type ServerChars struct {
	ServerID    byte
	CharCount   byte
	DeleteTimes []int32 // секунды до удаления
}

// WriteInit пишет пакет Init (LS→C): sessionID, ревизия протокола,
// скрэмблированный RSA-модуль (128 Б), константы GameGuard, Blowfish-ключ
// (16 Б) и null-терминатор. Возвращает число записанных байт (InitSize).
func WriteInit(dst []byte, sessionID int32, scrambledModulus, blowfishKey []byte) int {
	if len(dst) < InitSize {
		panic(fmt.Sprintf("protocol: WriteInit: dst длиной %d байт < InitSize=%d", len(dst), InitSize))
	}
	if len(scrambledModulus) != 128 {
		panic(fmt.Sprintf("protocol: WriteInit: скрэмблированный модуль %d байт; want 128", len(scrambledModulus)))
	}
	if len(blowfishKey) != 16 {
		panic(fmt.Sprintf("protocol: WriteInit: Blowfish-ключ %d байт; want 16", len(blowfishKey)))
	}
	dst[0] = OpInit
	WriteD(dst[1:], sessionID)
	WriteD(dst[5:], initProtocolRevision)
	copy(dst[9:], scrambledModulus)
	binary.LittleEndian.PutUint32(dst[137:], ggConst1)
	binary.LittleEndian.PutUint32(dst[141:], ggConst2)
	binary.LittleEndian.PutUint32(dst[145:], ggConst3)
	binary.LittleEndian.PutUint32(dst[149:], ggConst4)
	copy(dst[153:], blowfishKey)
	dst[169] = 0
	return InitSize
}

// WriteLoginOk пишет пакет LoginOk (LS→C): ключи сессии логина и
// зарезервированные поля канона. Возвращает число записанных байт (LoginOkSize).
func WriteLoginOk(dst []byte, loginOkID1, loginOkID2 int32) int {
	if len(dst) < LoginOkSize {
		panic(fmt.Sprintf("protocol: WriteLoginOk: dst длиной %d байт < LoginOkSize=%d", len(dst), LoginOkSize))
	}
	dst[0] = OpLoginOk
	WriteD(dst[1:], loginOkID1)
	WriteD(dst[5:], loginOkID2)
	WriteD(dst[9:], 0)
	WriteD(dst[13:], 0)
	WriteD(dst[17:], loginOkUnknown)
	WriteD(dst[21:], 0)
	WriteD(dst[25:], 0)
	WriteD(dst[29:], 0)
	clear(dst[33:49])
	return LoginOkSize
}

// WriteLoginFail пишет пакет LoginFail (LS→C) с байтовым кодом причины.
// Возвращает число записанных байт (LoginFailSize).
func WriteLoginFail(dst []byte, reason LoginFailReason) int {
	if len(dst) < LoginFailSize {
		panic(fmt.Sprintf("protocol: WriteLoginFail: dst длиной %d байт < LoginFailSize=%d", len(dst), LoginFailSize))
	}
	dst[0] = OpLoginFail
	dst[1] = byte(reason)
	return LoginFailSize
}

// WriteAccountKicked пишет пакет AccountKicked (LS→C) с int32-кодом причины.
// Возвращает число записанных байт (AccountKickedSize).
func WriteAccountKicked(dst []byte, reason KickReason) int {
	if len(dst) < AccountKickedSize {
		panic(fmt.Sprintf("protocol: WriteAccountKicked: dst длиной %d байт < AccountKickedSize=%d", len(dst), AccountKickedSize))
	}
	dst[0] = OpAccountKicked
	WriteD(dst[1:], int32(reason))
	return AccountKickedSize
}

// WritePlayOk пишет пакет PlayOk (LS→C): ключи игровой сессии. Возвращает
// число записанных байт (PlayOkSize).
func WritePlayOk(dst []byte, playOkID1, playOkID2 int32) int {
	if len(dst) < PlayOkSize {
		panic(fmt.Sprintf("protocol: WritePlayOk: dst длиной %d байт < PlayOkSize=%d", len(dst), PlayOkSize))
	}
	dst[0] = OpPlayOk
	WriteD(dst[1:], playOkID1)
	WriteD(dst[5:], playOkID2)
	return PlayOkSize
}

// WritePlayFail пишет пакет PlayFail (LS→C) с байтовым кодом причины.
// Возвращает число записанных байт (PlayFailSize).
func WritePlayFail(dst []byte, reason PlayFailReason) int {
	if len(dst) < PlayFailSize {
		panic(fmt.Sprintf("protocol: WritePlayFail: dst длиной %d байт < PlayFailSize=%d", len(dst), PlayFailSize))
	}
	dst[0] = OpPlayFail
	dst[1] = byte(reason)
	return PlayFailSize
}

// WriteGGAuth пишет пакет GGAuth (LS→C) — ответ на пробу GameGuard:
// код ответа (обычно sessionID) и четыре нулевых D канона. Возвращает число
// записанных байт (GGAuthSize).
func WriteGGAuth(dst []byte, response int32) int {
	if len(dst) < GGAuthSize {
		panic(fmt.Sprintf("protocol: WriteGGAuth: dst длиной %d байт < GGAuthSize=%d", len(dst), GGAuthSize))
	}
	dst[0] = OpGGAuth
	WriteD(dst[1:], response)
	clear(dst[5:21])
	return GGAuthSize
}

// ServerListSize возвращает размер WriteServerList для данных списков —
// sizing и запись одним расчётом.
func ServerListSize(servers []ServerListEntry, chars []ServerChars) int {
	size := 3 + 21*len(servers) + 3 // заголовок + записи + H(0) + счётчик секции 2
	for _, c := range chars {
		size += 3 + 4*len(c.DeleteTimes)
	}
	return size
}

// WriteServerList пишет пакет ServerList (LS→C): заголовок (счёт, последний
// сервер), записи серверов фиксированного шага, разделитель H(0) и секция
// счётчиков персонажей. Счётчики провода однобайтовые: списки длиннее 255 —
// нарушение программного контракта вызывающего. Возвращает число записанных
// байт.
func WriteServerList(dst []byte, servers []ServerListEntry, chars []ServerChars, lastServer byte) int {
	size := ServerListSize(servers, chars)
	if len(dst) < size {
		panic(fmt.Sprintf("protocol: WriteServerList: dst длиной %d байт < ServerListSize=%d", len(dst), size))
	}
	if len(servers) > 255 || len(chars) > 255 {
		panic(fmt.Sprintf("protocol: WriteServerList: счётчик секции вне байта: servers=%d, chars=%d", len(servers), len(chars)))
	}
	dst[0] = OpServerList
	dst[1] = byte(len(servers))
	dst[2] = lastServer
	off := 3
	for _, s := range servers {
		dst[off] = s.ID
		copy(dst[off+1:], s.IP[:])
		WriteD(dst[off+5:], s.Port)
		dst[off+9] = s.AgeLimit
		dst[off+10] = boolByte(s.PvP)
		WriteH(dst[off+11:], s.CurrentPlayers)
		WriteH(dst[off+13:], s.MaxPlayers)
		dst[off+15] = s.Status
		WriteD(dst[off+16:], s.ServerType)
		dst[off+20] = boolByte(s.Brackets)
		off += 21
	}
	WriteH(dst[off:], 0) // unknown — разделитель секций канона
	off += 2
	dst[off] = byte(len(chars))
	off++
	for _, c := range chars {
		if len(c.DeleteTimes) > 255 {
			panic(fmt.Sprintf("protocol: WriteServerList: счётчик удалений вне байта: %d", len(c.DeleteTimes)))
		}
		dst[off] = c.ServerID
		dst[off+1] = c.CharCount
		dst[off+2] = byte(len(c.DeleteTimes))
		off += 3
		for _, ts := range c.DeleteTimes {
			WriteD(dst[off:], ts)
			off += 4
		}
	}
	return off
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// WriteRequestAuthLogin пишет wire-пакет RequestAuthLogin (C→LS): опкод и
// 128-байтовый RSA-шифротекст учётных данных. Возвращает число записанных
// байт (RequestAuthLoginSize).
func WriteRequestAuthLogin(dst, rsaBlock []byte) int {
	if len(dst) < RequestAuthLoginSize {
		panic(fmt.Sprintf("protocol: WriteRequestAuthLogin: dst длиной %d байт < RequestAuthLoginSize=%d", len(dst), RequestAuthLoginSize))
	}
	if len(rsaBlock) != 128 {
		panic(fmt.Sprintf("protocol: WriteRequestAuthLogin: RSA-блоб %d байт; want 128 (new-method не типизируется)", len(rsaBlock)))
	}
	dst[0] = OpRequestAuthLogin
	copy(dst[1:], rsaBlock)
	return RequestAuthLoginSize
}

// WriteRequestAuthLoginPlain собирает 128-байтовый plain-блок учётных данных
// RequestAuthLogin (без опкода — блок, а не пакет): логин с 0x5E (14 Б),
// пароль с 0x6C (16 Б). Переполнение длины — ошибка: учётные данные приходят
// от пользователя и не являются программной инвариантой вызывающего.
func WriteRequestAuthLoginPlain(dst []byte, user, pass string) error {
	if len(dst) < RequestAuthLoginPlainSize {
		panic(fmt.Sprintf("protocol: WriteRequestAuthLoginPlain: dst длиной %d байт < RequestAuthLoginPlainSize=%d", len(dst), RequestAuthLoginPlainSize))
	}
	if len(user) > authLoginUserLen {
		return fmt.Errorf("protocol: WriteRequestAuthLoginPlain: логин %d байт; want ≤ %d", len(user), authLoginUserLen)
	}
	if len(pass) > authLoginPassLen {
		return fmt.Errorf("protocol: WriteRequestAuthLoginPlain: пароль %d байт; want ≤ %d", len(pass), authLoginPassLen)
	}
	clear(dst[:RequestAuthLoginPlainSize])
	copy(dst[authLoginUserOffset:], user)
	copy(dst[authLoginPassOffset:], pass)
	return nil
}

// WriteRequestServerList пишет пакет RequestServerList (C→LS): ключи сессии
// логина. Возвращает число записанных байт (RequestServerListSize).
func WriteRequestServerList(dst []byte, loginOkID1, loginOkID2 int32) int {
	if len(dst) < RequestServerListSize {
		panic(fmt.Sprintf("protocol: WriteRequestServerList: dst длиной %d байт < RequestServerListSize=%d", len(dst), RequestServerListSize))
	}
	dst[0] = OpRequestServerList
	WriteD(dst[1:], loginOkID1)
	WriteD(dst[5:], loginOkID2)
	return RequestServerListSize
}

// WriteRequestServerLogin пишет пакет RequestServerLogin (C→LS): ключи сессии
// логина и выбранный сервер. Возвращает число записанных байт
// (RequestServerLoginSize).
func WriteRequestServerLogin(dst []byte, loginOkID1, loginOkID2 int32, serverID byte) int {
	if len(dst) < RequestServerLoginSize {
		panic(fmt.Sprintf("protocol: WriteRequestServerLogin: dst длиной %d байт < RequestServerLoginSize=%d", len(dst), RequestServerLoginSize))
	}
	dst[0] = OpRequestServerLogin
	WriteD(dst[1:], loginOkID1)
	WriteD(dst[5:], loginOkID2)
	dst[9] = serverID
	return RequestServerLoginSize
}

// WriteAuthGameGuard пишет пробу GameGuard (C→LS): sessionID и 16 нулевых
// байт зарезервированных данных. Возвращает число записанных байт
// (AuthGameGuardSize).
func WriteAuthGameGuard(dst []byte, sessionID int32) int {
	if len(dst) < AuthGameGuardSize {
		panic(fmt.Sprintf("protocol: WriteAuthGameGuard: dst длиной %d байт < AuthGameGuardSize=%d", len(dst), AuthGameGuardSize))
	}
	dst[0] = OpAuthGameGuard
	WriteD(dst[1:], sessionID)
	clear(dst[5:21])
	return AuthGameGuardSize
}
