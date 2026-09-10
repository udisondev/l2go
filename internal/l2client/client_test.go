package l2client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Полный флоу на сценарий-сервере: логин → выбор сервера → game-хендшейк →
// CharSelected → стационарная фаза (типизированный/нетипизированный вход,
// Logout) — трафик-лог совпадает с golden.
func TestFullFlowGolden(t *testing.T) {
	srv, out := startGolden(t)
	ctx := context.Background()

	lc, err := DialLogin(ctx, srv.LoginAddr(), Options{Traffic: out})
	if err != nil {
		t.Fatalf("DialLogin: %v", err)
	}
	if err := lc.Handshake(); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	if err := lc.Login(ScenarioUser, ScenarioPass); err != nil {
		t.Fatalf("Login: %v", err)
	}
	servers, chars, err := lc.ServerList()
	if err != nil {
		t.Fatalf("ServerList: %v", err)
	}
	if len(servers) != 2 || servers[0].ID != 1 || len(chars) != 2 || chars[0].CharCount != 3 {
		t.Fatalf("ServerList = %+v/%+v; want 2 записи с id=1 и chars=3", servers, chars)
	}
	ep, err := lc.SelectServer(1)
	if err != nil {
		t.Fatalf("SelectServer: %v", err)
	}
	if err := lc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	gc, err := DialGame(ctx, ep.Addr, Options{Traffic: out})
	if err != nil {
		t.Fatalf("DialGame: %v", err)
	}
	if err := gc.Handshake(); err != nil {
		t.Fatalf("GameHandshake: %v", err)
	}
	chars2, err := gc.Auth(ep, ScenarioUser)
	if err != nil {
		t.Fatalf("Auth: %v", err)
	}
	if len(chars2) != 2 || chars2[0].Name != "Warrior" || chars2[1].Name != "Маг" {
		t.Fatalf("список персонажей = %+v; want Warrior/Маг", chars2)
	}
	if err := gc.SelectChar(0); err != nil {
		t.Fatalf("SelectChar: %v", err)
	}

	runErr := make(chan error, 1)
	go func() { runErr <- gc.Run(ctx) }()
	// детерминизм порядка: стационарные пуши сервера логируются до Logout —
	// ждём их в логе (кадры в полёте), затем команда
	waitForLog(t, out, "??(0xBA)")
	if err := gc.Logout(); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run: %v (чистое закрытие сервера — nil)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run не завершился после закрытия сервером")
	}

	golden, err := os.ReadFile("testdata/golden_flow.txt")
	if err != nil {
		t.Fatalf("golden: %v", err)
	}
	if out.String() != string(golden) {
		t.Fatalf("трафик-лог расходится с golden:\ngot:\n%s\nwant:\n%s", out.String(), golden)
	}
	if err := srv.Err(); err != nil {
		t.Fatalf("сценарий-сервер: %v", err)
	}
}

// waitForLog ждёт появления подстроки в трафик-логе (кадры в полёте).
func waitForLog(t *testing.T, out *bytes.Buffer, sub string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), sub) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("подстрока %q не появилась в логе за 5с", sub)
}

// startGolden поднимает сценарий-сервер с golden-скриптами обеих ног.
func startGolden(t *testing.T) (srv *ScenarioServer, out *bytes.Buffer) {
	t.Helper()
	srv, err := StartScenarioServer()
	if err != nil {
		t.Fatalf("StartScenarioServer: %v", err)
	}
	t.Cleanup(srv.Close)
	go func() { _ = srv.RunLoginScript(GoldenLoginScript(srv)) }()
	go func() { _ = srv.RunGameScript(GoldenGameScript()) }()
	return srv, &bytes.Buffer{}
}

func mustFlow(t *testing.T, err error, stage string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", stage, err)
	}
}

// Злые входы: клиент не паникует, завершается детерминированной ошибкой.
func TestEvilInputs(t *testing.T) {
	garbage16 := bytes.Repeat([]byte{0xA5}, 16)
	tests := []struct {
		name     string
		login    func(*ScenarioServer) LoginScript
		game     func() GameScript
		stage    string
		contains string
	}{
		{
			name: "мусор вместо Init",
			login: func(s *ScenarioServer) LoginScript {
				return LoginScript{Init: garbage16}
			},
			stage:    "Handshake",
			contains: "Init",
		},
		{
			name: "обрезанный Init (валидный шифрокадр, тело коротко)",
			login: func(s *ScenarioServer) LoginScript {
				return LoginScript{Init: garbage16[:8]}
			},
			stage:    "Handshake",
			contains: "Init",
		},
		{
			name: "GGAuth с чужим session",
			login: func(s *ScenarioServer) LoginScript {
				return LoginScript{Steps: []LoginStep{
					{Expect: opAuthGameGuard, Reply: wireGGAuth(-559038737)}, // 0xDEADBEEF
				}}
			},
			stage:    "Handshake",
			contains: "session",
		},
		{
			name: "битая чексумма после SetKey",
			login: func(s *ScenarioServer) LoginScript {
				return LoginScript{Steps: []LoginStep{
					{Expect: opAuthGameGuard, Reply: garbage16, Corrupt: true},
				}}
			},
			stage:    "Handshake",
			contains: "чексумм",
		},
		{
			name: "LoginFail с reason",
			login: func(s *ScenarioServer) LoginScript {
				return LoginScript{CheckAuth: true, Steps: []LoginStep{
					{Expect: opAuthGameGuard, Reply: wireGGAuth(ScenarioSession)},
					{Expect: opRequestAuthLogin, Reply: fixtureWire("login", "LOGIN_FAIL")},
					{Expect: opRequestAuthLogin, Reply: nil},
				}}
			},
			stage:    "Login",
			contains: "reason",
		},
		{
			name: "обрезанный LoginOk",
			login: func(s *ScenarioServer) LoginScript {
				return LoginScript{Steps: []LoginStep{
					{Expect: opAuthGameGuard, Reply: wireGGAuth(ScenarioSession)},
					{Expect: opRequestAuthLogin, Reply: []byte{0x03, 0x01, 0x02}},
				}}
			},
			stage:    "Login",
			contains: "LoginOk",
		},
		{
			name: "PlayFail на выборе сервера",
			login: func(s *ScenarioServer) LoginScript {
				return LoginScript{CheckAuth: true, Steps: []LoginStep{
					{Expect: opAuthGameGuard, Reply: wireGGAuth(ScenarioSession)},
					{Expect: opRequestAuthLogin, Reply: fixtureWire("login", "LOGIN_OK")},
					{Expect: opRequestServerList, Reply: fixtureWire("login", "SERVER_LIST")},
					{Expect: opRequestServerLogin, Reply: fixtureWire("login", "PLAY_FAIL")},
				}}
			},
			stage:    "SelectServer",
			contains: "reason",
		},
		{
			name: "молчащий сервер — стадийный таймаут",
			login: func(s *ScenarioServer) LoginScript {
				return LoginScript{Steps: []LoginStep{
					{Expect: opAuthGameGuard, Reply: nil},
					{Expect: opRequestAuthLogin, Reply: nil},
				}}
			},
			stage:    "Handshake",
			contains: "таймаут",
		},
		{
			name: "обрыв посреди кадра (сырой префикс + обрубленное тело)",
			login: func(s *ScenarioServer) LoginScript {
				return LoginScript{Raw: []byte{0x40, 0x00, 0x01, 0x02}, Close: true}
			},
			stage:    "Handshake",
			contains: "",
		},
		{
			name: "длина записи < 2",
			login: func(s *ScenarioServer) LoginScript {
				return LoginScript{Raw: []byte{0x01, 0x00, 0xAA}, Close: true}
			},
			stage:    "Handshake",
			contains: "длина кадра",
		},
		{
			name: "KeyPacket result=0 — версия отклонена",
			game: func() GameScript {
				s := GoldenGameScript()
				s.Steps[0].Reply = append([]byte{0x00, 0x00}, s.Steps[0].Reply[2:]...)
				return s
			},
			stage:    "GameHandshake",
			contains: "отклонена",
		},
		{
			name: "GSLoginFail на Auth",
			game: func() GameScript {
				s := GoldenGameScript()
				s.Steps[1].Reply = fixtureWire("handshake", "LOGIN_FAIL")
				return s
			},
			stage:    "Auth",
			contains: "reason",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("паника на злом входе: %v", r)
				}
			}()
			srv, err := StartScenarioServer()
			mustFlow(t, err, "StartScenarioServer")
			defer srv.Close()
			if tt.login != nil {
				go func() { _ = srv.RunLoginScript(tt.login(srv)) }()
			} else {
				go func() { _ = srv.RunLoginScript(GoldenLoginScript(srv)) }()
			}
			if tt.game != nil {
				go func() { _ = srv.RunGameScript(tt.game()) }()
			} else {
				go func() { _ = srv.RunGameScript(GoldenGameScript()) }()
			}
			ctx := context.Background()
			opts := Options{Timeout: 300 * time.Millisecond}

			err = runFlowTo(t, srv, ctx, opts, tt.stage)
			if err == nil {
				t.Fatalf("%s: err = nil; want ошибка", tt.stage)
			}
			if tt.contains != "" && !strings.Contains(err.Error(), tt.contains) {
				t.Errorf("%s: err = %v; want подстроку %q", tt.stage, err, tt.contains)
			}
		})
	}
}

// runFlowTo прогоняет флоу до указанной стадии и возвращает её ошибку.
func runFlowTo(t *testing.T, srv *ScenarioServer, ctx context.Context, opts Options, stage string) error {
	t.Helper()
	lc, err := DialLogin(ctx, srv.LoginAddr(), opts)
	if err != nil {
		return err
	}
	defer lc.Close()
	if stage == "Handshake" {
		return lc.Handshake()
	}
	if err := lc.Handshake(); err != nil {
		return err
	}
	if stage == "Login" {
		return lc.Login(ScenarioUser, ScenarioPass)
	}
	if err := lc.Login(ScenarioUser, ScenarioPass); err != nil {
		return err
	}
	if _, _, err := lc.ServerList(); err != nil {
		return err
	}
	if stage == "SelectServer" {
		_, err := lc.SelectServer(1)
		return err
	}
	_, err = lc.SelectServer(1)
	if err != nil {
		return fmt.Errorf("SelectServer: %w", err)
	}
	gc, err := DialGame(ctx, srv.GameAddr(), opts)
	if err != nil {
		return err
	}
	defer gc.Close()
	if stage == "GameHandshake" {
		return gc.Handshake()
	}
	if err := gc.Handshake(); err != nil {
		return err
	}
	if stage == "Auth" {
		ep := GameEndpoint{}
		_, err := gc.Auth(ep, ScenarioUser)
		return err
	}
	return nil
}

// scenarioCiphertext — детерминированный шифротекст учётных данных сценария
// (фиксированная пара + фиксированный plain-блок).
func scenarioCiphertext(t *testing.T) []byte {
	t.Helper()
	key, err := loadScenarioKey()
	if err != nil {
		t.Fatalf("loadScenarioKey: %v", err)
	}
	var block [128]byte
	copy(block[0x5E:], ScenarioUser)
	copy(block[0x6C:], ScenarioPass)
	c := new(big.Int).Exp(new(big.Int).SetBytes(block[:]), big.NewInt(int64(key.E)), key.N)
	out := make([]byte, 128)
	c.FillBytes(out)
	return out
}

// Мусорный auth-блоб: расшифровка и проверка сценарием — ошибка шага, не паника.
func TestScenarioAuthCheck(t *testing.T) {
	garbage := bytes.Repeat([]byte{0x5A}, 128)
	if _, _, err := decodeAuthBlock(garbage); err != nil {
		t.Fatalf("decodeAuthBlock(мусор): %v; want nil (блок валиден по длине, мусор в полях)", err)
	}
	// правильный шифротекст — фиксированная пара сценария
	ct := scenarioCiphertext(t)
	user, pass, err := decodeAuthBlock(ct)
	if err != nil {
		t.Fatalf("decodeAuthBlock(фиксированный шифротекст): %v", err)
	}
	if user != ScenarioUser || pass != ScenarioPass {
		t.Errorf("учётные данные = %q/%q; want %q/%q", user, pass, ScenarioUser, ScenarioPass)
	}
}

// Конкурентный стресс: Run + Logout + Close из разных горутин (гонки — CI -race).
func TestGameClientStress(t *testing.T) {
	srv, out := startGolden(t)
	ctx := context.Background()
	lc, err := DialLogin(ctx, srv.LoginAddr(), Options{Traffic: out})
	mustFlow(t, err, "DialLogin")
	mustFlow(t, lc.Handshake(), "Handshake")
	mustFlow(t, lc.Login(ScenarioUser, ScenarioPass), "Login")
	if _, _, err := lc.ServerList(); err != nil {
		t.Fatalf("ServerList: %v", err)
	}
	ep, err := lc.SelectServer(1)
	mustFlow(t, err, "SelectServer")
	mustFlow(t, lc.Close(), "Close")

	gc, err := DialGame(ctx, ep.Addr, Options{Traffic: out})
	mustFlow(t, err, "DialGame")
	mustFlow(t, gc.Handshake(), "GameHandshake")
	_, err = gc.Auth(ep, ScenarioUser)
	mustFlow(t, err, "Auth")
	mustFlow(t, gc.SelectChar(0), "SelectChar")

	runErr := make(chan error, 1)
	go func() { runErr <- gc.Run(ctx) }()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = gc.Logout() }()
	go func() { defer wg.Done(); time.Sleep(time.Millisecond); _ = gc.Close() }()
	wg.Wait()
	select {
	case <-runErr:
	case <-time.After(5 * time.Second):
		t.Fatal("Run не завершился после Logout/Close")
	}
	// после выхода — команда детерминированно ошибается, не блокируется
	if err := gc.Logout(); err == nil {
		t.Error("Logout после выхода Run: err = nil; want ошибка")
	}
}

// Команда после Close без запуска Run — ошибка, не висение.
func TestCommandAfterClose(t *testing.T) {
	srv, _ := startGolden(t)
	ctx := context.Background()
	gc, err := DialGame(ctx, srv.GameAddr(), Options{})
	mustFlow(t, err, "DialGame")
	mustFlow(t, gc.Close(), "Close")
	done := make(chan error, 1)
	go func() { done <- gc.Logout() }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Logout после Close: err = nil; want ошибка")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Logout после Close заблокировался")
	}
	// идемпотентный Close
	mustFlow(t, gc.Close(), "повторный Close")
}

// cmd/l2client: прогон собранного бинарника против golden-сценария
// воспроизводит лог (критерий p1.md).
func TestCmdScenario(t *testing.T) {
	if testing.Short() {
		t.Skip("exec-сборка бинарника — не для -short")
	}
	bin := filepath.Join(t.TempDir(), "l2client")
	build := exec.Command("go", "build", "-o", bin, "github.com/udisondev/l2go/cmd/l2client")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	srv, err := StartScenarioServer()
	if err != nil {
		t.Fatalf("StartScenarioServer: %v", err)
	}
	defer srv.Close()
	go func() { _ = srv.RunLoginScript(GoldenLoginScript(srv)) }()
	// cmd-сценарий — без стационарных пушей: Logout сразу после входа
	// (гонка «команда против кадра в полёте» не детерминизируется извне бинарника)
	full := GoldenGameScript()
	game := GameScript{Steps: append(full.Steps[:3:3], full.Steps[5])}
	go func() { _ = srv.RunGameScript(game) }()

	cmd := exec.Command(bin, "-addr", srv.LoginAddr(), "-account", ScenarioUser, "-server", "1", "-char", "0")
	cmd.Env = append(os.Environ(), "L2CLIENT_PASSWORD="+ScenarioPass)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("l2client: %v\nstderr: %s", err, stderr.String())
	}
	golden, err := os.ReadFile("testdata/golden_cmd.txt")
	if err != nil {
		t.Fatalf("golden: %v", err)
	}
	if stdout.String() != string(golden) {
		t.Fatalf("stdout бинарника расходится с golden:\ngot:\n%s\nwant:\n%s", stdout.String(), golden)
	}
	if s := stderr.String(); s != "" {
		t.Errorf("stderr не пуст (slog должен молчать на инфо-уровне): %q", s)
	}
}

// Ошибки сетевой стадии оборачиваются с контекстом стадии.
func TestErrorsWrapped(t *testing.T) {
	// никто не слушает — ошибка DialLogin с адресом
	_, err := DialLogin(context.Background(), "127.0.0.1:1", Options{Timeout: 300 * time.Millisecond})
	if err == nil {
		t.Fatal("DialLogin на закрытый порт: err = nil")
	}
	if !errors.Is(err, errDial) && !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("ошибка без контекста адреса: %v", err)
	}
}
