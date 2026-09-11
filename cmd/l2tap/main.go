// l2tap — сбор и разбор живого трафика L2 Interlude.
//
// Capture: слушает пары listen=upstream и зеркалирует трафик в бинарный журнал.
//
//	l2tap -login-map 127.0.0.1:2106=server:2106 -map 127.0.0.1:7777=server:7777 -out session.tap
//
// Decode: журнал → читаемый лог и/или фикстуры.
//
//	l2tap -decode session.tap -out-log traffic.txt -out-fixtures fixtures.json
//
// Журнал и читаемый лог содержат чувствительные данные сессии (чат, имена,
// ключи сессии, шифротекст учётных данных) — хранить как секрет.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"

	"github.com/udisondev/l2go/internal/tap"
)

// maps — повторяемый флаг пар listen=upstream; login-пара помечается для
// rewrite-ветки (подмена адреса ServerList).
type maps struct {
	rows  []tap.Map
	login bool
}

func (m *maps) String() string { return fmt.Sprint(m.rows) }

func (m *maps) Set(v string) error {
	listen, upstream, ok := strings.Cut(v, "=")
	if !ok || listen == "" || upstream == "" {
		return fmt.Errorf("пара %q: нужен вид listen=upstream", v)
	}
	m.rows = append(m.rows, tap.Map{Listen: listen, Upstream: upstream, Login: m.login})
	return nil
}

func main() {
	var (
		capture  maps
		logins   = maps{login: true}
		out      = flag.String("out", "", "файл журнала capture (обязателен для capture)")
		rewrite  = flag.Bool("rewrite", true, "подмена адреса ServerList на слушателя game-ноги")
		decode   = flag.String("decode", "", "файл журнала для разбора вместо capture")
		outLog   = flag.String("out-log", "", "файл читаемого лога (decode)")
		outFixts = flag.String("out-fixtures", "", "файл фикстур JSON (decode)")
		verbose  = flag.Bool("v", false, "детальный slog")
	)
	flag.Var(&capture, "map", "пара listen=upstream (повторяемый)")
	flag.Var(&logins, "login-map", "login-пара listen=upstream (rewrite-ветка)")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	if *decode != "" {
		if err := runDecode(*decode, *outLog, *outFixts); err != nil {
			slog.Error("decode", "err", err)
			os.Exit(1)
		}
		return
	}

	all := append(logins.rows, capture.rows...)
	if len(all) == 0 {
		slog.Error("нет пар listen=upstream: -map/-login-map обязательны")
		os.Exit(2)
	}
	if *out == "" {
		slog.Error("capture требует -out <файл журнала>")
		os.Exit(2)
	}
	f, err := os.Create(*out)
	if err != nil {
		slog.Error("журнал", "err", err)
		os.Exit(1)
	}
	defer func() {
		if err := f.Close(); err != nil {
			slog.Error("закрытие журнала", "err", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := tap.Run(ctx, tap.Options{Maps: all, Log: f, Rewrite: *rewrite}); err != nil {
		slog.Error("capture", "err", err)
		os.Exit(1)
	}
}

func runDecode(journal, outLog, outFixts string) error {
	raw, err := os.ReadFile(journal)
	if err != nil {
		return fmt.Errorf("чтение журнала: %w", err)
	}
	var logW, fixtW io.Writer
	if outLog != "" {
		f, err := os.Create(outLog)
		if err != nil {
			return fmt.Errorf("лог: %w", err)
		}
		defer f.Close() // вывод уже завершён к моменту закрытия
		logW = f
	}
	if outFixts != "" {
		f, err := os.Create(outFixts)
		if err != nil {
			return fmt.Errorf("фикстуры: %w", err)
		}
		defer f.Close() // аналогично логу
		fixtW = f
	}
	if logW == nil && fixtW == nil {
		logW = os.Stdout
	}
	return tap.Decode(strings.NewReader(string(raw)), tap.DecodeOptions{Log: logW, Fixtures: fixtW})
}
