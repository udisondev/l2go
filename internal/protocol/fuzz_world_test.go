package protocol

import (
	"testing"
	"unicode/utf16"
)

// FuzzWorldViews — представления стационарной фазы (P3.5): после успешного
// конструктора вызываются все геттеры, включая условные ветки (Target при
// любом type); после неуспешного — только факт ok=false. Паника недопустима
// на любых байтах (doc.go: читатели не паникуют на недоверенных буферах).
// Раундтрип строковых полей — с капом суммарной длины 4 КиБ (домен кадра —
// 8 КиБ; вне домена фаззер деградирует в memcpy).
func FuzzWorldViews(f *testing.F) {
	seeds := [][]byte{
		{byte(moveToLocation), 1, 0, 0, 0, 2, 0, 0, 0, 3, 0, 0, 0, 4, 0, 0, 0, 5, 0, 0, 0, 6, 0, 0, 0, 7, 0, 0, 0},
		{byte(validatePosition), 0xFF, 0xFF, 0xFF, 0x7F, 0, 0, 0, 0x80, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12},
		{byte(cannotMoveAnymore), 0, 0, 0, 0},
		{byte(say2), 'h', 0, 'i', 0, 0, 0, 1, 0, 0, 0},
		{byte(say2), 'p', 0, 0, 0, 2, 0, 0, 0, 'T', 0, 0, 0},
		{byte(characterCreate), 'N', 0, 'm', 0, 0, 0, 1, 0, 0, 0, 0},
		{byte(characterDelete), 7, 0, 0, 0},
		{byte(newCharacter)},
		{byte(enterWorld)},
		{byte(charMoveToLocation), 1, 2, 3, 4, 5, 6, 7, 8},
		{byte(stopMove), 1, 2, 3, 4, 5},
		{byte(teleportToLocation), 1, 2, 3, 4, 5, 6},
		{byte(validateLocation), 1, 2, 3, 4, 5},
		{byte(deleteObject), 1, 2, 3, 4, 5, 6, 7, 8},
		{byte(creatureSay), 1, 0, 0, 0, 0, 0, 0, 0, 'a', 0, 0, 0, 'b', 0, 0, 0},
		{byte(systemMessage), 34, 0, 0, 0, 0, 0, 0, 0},
		{byte(itemList), 0, 0, 0, 0},
		{byte(skillList), 0, 0, 0, 0},
		{byte(shortCutInit), 0, 0, 0, 0},
		{byte(actionFail)},
		{byte(charInfo), 0, 'x'},
		{byte(userInfo), 0, 'y'},
		{byte(npcInfo), 0, 'z'},
		{byte(charTemplates), 1, 0, 0, 0},
		{byte(charCreateOk), 1, 0, 0, 0},
		{byte(charCreateFail), 2, 0, 0, 0},
		{byte(charDeleteFail), 1, 0, 0, 0},
		charInfoSeed(), userInfoSeed(), npcInfoSeed(),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		fuzzOneView(t, b)
	})
}

// stringBudget — кап домена строковых полей фаззера.
const stringBudget = 4096

// Сиды большой тройки — полные кадры из писателей: ветка «все геттеры после
// успешного конструктора» исполняется с первого прогона, а не ждёт, пока
// фаззер сам выстроит терминированные строки с хвостом.
func charInfoSeed() []byte {
	dst := make([]byte, CharInfoSize(tCharInfo))
	WriteCharInfo(dst, tCharInfo)
	return dst
}

func userInfoSeed() []byte {
	dst := make([]byte, UserInfoSize(tUserInfo))
	WriteUserInfo(dst, tUserInfo)
	return dst
}

func npcInfoSeed() []byte {
	dst := make([]byte, NpcInfoSize(tNpcInfo))
	WriteNpcInfo(dst, tNpcInfo)
	return dst
}

func fuzzOneView(t *testing.T, b []byte) {
	t.Helper()
	if v, ok := NewMoveToLocationView(b); ok {
		_, _, _, _, _, _, _ = v.TargetX(), v.TargetY(), v.TargetZ(), v.OriginX(), v.OriginY(), v.OriginZ(), v.MovementMode()
	}
	if v, ok := NewValidatePositionView(b); ok {
		_, _, _, _, _ = v.X(), v.Y(), v.Z(), v.Heading(), v.VehicleID()
	}
	if v, ok := NewCannotMoveAnymoreView(b); ok {
		_, _, _, _ = v.X(), v.Y(), v.Z(), v.Heading()
	}
	if v, ok := NewSay2View(b); ok {
		_, _ = v.Text()
		_ = v.Type()
		_, _ = v.Target() // условный хвост — при любом type
	}
	if v, ok := NewCharacterCreateView(b); ok {
		_, _ = v.Name()
		_, _, _ = v.Race(), v.Sex(), v.ClassID()
		_, _, _, _, _, _ = v.Int(), v.Str(), v.Con(), v.Men(), v.Dex(), v.Wit()
		_, _, _ = v.HairStyle(), v.HairColor(), v.Face()
	}
	_, _ = NewCharacterDeleteView(b) // конструктор-онли пакет: геттеров нет, вызов — клеймо «без паники»
	_, _ = NewNewCharacterView(b)    // конструктор-онли пакет: геттеров нет, вызов — клеймо «без паники»
	_, _ = NewEnterWorldView(b)      // конструктор-онли пакет: геттеров нет, вызов — клеймо «без паники»
	if v, ok := NewCharMoveToLocationView(b); ok {
		_ = v.ObjID()
		_, _, _ = v.DstX(), v.DstY(), v.DstZ()
		_, _, _ = v.X(), v.Y(), v.Z()
	}
	if v, ok := NewStopMoveView(b); ok {
		_, _, _, _, _ = v.ObjID(), v.X(), v.Y(), v.Z(), v.Heading()
	}
	if v, ok := NewTeleportToLocationView(b); ok {
		_, _, _, _, _, _ = v.ObjID(), v.X(), v.Y(), v.Z(), v.Flags(), v.Heading()
	}
	if v, ok := NewValidateLocationView(b); ok {
		_, _, _, _, _ = v.ObjID(), v.X(), v.Y(), v.Z(), v.Heading()
	}
	if v, ok := NewDeleteObjectView(b); ok {
		_ = v.ObjID()
	}
	if v, ok := NewCreatureSayView(b); ok {
		_, _ = v.SenderObjID(), v.Type()
		_, _ = v.SenderName()
		_, _ = v.Text()
	}
	if v, ok := NewSystemMessageView(b); ok {
		_, _ = v.ID(), v.ParamCount()
	}
	if v, ok := NewItemListView(b); ok {
		_, _ = v.ShowWindow(), v.Count()
	}
	if v, ok := NewSkillListView(b); ok {
		_ = v.Count()
	}
	if v, ok := NewShortCutInitView(b); ok {
		_ = v.Count()
	}
	_, _ = NewActionFailedView(b)             // конструктор-онли пакет: геттеров нет, вызов — клеймо «без паники»
	if v, ok := NewCharTemplatesView(b); ok { // недоверенный счётчик: границы Template
		_ = v.Count()
		_, _ = v.Template(0)
		_, _ = v.Template(v.Count() - 1)
		_, _ = v.Template(v.Count())
	}
	if v, ok := NewCharCreateOkView(b); ok {
		_ = v.Ok()
	}
	if v, ok := NewCharCreateFailView(b); ok {
		_ = v.Reason()
	}
	if v, ok := NewCharDeleteFailView(b); ok {
		_ = v.Reason()
	}
	if v, ok := NewCharInfoView(b); ok {
		_, _, _ = v.X(), v.Y(), v.Z()
		_, _ = v.ObjID(), v.Race()
		_, _ = v.Name()
		_, _ = v.RunSpd(), v.ClassID()
		_ = v.Heading()
	}
	if v, ok := NewUserInfoView(b); ok {
		_, _, _ = v.X(), v.Y(), v.Z()
		_ = v.ObjID()
		_, _ = v.Name()
		_ = v.Level()
		_, _ = v.CurHP()
		_, _ = v.CurMP()
		_ = v.ClassID()
	}
	if v, ok := NewNpcInfoView(b); ok {
		_, _ = v.ObjID(), v.DisplayID()
		_, _, _ = v.X(), v.Y(), v.Z()
		_, _ = v.Name()
		_, _ = v.Title()
		_ = v.Heading()
	}
	// Раундтрип Say2: писатель ↔ представление в пределах строкового капа
	// (текст пишется дважды: поле текста и поле адресата whisper). Кап
	// проверяется до декода — мегабайтные входы не проходят utf16DecodeLossy.
	if len(b) < stringBudget {
		text := utf16DecodeLossy(b)
		if 2*LenS(text) < stringBudget {
			dst := make([]byte, Say2Size(text, ChatWhisper, text))
			n := WriteSay2(dst, text, ChatWhisper, text)
			if v, ok := NewSay2View(dst[:n]); ok {
				if got, _ := v.Text(); got != text {
					t.Fatalf("Say2 раундтрип: Text = %q; want %q", got, text)
				}
			} else {
				t.Fatalf("Say2 раундтрип: конструктор отклонил собственный кадр %q", text)
			}
		}
	}
}

// utf16DecodeLossy — обратная сторона WriteS: байты как юниты UTF-16LE
// (непарный хвост отбрасывается, терминатор режет поле), декод без паники.
func utf16DecodeLossy(b []byte) string {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u := uint16(b[i]) | uint16(b[i+1])<<8
		if u == 0 {
			break
		}
		units = append(units, u)
	}
	if len(units) == 0 {
		return ""
	}
	return string(utf16.Decode(units))
}
