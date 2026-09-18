// Команда l2go — точка входа игрового сервера L2 Interlude: полный контур
// (артефакт статики, метроном, регион-актор, персист-актор, энкод-стейдж,
// провода conn, шлюз, стык loginlink) с graceful shutdown. Игровой слушатель
// вяжется до регистрации (порт нужен объявлению LS), приём открывается только
// после WaitRegistered — fail-closed стыка (P3.4).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/udisondev/l2go/internal/artifact"
	"github.com/udisondev/l2go/internal/conn"
	"github.com/udisondev/l2go/internal/encode"
	"github.com/udisondev/l2go/internal/gateway"
	"github.com/udisondev/l2go/internal/loginlink"
	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/transport"
	"github.com/udisondev/l2go/internal/world"
	"github.com/udisondev/l2go/pkg/mtls"
)

// Таймауты и лимиты — константами дефолтов (ручные флаги не разводим:
// конфиги пакетов валидируются, прецедент — l2login).
const (
	handshakeTimeout = 15 * time.Second // accept→AuthLogin (абсолютный)
	presessionIdle   = 60 * time.Second // перезавод полным кадром
	writeTimeout     = 5 * time.Second
	keepAlive        = 30 * time.Second
	frameCapBytes    = 8192
	perConnEvents    = 4
	inboxCap         = 64
	drainCap         = 8
	persistTimeout   = 5 * time.Second  // шлюз: ожидание ответа персиста
	persistDrain     = 10 * time.Second // персист: финальный дрен (T3)
	linkRegisterTO   = 15 * time.Second
	panicLimit       = 3
	portionMaxFile   = 64 << 20
	fafCap           = 64
	// stageByteCap — байтовый кап очереди одного клиента стейджа; guard
	// решения 12: покрывает батч слитка входа с запасом.
	stageByteCap = 256 << 10
	regionID     = world.RegionID(1)
	gameName     = "l2go"
)

// gameHexID — идентификатор этого GS на стыке (единственный сервер владельца;
// смена — правкой константы до первого старта).
var gameHexID = []byte("l2go-interlude-gs")

// config — конфигурация контура: флаги run() или харнесс тестов (package main).
type config struct {
	Addr           string // слушатель игровой ноги (":0" — тесты)
	Host           string // адрес игры для ServerList; пусто — хост слушателя
	Hz             int
	PersistDir     string
	ArtifactPath   string
	PortionsDir    string
	LinkAddr       string // gRPC-стык LoginServer (mTLS)
	TLSDir         string
	MaxConns       int
	GraceTicks     int
	SaveRetryTicks int
	// NPCCenterX/Y, NPCRadius — срез разворачивания NPC-населения P3.10:
	// центр по умолчанию — стартовая точка новичка (КТ-1: географию среза
	// владелец утверждает по гео-покрытию).
	NPCCenterX int32
	NPCCenterY int32
	NPCRadius  int32
	// RegisterTimeout — бюджет регистрации на LS (харнесс ускоряет).
	RegisterTimeout time.Duration
}

// server — поднятый контур. shutdown останавливает ступенчато (решение 12):
// стоп приёма → стык/шлюз/провода → регион (сохранитель — в его Run) →
// персист (дрен дорабатывает письма) → метроном.
type server struct {
	cfg    config
	reg    *transport.Registry
	metro  *world.Metronome
	region *world.Region
	actor  *persist.Actor
	stage  *encode.Stage
	connS  *conn.Server
	gw     *gateway.Gateway
	link   *loginlink.Client

	ln   net.Listener
	addr string // фактический адрес игровой ноги

	wireStop    context.CancelFunc // стык + шлюз + провода
	worldStop   context.CancelFunc // регион
	persistStop context.CancelFunc
	metroStop   context.CancelFunc

	wireDone    chan struct{}
	regionDone  chan struct{}
	persistDone chan struct{}
	linkDone    chan struct{}
	unsub       []func()
	stopOnce    sync.Once
}

// bootstrap поднимает контур: метроном → персист → регион → стейдж → провода →
// слушатель (bind) → стык → шлюз → горутины → WaitRegistered → приём. Ошибка
// посреди подъёма сворачивает уже поднятое.
func bootstrap(cfg config) (*server, error) {
	switch {
	case cfg.ArtifactPath == "":
		return nil, fmt.Errorf("l2go: артефакт статики обязателен")
	case cfg.Hz <= 0:
		return nil, fmt.Errorf("l2go: Hz = %d: нулевые лимиты запрещены", cfg.Hz)
	case cfg.MaxConns <= 0:
		return nil, fmt.Errorf("l2go: MaxConns = %d: нулевые лимиты запрещены", cfg.MaxConns)
	}

	// Статика — только артефактом: XML-исходников на рантайм-пути нет.
	static, gm, meta, _, err := artifact.LoadFile(cfg.ArtifactPath)
	if err != nil {
		return nil, fmt.Errorf("l2go: артефакт %s: %w", cfg.ArtifactPath, err)
	}
	slog.Info("l2go: статика артефактом",
		"items", meta.Items, "npcs", meta.Npcs, "spawns", meta.Spawns, "geoRegions", meta.Regions)

	wcfg := world.DefaultConfig()
	wcfg.Hz = cfg.Hz
	wcfg.GraceTicks = cfg.GraceTicks
	wcfg.SaveRetryTicks = cfg.SaveRetryTicks
	metro, err := world.NewMetronome(wcfg)
	if err != nil {
		return nil, fmt.Errorf("l2go: метроном: %w", err)
	}
	reg := transport.NewRegistry(fafCap)
	persistBell, un1 := metro.Subscribe()
	actor, err := persist.New(persist.Config{
		Dir:          cfg.PersistDir,
		DrainTimeout: persistDrain,
		PanicLimit:   panicLimit,
	}, reg, persistBell)
	if err != nil {
		un1()
		return nil, fmt.Errorf("l2go: персист: %w", err)
	}
	plog, err := world.NewPortionLog(cfg.PortionsDir, regionID,
		time.Second/time.Duration(cfg.Hz), false, portionMaxFile)
	if err != nil {
		un1()
		return nil, fmt.Errorf("l2go: лог порций: %w", err)
	}
	stage, err := encode.NewStage(stageByteCap)
	if err != nil {
		un1()
		return nil, fmt.Errorf("l2go: стейдж: %w", err)
	}
	region, err := world.NewRegion(metro, reg, regionID, wcfg, plog, stage, gm)
	if err != nil {
		un1()
		return nil, fmt.Errorf("l2go: регион: %w", err)
	}
	connS, err := conn.New(conn.Config{
		MaxConns:         cfg.MaxConns,
		HandshakeTimeout: handshakeTimeout,
		IdleTimeout:      presessionIdle,
		WriteTimeout:     writeTimeout,
		KeepAlive:        keepAlive,
		FrameCap:         frameCapBytes,
		EventQueue:       4 * cfg.MaxConns,
		PerConnEvents:    perConnEvents,
	}, gateway.StageOutbounds{Stage: stage})
	if err != nil {
		un1()
		return nil, fmt.Errorf("l2go: провода: %w", err)
	}

	// Стык вяжется до шлюза (валидатор — клиент стыка), слушатель — до Dial
	// (порт нужен объявлению LS).
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		un1()
		return nil, fmt.Errorf("l2go: слушатель %s: %w", cfg.Addr, err)
	}
	host := announceHost(cfg.Host, ln)
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	tlsCfg, err := mtls.ClientConfig(
		filepath.Join(cfg.TLSDir, mtls.CAFile),
		filepath.Join(cfg.TLSDir, mtls.ClientCertFile),
		filepath.Join(cfg.TLSDir, mtls.ClientKeyFile), "localhost")
	if err != nil {
		_ = ln.Close()
		un1()
		return nil, fmt.Errorf("l2go: mTLS-материал стыка: %w (генерация: l2login cert -out %s)", err, cfg.TLSDir)
	}
	link, err := loginlink.Dial(loginlink.ClientConfig{
		Addr:  cfg.LinkAddr,
		HexID: gameHexID,
		Host:  host,
		Port:  int32(mustPort(port)),
		Name:  gameName,
		TLS:   tlsCfg,
	})
	if err != nil {
		_ = ln.Close()
		un1()
		return nil, fmt.Errorf("l2go: стык: %w", err)
	}
	gwBell, un2 := metro.Subscribe()
	gw, err := gateway.New(gateway.Config{
		Persist:        actor.ID(),
		Region:         transport.Addr{Entity: region.CtrlID()},
		PersistTimeout: persistTimeout,
		InboxCap:       inboxCap,
		DrainCap:       drainCap,
		PanicLimit:     panicLimit,
		// Спека §P3.6: канал завершений бездроповый при cap ≥ MaxConns.
		CompletionsCap: cfg.MaxConns,
	}, reg, link, connS, stage, gwBell)
	if err != nil {
		_ = link.Close()
		_ = ln.Close()
		un1()
		un2()
		return nil, fmt.Errorf("l2go: шлюз: %w", err)
	}

	// Адресаты контрольных писем региона (шлюз/персист) и whitelist
	// отправителей персиста — после создания шлюза (регион рождается раньше).
	if err := region.Wire(gw.ID(), actor.ID()); err != nil {
		_ = link.Close()
		_ = ln.Close()
		un1()
		un2()
		return nil, fmt.Errorf("l2go: Wire региона: %w", err)
	}
	if err := actor.AllowSender(gw.ID(), region.CtrlID()); err != nil {
		_ = link.Close()
		_ = ln.Close()
		un1()
		un2()
		return nil, fmt.Errorf("l2go: whitelist персиста: %w", err)
	}

	// Разворачивание NPC-населения (P3.10): письмо кладётся до старта Run —
	// применение первым шагом региона; конфиг среза едет в письме (реплей
	// воспроизводит разворот из лога порций); радиус 0 — без населения.
	if cfg.NPCRadius > 0 {
		if err := region.DeployNPCs(static, transport.NPCDeployMsg{
			CenterX: cfg.NPCCenterX, CenterY: cfg.NPCCenterY, Radius: cfg.NPCRadius,
		}); err != nil {
			_ = link.Close()
			_ = ln.Close()
			un1()
			un2()
			return nil, fmt.Errorf("l2go: разворот NPC: %w", err)
		}
	}

	s := &server{
		cfg: cfg, reg: reg, metro: metro, region: region, actor: actor,
		stage: stage, connS: connS, gw: gw, link: link,
		ln: ln, addr: ln.Addr().String(),
		wireDone:    make(chan struct{}),
		regionDone:  make(chan struct{}),
		persistDone: make(chan struct{}),
		linkDone:    make(chan struct{}),
		unsub:       []func(){un1, un2},
	}
	var metroCtx, persistCtx, worldCtx, wireCtx context.Context
	metroCtx, s.metroStop = context.WithCancel(context.Background())
	persistCtx, s.persistStop = context.WithCancel(metroCtx)
	worldCtx, s.worldStop = context.WithCancel(context.Background())
	wireCtx, s.wireStop = context.WithCancel(context.Background())

	go s.metro.Run(metroCtx)
	go func() { defer close(s.persistDone); s.actor.Run(persistCtx) }()
	go func() { defer close(s.regionDone); s.region.Run(worldCtx) }()
	go func() { defer close(s.linkDone); _ = s.link.Run(wireCtx) }()
	go func() { defer close(s.wireDone); s.gw.Run(wireCtx) }()

	// Приём открывается ТОЛЬКО после регистрации (fail-closed стыка, P3.4):
	// незарегистрированный GS не принимает ни одного коннекта.
	regTO := cfg.RegisterTimeout
	if regTO <= 0 {
		regTO = linkRegisterTO
	}
	regCtx, cancel := context.WithTimeout(context.Background(), regTO)
	defer cancel()
	if err := s.link.WaitRegistered(regCtx); err != nil {
		s.shutdown()
		return nil, fmt.Errorf("l2go: регистрация на LS %s: %w", cfg.LinkAddr, err)
	}
	go func() { _ = s.connS.Serve(ln) }()
	slog.Info("l2go: контур поднят", "addr", s.addr, "hz", cfg.Hz, "link", cfg.LinkAddr)
	return s, nil
}

// gameAddr — фактический адрес игровой ноги.
func (s *server) gameAddr() string { return s.addr }

// shutdown — ступенчатая остановка; синхронная (до выхода персист-дрейна),
// идемпотентная (повтор — no-op).
func (s *server) shutdown() {
	s.stopOnce.Do(func() { s.stop() })
}

func (s *server) stop() {
	_ = s.ln.Close() // стоп приёма
	s.wireStop()     // стык (снятие регистрации), шлюз, провода
	_ = s.link.Close()
	s.connS.Close()
	<-s.wireDone
	<-s.linkDone
	s.worldStop() // регион: финальный сохранитель — в его Run
	<-s.regionDone
	s.persistStop() // персист: дрен дорабатывает письма в бюджете
	<-s.persistDone
	s.metroStop()
	for _, un := range s.unsub {
		un()
	}
}

// announceHost — адрес игры для ServerList: флаг, иначе хост слушателя
// (wildcard-бинд анонсируется loopback-адресом).
func announceHost(cfgHost string, ln net.Listener) string {
	if cfgHost != "" {
		return cfgHost
	}
	if h, _, err := net.SplitHostPort(ln.Addr().String()); err == nil &&
		h != "" && h != "::" && h != "0.0.0.0" && h != "[::]" {
		return h
	}
	return "127.0.0.1"
}

func mustPort(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return n
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("l2go: завершение с ошибкой", "err", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("l2go", flag.ExitOnError)
	addr := fs.String("addr", ":7777", "адрес слушателя игровой ноги")
	host := fs.String("host", "", "адрес игры для ServerList (пусто — хост слушателя)")
	hz := fs.Int("hz", 10, "частота метронома")
	persistDir := fs.String("persist", "var/persist", "каталог персиста")
	artifactPath := fs.String("artifact", "", "путь к артефакту статики (обязателен)")
	portionsDir := fs.String("portions", "var/portions", "каталог лога порций")
	link := fs.String("link", "127.0.0.1:9011", "адрес gRPC-стыка LoginServer (mTLS)")
	tlsDir := fs.String("tls", "var/tls", "каталог mTLS-материала стыка")
	maxConns := fs.Int("max-conns", 20000, "лимит одновременных коннектов")
	npcCenter := fs.String("npc-center", "", "центр среза NPC \"x,y\" (пусто — стартовая точка новичка)")
	npcRadius := fs.Int("npc-radius", 20000, "радиус среза NPC вокруг центра; 0 — без NPC-населения")
	fs.Parse(args)
	setupLog()

	cx, cy := int32(persist.HumanFighter.StartX), int32(persist.HumanFighter.StartY)
	if *npcCenter != "" {
		parts := strings.SplitN(*npcCenter, ",", 2)
		if len(parts) != 2 {
			return fmt.Errorf("l2go: -npc-center %q: нужен формат \"x,y\" int32", *npcCenter)
		}
		x, errX := strconv.ParseInt(parts[0], 10, 32)
		y, errY := strconv.ParseInt(parts[1], 10, 32)
		if errX != nil || errY != nil {
			return fmt.Errorf("l2go: -npc-center %q: x/y вне домена int32: %v / %v", *npcCenter, errX, errY)
		}
		cx, cy = int32(x), int32(y)
	}
	if *npcRadius < 0 || int64(*npcRadius) > math.MaxInt32 {
		return fmt.Errorf("l2go: -npc-radius %d: вне домена [0, %d]", *npcRadius, math.MaxInt32)
	}

	srv, err := bootstrap(config{
		Addr: *addr, Host: *host, Hz: *hz,
		PersistDir: *persistDir, ArtifactPath: *artifactPath,
		PortionsDir: *portionsDir, LinkAddr: *link, TLSDir: *tlsDir,
		MaxConns:       *maxConns,
		GraceTicks:     world.DefaultGraceTicks,
		SaveRetryTicks: world.DefaultSaveRetryTicks,
		NPCCenterX:     cx,
		NPCCenterY:     cy,
		NPCRadius:      int32(*npcRadius),
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	slog.Info("l2go: остановка по сигналу")
	srv.shutdown()
	return nil
}

func setupLog() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
}
