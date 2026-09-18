// E2E-контур l2go: полный сервер in-process (реальная login-нога: login.New +
// стык loginlink сервер/клиент с mTLS во временном каталоге; game-нога — тот
// же bootstrap, что и run()) на 127.0.0.1:0. Красная фаза P3.7: ассерты зон
// приёмки падают по поведению (слиток не приходит), зелень — по мере шагов S7.

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/udisondev/l2go/internal/artifact"
	"github.com/udisondev/l2go/internal/data"
	"github.com/udisondev/l2go/internal/geo"
	"github.com/udisondev/l2go/internal/l2client"
	"github.com/udisondev/l2go/internal/login"
	"github.com/udisondev/l2go/internal/loginlink"
	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/transport"
	"github.com/udisondev/l2go/pkg/mtls"
)

// synthArtifact — артефакт из синтетики testdata (строится один раз на пакет;
// XML-каталогов рядом нет — фальсификация «статика только артефактом»).
var synthArtifact string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "l2go-art-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "l2go: temp артефакта:", err)
		os.Exit(1)
	}
	st, rep, err := data.Load(os.DirFS(filepath.Join("..", "..", "internal", "data", "testdata", "synth")))
	if err != nil {
		fmt.Fprintln(os.Stderr, "l2go: synth-датапак:", err)
		os.Exit(1)
	}
	if rep.HasErrors() {
		fmt.Fprintln(os.Stderr, "l2go: synth-датапак красный:", rep.Errors)
		os.Exit(1)
	}
	geoDir, err := os.MkdirTemp("", "l2go-geo-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "l2go: temp гео:", err)
		os.Exit(1)
	}
	for name, canon := range map[string]string{
		"golden_flat.l2j": "16_10.l2j",
		"golden_ml.l2j":   "17_10.l2j",
	} {
		raw, rerr := os.ReadFile(filepath.Join("..", "..", "internal", "geo", "testdata", name))
		if rerr != nil {
			fmt.Fprintln(os.Stderr, "l2go: чтение гео:", rerr)
			os.Exit(1)
		}
		if werr := os.WriteFile(filepath.Join(geoDir, canon), raw, 0o644); werr != nil {
			fmt.Fprintln(os.Stderr, "l2go: запись гео:", werr)
			os.Exit(1)
		}
	}
	gm, grep, err := geo.LoadDir(geoDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "l2go: synth-гео:", err)
		os.Exit(1)
	}
	if grep.HasErrors() {
		fmt.Fprintln(os.Stderr, "l2go: synth-гео красная:", grep.Errors)
		os.Exit(1)
	}
	out := filepath.Join(dir, "synth.l2a")
	if _, err = artifact.Build(out, st, rep, gm, grep); err != nil {
		fmt.Fprintln(os.Stderr, "l2go: сборка артефакта:", err)
		os.Exit(1)
	}
	synthArtifact = out
	code := m.Run()
	os.RemoveAll(dir)
	os.RemoveAll(geoDir)
	os.Exit(code)
}

// syncBuffer — потокобезопасный буфер трафик-лога.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitForLine — поллинг трафик-лога до появления подстроки (флаки-контроль:
// без снов-барьеров, с бюджетом).
func waitForLine(t *testing.T, out *syncBuffer, substr string, budget time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(out.String(), "\n") {
			if strings.Contains(line, substr) {
				return line
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("строка %q не появилась за %s; лог:\n%s", substr, budget, out.String())
	return ""
}

// e2eEnv — поднятый контур: LS (login-нога + стык) и GS (bootstrap).
type e2eEnv struct {
	t       *testing.T
	lsAddr  string
	gsAddr  string
	gs      *server
	persist string // каталог chars GS
}

// startE2E — полный контур с ускоренными тиками (hz) и коротким grace;
// npc=true — разворачивание NPC-населения стартовой окрестности (P3.10).
func startE2E(t *testing.T, hz, graceTicks int, npc ...bool) *e2eEnv {
	t.Helper()

	// mTLS-материал стыка (ed25519, миллисекунды).
	tlsDir := t.TempDir()
	mat, err := mtls.GenerateMaterial("l2go-e2e", []string{"127.0.0.1", "localhost"})
	if err != nil {
		t.Fatalf("mtls: %v", err)
	}
	if err := mtls.WriteMaterial(tlsDir, mat); err != nil {
		t.Fatalf("mtls write: %v", err)
	}
	lsTLS, err := mtls.ServerConfig(
		filepath.Join(tlsDir, mtls.CAFile),
		filepath.Join(tlsDir, mtls.ServerCertFile),
		filepath.Join(tlsDir, mtls.ServerKeyFile))
	if err != nil {
		t.Fatalf("mtls server: %v", err)
	}

	// LoginServer: аккаунты (авто-создание), сессии, стык, слушатель.
	accounts, err := persist.OpenAccounts(t.TempDir(), true)
	if err != nil {
		t.Fatalf("accounts: %v", err)
	}
	sessions := loginlink.NewSessions(time.Minute)
	linkSrv := loginlink.NewServer(sessions)
	gsrv := grpc.NewServer(loginlink.GRPCServerOptions(lsTLS)...)
	loginlink.RegisterLoginLinkServer(gsrv, linkSrv)
	linkLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("link listen: %v", err)
	}
	go func() { _ = gsrv.Serve(linkLn) }()
	t.Cleanup(gsrv.Stop)

	loginSrv, err := login.New(login.Config{
		MaxConns:         16,
		HandshakeTimeout: 5 * time.Second,
		IdleTimeout:      time.Minute,
	}, accounts, sessions, linkSrv)
	if err != nil {
		t.Fatalf("login.New: %v", err)
	}
	lsLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("login listen: %v", err)
	}
	go func() { _ = loginSrv.Serve(lsLn) }()
	t.Cleanup(func() { _ = lsLn.Close() })

	gsPersist := t.TempDir()
	radius := 0
	if len(npc) > 0 && npc[0] {
		radius = 20000
	}
	srv, err := bootstrap(config{
		Addr: "127.0.0.1:0", Hz: hz,
		PersistDir:     gsPersist,
		ArtifactPath:   synthArtifact,
		PortionsDir:    t.TempDir(),
		LinkAddr:       linkLn.Addr().String(),
		TLSDir:         tlsDir,
		MaxConns:       32,
		GraceTicks:     graceTicks,
		SaveRetryTicks: 2,
		NPCCenterX:     int32(persist.HumanFighter.StartX),
		NPCCenterY:     int32(persist.HumanFighter.StartY),
		NPCRadius:      int32(radius),
	})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	t.Cleanup(srv.shutdown)
	return &e2eEnv{t: t, lsAddr: lsLn.Addr().String(), gsAddr: srv.gameAddr(),
		gs: srv, persist: gsPersist}
}

// session — одна клиентская сессия до стационара (создание персонажа, вход).
type session struct {
	gc  *l2client.GameClient
	out *syncBuffer
	ctx context.Context
}

// enterWorld — логин (LS) → создание → выбор → EnterWorld → стационар (Run).
func enterWorld(t *testing.T, env *e2eEnv, user string) *session {
	t.Helper()
	ctx := t.Context()
	out := &syncBuffer{}
	lc, err := l2client.DialLogin(ctx, env.lsAddr, l2client.Options{Traffic: out})
	if err != nil {
		t.Fatalf("DialLogin: %v", err)
	}
	if err := lc.Handshake(); err != nil {
		t.Fatalf("LS handshake: %v", err)
	}
	if err := lc.Login(user, "pass-"+user); err != nil {
		t.Fatalf("Login: %v", err)
	}
	servers, _, err := lc.ServerList()
	if err != nil {
		t.Fatalf("ServerList: %v", err)
	}
	if len(servers) == 0 {
		t.Fatal("ServerList пуст (GS не зарегистрирован)")
	}
	ep, err := lc.SelectServer(servers[0].ID)
	if err != nil {
		t.Fatalf("SelectServer: %v", err)
	}
	if err := lc.Close(); err != nil {
		t.Fatalf("закрытие LS-коннекта: %v", err)
	}

	gc, err := l2client.DialGame(ctx, ep.Addr, l2client.Options{Traffic: out})
	if err != nil {
		t.Fatalf("DialGame: %v", err)
	}
	if err := gc.Handshake(); err != nil {
		t.Fatalf("GS handshake: %v", err)
	}
	chars, err := gc.Auth(ep, user)
	if err != nil {
		t.Fatalf("Auth: %v", err)
	}
	if len(chars) == 0 {
		if _, err := gc.CreateChar(protocol.CharacterCreateData{
			Name: charName(user), Race: 0, Sex: 0, ClassID: 0,
			Int: 11, Str: 40, Con: 43, Men: 25, Dex: 30, Wit: 11,
			HairStyle: 0, HairColor: 0, Face: 0,
		}); err != nil {
			t.Fatalf("CreateChar: %v", err)
		}
	}
	if err := gc.SelectChar(0); err != nil {
		t.Fatalf("SelectChar: %v", err)
	}
	runErr := make(chan error, 1)
	go func() { runErr <- gc.Run(ctx) }()
	if err := gc.EnterWorld(); err != nil {
		t.Fatalf("EnterWorld: %v", err)
	}
	s := &session{gc: gc, out: out, ctx: ctx}
	// Run завершается на Logout/LeaveWorld — ждём в cleanup с бюджетом.
	t.Cleanup(func() {
		select {
		case err := <-runErr:
			if err != nil {
				t.Logf("Run: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Errorf("Run не завершился после сессии %s", user)
		}
	})
	return s
}

// charName — детерминированное имя персонажа пользователя (домен alnum ASCII).
func charName(user string) string {
	name := "Bot" + strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return -1
		}
	}, user)
	if len(name) > 16 {
		name = name[:16]
	}
	return name
}

// restartGS — рестарт контура GS на тех же каталогах (LS живёт).
func (env *e2eEnv) restartGS(t *testing.T) {
	t.Helper()
	env.gs.shutdown()
	srv, err := bootstrap(env.gs.cfg)
	if err != nil {
		t.Fatalf("рестарт bootstrap: %v", err)
	}
	t.Cleanup(srv.shutdown)
	env.gs = srv
	env.gsAddr = srv.gameAddr()
}

// charFile — путь файла персонажей аккаунта GS.
func (env *e2eEnv) charFile(user string) string {
	return filepath.Join(env.persist, "chars", strings.ToLower(user)+".json")
}

// readCharsRaw — чтение файла персонажей; на Windows мгновенный rename-обмен
// назначением даёт читателю sharing-violation (errno 32) — поллинг-циклы
// waitFreshChars переживают её повтором, не ошибкой (тайминг-инвариант
// с бюджетом, не синхронизация).
func readCharsRaw(path string) ([]byte, error) {
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			return raw, nil
		}
		if errors.Is(err, syscall.Errno(32)) && time.Now().Before(deadline) {
			time.Sleep(3 * time.Millisecond)
			continue
		}
		return nil, err
	}
}

// readChars — чтение файла персонажей: конверт persist {schema,data,sha256},
// data — список записей.
func readChars(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := readCharsRaw(path)
	if err != nil {
		t.Fatalf("чтение %s: %v", path, err)
	}
	var env struct {
		Schema int             `json:"schema"`
		Data   json.RawMessage `json:"data"`
		SHA256 string          `json:"sha256"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("конверт %s: %v", path, err)
	}
	var recs []map[string]any
	if err := json.Unmarshal(env.Data, &recs); err != nil {
		t.Fatalf("записи %s: %v", path, err)
	}
	return recs
}

// writeChars — запись файла персонажей тем же конвертом (правка позиции
// между сессиями; чексумма пересчитывается).
func writeChars(t *testing.T, path string, recs []map[string]any) {
	t.Helper()
	data, err := json.Marshal(recs)
	if err != nil {
		t.Fatalf("marshal записей: %v", err)
	}
	sum := sha256.Sum256(data)
	env := struct {
		Schema int             `json:"schema"`
		Data   json.RawMessage `json:"data"`
		SHA256 string          `json:"sha256"`
	}{Schema: 1, Data: data, SHA256: hex.EncodeToString(sum[:])}
	raw, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		t.Fatalf("marshal конверта: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("запись %s: %v", path, err)
	}
}

// Зона 1: слиток входа — 16 опкодов в канонном порядке, поля позиции/имени.
func TestE2EEnterSlivok(t *testing.T) {
	env := startE2E(t, 50, 4)
	s := enterWorld(t, env, "slivok")
	line := waitForLine(t, s.out, "USER_INFO", 3*time.Second)
	if !strings.Contains(line, "name=\""+charName("slivok")+"\"") {
		t.Errorf("UserInfo без имени: %s", line)
	}
	assertSlivokOrder(t, s.out)
	log := s.out.String()
	if i := strings.Index(log, "CHAR_SELECTED"); i < 0 || i > strings.Index(log, "USER_INFO") {
		t.Errorf("CharSelected не предшествует UserInfo (index=%d)", i)
	}
}

// slivokNames — канонный порядок кадров слитка в терминах трафик-лога.
var slivokNames = []string{
	"USER_INFO", "SEND_MACRO_LIST", "ITEM_LIST", "SHORT_CUT_INIT", "HENNA_INFO",
	"QUEST_LIST", "ETC_STATUS_UPDATE", "EX_STORAGE_MAX_COUNT", "FRIEND_LIST",
	"SYSTEM_MESSAGE", "SYSTEM_MESSAGE", "SKILL_COOL_TIME", "SKILL_LIST",
	"VALIDATE_LOCATION", "ACTION_FAIL", "CLIENT_SET_TIME",
}

// assertSlivokOrder — 16 опкодов слитка в канонном порядке в логе.
func assertSlivokOrder(t *testing.T, out *syncBuffer) {
	t.Helper()
	waitForLine(t, out, "CLIENT_SET_TIME", 3*time.Second)
	pos := 0
	for _, name := range slivokNames {
		i := strings.Index(out.String()[pos:], "\n← "+name+" ")
		if i < 0 {
			t.Fatalf("кадр %s отсутствует в слитке (от смещения %d)", name, pos)
		}
		pos += i + 1
	}
}

// Зона 2: логаут — LeaveWorld последним кадром, файл обновлён, round-trip
// позиции редактированием файла между сессиями.
func TestE2ELogoutRoundTripPosition(t *testing.T) {
	env := startE2E(t, 50, 4)
	s := enterWorld(t, env, "roundtrip")
	waitForLine(t, s.out, "USER_INFO", 3*time.Second)

	if err := s.gc.Logout(); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	waitForLeaveWorld(t, s, env)
	// Сохранения сессии должны лечь до правки файла (см. waitPersistIdle).
	waitPersistIdle(t, env.gs.actor)
	// LeaveWorld — последний входящий кадр сессии (close-after-flush).
	if tail := tailAfter(t, s.out, "LEAVE_WORLD"); len(tail) != 0 {
		t.Errorf("кадры после LeaveWorld: %q", tail)
	}

	// Путь сохранения: файл отражает сессию. waitPersistIdle уже доказал,
	// что все письма сессии обработаны и записаны — маркер свежести не нужен
	// (last_seen-порог после idle гоняется с секундной границей Unix-времени,
	// F86); читаем непосредственно.
	path := env.charFile("roundtrip")
	recs := readChars(t, path)
	if len(recs) != 1 {
		t.Fatalf("персонажей в файле %d; want 1", len(recs))
	}
	if x, _ := recs[0]["x"].(float64); int(x) != -71338 {
		t.Errorf("файл после логаута: x=%v; want стартовая позиция сессии", recs[0]["x"])
	}

	// Путь восстановления: смещённая позиция в файле → в UserInfo перезахода.
	// Персист-актор — единственный писатель и держит список в кеше (P3.3):
	// правка файла видна новому процессу — перезаход идёт через рестарт
	// контура GS (заодно проверяется рестарт-персистентность).
	const wantX = -12345
	patchCharX(t, path, wantX)
	env.restartGS(t)
	s2 := enterWorld(t, env, "roundtrip")
	line := waitForLine(t, s2.out, "USER_INFO", 3*time.Second)
	if !strings.Contains(line, fmt.Sprintf("x=%d", wantX)) {
		t.Errorf("перезаход не на сохранённой позиции: %s", line)
	}
}

// waitForLeaveWorld — ожидание LeaveWorld с диагностикой контура.
func waitForLeaveWorld(t *testing.T, s *session, env *e2eEnv) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.out.String(), "LEAVE_WORLD") {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatalf("LeaveWorld не пришёл; gw=%+v region=%+v stage=%+v",
		env.gs.gw.Stats(), env.gs.region.Stats(), env.gs.stage.Stats())
}

// tailAfter — строки лога после последнего вхождения подстроки.
func tailAfter(t *testing.T, out *syncBuffer, sub string) string {
	t.Helper()
	log := out.String()
	i := strings.LastIndex(log, sub)
	if i < 0 {
		t.Fatalf("подстроки %s нет в логе", sub)
	}
	rest := log[i:]
	if j := strings.IndexByte(rest, '\n'); j >= 0 {
		rest = rest[j+1:]
	} else {
		rest = ""
	}
	return strings.TrimSpace(rest)
}

// waitPersistIdle — сохранения сессии сошлись: очередь актора пуста и
// незавершённых писем нет (письмо считается взятым до записи и отвеченным
// после — запись видна как handled > replies). Все письма сессии отправлены
// к моменту LeaveWorld, дальнейших пишущих нет: правка файла после схождения
// не перезаписывается догоняющим сохранением. Второй замер через паузу
// закрывает окно между письмами пачки (взятые подряд письма мгновенно
// держат handled == replies между ответами).
func waitPersistIdle(t *testing.T, act *persist.Actor) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if idle(act) {
			time.Sleep(5 * time.Millisecond)
			if idle(act) {
				return
			}
		}
		if !time.Now().Before(deadline) {
			st := act.Stats()
			t.Fatalf("персист не сошёлся за 3с: depth=%d handled=%d replies=%d",
				st.Depth, st.Handled, st.Replies)
		}
		time.Sleep(3 * time.Millisecond) // темп поллинга, не синхронизация
	}
}

func idle(act *persist.Actor) bool {
	st := act.Stats()
	return st.Depth == 0 && st.Handled == st.Replies
}

// waitFreshChars — поллинг файла персонажей до записи сессии (LastSeenUnix
// моложе start; атомарный rename хранилища делает чтение целым).
func waitFreshChars(t *testing.T, path string, start int64) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		recs := readChars(t, path)
		if len(recs) > 0 {
			if ls, ok := recs[0]["last_seen_unix"].(float64); ok && int64(ls) >= start {
				return recs
			}
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatalf("сохранение сессии не появилось в %s за 3с", path)
	return nil
}

// patchCharX — правка x-координаты первой записи файла персонажей.
func patchCharX(t *testing.T, path string, x int) {
	t.Helper()
	recs := readChars(t, path)
	recs[0]["x"] = x
	writeChars(t, path, recs)
}

// RequestRestart — отказ канона, коннект жив (затем Logout работает).
func TestE2ERequestRestartKeepsConn(t *testing.T) {
	env := startE2E(t, 50, 4)
	s := enterWorld(t, env, "restart")
	waitForLine(t, s.out, "USER_INFO", 3*time.Second)

	if err := s.gc.RequestRestart(); err != nil {
		t.Fatalf("RequestRestart: %v", err)
	}
	waitForLine(t, s.out, "RESTART_RESPONSE", 3*time.Second)
	if err := s.gc.Logout(); err != nil {
		t.Fatalf("Logout после рестарта: %v", err)
	}
	waitForLine(t, s.out, "LEAVE_WORLD", 3*time.Second)
}

// Кадр после деспавна — классовый дроп транспорта (агрегат deadBox), не паника.
func TestE2EFrameAfterDespawnTransportCount(t *testing.T) {
	env := startE2E(t, 50, 2)
	s := enterWorld(t, env, "straggler")
	waitForLine(t, s.out, "USER_INFO", 3*time.Second)

	if err := s.gc.Close(); err != nil {
		t.Fatalf("закрытие сокета: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && env.gs.region.Stats().Residents != 0 {
		time.Sleep(3 * time.Millisecond)
	}
	if n := env.gs.region.Stats().Residents; n != 0 {
		t.Fatalf("Residents = %d после grace; want 0", n)
	}
	// Страгглер: retired id на том же реестре (scratch-ящик), надёжное
	// письмо в него — классовый дроп транспорта (решение 10), наблюдаемый
	// дельтой агрегата deadBox на поднятом контуре.
	var scratch transport.Mailbox
	sid := env.gs.reg.Register(&scratch)
	if err := scratch.Claim(uint64(sid)); err != nil {
		t.Fatalf("Claim scratch: %v", err)
	}
	scratch.Despawn(uint64(sid))
	env.gs.reg.Retire(sid)
	_, rel0 := env.gs.reg.DeadDrops()
	env.gs.reg.Send(transport.Envelope{
		To:      transport.Addr{Entity: sid},
		FromID:  env.gs.region.CtrlID(),
		Kind:    transport.KindAggro,
		Payload: []byte{1},
	})
	_, rel1 := env.gs.reg.DeadDrops()
	if rel1-rel0 != 1 {
		t.Fatalf("deadBox reliable-дропы: %d → %d; want +1", rel0, rel1)
	}
}

// Зона 3: LinkDead — grace удерживает; перезаход в grace — одна сущность,
// файл отражает вторую сессию (старый снимок не перезаписывает новый).
func TestE2ELinkDeadGraceAndReenter(t *testing.T) {
	// Grace с запасом: перезаход идёт через полный LS+GS контур — окно в
	// тиках обязано покрывать его под -race (компенсация §4 testing.md).
	env := startE2E(t, 50, 500)
	s := enterWorld(t, env, "grace")
	waitForLine(t, s.out, "USER_INFO", 3*time.Second)

	start2 := time.Now().Unix()
	// Обрыв TCP без Logout.
	if err := s.gc.Close(); err != nil {
		t.Fatalf("закрытие сокета: %v", err)
	}

	// Перезаход в grace-окне: ровно одна сущность аккаунта.
	s2 := enterWorld(t, env, "grace")
	line := waitForLine(t, s2.out, "USER_INFO", 3*time.Second)
	if !strings.Contains(line, "name=\""+charName("grace")+"\"") {
		t.Errorf("перезаход без слитка: %s", line)
	}
	waitForResidents(t, env.gs, 1)

	// Файл отражает вторую сессию: логаут s2 → свежий LastSeenUnix.
	if err := s2.gc.Logout(); err != nil {
		t.Fatalf("Logout второй сессии: %v", err)
	}
	waitForLine(t, s2.out, "LEAVE_WORLD", 3*time.Second)
	recs := waitFreshChars(t, env.charFile("grace"), start2)
	if len(recs) != 1 {
		t.Fatalf("персонажей после второй сессии: %d; want 1", len(recs))
	}
}

// waitForResidents — поллинг населения региона до want (бюджет, без снов-
// барьеров:Residents — атомик).
func waitForResidents(t *testing.T, srv *server, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if srv.region.Stats().Residents == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Residents = %d; want %d", srv.region.Stats().Residents, want)
}

// Зона 4: TERM (ctx-отмена) — персист-файлы консистентны, все живые сохранены.
func TestCmdTermCtxPersistsConsistently(t *testing.T) {
	env := startE2E(t, 50, 4)
	s := enterWorld(t, env, "termuser")
	waitForLine(t, s.out, "USER_INFO", 3*time.Second)

	termAt := time.Now().Unix()
	env.gs.shutdown()

	path := env.charFile("termuser")
	recs := waitFreshChars(t, path, termAt)
	if len(recs) != 1 {
		t.Fatalf("TERM: персонажей в файле %d; want 1 (живой сохранён)", len(recs))
	}
	tmp, _ := filepath.Glob(filepath.Join(env.persist, "chars", "*.tmp"))
	if len(tmp) != 0 {
		t.Errorf("TERM: temp-остатки персиста: %v", tmp)
	}
}

// Структурные: контур поднимается на артефакте без XML (TestMain это
// гарантирует самим построением) и стык fail-closed.
func TestCmdLoginLinkFailClosed(t *testing.T) {
	dir := t.TempDir()
	mat, err := mtls.GenerateMaterial("l2go-fc", []string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("mtls: %v", err)
	}
	if err := mtls.WriteMaterial(dir, mat); err != nil {
		t.Fatalf("mtls write: %v", err)
	}
	// Свободный порт без LS: регистрация не состоится — bootstrap обязан
	// отказать, слушатель игры не открывается.
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("dead port: %v", err)
	}
	deadAddr := dead.Addr().String()
	_ = dead.Close()

	_, err = bootstrap(config{
		Addr: "127.0.0.1:0", Hz: 50,
		PersistDir: t.TempDir(), ArtifactPath: synthArtifact,
		PortionsDir: t.TempDir(), LinkAddr: deadAddr, TLSDir: dir,
		MaxConns: 8, GraceTicks: 2, SaveRetryTicks: 2,
		RegisterTimeout: 2 * time.Second,
	})
	if err == nil {
		t.Fatal("bootstrap прошёл без LS (fail-closed нарушен)")
	}
}

// I9: Hz доходит до метронома и слитка — ClientSetTime несёт минуты,
// вычисленные из фактического Гц (инвариант 7: без хардкода 10 Гц).
func TestCmdHzReachesMetronome(t *testing.T) {
	const hz = 7
	env := startE2E(t, hz, 4)
	s := enterWorld(t, env, "hzcheck")
	waitForLine(t, s.out, "CLIENT_SET_TIME", 3*time.Second)

	// Период метронома — из конфигурации (наблюдение контура package main).
	if got := env.gs.metro.Config().Period(); got != time.Second/hz {
		t.Errorf("период метронома = %s; want %s (Гц=%d)", got, time.Second/hz, hz)
	}
	// ClientSetTime: минуты = (tick % сутки)/минута при 7 Гц — поле кратно
	// шагу минуты 70 тиков; проверяем вычислимость из тика doneTick.
	line := waitForLine(t, s.out, "CLIENT_SET_TIME", 3*time.Second)
	if !strings.Contains(line, "hex=ec") {
		t.Fatalf("ClientSetTime не распознан: %s", line)
	}
	// минуты извлекаются из hex-хвоста лога.
	fields := strings.Split(line, "hex=")
	if len(fields) != 2 || len(fields[1]) < 18 {
		t.Fatalf("ClientSetTime hex: %s", line)
	}
	minutes := int64(le32hex(fields[1][2:10]))
	igdays := le32hex(fields[1][10:18])
	if igdays != 6 {
		t.Errorf("IG_DAYS_PER_DAY = %d; want 6", igdays)
	}
	if minutes < 0 || minutes >= 1440 {
		t.Errorf("минуты суток = %d; want 0..1439", minutes)
	}
}

// le32hex — LE-uint32 из hex-строки (первый dword после опкода).
func le32hex(h string) uint32 {
	var v uint32
	for i := 0; i+1 < len(h) && i < 8; i += 2 {
		b := hexVal(h[i])<<4 | hexVal(h[i+1])
		v |= uint32(b) << (8 * (i / 2))
	}
	return v
}

func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return 0
	}
}

// I4: утечки горутин нет — контур полностью сворачивается shutdown'ом
// (включая рестартнутые), goroutine-базовая линия восстанавливается.
func TestE2EGoroutineLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	env := startE2E(t, 50, 4)
	s := enterWorld(t, env, "leaky")
	waitForLine(t, s.out, "USER_INFO", 3*time.Second)
	if err := s.gc.Logout(); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	waitForLine(t, s.out, "LEAVE_WORLD", 3*time.Second)

	env.restartGS(t) // рестарт тоже не течёт (cleanup на месте)
	s2 := enterWorld(t, env, "leaky")
	waitForLine(t, s2.out, "USER_INFO", 3*time.Second)

	env.gs.shutdown() // явный (cleanup продублирует идемпотентно)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+5 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("горутины утекли: до=%d после≤%d, теперь=%d",
		before, before+5, runtime.NumGoroutine())
}

// TestE2EMutualVisibilityAndLogoutDelete — два клиента: взаимные CHAR_INFO
// после второго входа, DELETE_OBJECT у оставшегося после логаута второго
// (join AoI, P3.8). Молчун: у ушедшего после LEAVE_WORLD join-кадров нет.
func TestE2EMutualVisibilityAndLogoutDelete(t *testing.T) {
	env := startE2E(t, 50, 4)
	alice := enterWorld(t, env, "alice")
	waitForLine(t, alice.out, "USER_INFO", 3*time.Second)
	bob := enterWorld(t, env, "bob")
	waitForLine(t, bob.out, "USER_INFO", 3*time.Second)

	lineA := waitForLine(t, alice.out, "CHAR_INFO name=\"Botbob\"", 3*time.Second)
	obj := regexp.MustCompile(`objID=([0-9]+)`).FindStringSubmatch(lineA)
	if obj == nil {
		t.Fatalf("CHAR_INFO у Alice без objID: %s", lineA)
	}
	waitForLine(t, bob.out, "CHAR_INFO name=\"Botalice\"", 3*time.Second)

	if err := bob.gc.Logout(); err != nil {
		t.Fatalf("Logout(Bob): %v", err)
	}
	waitForLeaveWorld(t, bob, env)
	lineD := waitForLine(t, alice.out, "DELETE_OBJECT", 3*time.Second)
	if !strings.Contains(lineD, "objID="+obj[1]) {
		t.Errorf("DELETE_OBJECT с чужим objID: %s (введён был %s)", lineD, obj[1])
	}
	if tail := tailAfter(t, bob.out, "LEAVE_WORLD"); len(tail) != 0 {
		t.Errorf("кадры после LEAVE_WORLD ушедшего: %q", tail)
	}
}

// TestE2ESingleClientJoinSilence — одиночный клиент: join-кадров нет
// (регресс P3.7; тайминг-инвариант «молчун», не синхронизация).
func TestE2ESingleClientJoinSilence(t *testing.T) {
	env := startE2E(t, 50, 4)
	s := enterWorld(t, env, "lone")
	waitForLine(t, s.out, "USER_INFO", 3*time.Second)
	time.Sleep(150 * time.Millisecond) // ≥3 тиков 50 Гц без событий членства
	if txt := s.out.String(); strings.Contains(txt, "CHAR_INFO") || strings.Contains(txt, "DELETE_OBJECT") {
		t.Errorf("одиночный клиент получил join-кадры: лог содержит CHAR_INFO/DELETE_OBJECT")
	}
}

// parseCoord — числовое поле трафик-лога (x=..., y=...).
func parseCoord(t *testing.T, line, key string) int {
	t.Helper()
	m := regexp.MustCompile(key + `=(-?[0-9]+)`).FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("поле %s отсутствует: %s", key, line)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("поле %s: %v", key, err)
	}
	return n
}

// TestE2EMovementDeliveredToObserver — два клиента: движение Alice доведено
// до Bob (CharMoveToLocation с авторитетной позицией), эхо себе, StopMove на
// прибытие; перезаход на позиции прибытия (снимок с живым heading).
func TestE2EMovementDeliveredToObserver(t *testing.T) {
	env := startE2E(t, 10, 4)
	alice := enterWorld(t, env, "mova")
	lineUI := waitForLine(t, alice.out, "USER_INFO", 3*time.Second)
	ax, ay := parseCoord(t, lineUI, "x"), parseCoord(t, lineUI, "y")
	bob := enterWorld(t, env, "movb")
	waitForLine(t, bob.out, "CHAR_INFO name=\"Botmova\"", 3*time.Second)

	// движение на 100 юн восточнее (10 Гц: ~9 тиков)
	if err := alice.gc.MoveToLocation(int32(ax+100), int32(ay), -3104, int32(ax), int32(ay), -3104, 1); err != nil {
		t.Fatalf("MoveToLocation: %v", err)
	}
	echo := waitForLine(t, alice.out, "CHAR_MOVE_TO_LOCATION", 3*time.Second)
	if got := parseCoord(t, echo, "dstX"); got != ax+100 {
		t.Errorf("эхо dstX = %d; want %d (цель)", got, ax+100)
	}
	stream := waitForLine(t, bob.out, "CHAR_MOVE_TO_LOCATION", 3*time.Second)
	curX := parseCoord(t, stream, "curX")
	if curX < ax-50 || curX > ax+110 {
		t.Errorf("наблюдатель получил curX = %d; want в пределах шага от %d", curX, ax)
	}
	stop := waitForLine(t, alice.out, "STOP_MOVE", 3*time.Second)
	if got := parseCoord(t, stop, "x"); got != ax+100 {
		t.Errorf("прибытие x = %d; want %d", got, ax+100)
	}
	waitForLine(t, bob.out, "STOP_MOVE", 3*time.Second)

	// перезаход на позиции прибытия (снимок сохранения: позиция, не исходная)
	if err := alice.gc.Logout(); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	waitForLeaveWorld(t, alice, env)
	again := enterWorld(t, env, "mova")
	line2 := waitForLine(t, again.out, "USER_INFO", 3*time.Second)
	if got := parseCoord(t, line2, "x"); got != ax+100 {
		t.Errorf("перезаход x = %d; want %d (позиция прибытия)", got, ax+100)
	}
}

// TestE2EMovementObserverOutsideRadius — третий клиент вне радиуса: кадров
// движения не получает (тайминг-инвариант молчуна, не синхронизация).
// Позиция «далеко́го» — правкой файла персонажей + рестартом GS (charStore
// живого актора держит записи в памяти — правка диска видима после релоада).
func TestE2EMovementObserverOutsideRadius(t *testing.T) {
	env := startE2E(t, 10, 4)
	// подготовка «далеко́го» персонажа: создание, сохранение, правка позиции,
	// рестарт GS (charStore живого актора держит записи в памяти — правка диска
	// видима после релоада; коннектов ещё нет).
	far := enterWorld(t, env, "faraway")
	lineF := waitForLine(t, far.out, "USER_INFO", 3*time.Second)
	ax, ay := parseCoord(t, lineF, "x"), parseCoord(t, lineF, "y")
	if err := far.gc.Logout(); err != nil {
		t.Fatalf("Logout(far): %v", err)
	}
	waitForLeaveWorld(t, far, env)
	// Сохранения сессии должны лечь до правки файла (см. waitPersistIdle):
	// idle доказывает запись — читаем непосредственно, без last_seen-маркера
	// (его порог после idle гоняется с секундной границей Unix-времени, F86).
	waitPersistIdle(t, env.gs.actor)
	path := env.charFile("faraway")
	recs := readChars(t, path)
	recs[0]["x"] = ax + 10000
	recs[0]["y"] = ay
	writeChars(t, path, recs)
	env.restartGS(t)

	alice := enterWorld(t, env, "neara")
	waitForLine(t, alice.out, "USER_INFO", 3*time.Second)
	far2 := enterWorld(t, env, "faraway")
	lineFar := waitForLine(t, far2.out, "USER_INFO", 3*time.Second)
	if got := parseCoord(t, lineFar, "x"); got != ax+10000 {
		t.Fatalf("далёкий клиент вошёл на x=%d; want %d (правка файла не видна)", got, ax+10000)
	}
	if err := alice.gc.MoveToLocation(int32(ax+100), int32(ay), -3104, int32(ax), int32(ay), -3104, 1); err != nil {
		t.Fatalf("MoveToLocation: %v", err)
	}
	waitForLine(t, alice.out, "STOP_MOVE", 3*time.Second)
	time.Sleep(300 * time.Millisecond) // ≥3 тика 10 Гц: молчание далеко́го устойчиво
	if txt := far2.out.String(); strings.Contains(txt, "CHAR_MOVE_TO_LOCATION") ||
		strings.Contains(txt, "STOP_MOVE") || strings.Contains(txt, "CHAR_INFO") {
		t.Errorf("клиент вне радиуса получил кадры движения/ввода; лог:\n%s", txt)
	}
}

// TestE2ESpeedhackHeadlessSnapBack — headless-клиент шлёт ValidatePosition со
// скачком ×3: snap-back (VALIDATE_LOCATION) доставлен, серверная позиция
// авторитетна (перезаход на серверной позиции).
func TestE2ESpeedhackHeadlessSnapBack(t *testing.T) {
	env := startE2E(t, 10, 4)
	s := enterWorld(t, env, "cheat")
	lineUI := waitForLine(t, s.out, "USER_INFO", 3*time.Second)
	startX, ay := parseCoord(t, lineUI, "x"), parseCoord(t, lineUI, "y")
	for range 5 { // серия отчётов с нарастающим скачком ×3 (движения не было)
		if err := s.gc.ValidatePosition(int32(startX+900), int32(ay), -3104, 0); err != nil {
			t.Fatalf("ValidatePosition: %v", err)
		}
		time.Sleep(120 * time.Millisecond) // темп серии (нагрузка, не синхронизация)
	}
	line := waitForLine(t, s.out, "VALIDATE_LOCATION", 3*time.Second)
	if got := parseCoord(t, line, "x"); got != startX {
		t.Errorf("snap-back x = %d; want серверную %d (сущность не двигалась)", got, startX)
	}
	if err := s.gc.Logout(); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	waitForLeaveWorld(t, s, env)
	again := enterWorld(t, env, "cheat")
	line2 := waitForLine(t, again.out, "USER_INFO", 3*time.Second)
	if got := parseCoord(t, line2, "x"); got != startX {
		t.Errorf("перезаход после спидхака x = %d; want серверную %d (позиция не мутирована отчётами)",
			got, startX)
	}
}

// TestE2EMovementCoalescingOneFramePerStep — серия быстрых кликов в окне шага:
// стрим наблюдателя не превосходит кадровой частоты (одна позиция на кадр).
func TestE2EMovementCoalescingOneFramePerStep(t *testing.T) {
	env := startE2E(t, 10, 4)
	alice := enterWorld(t, env, "coala")
	lineUI := waitForLine(t, alice.out, "USER_INFO", 3*time.Second)
	ax, ay := parseCoord(t, lineUI, "x"), parseCoord(t, lineUI, "y")
	bob := enterWorld(t, env, "coalb")
	waitForLine(t, bob.out, "CHAR_INFO name=\"Botcoala\"", 3*time.Second)

	for i := range 5 { // быстрые клики: шлюз коалесит до 1 письма/конн/шаг
		if err := alice.gc.MoveToLocation(int32(ax+300+i), int32(ay), -3104, int32(ax), int32(ay), -3104, 1); err != nil {
			t.Fatalf("MoveToLocation: %v", err)
		}
	}
	time.Sleep(400 * time.Millisecond) // 4 тика 10 Гц — окно подсчёта (тайминг-инвариант)
	n := strings.Count(bob.out.String(), "CHAR_MOVE_TO_LOCATION")
	if n > 12 { // окно + калибровка + запас: runaway-дубли ловятся, честный стрим ~4-6
		t.Errorf("кадров движения наблюдателю = %d за окно; want ≤12 (одна позиция на кадр)", n)
	}
}

// TestE2EJoinCarriesLiveHeading — стоячий поворот до входа наблюдателя:
// join-кадр CharInfo несёт живой heading записи, а не застоявшийся спавна
// (KT4-6: расхождение собственного и чужого рендера на входе живых клиентов).
func TestE2EJoinCarriesLiveHeading(t *testing.T) {
	env := startE2E(t, 10, 4)
	alice := enterWorld(t, env, "turna")
	lineUI := waitForLine(t, alice.out, "USER_INFO", 3*time.Second)
	ax, ay := parseCoord(t, lineUI, "x"), parseCoord(t, lineUI, "y")
	az := parseCoord(t, lineUI, "z")

	const want = 12345
	b := make([]byte, protocol.CannotMoveAnymoreSize)
	protocol.WriteCannotMoveAnymore(b, int32(ax), int32(ay), int32(az), want)
	if err := alice.gc.SendRaw(b, "CANNOT_MOVE_ANYMORE"); err != nil {
		t.Fatalf("SendRaw(CannotMoveAnymore): %v", err)
	}
	// Стоячая ветка шлёт StopMove-самоэхо той же свёрткой — его появление в
	// трафик-логе доказывает применение поворота до входа наблюдателя.
	if line := waitForLine(t, alice.out, "STOP_MOVE", 3*time.Second); line != "" {
		if got := parseCoord(t, line, "heading"); got != want {
			t.Errorf("самоэхо StopMove heading = %d; want %d", got, want)
		}
	}

	bob := enterWorld(t, env, "turnb")
	line := waitForLine(t, bob.out, "CHAR_INFO name=\"Botturna\"", 3*time.Second)
	if got := parseCoord(t, line, "heading"); got != want {
		t.Errorf("join CharInfo heading = %d; want %d (живой поворот записи)", got, want)
	}
}

// npcInfoLines — строки трафик-лога с кадрами NPC_INFO (порядок сохранён).
func npcInfoLines(out *syncBuffer) []string {
	var lines []string
	for _, ln := range strings.Split(out.String(), "\n") {
		if strings.Contains(ln, "NPC_INFO") {
			lines = append(lines, ln)
		}
	}
	return lines
}

// npcInfoSet — множество objID из кадров NPC_INFO (t — для parseCoord).
func npcInfoSet(t *testing.T, lines []string) map[int]bool {
	set := make(map[int]bool)
	for _, ln := range lines {
		if v := parseCoord(t, ln, "objID"); v != 0 {
			set[v] = true
		}
	}
	return set
}

// TestE2ENpcInfoRadiusOnly — вход в развёрнутой толпе: NpcInfo приходят
// ровно NPC стартовой окрестности (6 синтетики), каждый — в enter-радиусе
// 3500 от стартовой точки; первый NPC_INFO строго после слитка USER_INFO.
func TestE2ENpcInfoRadiusOnly(t *testing.T) {
	env := startE2E(t, 10, 4, true)
	s := enterWorld(t, env, "npcguy")
	waitForLine(t, s.out, "USER_INFO", 3*time.Second)
	lines := func() []string {
		deadline := time.Now().Add(5 * time.Second)
		for {
			ls := npcInfoLines(s.out)
			if len(ls) >= 6 || time.Now().After(deadline) {
				return ls
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	if len(lines) != 6 {
		t.Fatalf("NPC_INFO кадров = %d; want 6 (стартовая окрестность синтетики)", len(lines))
	}
	const enter = 3500
	for _, ln := range lines {
		x, y := parseCoord(t, ln, "x")-persist.HumanFighter.StartX,
			parseCoord(t, ln, "y")-persist.HumanFighter.StartY
		if x*x+y*y > enter*enter {
			t.Errorf("NPC_INFO вне enter-радиуса: x=%d y=%d", x, y)
		}
	}
	// Порядок: первый NPC_INFO строго после слитка USER_INFO.
	uiIdx, npcIdx := strings.Index(s.out.String(), "USER_INFO"), strings.Index(s.out.String(), "NPC_INFO")
	if uiIdx < 0 || npcIdx < uiIdx {
		t.Errorf("первый NPC_INFO (%d) раньше USER_INFO (%d)", npcIdx, uiIdx)
	}
}

// TestE2EReenterNpcSetIdenticalById — логаут и повторный вход: набор NpcInfo
// идентичен по objID (NPC не умирают, ID монотонны — структурная
// идентичность, сид разворота не участвует).
func TestE2EReenterNpcSetIdenticalById(t *testing.T) {
	env := startE2E(t, 10, 4, true)
	s := enterWorld(t, env, "reenter")
	waitForLine(t, s.out, "USER_INFO", 3*time.Second)
	waitLines := func(out *syncBuffer, want int) []string {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second) // бюджет — на каждое ожидание
		for {
			lines := npcInfoLines(out)
			if len(lines) >= want || time.Now().After(deadline) {
				return lines
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	first := waitLines(s.out, 6)
	if len(first) != 6 {
		t.Fatalf("первый набор NPC_INFO = %d; want 6", len(first))
	}
	if err := s.gc.Logout(); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	s2 := enterWorld(t, env, "reenter")
	waitForLine(t, s2.out, "USER_INFO", 3*time.Second)
	waitLines(s2.out, 6)
	second := npcInfoLines(s2.out)
	if len(second) != 6 {
		t.Fatalf("второй набор NPC_INFO = %d; want 6", len(second))
	}
	f, sec := npcInfoSet(t, first), npcInfoSet(t, second)
	for id := range f {
		if !sec[id] {
			t.Errorf("NPC %d пропал из набора повторного входа", id)
		}
	}
	for id := range sec {
		if !f[id] {
			t.Errorf("NPC %d появился в повторном входе (фантом)", id)
		}
	}
}

// TestE2EPairMoveAcrossCellBoundary — живой контур сетки ячеек AoI (P4.1):
// пара в соседних клетках (граница x=−73728, cellSize 8192), движение Alice
// через границу — непрерывный CHAR_MOVE_TO_LOCATION у наблюдателя из другой
// клетки, известность не мерцает (молчание ≥3 тиков — тайминг-инвариант),
// прибытие — STOP_MOVE. Позиция наблюдателя west — правкой char-файла +
// рестартом GS (прецедент TestE2EMovementObserverOutsideRadius).
func TestE2EPairMoveAcrossCellBoundary(t *testing.T) {
	env := startE2E(t, 10, 4)
	// обе сессии создаются на дефолтных позициях, сохраняются, переносятся
	// вплотную к границе клеток (west=−73729 клетка 70, alice=−73727 клетка 71)
	// и контур GS перезапускается (charStore живого актора держит записи в
	// памяти — правка диска видима после релоада; маршрут 200 юнитов — в
	// пределах скоростного бюджета сессии, иначе спидхак-детектор душитadvance)
	for _, user := range []string{"westb", "pairmove"} {
		s := enterWorld(t, env, user)
		if err := s.gc.Logout(); err != nil {
			t.Fatalf("Logout(%s): %v", user, err)
		}
		waitForLeaveWorld(t, s, env)
		waitPersistIdle(t, env.gs.actor)
	}
	recs := readChars(t, env.charFile("westb"))
	recs[0]["x"] = -73729
	writeChars(t, env.charFile("westb"), recs)
	recs = readChars(t, env.charFile("pairmove"))
	recs[0]["x"] = -73727
	writeChars(t, env.charFile("pairmove"), recs)
	env.restartGS(t)

	alice := enterWorld(t, env, "pairmove")
	lineUI := waitForLine(t, alice.out, "USER_INFO", 3*time.Second)
	ay := parseCoord(t, lineUI, "y")
	west2 := enterWorld(t, env, "westb")
	lineW := waitForLine(t, west2.out, "USER_INFO", 3*time.Second)
	if got := parseCoord(t, lineW, "x"); got != -73729 {
		t.Fatalf("западный клиент вошёл на x=%d; want −73729 (правка файла не видна)", got)
	}
	// взаимный ввод через границу клеток
	waitForLine(t, alice.out, `CHAR_INFO name="Botwestb"`, 3*time.Second)
	waitForLine(t, west2.out, `CHAR_INFO name="Botpairmove"`, 3*time.Second)

	// движение Alice через границу: 200 юнитов западнее (первый же юнит
	// пересекает −73728), прибытие за секунды
	const dstX = -73927
	if err := alice.gc.MoveToLocation(dstX, int32(ay), -3104, -73727, int32(ay), -3104, 1); err != nil {
		t.Fatalf("MoveToLocation: %v", err)
	}
	echo := waitForLine(t, alice.out, "CHAR_MOVE_TO_LOCATION", 3*time.Second)
	if got := parseCoord(t, echo, "dstX"); got != dstX {
		t.Errorf("эхо dstX = %d; want %d", got, dstX)
	}
	waitForLine(t, west2.out, "CHAR_MOVE_TO_LOCATION", 3*time.Second) // стрим из соседней клетки
	time.Sleep(300 * time.Millisecond)                                // ≥3 тика 10 Гц: известность не мерцает
	txt := west2.out.String()
	if strings.Contains(txt, "DELETE_OBJECT") {
		t.Errorf("удаление в окне стрима через границу; лог:\n%s", txt)
	}
	if n := strings.Count(txt, `CHAR_INFO name="Botpairmove"`); n != 1 {
		t.Errorf("CharInfo(pairmove) = %d за сессию; want 1 (повторный ввод — мерцание)", n)
	}
	stop := waitForLine(t, alice.out, "STOP_MOVE", 30*time.Second)
	if got := parseCoord(t, stop, "x"); got != dstX {
		t.Errorf("прибытие x = %d; want %d", got, dstX)
	}
	waitForLine(t, west2.out, "STOP_MOVE", 3*time.Second)
}
