// Пакеты флоу создания персонажа. C→GS (клиент): CharacterCreate,
// CharacterDelete, NewCharacter (маркер экрана создания — триггер CharTemplates);
// GS→C (сервер): CharTemplates (NewCharacterSuccess канона), CharCreateOk/Fail,
// CharDeleteFail. Порты L2J Mobius CT_0_Interlude @43ac8878:
// clientpackets/{CharacterCreate,CharacterDelete}.java, serverpackets/
// {NewCharacterSuccess,CharCreateOk,CharCreateFail,CharDeleteFail}.java.

package protocol

// Опкоды (связь с каталогом — TestConstantsMatchCatalog).
const (
	characterCreate = 0x0B
	characterDelete = 0x0C
	newCharacter    = 0x0E
	charTemplates   = 0x17
	charCreateOk    = 0x19
	charCreateFail  = 0x1A
	charDeleteFail  = 0x24
)

// Публичные опкоды C→GS фазы создания — белый список шлюза (P3.6);
// имена и опкоды GS→C-ответов для диспетчера клиента.
const (
	OpCCharacterCreate = characterCreate
	OpCCharacterDelete = characterDelete
	OpCNewCharacter    = newCharacter

	OpCharTemplates  = charTemplates
	OpCharCreateOk   = charCreateOk
	OpCharCreateFail = charCreateFail

	NameNewCharacter    = "NEW_CHARACTER"
	NameCharTemplates   = "CHAR_TEMPLATES"
	NameCharacterCreate = "CHARACTER_CREATE"
	NameCharCreateOk    = "CHAR_CREATE_OK"
	NameCharCreateFail  = "CHAR_CREATE_FAIL"
)

// CharCreateFailReason — причины отказа создания (CharCreateFail.java канона).
type CharCreateFailReason int32

// Причины CharCreateFail; имена и значения — порт CharCreateFail.java @43ac8878.
const (
	CharCreateReasonCreationFailed    CharCreateFailReason = 0x00
	CharCreateReasonTooManyCharacters CharCreateFailReason = 0x01
	CharCreateReasonNameAlreadyExists CharCreateFailReason = 0x02
	CharCreateReasonNameTooLong       CharCreateFailReason = 0x03
	CharCreateReasonIncorrectName     CharCreateFailReason = 0x04
	CharCreateReasonCreateNotAllowed  CharCreateFailReason = 0x05
	CharCreateReasonChooseAnotherSrvr CharCreateFailReason = 0x06
)

// CharDeleteFailReason — причины отказа удаления (CharDeleteFail.java канона).
// Удаление персонажа — анти-скоуп: сервер всегда отвечает отказом с причиной
// DeletionFailed.
type CharDeleteFailReason int32

// Причины CharDeleteFail; порт CharDeleteFail.java @43ac8878.
const (
	CharDeleteReasonDeletionFailed CharDeleteFailReason = 1
	CharDeleteReasonClanMember     CharDeleteFailReason = 2
	CharDeleteReasonClanLeader     CharDeleteFailReason = 3
)

// CharacterCreateData — поля кадра CharacterCreate (C→GS): имя и 12×D в
// порядке канона. Отправитель — l2client; сервер разбирает представлением.
// Передаётся по значению: событийная частота, стек-копия без алиасинга.
type CharacterCreateData struct {
	Name      string
	Race      int32
	Sex       int32
	ClassID   int32
	Int       int32
	Str       int32
	Con       int32
	Men       int32
	Dex       int32
	Wit       int32
	HairStyle int32
	HairColor int32
	Face      int32
}

// CharacterCreateSize — размер кадра CharacterCreate с опкодом.
func CharacterCreateSize(d CharacterCreateData) int { return 1 + LenS(d.Name) + 12*4 }

// WriteCharacterCreate пишет кадр CharacterCreate (C→GS).
func WriteCharacterCreate(dst []byte, d CharacterCreateData) int {
	if len(dst) < CharacterCreateSize(d) {
		panic(shortDst("WriteCharacterCreate", len(dst), CharacterCreateSize(d)))
	}
	dst[0] = byte(characterCreate)
	off := 1 + WriteS(dst[1:], d.Name)
	writeD := func(v int32) {
		WriteD(dst[off:], v)
		off += 4
	}
	writeD(d.Race)
	writeD(d.Sex)
	writeD(d.ClassID)
	writeD(d.Int)
	writeD(d.Str)
	writeD(d.Con)
	writeD(d.Men)
	writeD(d.Dex)
	writeD(d.Wit)
	writeD(d.HairStyle)
	writeD(d.HairColor)
	writeD(d.Face)
	return off
}

// CharacterDeleteSize — размер кадра CharacterDelete с опкодом.
const CharacterDeleteSize = 5

// WriteCharacterDelete пишет кадр CharacterDelete (C→GS): слот персонажа.
func WriteCharacterDelete(dst []byte, charSlot int32) int {
	if len(dst) < CharacterDeleteSize {
		panic(shortDst("WriteCharacterDelete", len(dst), CharacterDeleteSize))
	}
	dst[0] = byte(characterDelete)
	WriteD(dst[1:], charSlot)
	return CharacterDeleteSize
}

// NewCharacterSize — размер кадра NewCharacter: маркер из одного опкода.
const NewCharacterSize = 1

// WriteNewCharacter пишет кадр NewCharacter (C→GS): пустой маркер запроса
// экрана создания (ответ — CharTemplates).
func WriteNewCharacter(dst []byte) int {
	if len(dst) < NewCharacterSize {
		panic(shortDst("WriteNewCharacter", len(dst), NewCharacterSize))
	}
	dst[0] = byte(newCharacter)
	return NewCharacterSize
}

// CharTemplate — запись шаблона в кадре CharTemplates; числа новичка Human
// Fighter — константы канона HumanFighter.xml @43ac8878 (дубль
// persist.HumanFighter осознан, синхронизация — атрибуцией).
type CharTemplate struct {
	Race    int32
	ClassID int32
	Str     int32
	Dex     int32
	Con     int32
	Int     int32
	Wit     int32
	Men     int32
}

// charTemplateWire — D на запись: race, classId и 6×{0x46, stat, 0x0A}
// (NewCharacterSuccess.java: границы 0x46/0x0A визуализации слайдеров).
const charTemplateWire = 20 * 4

// CharTemplatesSize — размер кадра CharTemplates с опкодом.
func CharTemplatesSize(count int) int { return 1 + 4 + count*charTemplateWire }

// WriteCharTemplates пишет кадр CharTemplates (GS→C).
func WriteCharTemplates(dst []byte, templates []CharTemplate) int {
	if len(dst) < CharTemplatesSize(len(templates)) {
		panic(shortDst("WriteCharTemplates", len(dst), CharTemplatesSize(len(templates))))
	}
	dst[0] = byte(charTemplates)
	WriteD(dst[1:], int32(len(templates)))
	off := 5
	for _, t := range templates {
		WriteD(dst[off:], t.Race)
		WriteD(dst[off+4:], t.ClassID)
		stats := [...]int32{t.Str, t.Dex, t.Con, t.Int, t.Wit, t.Men}
		for i, s := range stats {
			WriteD(dst[off+8+i*12:], 0x46)
			WriteD(dst[off+12+i*12:], s)
			WriteD(dst[off+16+i*12:], 0x0A)
		}
		off += charTemplateWire
	}
	return off
}

// CharCreateOkSize — размер кадра CharCreateOk с опкодом.
const CharCreateOkSize = 5

// WriteCharCreateOk пишет кадр CharCreateOk (GS→C); поле-константа 1 канона.
func WriteCharCreateOk(dst []byte) int {
	if len(dst) < CharCreateOkSize {
		panic(shortDst("WriteCharCreateOk", len(dst), CharCreateOkSize))
	}
	dst[0] = byte(charCreateOk)
	WriteD(dst[1:], 1)
	return CharCreateOkSize
}

// CharCreateFailSize — размер кадра CharCreateFail с опкодом.
const CharCreateFailSize = 5

// WriteCharCreateFail пишет кадр CharCreateFail (GS→C) с причиной отказа.
func WriteCharCreateFail(dst []byte, reason CharCreateFailReason) int {
	if len(dst) < CharCreateFailSize {
		panic(shortDst("WriteCharCreateFail", len(dst), CharCreateFailSize))
	}
	dst[0] = byte(charCreateFail)
	WriteD(dst[1:], int32(reason))
	return CharCreateFailSize
}

// CharDeleteFailSize — размер кадра CharDeleteFail с опкодом.
const CharDeleteFailSize = 5

// WriteCharDeleteFail пишет кадр CharDeleteFail (GS→C): отказ удаления.
func WriteCharDeleteFail(dst []byte, reason CharDeleteFailReason) int {
	if len(dst) < CharDeleteFailSize {
		panic(shortDst("WriteCharDeleteFail", len(dst), CharDeleteFailSize))
	}
	dst[0] = byte(charDeleteFail)
	WriteD(dst[1:], int32(reason))
	return CharDeleteFailSize
}
