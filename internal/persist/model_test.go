package persist

import "testing"

func TestNormalizeLogin(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"Player1", "player1", true},
		{"ABC", "abc", true},
		{"a", "", false},
		{"ab", "", false},
		{"abc", "abc", true},
		{"fifteenchars155", "", false}, // 15 символов
		{"fourteenchars1", "fourteenchars1", true},
		{"", "", false},
		{"../x", "", false},
		{"a b", "", false},
		{"привет", "", false},
		{"player_1", "", false},
	}
	for _, tc := range tests {
		got, err := NormalizeLogin(tc.in)
		if tc.ok {
			if err != nil {
				t.Errorf("NormalizeLogin(%q) error = %v; want nil", tc.in, err)
			} else if got != tc.want {
				t.Errorf("NormalizeLogin(%q) = %q; want %q", tc.in, got, tc.want)
			}
		} else if err == nil {
			t.Errorf("NormalizeLogin(%q) = %q; want ошибка", tc.in, got)
		}
	}
}

func TestValidName(t *testing.T) {
	tests := []struct {
		in string
		ok bool
	}{
		{"", false},
		{"V", true},
		{"Vasya123", true},
		{"Шестнадцатьсимв", false},
		{pad16, true},
		{pad17, false},
		{"Вася", false},
		{"Vasya Пупкин", false},
		{"../x", false},
	}
	for _, tc := range tests {
		if got := ValidName(tc.in); got != tc.ok {
			t.Errorf("ValidName(%q) = %v; want %v", tc.in, got, tc.ok)
		}
	}
}

const (
	pad16 = "Abcdefghijklmnop" // 16 символов
	pad17 = "Abcdefghijklmnopq"
)

func TestValidateAppearance(t *testing.T) {
	tests := []struct {
		name                       string
		sex, hairStyle, hair, face int
		ok                         bool
	}{
		{"мужчина минимум", 0, 0, 0, 0, true},
		{"мужчина максимум", 0, 4, 3, 2, true},
		{"мужчина hairStyle 5", 0, 5, 0, 0, false},
		{"женщина максимум", 1, 6, 3, 2, true},
		{"женщина hairStyle 7", 1, 7, 0, 0, false},
		{"женщина hairStyle 5", 1, 5, 0, 0, true},
		{"hairColor 4", 0, 0, 4, 0, false},
		{"hairColor -1", 0, 0, -1, 0, false},
		{"face 3", 0, 0, 0, 3, false},
		{"face -1", 0, 0, 0, -1, false},
		{"hairStyle -1", 0, -1, 0, 0, false},
		{"sex 2", 2, 0, 0, 0, false},
		{"sex -1", -1, 0, 0, 0, false},
	}
	for _, tc := range tests {
		err := ValidateAppearance(tc.sex, tc.hairStyle, tc.hair, tc.face)
		if tc.ok && err != nil {
			t.Errorf("%s: ValidateAppearance(%d,%d,%d,%d) error = %v; want nil",
				tc.name, tc.sex, tc.hairStyle, tc.hair, tc.face, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: ValidateAppearance(%d,%d,%d,%d) = nil; want ошибка",
				tc.name, tc.sex, tc.hairStyle, tc.hair, tc.face)
		}
	}
}

// TestHumanFighterPin фиксирует числа порта канона (Mobius CT0 Interlude,
// HumanFighter.xml): дрейф константы без смены источника = красный тест.
func TestHumanFighterPin(t *testing.T) {
	tr := HumanFighter
	want := Template{
		ClassID: 0, Race: 0,
		Str: 40, Dex: 30, Con: 43, Int: 21, Wit: 11, Men: 25,
		BaseHP: 80, BaseMP: 30, BaseCP: 32,
		WalkSpd: 80, RunSpd: 115,
		StartX: -71338, StartY: 258271, StartZ: -3104,
	}
	if tr != want {
		t.Errorf("HumanFighter = %+v; want %+v", tr, want)
	}
}

func TestValidateCharRecord(t *testing.T) {
	base := CharRecord{
		Account: "acc", Slot: 0, Name: "Vasya",
		ClassID: HumanFighter.ClassID, Race: HumanFighter.Race,
		Sex: 0, HairStyle: 0, HairColor: 0, Face: 0,
		X: 1, Y: 2, Z: 3, Heading: 0,
		Level: 1, Exp: 0, HP: HumanFighter.BaseHP, MP: HumanFighter.BaseMP,
	}
	if err := validateCharRecord(base); err != nil {
		t.Errorf("ValidateCharRecord(база) error = %v; want nil", err)
	}
	bad := []struct {
		name   string
		mutate func(*CharRecord)
	}{
		{"чужой класс", func(r *CharRecord) { r.ClassID = 88 }},
		{"чужая раса", func(r *CharRecord) { r.Race = 1 }},
		{"слот 7", func(r *CharRecord) { r.Slot = 7 }},
		{"слот -1", func(r *CharRecord) { r.Slot = -1 }},
		{"злое имя", func(r *CharRecord) { r.Name = "../x" }},
		{"sex 2", func(r *CharRecord) { r.Sex = 2 }},
		{"hairStyle 9", func(r *CharRecord) { r.HairStyle = 9 }},
		{"уровень 0", func(r *CharRecord) { r.Level = 0 }},
		{"отрицательный HP", func(r *CharRecord) { r.HP = -1 }},
	}
	for _, tc := range bad {
		r := base
		tc.mutate(&r)
		if err := validateCharRecord(r); err == nil {
			t.Errorf("%s: ValidateCharRecord() = nil; want ошибка", tc.name)
		}
	}
}
