package data

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEvilInputs(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantCode string
	}{
		{"обрезанный XML", "<list><item id=\"9001\" type=\"Weapon\" name=\"A\"><set na", CodeXML},
		{"пустой файл", "", CodeXML},
		{"чужой корневой элемент", "<items><item id=\"1\" type=\"Weapon\" name=\"A\"/></items>", CodeRoot},
		{"корневой элемент в верхнем регистре", "<LIST/>", CodeRoot},
		{"дубликат ID", "<list>" + itemBody(9300, "первый") + itemBody(9300, "второй") + "</list>", CodeDupID},
		{"id за границами int32", itemXMLWithID("2147483648"), CodeNumber},
		{"id отрицательный", itemXMLWithID("-5"), CodeNumber},
		{"id ноль", itemXMLWithID("0"), CodeNumber},
		{"нет обязательного атрибута name", "<list><item id=\"9001\" type=\"Weapon\"/></list>", CodeAttr},
		{"нет обязательного атрибута type", "<list><item id=\"9001\" name=\"A\"/></list>", CodeAttr},
		{"повтор атрибута", "<list>" + strings.Replace(itemBody(9303, "n"), "name=\"n\"", "name=\"n\" id=\"9303\"", 1) + "</list>", CodeAttr},
		{"кривое число в типизированном set", "<list>" + strings.Replace(itemBody(9301, "n"), `val="1"`, `val="heavy"`, 1) + "</list>", CodeNumber},
		{"кривой bool в типизированном set", "<list>" + strings.Replace(itemBody(9302, "n"), "weight", "is_stackable", 1) + "</list>", CodeNumber},
		{"set после закрытого stats", "<list><item id=\"9304\" type=\"EtcItem\" name=\"n\"><stats><stat type=\"pAtk\">8</stat></stats>" +
			"<set name=\"weight\" val=\"1\"/></item></list>", CodeAttr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := fstest.MapFS{"stats/items/x.xml": &fstest.MapFile{Data: []byte(tt.content)}}
			_, rep, err := Load(fsys)
			if err != nil {
				t.Fatalf("Load вернул фатальную ошибку на данных: %v", err)
			}
			if rep == nil {
				t.Fatal("отчёт nil: Load обязан прочитать всё и вернуть отчёт")
			}
			if !rep.HasErrors() {
				t.Fatalf("злой вход не отмечен ошибкой; отчёт: %+v", rep)
			}
			found := false
			for _, e := range rep.Errors {
				if e.Code == tt.wantCode {
					found = true
					if e.File == "" || e.Line <= 0 {
						t.Errorf("запись без места: %+v (нужны файл и строка)", e)
					}
				}
			}
			if !found {
				t.Errorf("нет записи с кодом %q; записи: %+v", tt.wantCode, rep.Errors)
			}
			if tt.wantCode == CodeXML {
				// обрыв файла — одна запись, не дублируемая внешним циклом
				count := 0
				for _, e := range rep.Errors {
					if e.Code == CodeXML {
						count++
					}
				}
				if count > 1 {
					t.Errorf("обрыв XML дал %d записей; want 1: %+v", count, rep.Errors)
				}
			}
		})
	}
}

func TestDupIDFirstWins(t *testing.T) {
	content := "<list>" + itemBody(9310, "первый") + itemBody(9310, "второй") + "</list>"
	st, rep, err := Load(fstest.MapFS{"stats/items/x.xml": &fstest.MapFile{Data: []byte(content)}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !rep.HasErrors() {
		t.Fatal("дубликат должен дать ошибку")
	}
	if st.Len() != 1 {
		t.Fatalf("Len = %d; want 1", st.Len())
	}
	it, _ := st.Item(9310)
	if it.Name != "первый" {
		t.Errorf("победил %q; want первая запись (первый)", it.Name)
	}
}

func TestFileOverLimit(t *testing.T) {
	huge := bytes.Repeat([]byte("<!-- padding -->"), (16<<20)/16+2)
	fsys := fstest.MapFS{"stats/items/big.xml": &fstest.MapFile{Data: huge}}
	_, rep, err := Load(fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := false
	for _, e := range rep.Errors {
		if e.Code == CodeLimit {
			found = true
		}
		if e.Code == CodeXML {
			t.Errorf("превышение потолка замаскировалось под кривой XML: %+v", e)
		}
	}
	if !found {
		t.Errorf("нет записи %q; записи: %+v", CodeLimit, rep.Errors)
	}
}

func TestUnknownValuesAreCounters(t *testing.T) {
	content := "<list>" +
		"<item id=\"9400\" type=\"Relic\" name=\"Древняя вещица\">" +
		"<set name=\"enigma\" val=\"1\"/>" +
		"<skills><skill id=\"1200\" level=\"1\"/></skills>" +
		"<conditions><player level=\"40\"/></conditions>" +
		"<set/>" +
		"</item></list>"
	fsys := fstest.MapFS{"stats/items/x.xml": &fstest.MapFile{Data: []byte(content)}}
	st, rep, err := Load(fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if st == nil {
		t.Fatal("Static nil")
	}
	if rep.HasErrors() {
		t.Fatalf("неизвестное — широта данных, не ошибка: %+v", rep.Errors)
	}
	if got := rep.UnknownTypes["Relic"]; got != 1 {
		t.Errorf("UnknownTypes[Relic] = %d; want 1", got)
	}
	if got := rep.UnknownKeys["enigma"]; got != 1 {
		t.Errorf("UnknownKeys[enigma] = %d; want 1", got)
	}
	if got := rep.SkippedElements["skills"]; got != 1 {
		t.Errorf("SkippedElements[skills] = %d; want 1", got)
	}
	if got := rep.SkippedElements["conditions"]; got != 1 {
		t.Errorf("SkippedElements[conditions] = %d; want 1", got)
	}
	if rep.UnnamedSets != 1 {
		t.Errorf("UnnamedSets = %d; want 1 (set без name пропускается)", rep.UnnamedSets)
	}
	it, _ := st.Item(9400)
	if it.Type != "Relic" {
		t.Errorf("Type = %q; want исходная строка Relic", it.Type)
	}
	if v, ok := it.Set("enigma"); !ok || v != "1" {
		t.Errorf("Set(enigma) = (%q, %v); want (1, true)", v, ok)
	}
}

func TestValFallbackToText(t *testing.T) {
	content := "<list><item id=\"9500\" type=\"EtcItem\" name=\"Текстовый вес\">" +
		"<set name=\"weight\">777</set></item></list>"
	fsys := fstest.MapFS{"stats/items/x.xml": &fstest.MapFile{Data: []byte(content)}}
	st, rep, err := Load(fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if st == nil {
		t.Fatal("Static nil")
	}
	if rep.HasErrors() {
		t.Fatalf("фолбэк val→текст — семантика канона, не ошибка: %+v", rep.Errors)
	}
	if got, _ := st.Item(9500); got.Weight != 777 {
		t.Errorf("Weight = %d; want 777 (значение из текста тега)", got.Weight)
	}
}

func TestNestedTextIncluded(t *testing.T) {
	content := "<list><item id=\"9501\" type=\"EtcItem\" name=\"Вложенный текст\">" +
		"<set name=\"weight\"><b>1</b>0</set></item></list>"
	fsys := fstest.MapFS{"stats/items/x.xml": &fstest.MapFile{Data: []byte(content)}}
	st, _, err := Load(fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, _ := st.Item(9501); got.Weight != 10 {
		t.Errorf("Weight = %d; want 10 (текст вложенных элементов включается, как getTextContent канона)", got.Weight)
	}
}

func TestStatDupCounter(t *testing.T) {
	content := "<list><item id=\"9502\" type=\"Weapon\" name=\"Дубль стата\">" +
		"<stats><stat type=\"pAtk\">8</stat><stat type=\"pAtk\">9</stat></stats></item></list>"
	fsys := fstest.MapFS{"stats/items/x.xml": &fstest.MapFile{Data: []byte(content)}}
	st, rep, err := Load(fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rep.HasErrors() {
		t.Fatalf("дубль stat-типа — перезапись + счётчик, не ошибка: %+v", rep.Errors)
	}
	if rep.DupKeys != 1 {
		t.Errorf("DupKeys = %d; want 1", rep.DupKeys)
	}
	if v, _ := mustItem(t, st, 9502).Set("stat.pAtk"); v != "9" {
		t.Errorf("stat.pAtk = %q; want 9 (последний выигрывает)", v)
	}
}

func mustItem(t *testing.T, st *Static, id ItemID) Item {
	t.Helper()
	it, ok := st.Item(id)
	if !ok {
		t.Fatalf("предмет %d отсутствует", id)
	}
	return it
}

func itemXMLWithID(id string) string {
	return "<list><item id=\"" + id + "\" type=\"EtcItem\" name=\"n\"><set name=\"weight\" val=\"1\"/></item></list>"
}

func itemBody(id int, name string) string {
	return "<item id=\"" + strconv.Itoa(id) + "\" type=\"EtcItem\" name=\"" + name +
		"\"><set name=\"weight\" val=\"1\"/></item>"
}
