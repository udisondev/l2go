package data

import (
	"encoding/binary"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadSynthForCodec(t *testing.T) (*Static, *Report) {
	t.Helper()
	st, rep, err := Load(os.DirFS("testdata/synth"))
	if err != nil {
		t.Fatalf("Load synth: %v", err)
	}
	if rep.HasErrors() {
		t.Fatalf("synth красная: %+v", rep.Errors)
	}
	return st, rep
}

func TestCodecRoundtripSynth(t *testing.T) {
	st, _ := loadSynthForCodec(t)
	enc := EncodeStatic(st)
	if len(enc) == 0 {
		t.Fatalf("EncodeStatic вернул пустую секцию")
	}
	st2, err := DecodeStatic(enc)
	if err != nil {
		t.Fatalf("DecodeStatic: %v", err)
	}
	if a, b := st.Dump(), st2.Dump(); a != b {
		t.Fatalf("дамп после раундтрипa разошёлся (длина %d против %d)", len(a), len(b))
	}
	// Пробы контракта помимо дампа: лукапы уровней и энчант-маршрутов.
	if _, ok := st2.Item(9001); !ok {
		t.Errorf("Item(9001) после раундтрипa не найден")
	}
	def, ok := st2.SkillDef(7001)
	if !ok {
		t.Fatalf("SkillDef(7001) после раундтрипa не найден")
	}
	if def.Levels != 4 || len(def.Enchant) != 2 {
		t.Errorf("SkillDef(7001): levels=%d enchant=%d, хочу 4/2", def.Levels, len(def.Enchant))
	}
	for _, lvl := range []int32{1, 4, 101, 141} {
		if _, ok := st2.Skill(7001, lvl); !ok {
			t.Errorf("Skill(7001,%d) после раундтрипa не найден", lvl)
		}
	}
	if _, ok := st2.Skill(7001, 5); ok {
		t.Errorf("Skill(7001,5) вне диапазона уровней, хочу ok=false")
	}
	if len(st2.Zones()) != len(st.Zones()) || len(st2.Spawns()) != len(st.Spawns()) {
		t.Errorf("число зон/спавнов разошлось: %d/%d против %d/%d",
			len(st2.Zones()), len(st2.Spawns()), len(st.Zones()), len(st.Spawns()))
	}
}

func TestEncodeStaticDeterministic(t *testing.T) {
	st, _ := loadSynthForCodec(t)
	a, b := EncodeStatic(st), EncodeStatic(st)
	if string(a) != string(b) {
		t.Fatalf("две кодировки одной статики различаются (недетерминированный обход)")
	}
}

func TestDecodeStaticEvilInputs(t *testing.T) {
	st, _ := loadSynthForCodec(t)
	base := EncodeStatic(st)
	cases := []struct {
		name string
		mut  func([]byte) []byte
	}{
		{"пустой", func(b []byte) []byte { return nil }},
		{"обрыв 1 байт", func(b []byte) []byte { return b[:len(b)-1] }},
		{"обрыв половина", func(b []byte) []byte { return b[:len(b)/2] }},
		{"обрыв 3 байта", func(b []byte) []byte { return b[:3] }},
		{
			"счётчик предметов u32max",
			func(b []byte) []byte { binary.LittleEndian.PutUint32(b[0:4], 0xFFFFFFFF); return b },
		},
		{
			"хвост",
			func(b []byte) []byte { return append(b, 0) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mut := tc.mut(append([]byte(nil), base...))
			if _, err := DecodeStatic(mut); err == nil {
				t.Fatalf("малформленный вход декодирован без ошибки")
			}
		})
	}
}

// TestDecodeStaticNoPanic — свойство: переворот любого байта не паникует
// (ошибка или успех — только не паника и не OOM).
func TestDecodeStaticNoPanic(t *testing.T) {
	st, _ := loadSynthForCodec(t)
	base := EncodeStatic(st)
	step := max(len(base)/256, 1)
	for off := 0; off < len(base); off += step {
		mut := append([]byte(nil), base...)
		mut[off] ^= 0xFF
		_, _ = DecodeStatic(mut) // паника уронит тест
	}
}

// TestSkillRawDepthCap — симметричный потолок глубины raw-деревьев: XML с
// вложенностью выше потолка — ошибка разбора, не безлимитная рекурсия.
func TestSkillRawDepthCap(t *testing.T) {
	root := t.TempDir()
	copySynthTree(t, "testdata/synth", root)

	const deep = 70
	var sb strings.Builder
	sb.WriteString(`<skill id="7999" levels="1" name="глубокий"><operateType>A1</operateType><effects>`)
	for range deep {
		sb.WriteString("<n>")
	}
	for range deep {
		sb.WriteString("</n>")
	}
	sb.WriteString(`</effects></skill>`)

	skillsPath := filepath.Join(root, "stats", "skills", "skills.xml")
	raw, err := os.ReadFile(skillsPath)
	if err != nil {
		t.Fatalf("чтение skills.xml: %v", err)
	}
	idx := strings.LastIndex(string(raw), "</list>")
	if idx < 0 {
		t.Fatalf("в skills.xml нет закрывающего list")
	}
	out := string(raw[:idx]) + sb.String() + string(raw[idx:])
	if err := os.WriteFile(skillsPath, []byte(out), 0o644); err != nil {
		t.Fatalf("запись skills.xml: %v", err)
	}

	_, rep, err := Load(os.DirFS(root))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !rep.HasErrors() {
		t.Fatalf("вложенность %d принята парсером: потолок глубины не работает", deep)
	}
}

func copySynthTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("копия синтетики: %v", err)
	}
}

// craftSection собирает секцию примитивами энкодера — для злых входов,
// которые не может породить кодирование честной статики (дубли, подделанные
// счётчики и домены).
func craftSection(t *testing.T, f func(e *enc)) []byte {
	t.Helper()
	e := &enc{}
	f(e)
	return e.buf
}

func craftItem(e *enc, id int32) {
	e.i32(id)
	e.str("имя")
	e.str("Weapon")
	e.i64(1)
	e.i64(2)
	e.boolean(false)
	e.str("")
	e.i64(0)
	e.str("")
	e.str("")
	e.u32(0) // set
}

// craftDef пишет минимальное определение скилла; ench пишет отдельно для
// вариаций домена маршрутов.
func craftDef(e *enc, id int32, ench func(e *enc)) {
	e.i32(id)
	e.str("имя")
	e.i32(1) // levels
	if ench != nil {
		ench(e)
	} else {
		e.u32(0)
	}
	e.u32(0) // base
	e.u32(0) // enchantLevels
	e.u32(0) // tables
	e.u32(0) // set
	e.u32(0) // overrides
	e.u32(0) // raw
}

func TestDecodeStaticCraftedEvil(t *testing.T) {
	cases := []struct {
		name  string
		craft func(t *testing.T) []byte
	}{
		{
			"дубль предмета",
			func(t *testing.T) []byte {
				return craftSection(t, func(e *enc) {
					e.u32(2) // items
					for range 5 {
						e.u32(0)
					}
					craftItem(e, 9001)
					craftItem(e, 9001)
				})
			},
		},
		{
			"строка u32max",
			func(t *testing.T) []byte {
				return craftSection(t, func(e *enc) {
					e.u32(1)
					for range 5 {
						e.u32(0)
					}
					e.i32(9001)
					e.u32(0xFFFFFFFF) // длина имени
				})
			},
		},
		{
			"маршрут 0",
			func(t *testing.T) []byte {
				return craftSection(t, func(e *enc) {
					for range 5 {
						e.u32(0)
					}
					e.u32(1) // skills
					craftDef(e, 7001, func(e *enc) {
						e.u32(1)
						e.u8(0)
					})
				})
			},
		},
		{
			"маршрут 9",
			func(t *testing.T) []byte {
				return craftSection(t, func(e *enc) {
					for range 5 {
						e.u32(0)
					}
					e.u32(1)
					craftDef(e, 7001, func(e *enc) {
						e.u32(1)
						e.u8(9)
					})
				})
			},
		},
		{
			"дубль маршрута",
			func(t *testing.T) []byte {
				return craftSection(t, func(e *enc) {
					for range 5 {
						e.u32(0)
					}
					e.u32(1)
					craftDef(e, 7001, func(e *enc) {
						e.u32(2)
						e.u8(1)
						e.u8(1)
					})
				})
			},
		},
		{
			"глубина raw-дерева 65 в декодере",
			func(t *testing.T) []byte {
				return craftSection(t, func(e *enc) {
					for range 5 {
						e.u32(0)
					}
					e.u32(1) // skills
					craftDef(e, 7001, nil)
					// заменяем хвост def (raw count) на дерево глубины 65:
					e.buf = e.buf[:len(e.buf)-4]
					e.u32(1)
					node := RawNode{Name: "n"}
					for i := 1; i < 65; i++ {
						node = RawNode{Name: "n", Children: []RawNode{node}}
					}
					encRawNode(e, node)
				})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeStatic(tc.craft(t)); err == nil {
				t.Fatalf("крафтовый злой вход декодирован без ошибки")
			}
		})
	}
}

// TestDecodeRouteDomainIsolated — домен маршрута изолирован от сверок
// консистентности: маршрут вне 1..8 при СОГЛАСОВАННОМ числе слайсов уровней
// отвергается самим домен-гвардом (мутация C раунда-1 не выживала лишь
// благодаря сверке длин).
func TestDecodeRouteDomainIsolated(t *testing.T) {
	section := craftSection(t, func(e *enc) {
		for range 5 {
			e.u32(0)
		}
		e.u32(1) // skills
		e.i32(7001)
		e.str("имя")
		e.i32(1) // levels
		e.u32(1) // enchant: один маршрут
		e.u8(0)  // ...со значением вне домена
		e.u32(0) // base
		e.u32(1) // enchantLevels: маршрут согласован
		e.u32(0) // уровней в маршруте
		e.u32(0) // tables
		e.u32(0) // set
		e.u32(0) // overrides
		e.u32(0) // raw
	})
	if _, err := DecodeStatic(section); err == nil {
		t.Fatalf("маршрут 0 при согласованном EnchantLevels декодирован без ошибки")
	}
}
