package data

import (
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
)

// benchSkillsXML — детерминированный набор по распределению дистрибутива:
// один скилл с 80 уровнями и двумя энчант-маршрутами (140 записей уровней),
// 999 простых defs.
func benchSkillsXML() string {
	var sb strings.Builder
	sb.WriteString("<list>")
	fmt.Fprintf(&sb, `<skill id="8001" levels="80" name="Bench" enchantGroup1="1" enchantGroup2="2">`)
	fmt.Fprintf(&sb, `<table name="#hit">`)
	for l := range 80 {
		fmt.Fprintf(&sb, "%d ", 100+l)
	}
	sb.WriteString("</table>")
	fmt.Fprintf(&sb, `<table name="#e">`)
	for l := range 30 {
		fmt.Fprintf(&sb, "%d ", 500+l)
	}
	sb.WriteString("</table>")
	sb.WriteString(`<operateType>A1</operateType><targetType>ONE</targetType>`)
	sb.WriteString(`<hitTime>#hit</hitTime><reuseDelay>3000</reuseDelay><abnormalTime>60</abnormalTime>`)
	sb.WriteString(`<enchant1 name="reuseDelay">#e</enchant1>`)
	sb.WriteString(`<effects><effect name="BenchEffect" power="10"><amount>5</amount></effect></effects>`)
	sb.WriteString(`</skill>`)
	for i := range 999 {
		fmt.Fprintf(&sb, `<skill id="%d" levels="1" name="Bench %d"><operateType>P</operateType><targetType>SELF</targetType></skill>`, 8100+i, i)
	}
	sb.WriteString("</list>")
	return sb.String()
}

// benchSkillStatic — статика с бенчмарк-набором скиллов.
func benchSkillStatic(tb testing.TB) *Static {
	tb.Helper()
	fsys := catFS(map[string]*fstest.MapFile{
		"stats/skills/bench.xml": {Data: []byte(benchSkillsXML())},
	})
	st, rep, err := Load(fsys)
	if err != nil {
		tb.Fatalf("Load: %v", err)
	}
	if rep.HasErrors() {
		tb.Fatalf("ошибки: %+v", rep.Errors)
	}
	return st
}

// BenchmarkSkillLookup: лукап уровня скилла — база, энчант-маршрут, промах.
// Гейт нуля аллокаций — TestSkillLookupZeroAllocs.
func BenchmarkSkillLookup(b *testing.B) {
	st := benchSkillStatic(b)
	cases := []struct {
		name string
		id   SkillID
		lv   int32
		hit  bool
	}{
		{"base-hit", 8001, 40, true},
		{"ench1-hit", 8001, 115, true},
		{"ench2-hit", 8001, 155, true},
		{"gap-miss", 8001, 135, false},
		{"beyond-miss", 8001, 200, false},
		{"absent-miss", 9999, 1, false},
	}
	for _, tt := range cases {
		b.Run(tt.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				sk, ok := st.Skill(tt.id, tt.lv)
				if tt.hit {
					benchSink = ok && sk.HitTime > 0
				} else {
					benchSink = ok
				}
			}
		})
	}
}

// TestSkillLookupZeroAllocs: лукап не аллоцирует (запись по значению).
func TestSkillLookupZeroAllocs(t *testing.T) {
	st := benchSkillStatic(t)
	allocs := testing.AllocsPerRun(200, func() {
		sk, ok := st.Skill(8001, 115)
		benchSink = ok && sk.ReuseDelay > 0
	})
	if allocs != 0 {
		t.Errorf("AllocsPerRun(Skill) = %v; want 0", allocs)
	}
}
