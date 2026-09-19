//go:build embedded

package main

// Диспетчер статики embedded-сборки (тест-план P3.13, кейсы S4–S6, S9):
// пустой путь грузит вшитые в бинарарь байты (самодостаточная поставка);
// непустой путь предпочтён вшитым (фальсификатор — артефакт-«самозванец»
// без гео); -replay остаётся строго с явным -artifact; полный контур
// поднимается на вшитой статике (уровень — интеграционный: сервер и клиент
// в одном процессе, имя по конвенции пакета).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/udisondev/l2go/internal/artifact"
	"github.com/udisondev/l2go/internal/data"
)

// embeddedArtifactFile — вшитый артефакт на диске (тот же файл, что
// go:embed забрал в бинарарь при сборке).
var embeddedArtifactFile = filepath.Join("..", "..", "internal", "artifact", "embedded", "artifact.l2a")

func TestLoadStaticEmbeddedEmptyUsesEmbeddedBytes(t *testing.T) {
	_, _, meta, err := loadStatic("")
	if err != nil {
		t.Fatalf("loadStatic(\"\") на embedded-сборке: %v", err)
	}
	_, _, want, _, werr := artifact.LoadFile(embeddedArtifactFile)
	if werr != nil {
		t.Fatalf("LoadFile вшитого артефакта: %v", werr)
	}
	if *meta != *want {
		t.Fatalf("мета вшитых байт: got %+v, want %+v", *meta, *want)
	}
}

func TestLoadStaticEmbeddedNonEmptyPrefersFile(t *testing.T) {
	// Предусловие фальсификатора: вшитый артефакт обязан нести гео,
	// иначе «самозванец без гео» от него неотличим.
	if embeddedRegions(t) == 0 {
		t.Skipf("вшитый артефакт %s без гео: фальсификатор подмены источника неприменим", embeddedArtifactFile)
	}
	// Артефакт-самозванец: тот же синт-датапак, но без гео (Regions = 0,
	// у вшитого — есть); строится тем же конвейером Build.
	st, rep, err := data.Load(os.DirFS(filepath.Join("..", "..", "internal", "data", "testdata", "synth")))
	if err != nil {
		t.Fatalf("synth-датапак: %v", err)
	}
	if rep.HasErrors() {
		t.Fatalf("synth-датапак красный: %v", rep.Errors)
	}
	noGeo := filepath.Join(t.TempDir(), "no-geo.l2a")
	if _, err := artifact.Build(noGeo, st, rep, nil, nil); err != nil {
		t.Fatalf("Build без гео: %v", err)
	}
	_, gm, meta, err := loadStatic(noGeo)
	if err != nil {
		t.Fatalf("loadStatic(%s): %v", noGeo, err)
	}
	_, _, want, _, werr := artifact.LoadFile(noGeo)
	if werr != nil {
		t.Fatalf("LoadFile-эталон: %v", werr)
	}
	if *meta != *want {
		t.Fatalf("мета %s: got %+v, want %+v", noGeo, *meta, *want)
	}
	if gm == nil {
		t.Fatal("гео nil при непустом пути (in-band пустой источник)")
	}
	if meta.Regions == embeddedRegions(t) {
		t.Fatalf("непустой путь не различим от вшитых байт (Regions=%d): подмена источника", meta.Regions)
	}
}

// embeddedRegions — счётчик регионов вшитого артефакта (самозапись эталона
// для фальсификатора S5).
func embeddedRegions(t *testing.T) uint32 {
	t.Helper()
	_, _, want, _, err := artifact.LoadFile(embeddedArtifactFile)
	if err != nil {
		t.Fatalf("LoadFile вшитого артефакта: %v", err)
	}
	return want.Regions
}

func TestReplayRequiresArtifactInEmbeddedBuild(t *testing.T) {
	err := run([]string{"-replay", t.TempDir()})
	if err == nil {
		t.Fatal("run(-replay) без -artifact на embedded-сборке: ожидалась ошибка")
	}
	if !strings.Contains(err.Error(), "-replay требует -artifact") {
		t.Fatalf("replay ушёл с вшитой статикой вместо явного guard: %v", err)
	}
}

// Кейс S9 (желательный): полный контур на вшитой статике — вход до слитка
// и NPC стартальной окрестности развёрнуты (сквозная страховка диспатча).
func TestBootstrapEmbeddedEmptyArtifactFullContour(t *testing.T) {
	env := startE2EOpts(t, 50, 4, e2eOpts{npc: true, useEmbeddedStatic: true})
	s := enterWorld(t, env, "embedded")
	waitForLine(t, s.out, "USER_INFO", 3*time.Second)
	assertSlivokOrder(t, s.out)
	waitForLine(t, s.out, "NPC_INFO", 3*time.Second)
}
