// l2client — тонкая обёртка headless-клиента: полный флоу логина и входа
// против заданного LoginServer, трафик-лог — stdout, служебные события —
// slog в stderr.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/udisondev/l2go/internal/l2client"
)

func main() {
	addr := flag.String("addr", "", "адрес LoginServer (host:port)")
	account := flag.String("account", "", "имя аккаунта")
	serverID := flag.Int("server", 1, "ID игрового сервера")
	slot := flag.Int("char", 0, "слот персонажа")
	verbose := flag.Bool("v", false, "подробный slog (stderr)")
	flag.Parse()
	if *verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}
	pass := os.Getenv("L2CLIENT_PASSWORD")
	if *addr == "" || *account == "" || pass == "" {
		fmt.Fprintln(os.Stderr, "нужны -addr, -account и env L2CLIENT_PASSWORD")
		os.Exit(2)
	}

	ctx := context.Background()
	lc, err := l2client.DialLogin(ctx, *addr, l2client.Options{Traffic: os.Stdout})
	if err != nil {
		slog.Error("логин-подключение", "err", err)
		os.Exit(1)
	}
	var ep l2client.GameEndpoint
	stages := []struct {
		name string
		call func() error
	}{
		{"хендшейк логина", lc.Handshake},
		{"логин", func() error { return lc.Login(*account, pass) }},
		{"список серверов", func() error { _, _, err := lc.ServerList(); return err }},
		{"выбор сервера", func() error {
			e, err := lc.SelectServer(byte(*serverID))
			ep = e
			return err
		}},
	}
	for _, st := range stages {
		if err := st.call(); err != nil {
			slog.Error(st.name, "err", err)
			os.Exit(1)
		}
	}
	if err := lc.Close(); err != nil {
		slog.Error("закрытие логин-соединения", "err", err)
	}

	gc, err := l2client.DialGame(ctx, ep.Addr, l2client.Options{Traffic: os.Stdout})
	if err != nil {
		slog.Error("game-подключение", "err", err)
		os.Exit(1)
	}
	if err := gc.Handshake(); err != nil {
		slog.Error("game-хендшейк", "err", err)
		os.Exit(1)
	}
	if _, err := gc.Auth(ep, *account); err != nil {
		slog.Error("вход", "err", err)
		os.Exit(1)
	}
	if err := gc.SelectChar(int32(*slot)); err != nil {
		slog.Error("выбор персонажа", "err", err)
		os.Exit(1)
	}
	runErr := make(chan error, 1)
	go func() { runErr <- gc.Run(ctx) }()
	if err := gc.Logout(); err != nil {
		slog.Error("выход", "err", err)
		os.Exit(1)
	}
	if err := <-runErr; err != nil {
		slog.Error("стационарная фаза", "err", err)
		os.Exit(1)
	}
}
