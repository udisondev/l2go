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
	if f.Name != "ATTACK" || f.Origin != "generated" {
		t.Errorf("name/origin = %s/%s; want ATTACK/generated", f.Name, f.Origin)
	}
	if len(f.Payload) != 39 {
		t.Errorf("payload %d байт; want 39 (40 с опкодом)", len(f.Payload))
	}
}

func TestLoadErrors(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T) string // возвращает имя для Load
		want  string // подстрока ошибки
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
	}
	for _, c := range cases {
		name := c.setup(t)
		_, err := Load(name)
		if err == nil {
			t.Errorf("%s: ошибки нет", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) && !strings.Contains(err.Error(), name) {
			t.Errorf("%s: ошибка %q без контекста %q/%q", c.name, err, c.want, name)
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
