// Пакеты game-хендшейка и минимального входа Interlude: писатели GameServer→C
// и C→GameServer. Порт udisondev/interlude@34fe4c86 pkg/packet/{server,client};
// формат сверен с каноном Mobius master CT_0_Interlude (43ac8878) — расхождения
// источника (KeyPacket без хвоста, CharacterSelect без хвоста, CharSelected
// короче на статовый блок) закрыты по канону, список — в реестре задачи P1.4.

package protocol

import "fmt"

// Опкоды типизируемых пакетов хендшейка (значения — из каталога).
const (
	protocolVersion = 0x00 // PROTOCOL_VERSION, C→GS
	authLogin       = 0x08 // AUTH_LOGIN, C→GS
	logout          = 0x09 // LOGOUT, C→GS
	characterSelect = 0x0D // CHARACTER_SELECT, C→GS
	keyPacket       = 0x00 // KEY_PACKET, GS→C
	charSelectInfo  = 0x13 // CHAR_SELECT_INFO, GS→C
	gsLoginFail     = 0x14 // LOGIN_FAIL, GS→C
	charSelected    = 0x15 // CHAR_SELECTED, GS→C
)

// ProtocolVersionInterlude — версия протокола, которую шлёт клиент Interlude.
const ProtocolVersionInterlude int32 = 746

// Фиксированные размеры пакетов (с опкодом).
const (
	ProtocolVersionSize = 5
	LogoutSize          = 1
	CharacterSelectSize = 19
	KeyPacketSize       = 23
	GSLoginFailSize     = 5
)

// GSLoginFailReason — код причины LoginFail game-стороны (int32, шире
// байтовой LS-формы). Перенос Mobius master CT_0_Interlude LoginFail.
type GSLoginFailReason int32

// Коды причин LoginFail game-стороны.
const (
	GSReasonNoText                                     GSLoginFailReason = 0
	GSReasonSystemErrorLoginLater                      GSLoginFailReason = 1
	GSReasonPasswordDoesNotMatchThisAccount            GSLoginFailReason = 2
	GSReasonPasswordDoesNotMatchThisAccount2           GSLoginFailReason = 3
	GSReasonAccessFailedTryLater                       GSLoginFailReason = 4
	GSReasonIncorrectAccountInfoContactCustomerSupport GSLoginFailReason = 5
	GSReasonAccessFailedTryLater2                      GSLoginFailReason = 6
	GSReasonAccountAlreadyInUse                        GSLoginFailReason = 7
	GSReasonAccessFailedTryLater3                      GSLoginFailReason = 8
	GSReasonAccessFailedTryLater4                      GSLoginFailReason = 9
	GSReasonAccessFailedTryLater5                      GSLoginFailReason = 10
)

// CharSelectionEntry — запись списка персонажей CharSelectionInfo; аргумент
// писателя и результат геттера представления (раундтрип одного wire-формата).
type CharSelectionEntry struct {
	Name               string
	CharID             int32
	LoginName          string
	SessionID          int32
	ClanID             int32
	Sex                int32
	Race               int32
	BaseClassID        int32
	CurHP              float64
	CurMP              float64
	SP                 int32
	Exp                int64
	Level              int32
	Karma              int32
	PaperdollObjectIDs [17]int32
	PaperdollItemIDs   [17]int32
	HairStyle          int32
	HairColor          int32
	Face               int32
	MaxHP              float64
	MaxMP              float64
	DeleteTime         int32 // секунды до удаления, 0 — не запланирован
	ClassID            int32
	Enchant            byte // кап 127 — по канону
	AugmentationID     int32
}

// CharSelectedData — поля пакета CharSelected (аргумент писателя).
type CharSelectedData struct {
	Name      string
	CharID    int32
	Title     string
	SessionID int32
	ClanID    int32
	Sex       int32
	Race      int32
	ClassID   int32
	X, Y, Z   int32
	CurHP     float64
	CurMP     float64
	SP        int32
	Exp       int64
	Level     int32
	Karma     int32
	PkKills   int32
	INT       int32
	STR       int32
	CON       int32
	MEN       int32
	DEX       int32
	WIT       int32
	GameTime  int32 // пишется как GameTime % (24*60) — сброс на 24-м часу канона
}

// WriteProtocolVersion пишет пакет ProtocolVersion (C→GS) с версией протокола.
// Возвращает число записанных байт (ProtocolVersionSize).
func WriteProtocolVersion(dst []byte, version int32) int {
	if len(dst) < ProtocolVersionSize {
		panic(fmt.Sprintf("protocol: WriteProtocolVersion: dst длиной %d байт < ProtocolVersionSize=%d", len(dst), ProtocolVersionSize))
	}
	dst[0] = protocolVersion
	WriteD(dst[1:], version)
	return ProtocolVersionSize
}

// AuthLoginSize возвращает размер WriteAuthLogin для аккаунта — sizing и
// запись одним расчётом.
func AuthLoginSize(account string) int { return 1 + LenS(account) + 16 }

// WriteAuthLogin пишет пакет AuthLogin (C→GS): аккаунт и четыре ключа сессии
// (playKey2, playKey1, loginKey1, loginKey2 — порядок канона). Возвращает
// число записанных байт.
func WriteAuthLogin(dst []byte, account string, playKey2, playKey1, loginKey1, loginKey2 int32) int {
	size := AuthLoginSize(account)
	if len(dst) < size {
		panic(fmt.Sprintf("protocol: WriteAuthLogin: dst длиной %d байт < AuthLoginSize=%d", len(dst), size))
	}
	dst[0] = authLogin
	off := 1 + WriteS(dst[1:], account)
	WriteD(dst[off:], playKey2)
	WriteD(dst[off+4:], playKey1)
	WriteD(dst[off+8:], loginKey1)
	WriteD(dst[off+12:], loginKey2)
	return size
}

// WriteLogout пишет маркер-пакет Logout (C→GS) — только опкод. Возвращает
// LogoutSize.
func WriteLogout(dst []byte) int {
	if len(dst) < LogoutSize {
		panic(fmt.Sprintf("protocol: WriteLogout: dst длиной %d байт < LogoutSize=%d", len(dst), LogoutSize))
	}
	dst[0] = logout
	return LogoutSize
}

// WriteCharacterSelect пишет пакет CharacterSelect (C→GS): слот и хвост
// канона (H + 3×D). Возвращает число записанных байт (CharacterSelectSize).
func WriteCharacterSelect(dst []byte, charSlot int32) int {
	if len(dst) < CharacterSelectSize {
		panic(fmt.Sprintf("protocol: WriteCharacterSelect: dst длиной %d байт < CharacterSelectSize=%d", len(dst), CharacterSelectSize))
	}
	dst[0] = characterSelect
	WriteD(dst[1:], charSlot)
	WriteH(dst[5:], 0)
	WriteD(dst[7:], 0)
	WriteD(dst[11:], 0)
	WriteD(dst[15:], 0)
	return CharacterSelectSize
}

// WriteKeyPacket пишет пакет KeyPacket (GS→C): результат приёма протокола,
// 8 случайных байт ключа, флаг шифрования, ID сервера и хвост канона
// (C(1) + obfuscation D(0)). Возвращает число записанных байт (KeyPacketSize).
func WriteKeyPacket(dst []byte, result byte, key []byte, encryption bool, serverID int32) int {
	if len(dst) < KeyPacketSize {
		panic(fmt.Sprintf("protocol: WriteKeyPacket: dst длиной %d байт < KeyPacketSize=%d", len(dst), KeyPacketSize))
	}
	if len(key) != 8 {
		panic(fmt.Sprintf("protocol: WriteKeyPacket: ключ %d байт; want 8 (случайная половина)", len(key)))
	}
	dst[0] = keyPacket
	dst[1] = result
	copy(dst[2:], key)
	WriteD(dst[10:], int32(boolByte(encryption)))
	WriteD(dst[14:], serverID)
	dst[18] = 1
	WriteD(dst[19:], 0)
	return KeyPacketSize
}

// WriteGSLoginFail пишет пакет LoginFail game-стороны (GS→C) с int32-кодом
// причины. Возвращает число записанных байт (GSLoginFailSize).
func WriteGSLoginFail(dst []byte, reason GSLoginFailReason) int {
	if len(dst) < GSLoginFailSize {
		panic(fmt.Sprintf("protocol: WriteGSLoginFail: dst длиной %d байт < GSLoginFailSize=%d", len(dst), GSLoginFailSize))
	}
	dst[0] = gsLoginFail
	WriteD(dst[1:], int32(reason))
	return GSLoginFailSize
}

// CharSelectionInfoSize возвращает размер WriteCharSelectionInfo — sizing и
// запись одним расчётом.
func CharSelectionInfoSize(chars []CharSelectionEntry) int {
	size := 5 // опкод + счётчик
	for _, c := range chars {
		size += LenS(c.Name) + 4 + LenS(c.LoginName) + 293
	}
	return size
}

// WriteCharSelectionInfo пишет пакет CharSelectionInfo (GS→C): счётчик и
// записи персонажей; activeSlot — индекс последнего выбранного (D 1/0 по
// записи). Возвращает число записанных байт.
func WriteCharSelectionInfo(dst []byte, chars []CharSelectionEntry, activeSlot int) int {
	size := CharSelectionInfoSize(chars)
	if len(dst) < size {
		panic(fmt.Sprintf("protocol: WriteCharSelectionInfo: dst длиной %d байт < CharSelectionInfoSize=%d", len(dst), size))
	}
	dst[0] = charSelectInfo
	WriteD(dst[1:], int32(len(chars)))
	off := 5
	for i, c := range chars {
		off += WriteS(dst[off:], c.Name)
		WriteD(dst[off:], c.CharID)
		off += 4
		off += WriteS(dst[off:], c.LoginName)
		WriteD(dst[off:], c.SessionID)
		off += 4
		WriteD(dst[off:], c.ClanID)
		off += 4
		WriteD(dst[off:], 0) // builder level
		off += 4
		WriteD(dst[off:], c.Sex)
		off += 4
		WriteD(dst[off:], c.Race)
		off += 4
		WriteD(dst[off:], c.BaseClassID)
		off += 4
		WriteD(dst[off:], 1) // GameServerName
		off += 4
		WriteD(dst[off:], 0) // X
		WriteD(dst[off+4:], 0)
		WriteD(dst[off+8:], 0)
		off += 12
		WriteF(dst[off:], c.CurHP)
		off += 8
		WriteF(dst[off:], c.CurMP)
		off += 8
		WriteD(dst[off:], c.SP)
		off += 4
		WriteQ(dst[off:], c.Exp)
		off += 8
		WriteD(dst[off:], c.Level)
		off += 4
		WriteD(dst[off:], c.Karma)
		off += 4
		clear(dst[off : off+36]) // 9 зарезервированных D
		off += 36
		for j, id := range c.PaperdollObjectIDs {
			WriteD(dst[off+4*j:], id)
		}
		off += 68
		for j, id := range c.PaperdollItemIDs {
			WriteD(dst[off+4*j:], id)
		}
		off += 68
		WriteD(dst[off:], c.HairStyle)
		WriteD(dst[off+4:], c.HairColor)
		WriteD(dst[off+8:], c.Face)
		off += 12
		WriteF(dst[off:], c.MaxHP)
		off += 8
		WriteF(dst[off:], c.MaxMP)
		off += 8
		WriteD(dst[off:], c.DeleteTime)
		off += 4
		WriteD(dst[off:], c.ClassID)
		off += 4
		if i == activeSlot {
			WriteD(dst[off:], 1)
		} else {
			WriteD(dst[off:], 0)
		}
		off += 4
		dst[off] = min(c.Enchant, 127) // кап канона
		off++
		WriteD(dst[off:], c.AugmentationID)
		off += 4
	}
	return off
}

// CharSelectedSize возвращает размер WriteCharSelected — sizing и запись
// одним расчётом.
func CharSelectedSize(d CharSelectedData) int {
	return 1 + LenS(d.Name) + 4 + LenS(d.Title) + 292
}

// WriteCharSelected пишет пакет CharSelected (GS→C) — подтверждение входа:
// поля персонажа и статовый блок канона (6 статов, 30 нулей paperdoll,
// повторный classId, 12 нулей). Возвращает число записанных байт.
func WriteCharSelected(dst []byte, d CharSelectedData) int {
	size := CharSelectedSize(d)
	if len(dst) < size {
		panic(fmt.Sprintf("protocol: WriteCharSelected: dst длиной %d байт < CharSelectedSize=%d", len(dst), size))
	}
	dst[0] = charSelected
	off := 1 + WriteS(dst[1:], d.Name)
	WriteD(dst[off:], d.CharID)
	off += 4
	off += WriteS(dst[off:], d.Title)
	WriteD(dst[off:], d.SessionID)
	off += 4
	WriteD(dst[off:], d.ClanID)
	off += 4
	WriteD(dst[off:], 0) // unknown канона
	off += 4
	WriteD(dst[off:], d.Sex)
	off += 4
	WriteD(dst[off:], d.Race)
	off += 4
	WriteD(dst[off:], d.ClassID)
	off += 4
	WriteD(dst[off:], 1) // active
	off += 4
	WriteD(dst[off:], d.X)
	WriteD(dst[off+4:], d.Y)
	WriteD(dst[off+8:], d.Z)
	off += 12
	WriteF(dst[off:], d.CurHP)
	off += 8
	WriteF(dst[off:], d.CurMP)
	off += 8
	WriteD(dst[off:], d.SP)
	off += 4
	WriteQ(dst[off:], d.Exp)
	off += 8
	WriteD(dst[off:], d.Level)
	off += 4
	WriteD(dst[off:], d.Karma)
	off += 4
	WriteD(dst[off:], d.PkKills)
	off += 4
	for _, stat := range [...]int32{d.INT, d.STR, d.CON, d.MEN, d.DEX, d.WIT} {
		WriteD(dst[off:], stat)
		off += 4
	}
	clear(dst[off : off+120]) // 30 нулевых D paperdoll
	off += 120
	WriteD(dst[off:], 0)
	WriteD(dst[off+4:], 0)
	WriteD(dst[off+8:], d.GameTime%(24*60))
	WriteD(dst[off+12:], 0)
	off += 16
	WriteD(dst[off:], d.ClassID) // повторно — по канону
	off += 4
	clear(dst[off : off+48]) // 12 нулевых D
	off += 48
	return off
}
