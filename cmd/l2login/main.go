// l2login — LoginServer: слушатель login-флоу (:2106) и mTLS-стык login↔game
// (:9011). Подкоманды: cert — генерация mTLS-материала стыка; account —
// создание аккаунта (пароль из env L2LOGIN_PASSWORD, только при останованном
// сервере — кэш живого LS файлы не перечитывает); gs — фиктивная регистрация
// игрового сервера (исполнитель ручной контрольной точки: реальный клиент
// видит сервер в ServerList и получает PlayOk).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/udisondev/l2go/internal/login"
	"github.com/udisondev/l2go/internal/loginlink"
	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/pkg/mtls"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("l2login: завершение с ошибкой", "err", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "cert":
			return runCert(args[1:])
		case "account":
			return runAccount(args[1:])
		case "gs":
			return runGS(args[1:])
		}
	}
	return runServer(args)
}

func setupLog(verbose bool) {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}

func runServer(args []string) error {
	fs := flag.NewFlagSet("l2login", flag.ExitOnError)
	persistRoot := fs.String("persist", "var/persist", "каталог персиста аккаунтов")
	addr := fs.String("addr", ":2106", "адрес слушателя login-флоу")
	link := fs.String("link", "127.0.0.1:9011", "адрес стыка login↔game (mTLS)")
	tlsDir := fs.String("tls", "var/tls", "каталог mTLS-материала стыка (генерация: l2login cert)")
	autoCreate := fs.Bool("auto-create", true, "авто-создание аккаунтов при первом входе")
	maxConns := fs.Int("max-conns", 256, "лимит одновременных коннектов")
	handshake := fs.Duration("handshake", 15*time.Second, "абсолютный дедлайн фазы хендшейка")
	idle := fs.Duration("idle", 5*time.Minute, "дедлайн полного кадра после LoginOk")
	sessionTTL := fs.Duration("session-ttl", 5*time.Minute, "TTL сессий стыка")
	drain := fs.Duration("drain", 10*time.Second, "таймаут дрена коннектов при остановке")
	grpcGrace := fs.Duration("grpc-grace", 5*time.Second, "таймаут GracefulStop стыка")
	fs.Parse(args)
	setupLog(false)

	// «0 = без лимита» запрещён (F32): нулевые значения — ошибка старта.
	for _, zero := range []struct {
		name string
		d    time.Duration
	}{
		{"-drain", *drain},
		{"-grpc-grace", *grpcGrace},
		{"-session-ttl", *sessionTTL},
	} {
		if zero.d <= 0 {
			return fmt.Errorf("%s = %s: нулевые таймауты запрещены", zero.name, zero.d)
		}
	}

	tlsCfg, err := mtls.ServerConfig(
		filepath.Join(*tlsDir, mtls.CAFile),
		filepath.Join(*tlsDir, mtls.ServerCertFile),
		filepath.Join(*tlsDir, mtls.ServerKeyFile))
	if err != nil {
		return fmt.Errorf("mTLS-материал стыка: %w (генерация: l2login cert -out %s)", err, *tlsDir)
	}
	accounts, err := persist.OpenAccounts(*persistRoot, *autoCreate)
	if err != nil {
		return fmt.Errorf("персист %s: %w", *persistRoot, err)
	}
	sessions := loginlink.NewSessions(*sessionTTL)
	linkSrv := loginlink.NewServer(sessions)

	linkGS := grpc.NewServer(loginlink.GRPCServerOptions(tlsCfg)...)
	loginlink.RegisterLoginLinkServer(linkGS, linkSrv)
	linkLn, err := net.Listen("tcp", *link)
	if err != nil {
		return fmt.Errorf("стык %s: %w", *link, err)
	}
	go func() { _ = linkGS.Serve(linkLn) }()

	loginSrv, err := login.New(login.Config{
		MaxConns:         *maxConns,
		HandshakeTimeout: *handshake,
		IdleTimeout:      *idle,
	}, accounts, sessions, linkSrv)
	if err != nil {
		return err
	}
	loginLn, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("слушатель %s: %w", *addr, err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- loginSrv.Serve(loginLn) }()
	slog.Info("l2login: слушает", "login", *addr, "link", *link,
		"persist", *persistRoot, "auto_create", *autoCreate)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}
	// Порядок остановки (F3): листенер и дрен коннектов → закрытие потоков
	// регистрации → GracefulStop под таймаутом (вечные потоки иначе вешают).
	loginSrv.Close(*drain)
	linkSrv.ShutdownStreams()
	gracefulStop(linkGS, *grpcGrace)
	slog.Info("l2login: остановлен")
	return nil
}

// gracefulStop останавливает grpc-сервер под таймаутом: GracefulStop блокирует
// до конца всех RPC; потоки регистрации уже закрыты ShutdownStreams, таймаут
// страхует зависшие вызовы.
func gracefulStop(gs *grpc.Server, timeout time.Duration) {
	stopped := make(chan struct{})
	go func() {
		gs.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(timeout):
		slog.Warn("l2login: GracefulStop стыка исчерпан — жёсткий Stop")
		gs.Stop()
	}
}

func runCert(args []string) error {
	fs := flag.NewFlagSet("l2login cert", flag.ExitOnError)
	out := fs.String("out", "var/tls", "каталог материала")
	serverNames := fs.String("server-name", "", "доп. SAN серверного сертификата через запятую (для remote-GS)")
	fs.Parse(args)
	setupLog(false)

	var extra []string
	for _, name := range strings.Split(*serverNames, ",") {
		if name = strings.TrimSpace(name); name != "" {
			extra = append(extra, name)
		}
	}
	m, err := mtls.GenerateMaterial("l2go loginlink", extra)
	if err != nil {
		return err
	}
	if err := mtls.WriteMaterial(*out, m); err != nil {
		return fmt.Errorf("%w (существующий материал не перезаписывается — ротация удалением каталога)", err)
	}
	slog.Info("l2login: mTLS-материал стыка готов", "dir", *out,
		"files", strings.Join([]string{mtls.CAFile, mtls.ServerCertFile, mtls.ServerKeyFile,
			mtls.ClientCertFile, mtls.ClientKeyFile}, ", "))
	return nil
}

func runAccount(args []string) error {
	fs := flag.NewFlagSet("l2login account", flag.ExitOnError)
	name := fs.String("login", "", "имя аккаунта")
	persistRoot := fs.String("persist", "var/persist", "каталог персиста")
	fs.Parse(args)
	setupLog(false)

	if *name == "" {
		return errors.New("укажите -login")
	}
	password := os.Getenv("L2LOGIN_PASSWORD")
	if password == "" {
		return errors.New("пароль задайте переменной окружения L2LOGIN_PASSWORD (не флагом и не аргументом)")
	}
	// Только при останованном LS: кэш живого сервера не перечитывает файлы
	// (межпроцессной инвалидации нет).
	accounts, err := persist.OpenAccounts(*persistRoot, false)
	if err != nil {
		return fmt.Errorf("персист %s: %w", *persistRoot, err)
	}
	if err := accounts.Create(*name, password); err != nil {
		return err
	}
	slog.Info("l2login: аккаунт создан", "login", strings.ToLower(*name), "root", *persistRoot)
	return nil
}

// demoHexID — фиксированный идентификатор фиктивной регистрации: повторный
// запуск подкоманды идемпотентно заменяет запись, не плодя GS в ServerList.
var demoHexID = []byte("demo-gs")

func runGS(args []string) error {
	fs := flag.NewFlagSet("l2login gs", flag.ExitOnError)
	link := fs.String("link", "127.0.0.1:9011", "адрес стыка LS (хост сверяется с SAN серверного сертификата)")
	host := fs.String("host", "127.0.0.1", "адрес GS для ServerList: IP-литерал (имена не резолвятся)")
	port := fs.Int("port", 7777, "порт GS для ServerList")
	name := fs.String("name", "Bartz", "имя мира (журналы)")
	tlsDir := fs.String("tls", "var/tls", "каталог mTLS-материала (клиентские креды)")
	fs.Parse(args)
	setupLog(false)

	// Имя сервера TLS = хост стыка: пиннинг следует за адресом (для remote-GS
	// серт генерится с -server-name тем же именем).
	linkHost, _, err := net.SplitHostPort(*link)
	if err != nil {
		return fmt.Errorf("адрес стыка %s: %w", *link, err)
	}
	if linkHost == "" {
		linkHost = "localhost"
	}
	tlsCfg, err := mtls.ClientConfig(
		filepath.Join(*tlsDir, mtls.CAFile),
		filepath.Join(*tlsDir, mtls.ClientCertFile),
		filepath.Join(*tlsDir, mtls.ClientKeyFile),
		linkHost)
	if err != nil {
		return fmt.Errorf("mTLS-материал стыка: %w (генерация: l2login cert -out %s)", err, *tlsDir)
	}
	c, err := loginlink.Dial(loginlink.ClientConfig{
		Addr:  *link,
		HexID: demoHexID,
		Host:  *host,
		Port:  int32(*port),
		Name:  *name,
		TLS:   tlsCfg,
	})
	if err != nil {
		return err
	}
	defer c.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
	defer wcancel()
	if err := c.WaitRegistered(wctx); err != nil {
		return fmt.Errorf("регистрация на LS: %w (сервер запущен? материал: l2login cert -out %s)", err, *tlsDir)
	}
	slog.Info("l2login: фиктивный GS зарегистрирован — держу до сигнала",
		"link", *link, "name", *name, "hexid", string(demoHexID))
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return nil
	}
}
