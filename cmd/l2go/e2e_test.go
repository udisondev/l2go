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
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

// startE2E — полный контур с ускоренными тиками (hz) и коротким grace.
func startE2E(t *testing.T, hz, graceTicks int) *e2eEnv {
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

// readChars — чтение файла персонажей: конверт persist {schema,data,sha256},
// data — список записей.
func readChars(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
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
	// LeaveWorld — последний входящий кадр сессии (close-after-flush).
	if tail := tailAfter(t, s.out, "LEAVE_WORLD"); len(tail) != 0 {
		t.Errorf("кадры после LeaveWorld: %q", tail)
	}

	// Путь сохранения: файл отражает сессию. Персист пишет асинхронно —
	// ждём свежего LastSeenUnix и погашенной очереди сохранений (F22:
	// патч поверх невысохшего сохранения был бы перезаписан; свежий
	// LastSeen ещё не значит «ответ персиста применён» — ретрай/финальный
	// сохранитель переписали бы файл своим снимком).
	path := env.charFile("roundtrip")
	start := time.Now().Unix()
	recs := waitFreshChars(t, path, start)
	waitForSaveQueue(t, env.gs, 0)
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

// waitForSaveQueue — поллинг очереди несохранённых персонажей региона до
// want (SaveQueue — атомик: ретраи/финальный сохранитель исключены).
func waitForSaveQueue(t *testing.T, srv *server, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if srv.region.Stats().SaveQueue == want {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatalf("SaveQueue = %d; want %d", srv.region.Stats().SaveQueue, want)
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
