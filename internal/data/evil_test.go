package data

import (
	"bytes"
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
		{"дубликат ID", itemXML(9300) + itemXML(9300), CodeDupID},
		{"id за границами int32", itemXMLWithID("2147483648"), CodeNumber},
		{"id отрицательный", itemXMLWithID("-5"), CodeNumber},
		{"id ноль", itemXMLWithID("0"), CodeNumber},
		{"нет обязательного атрибута name", "<list><item id=\"9001\" type=\"Weapon\"/></list>", CodeAttr},
		{"нет обязательного атрибута type", "<list><item id=\"9001\" name=\"A\"/></list>", CodeAttr},
		{"кривое число в типизированном set", strings.Replace(itemXML(9301), `val="1"`, `val="heavy"`, 1), CodeNumber},
		{"кривой bool в типизированном set", strings.Replace(itemXML(9302), "weight", "is_stackable", 1), CodeNumber},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := fstest.MapFS{"stats/items/x.xml": &fstest.MapFile{Data: []byte(tt.content)}}
			st, rep, err := Load(fsys)
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
			if tt.wantCode == CodeDupID && (st == nil || len(st.Items) != 1) {
				t.Errorf("при дубликате побеждает первая запись: st = %+v", st)
			}
		})
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
	it := st.Items[9400]
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
	if got := st.Items[9500].Weight; got != 777 {
		t.Errorf("Weight = %d; want 777 (значение из текста тега)", got)
	}
}

func itemXMLWithID(id string) string {
	return "<list><item id=\"" + id + "\" type=\"EtcItem\" name=\"n\"><set name=\"weight\" val=\"1\"/></item></list>"
}
