// Package persist — файловый персист скелета: аккаунты (login-процесс) и
// персонажи (persist-актор game-процесса, письма через транспорт). Владение
// разделено по каталогам: единственный писатель файла — единственный владелец
// каталога. Формат — детерминированный JSON с конвертом версии схемы и
// чексуммой; миграций нет (персональный сервер, пересоздание допустимо).
package persist

import (
	"fmt"
	"regexp"
)

// maxSlot — верхняя граница слота персонажа: 7 на аккаунт (канон).
const maxSlot = 6

var (
	// Логин аккаунта: 3–14 символов alnum, хранится нормализованным к нижнему
	// регистру (канон); алфавит замкнут относительно нормализации и не содержит
	// разделителей путей — имя файла безопасно применением regex у владельца.
	loginRe = regexp.MustCompile(`^[a-z0-9]{3,14}$`)
	// Имя персонажа: 1–16 alnum (канон).
	nameRe = regexp.MustCompile(`^[A-Za-z0-9]{1,16}$`)
)

// NormalizeLogin нормализует и валидирует логин; невалидный — ошибка.
func NormalizeLogin(login string) (string, error) {
	lowered := lowercaseASCII(login)
	if !loginRe.MatchString(lowered) {
		return "", fmt.Errorf("persist: логин %q вне домена (3–14 alnum)", login)
	}
	return lowered, nil
}

// lowercaseASCII приводит alnum-алфавит к нижнему регистру без unicode-таблиц:
// не-ASCII отсекается regex'ом.
func lowercaseASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// MaxNameLen — верхняя граница домена имени (канон: 16 символов).
const MaxNameLen = 16

// Коды отказов операций — стабильный контракт для потребителей ответов
// (причины UI канона маппятся по коду, не по тексту ошибки).
const (
	CodeNameInvalid = "name_invalid"
	CodeNameTaken   = "name_taken"
	CodeCharLimit   = "char_limit"
	CodeAppearance  = "appearance"
	CodeLogin       = "login"
	CodeIO          = "io"
)

// ValidName сообщает, что имя персонажа в домене канона (1–16 alnum).
func ValidName(name string) bool {
	return nameRe.MatchString(name)
}

// ValidateAppearance проверяет диапазоны внешности канона: face 0–2,
// hairColor 0–3, hairStyle 0–4 (мужчина) / 0–6 (женщина).
func ValidateAppearance(sex, hairStyle, hairColor, face int) error {
	if sex != 0 && sex != 1 {
		return fmt.Errorf("persist: пол %d вне домена (0/1)", sex)
	}
	top := 4
	if sex == 1 {
		top = 6
	}
	if hairStyle < 0 || hairStyle > top {
		return fmt.Errorf("persist: hairStyle %d вне домена (0–%d, пол %d)", hairStyle, top, sex)
	}
	if hairColor < 0 || hairColor > 3 {
		return fmt.Errorf("persist: hairColor %d вне домена (0–3)", hairColor)
	}
	if face < 0 || face > 2 {
		return fmt.Errorf("persist: face %d вне домена (0–2)", face)
	}
	return nil
}

// Template — константы новичка Human Fighter.
// Порт канона: L2J-Mobius CT0 Interlude,
// dist/game/data/stats/players/templates/StartingClass/HumanFighter.xml
// (первая точка создания, скорости, статы уровня 1; attack speeds и
// коллизии — CharInfo-потребитель фазы 3.8, тот же файл).
type Template struct {
	ClassID int
	Race    int
	Str     int
	Dex     int
	Con     int
	Int     int
	Wit     int
	Men     int
	BaseHP  int
	BaseMP  int
	BaseCP  int
	WalkSpd int
	RunSpd  int
	// BasePAtkSpd/BaseMAtkSpd/SwimSpd — скорости кадров UserInfo/CharInfo
	// (basePAtkSpd=300, slowSwim/fastSwim=50 — файл шаблона; baseMAtkSpd=333
	// — дефолт CreatureTemplate.java кода Mobius, в XML узла нет).
	BasePAtkSpd int
	BaseMAtkSpd int
	SwimSpd     int
	// CollisionR/CollisionH — габариты male-модели (collisionMale 9/23;
	// female 8/23.5 — с появлением выбора; мультипликаторы — плейсхолдеры
	// до формул производных статов).
	CollisionR float64
	CollisionH float64
	StartX     int
	StartY     int
	StartZ     int
}

// HumanFighter — единственный шаблон создания фазы 3.
var HumanFighter = Template{
	ClassID: 0,
	Race:    0,
	Str:     40,
	Dex:     30,
	Con:     43,
	Int:     21,
	Wit:     11,
	Men:     25,
	BaseHP:  80,
	BaseMP:  30,
	BaseCP:  32,
	WalkSpd: 80,
	RunSpd:  115,

	BasePAtkSpd: 300,
	BaseMAtkSpd: 333,
	SwimSpd:     50,
	CollisionR:  9,
	CollisionH:  23,

	StartX: -71338,
	StartY: 258271,
	StartZ: -3104,
}

// AccountRecord — запись файла accounts/<login>.json.
type AccountRecord struct {
	Login       string `json:"login"`
	Salt        []byte `json:"salt"`
	Hash        []byte `json:"hash"`
	CreatedUnix int64  `json:"created_unix"`
	Banned      bool   `json:"banned"`
}

// CharRecord — персонаж; файл chars/<account>.json — список записей.
type CharRecord struct {
	Account      string `json:"account"`
	Slot         int    `json:"slot"`
	Name         string `json:"name"`
	ClassID      int    `json:"class_id"`
	Race         int    `json:"race"`
	Sex          int    `json:"sex"`
	HairStyle    int    `json:"hair_style"`
	HairColor    int    `json:"hair_color"`
	Face         int    `json:"face"`
	X            int    `json:"x"`
	Y            int    `json:"y"`
	Z            int    `json:"z"`
	Heading      int    `json:"heading"`
	Level        int    `json:"level"`
	Exp          int64  `json:"exp"`
	HP           int    `json:"hp"`
	MP           int    `json:"mp"`
	CreatedUnix  int64  `json:"created_unix"`
	LastSeenUnix int64  `json:"last_seen_unix"`
}

// validateCharRecord проверяет запись на применении у владельца: домены имён,
// диапазоны внешности, единственный шаблон фазы, слот, уровень и пулы.
func validateCharRecord(r CharRecord) error {
	if _, err := NormalizeLogin(r.Account); err != nil {
		return fmt.Errorf("persist: запись персонажа %q: %w", r.Name, err)
	}
	if !ValidName(r.Name) {
		return fmt.Errorf("persist: имя персонажа %q вне домена (1–16 alnum)", r.Name)
	}
	if r.ClassID != HumanFighter.ClassID {
		return fmt.Errorf("persist: класс %d не входит в единственный шаблон фазы", r.ClassID)
	}
	if r.Race != HumanFighter.Race {
		return fmt.Errorf("persist: раса %d не входит в единственный шаблон фазы", r.Race)
	}
	if err := ValidateAppearance(r.Sex, r.HairStyle, r.HairColor, r.Face); err != nil {
		return fmt.Errorf("persist: внешность %q: %w", r.Name, err)
	}
	if r.Slot < 0 || r.Slot > maxSlot {
		return fmt.Errorf("persist: слот %d вне домена (0–%d)", r.Slot, maxSlot)
	}
	if r.Level < 1 {
		return fmt.Errorf("persist: уровень %d < 1", r.Level)
	}
	if r.HP < 0 || r.MP < 0 {
		return fmt.Errorf("persist: пулы %d/%d отрицательны", r.HP, r.MP)
	}
	return nil
}
