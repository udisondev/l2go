// Реплей-гейт D6 (P3.12): e2e-запись DoD-сессии с payloads → FinalDump ==
// Replay бит-в-бит; записыватель фикстуры; CLI-путь -replay/-expect.

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/udisondev/l2go/internal/artifact"
	"github.com/udisondev/l2go/internal/data"
	"github.com/udisondev/l2go/internal/geo"
	"github.com/udisondev/l2go/internal/transport"
	"github.com/udisondev/l2go/internal/world"
)

// emptyGeo — карта без гео-регионов (NullRegion-семантика) для CLI-тестов.
var emptyGeo = func() *geo.Map {
	m, err := geo.NewMapFromRegions(nil)
	if err != nil {
		panic("тест: пустая карта гео: " + err.Error())
	}
	return m
}()

// fixtureDir — закоммиченная фикстура реплей-гейта CI (перезапись —
// L2GO_RECORD_FIXTURE=1 go test -run TestReplayRecordFixture ./cmd/l2go).
const fixtureDir = "testdata/replay-dod"

// loadedArtifact — статика и гео синт-артефакта (тот же файл, что грузит
// bootstrap: вход свёртки вне лога, обязан совпадать с записью).
func loadedArtifact(t *testing.T) (*data.Static, *geo.Map) {
	t.Helper()
	static, gm, _, _, err := artifact.LoadFile(synthArtifact)
	if err != nil {
		t.Fatalf("артефакт: %v", err)
	}
	return static, gm
}

// TestReplayGateDoD — критерии (а)+(б) и «реальный источник тика»: живой
// DoD-контур на реальном метрономе пишет лог с payloads; после остановки
// FinalDump равен реплею бит-в-бит, повторный прогон — тоже, маркеров паник нет.
func TestReplayGateDoD(t *testing.T) {
	env := startE2EOpts(t, 10, 4, e2eOpts{npc: true, portionsPayloads: true, portionsDir: t.TempDir()})
	dodScenario(t, env)
	env.gs.shutdown() // стоп контура; регион завершён — чтение FinalDump легально

	live := env.gs.region.FinalDump()
	if len(live) == 0 {
		t.Fatal("FinalDump пуст")
	}
	hdr, frames, err := world.ReadPortionFrames(env.portions, 1)
	if err != nil {
		t.Fatalf("ReadPortionFrames: %v", err)
	}
	for i, f := range frames {
		if f.Panic != nil {
			t.Fatalf("маркер паники в DoD-сессии: frames[%d] = %+v", i, f.Panic)
		}
	}
	static, gm := loadedArtifact(t)
	r1, err := world.Replay(hdr, frames, static, gm)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if !bytes.Equal(live, r1.Dump) {
		t.Fatalf("реплей ≠ живой дамп: len %d vs %d", len(r1.Dump), len(live))
	}
	r2, err := world.Replay(hdr, frames, static, gm)
	if err != nil {
		t.Fatalf("Replay повтор: %v", err)
	}
	if !bytes.Equal(r1.Dump, r2.Dump) {
		t.Fatal("повторный реплей разошёлся")
	}
}

// TestReplayRecordFixture — записыватель фикстуры (env-гейт): DoD-сессия
// пишется в testdata/replay-dod (цепочка логов + final.dump + manifest.txt +
// artifact.sha256). Пропуск без env — с командой перезаписи в сообщении.
func TestReplayRecordFixture(t *testing.T) {
	if os.Getenv("L2GO_RECORD_FIXTURE") == "" {
		t.Skip("перезапись фикстуры: L2GO_RECORD_FIXTURE=1 go test -run TestReplayRecordFixture ./cmd/l2go")
	}
	if err := os.RemoveAll(fixtureDir); err != nil {
		t.Fatalf("очистка фикстуры: %v", err)
	}
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		t.Fatalf("каталог фикстуры: %v", err)
	}
	env := startE2EOpts(t, 10, 4, e2eOpts{npc: true, portionsPayloads: true, portionsDir: fixtureDir})
	dodScenario(t, env)
	env.gs.shutdown()
	live := env.gs.region.FinalDump()
	if len(live) == 0 {
		t.Fatal("FinalDump пуст")
	}
	dumpPath := filepath.Join(fixtureDir, "final.dump")
	if err := os.WriteFile(dumpPath, live, 0o644); err != nil {
		t.Fatalf("final.dump: %v", err)
	}

	// самосогласованность фикстуры до коммита
	hdr, frames, err := world.ReadPortionFrames(fixtureDir, 1)
	if err != nil {
		t.Fatalf("ReadPortionFrames: %v", err)
	}
	static, gm := loadedArtifact(t)
	res, err := world.Replay(hdr, frames, static, gm)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if !bytes.Equal(res.Dump, live) {
		t.Fatal("фикстура несамосогласована: реплей ≠ final.dump")
	}

	// манифест — ревьюабельность бинарного диффа фикстуры
	sum := sha256.Sum256(live)
	manifest := fmt.Sprintf("session=%d\nregion=%d payloads=%v period_ns=%d grace=%d save_retry=%d persist=%d gateway=%d ctrl=%d\nsteps=%d letters=%d\nfinal.dump sha256=%s\n",
		hdr.Session, hdr.Region, hdr.Payloads, hdr.PeriodNS, hdr.GraceTicks, hdr.SaveRetryTicks,
		hdr.Persist, hdr.Gateway, hdr.CtrlFrom, res.Steps, res.Letters, hex.EncodeToString(sum[:]))
	logs, err := filepath.Glob(filepath.Join(fixtureDir, "portion-*.log"))
	if err != nil {
		t.Fatalf("glob логов фикстуры: %v", err)
	}
	for _, f := range logs {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("чтение %s: %v", f, err)
		}
		lsum := sha256.Sum256(raw)
		manifest += fmt.Sprintf("%s %d байт sha256=%s\n", filepath.Base(f), len(raw), hex.EncodeToString(lsum[:]))
	}
	if err := os.WriteFile(filepath.Join(fixtureDir, "manifest.txt"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("manifest.txt: %v", err)
	}

	// отпечаток артефакта записи (сверка CI — при подтверждённом детермизме
	// сборки; диагностика «не тот артефакт» вместо «недетерминизм»)
	raw, err := os.ReadFile(synthArtifact)
	if err != nil {
		t.Fatalf("артефакт: %v", err)
	}
	asum := sha256.Sum256(raw)
	if err := os.WriteFile(filepath.Join(fixtureDir, "artifact.sha256"), []byte(hex.EncodeToString(asum[:])+"\n"), 0o644); err != nil {
		t.Fatalf("artifact.sha256: %v", err)
	}
}

// TestReplayFixture — CLI-путь реплей-гейта: run(-replay testdata -expect
// final.dump) — тот же путь, который исполняет CI-шаг.
func TestReplayFixture(t *testing.T) {
	if _, err := os.Stat(filepath.Join(fixtureDir, "final.dump")); err != nil {
		t.Skipf("фикстура не записана (%v); перезапись: L2GO_RECORD_FIXTURE=1 go test -run TestReplayRecordFixture ./cmd/l2go", err)
	}
	err := run([]string{"-replay", fixtureDir, "-artifact", synthArtifact,
		"-expect", filepath.Join(fixtureDir, "final.dump")})
	if err != nil {
		t.Fatalf("run(-replay): %v", err)
	}
}

// ——— CLI: неполнота, диагностика расхождения, злые аргументы ———

// craftCliSession — минимальная сессия в каталоге (шаги с контрольным
// письмом; withPanic — маркер паники в середине).
func craftCliSession(t *testing.T, dir string, withPanic bool) {
	t.Helper()
	l, err := world.NewPortionLog(dir, 1, true, 1<<20)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
	}
	if err := l.SetRules(world.Rules{GraceTicks: 4, SaveRetryTicks: 2,
		PeriodNS: 20_000_000, Persist: 3, Gateway: 2, From: 1}); err != nil {
		t.Fatalf("SetRules: %v", err)
	}
	wake := transport.Envelope{To: transport.Addr{Entity: 1}, FromID: 2, Kind: transport.KindXP}
	for i := range 6 {
		s := world.StepInput{Tick: world.Tick(i + 1), Delta: 1,
			Portions: []world.PortionRecord{{Box: 1, Envs: []transport.Envelope{wake}}}}
		if err := l.LogStep(s); err != nil {
			t.Fatalf("LogStep: %v", err)
		}
		if withPanic && i == 2 {
			if err := l.LogPanic(world.Tick(i+1), 2); err != nil {
				t.Fatalf("LogPanic: %v", err)
			}
		}
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// Неполный результат (маркер паники, оборванный хвост) — ненулевой выход БЕЗ
// сверки: частичный дамп не выдаётся за полный, даже если -expect совпадает.
func TestReplayCliIncompleteResultExitsNonZero(t *testing.T) {
	t.Run("маркер паники", func(t *testing.T) {
		dir := t.TempDir()
		craftCliSession(t, dir, true)
		hdr, frames, err := world.ReadPortionFrames(dir, 1)
		if err != nil {
			t.Fatalf("ReadPortionFrames: %v", err)
		}
		res, err := world.Replay(hdr, frames, nil, emptyGeo)
		if err != nil {
			t.Fatalf("Replay: %v", err)
		}
		if !res.StoppedAtPanic {
			t.Fatal("сессия без маркера — кейс не о том")
		}
		expect := filepath.Join(dir, "expect.dump")
		if err := os.WriteFile(expect, res.Dump, 0o644); err != nil {
			t.Fatal(err)
		}
		err = run([]string{"-replay", dir, "-artifact", synthArtifact, "-expect", expect})
		if err == nil {
			t.Fatal("неполный реплей с совпадающим -expect прошёл; want ненулевой выход")
		}
		if s := err.Error(); strings.Contains(s, "офсете") {
			t.Fatalf("диагностика сверки при неполном результате: %s", s)
		}
	})
	t.Run("оборванный хвост", func(t *testing.T) {
		dir := t.TempDir()
		craftCliSession(t, dir, false)
		expect := filepath.Join(dir, "expect.dump")
		if err := os.WriteFile(expect, []byte{0}, 0o644); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "portion-1-1.log")
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(path, info.Size()-2); err != nil {
			t.Fatal(err)
		}
		err = run([]string{"-replay", dir, "-artifact", synthArtifact, "-expect", expect})
		if err == nil {
			t.Fatal("оборванная цепочка прошла; want ненулевой выход")
		}
		// сверка не выполняется: ошибка неполноты, а не чтения/расхождения
		if s := err.Error(); strings.Contains(s, "офсете") || strings.Contains(s, "expect.dump") {
			t.Fatalf("диагностика сверки при неполном результате: %s", s)
		}
	})
}

// Расхождение с -expect: первый различающийся офсет, длины и команда
// перезаписи фикстуры.
func TestReplayCliExpectMismatchOffsetHint(t *testing.T) {
	dir := t.TempDir()
	craftCliSession(t, dir, false)
	hdr, frames, err := world.ReadPortionFrames(dir, 1)
	if err != nil {
		t.Fatalf("ReadPortionFrames: %v", err)
	}
	res, err := world.Replay(hdr, frames, nil, emptyGeo)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	want := append([]byte(nil), res.Dump...)
	want[len(want)/2] ^= 0xFF
	expect := filepath.Join(dir, "expect.dump")
	if err := os.WriteFile(expect, want, 0o644); err != nil {
		t.Fatal(err)
	}
	err = run([]string{"-replay", dir, "-artifact", synthArtifact, "-expect", expect})
	if err == nil {
		t.Fatal("расхождение прошло; want ненулевой выход")
	}
	for _, sub := range []string{"офсете", "перезапись фикстуры"} {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("ошибка без %q: %s", sub, err)
		}
	}
}

// Полный результат без -expect — дамп в stdout, журнал — в stderr.
func TestReplayCliFullResultDumpsToStdout(t *testing.T) {
	dir := t.TempDir()
	craftCliSession(t, dir, false)
	hdr, frames, err := world.ReadPortionFrames(dir, 1)
	if err != nil {
		t.Fatalf("ReadPortionFrames: %v", err)
	}
	res, err := world.Replay(hdr, frames, nil, emptyGeo)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		got []byte
		err error
	}
	readDone := make(chan result, 1)
	go func() { // читатель параллелен писателю: дамп больше буфера пайпа не вешает тест
		got, err := io.ReadAll(r)
		readDone <- result{got, err}
	}()
	old := os.Stdout
	os.Stdout = w
	runErr := run([]string{"-replay", dir, "-artifact", synthArtifact})
	w.Close()
	os.Stdout = old

	read := <-readDone
	if read.err != nil {
		t.Fatal(read.err)
	}
	got := read.got
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}
	if !bytes.Equal(got, res.Dump) {
		t.Fatalf("stdout = %d байт; want дамп %d байт (без примеси журнала)", len(got), len(res.Dump))
	}
}

// Злые аргументы: каждая строка — именованная ошибка, не «пустой реплей 0».
func TestReplayCliEvilArgsTable(t *testing.T) {
	emptyDir := t.TempDir()
	garbage := t.TempDir()
	if err := os.WriteFile(filepath.Join(garbage, "junk.l2a"), []byte("мусор"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		args func(t *testing.T) []string
	}{
		{"без -artifact", func(t *testing.T) []string { return []string{"-replay", emptyDir} }},
		{"каталог без сессии", func(t *testing.T) []string { return []string{"-replay", emptyDir, "-artifact", synthArtifact} }},
		{"мусорный артефакт", func(t *testing.T) []string {
			return []string{"-replay", emptyDir, "-artifact", filepath.Join(garbage, "junk.l2a")}
		}},
		{"чужой регион", func(t *testing.T) []string {
			dir := t.TempDir()
			craftCliSession(t, dir, false) // сессия региона 1
			return []string{"-replay", dir, "-artifact", synthArtifact, "-region", "2"}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			args := c.args(t)
			if err := run(args); err == nil {
				t.Fatalf("run(%v) прошёл молча; want ошибка", args)
			}
		})
	}
}
