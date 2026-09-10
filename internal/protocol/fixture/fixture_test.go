package fixture

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadAttack(t *testing.T) {
	fixtures, err := Load("attack")
	if err != nil {
		t.Fatalf("Load(attack): %v", err)
	}
	if len(fixtures) != 1 {
		t.Fatalf("записей: %d; want 1", len(fixtures))
	}
	f := fixtures[0]
	if f.Dir != GameServer || f.Op != 0x05 || f.Sub != 0 {
		t.Errorf("фикстура = %s op=0x%02X sub=0x%02X; want game-server 0x05 0x00", f.Dir, f.Op, f.Sub)
	}
	if f.Name != "ATTACK" || f.Origin != OriginGenerated {
		t.Errorf("name/origin = %s/%s; want ATTACK/%s", f.Name, f.Origin, OriginGenerated)
	}
	if len(f.Payload) != 39 {
		t.Errorf("payload %d байт; want 39 (40 с опкодом)", len(f.Payload))
	}
}

func TestLoadErrors(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T) string // возвращает имя для Load
		want  string                    // подстрока ошибки
	}{
		{
			"нет файла",
			func(t *testing.T) string { return "несуществующий" },
			"несуществующий",
		},
		{
			"битый JSON",
			func(t *testing.T) string {
				return writeTemp(t, "broken.json", "{не json")
			},
			"broken",
		},
		{
			"битый hex",
			func(t *testing.T) string {
				return writeTemp(t, "badhex.json",
					`[{"dir":"game-server","op":5,"sub":0,"name":"X","payload":"zz","origin":"generated"}]`)
			},
			"payload",
		},
		{
			"null-JSON",
			func(t *testing.T) string {
				return writeTemp(t, "nulljson.json", "null")
			},
			"null-JSON",
		},
	}
	for _, c := range cases {
		name := c.setup(t)
		_, err := Load(name)
		if err == nil {
			t.Errorf("%s: ошибки нет", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: ошибка %q без подстроки %q", c.name, err, c.want)
		}
	}
}

// Имя заперто в testdata: абсолютные пути и обход «..» — ошибка.
func TestLoadRejectsEscape(t *testing.T) {
	for _, name := range []string{"../escape", "a/../../escape", "/etc/passwd"} {
		if _, err := Load(name); err == nil {
			t.Errorf("Load(%q): ошибки нет — побег за testdata", name)
		}
	}
}

func writeTemp(t *testing.T, file, content string) string {
	t.Helper()
	dir, err := os.MkdirTemp(filepath.Join(testdataDir(t)), "loadtest-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return filepath.Join(filepath.Base(dir), strings.TrimSuffix(file, ".json"))
}

// testdataDir — путь к testdata пакета (анкер повторяет реализацию Load).
func testdataDir(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata")
}
