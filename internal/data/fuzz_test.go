package data

import (
	"os"
	"testing"
	"testing/fstest"
)

// FuzzLoadItems: любые байты как файл категории — без паник; Load возвращает
// отчёт (или фатальную FS-ошибку) и детерминирован по факту наличия ошибок.
func FuzzLoadItems(f *testing.F) {
	seed, err := os.ReadFile("testdata/synth/stats/items/items.xml")
	if err != nil {
		f.Fatalf("чтение seed: %v", err)
	}
	f.Add(seed)
	f.Add([]byte("<list><item id=\"1\" type=\"We"))
	f.Add([]byte("<list><item id=\"-3\" type=\"\" name=\"\"><set val=\"\""))
	f.Add([]byte{0x00, 0xff, 0xfe})

	f.Fuzz(func(t *testing.T, data []byte) {
		fsys := fstest.MapFS{"stats/items/x.xml": &fstest.MapFile{Data: data}}
		_, rep1, err1 := Load(fsys)
		_, rep2, err2 := Load(fsys)
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("недетерминированная фатальная ошибка: %v vs %v", err1, err2)
		}
		if err1 != nil {
			return
		}
		if rep1 == nil || rep2 == nil {
			t.Fatal("отчёт nil на произвольном входе")
		}
		if rep1.HasErrors() != rep2.HasErrors() || rep1.Items != rep2.Items {
			t.Fatalf("недетерминированный отчёт: %+v vs %+v", rep1, rep2)
		}
	})
}
