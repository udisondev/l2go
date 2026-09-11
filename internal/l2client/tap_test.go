package l2client

// Интеграция тапа: полный флоу клиента через прокси-тап (login+game ноги),
// декодированный лог побайтово равен логу прямой сессии; фикстуры выгружаются,
// AuthLogin обеих ног исключён; перезапись ServerList оставляет original.

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/tap"
)

// singleServerLoginScript — golden-диалог LS с одним сервером в списке
// (rewrite тапа работает при ровно одном сервере).
func singleServerLoginScript(s *ScenarioServer) LoginScript {
	tcp, err := net.ResolveTCPAddr("tcp", s.GameAddr())
	if err != nil {
		panic("сценарий: адрес GS: " + err.Error())
	}
	var ip [4]byte
	copy(ip[:], tcp.IP.To4())
	servers := []protocol.ServerListEntry{
		{ID: 1, IP: ip, Port: int32(tcp.Port), CurrentPlayers: 42, MaxPlayers: 500, Status: 1, ServerType: 1},
	}
	chars := []protocol.ServerChars{{ServerID: 1, CharCount: 3, DeleteTimes: []int32{3600}}}
	list := make([]byte, protocol.ServerListSize(servers, chars))
	protocol.WriteServerList(list, servers, chars, 1)
	return LoginScript{
		CheckAuth: true,
		Steps: []LoginStep{
			{Expect: protocol.OpAuthGameGuard, Reply: mustLSFixture("GG_AUTH")},
			{Expect: protocol.OpRequestAuthLogin, Reply: mustLSFixture("LOGIN_OK")},
			{Expect: protocol.OpRequestServerList, Reply: list},
			{Expect: protocol.OpRequestServerLogin, Reply: mustLSFixture("PLAY_OK")},
		},
	}
}

// runFlow прогоняет полный флоу клиента против addr (LS) и пишет Traffic в out.
func runFlow(t *testing.T, ctx context.Context, lsAddr string, out *syncBuffer) {
	t.Helper()
	lc, err := DialLogin(ctx, lsAddr, Options{Traffic: out})
	if err != nil {
		t.Fatalf("DialLogin: %v", err)
	}
	mustFlow(t, lc.Handshake(), "Handshake")
	mustFlow(t, lc.Login(ScenarioUser, ScenarioPass), "Login")
	servers, _, err := lc.ServerList()
	if err != nil {
		t.Fatalf("ServerList: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("ServerList: записей %d, want 1", len(servers))
	}
	ep, err := lc.SelectServer(1)
	if err != nil {
		t.Fatalf("SelectServer: %v", err)
	}
	mustFlow(t, lc.Close(), "Close login")

	gc, err := DialGame(ctx, ep.Addr, Options{Traffic: out})
	if err != nil {
		t.Fatalf("DialGame: %v", err)
	}
	mustFlow(t, gc.Handshake(), "GameHandshake")
	if _, err := gc.Auth(ep, ScenarioUser); err != nil {
		t.Fatalf("Auth: %v", err)
	}
	mustFlow(t, gc.SelectChar(0), "SelectChar")
	runErr := make(chan error, 1)
	go func() { runErr <- gc.Run(ctx) }()
	waitForLog(t, out, "??(0xBA)")
	mustFlow(t, gc.Logout(), "Logout")
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run не завершился")
	}
}

// startScripted поднимает сценарий-сервер с данным login-скриптом и golden
// game-скриптом.
func startScripted(t *testing.T, login func(s *ScenarioServer) LoginScript) (srv *ScenarioServer, out *syncBuffer) {
	t.Helper()
	srv, err := StartScenarioServer()
	if err != nil {
		t.Fatalf("StartScenarioServer: %v", err)
	}
	t.Cleanup(srv.Close)
	go func() { _ = srv.RunLoginScript(login(srv)) }()
	go func() { _ = srv.RunGameScript(GoldenGameScript()) }()
	return srv, &syncBuffer{}
}

// freeAddr — свободный порт на loopback (привязка-освобождение; коллизии в
// тестовом окружении практически исключены).
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freeAddr: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("freeAddr close: %v", err)
	}
	return addr
}

// Полный флоу через тап: лог декодера побайтово равен логу прямой сессии.
func TestTapFullFlowLogParity(t *testing.T) {
	ctx := context.Background()

	// прямая сессия — эталон лога
	srvDirect, outDirect := startScripted(t, singleServerLoginScript)
	runFlow(t, ctx, srvDirect.LoginAddr(), outDirect)
	if err := srvDirect.Err(); err != nil {
		t.Fatalf("сценарий (прямая): %v", err)
	}

	// сессия через тап
	srv, outTap := startScripted(t, singleServerLoginScript)

	lsAddr := freeAddr(t)
	gsAddr := freeAddr(t)
	var journal bytes.Buffer
	tapCtx, stopTap := context.WithCancel(ctx)
	tapDone := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		tapDone <- tap.Run(tapCtx, tap.Options{
			Maps: []tap.Map{
				{Listen: lsAddr, Upstream: srv.LoginAddr(), Login: true},
				{Listen: gsAddr, Upstream: srv.GameAddr()},
			},
			Log:     &journal,
			Rewrite: true,
			OnReady: func() { close(ready) },
		})
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("тап не поднял слушатели за 5с")
	}
	runFlow(t, ctx, lsAddr, outTap)
	if err := srv.Err(); err != nil {
		t.Fatalf("сценарий (тап): %v", err)
	}
	stopTap()
	select {
	case err := <-tapDone:
		if err != nil {
			t.Fatalf("tap.Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tap.Run не завершился после отмены")
	}

	var decoded bytes.Buffer
	var fixts bytes.Buffer
	if err := tap.Decode(bytes.NewReader(journal.Bytes()), tap.DecodeOptions{Log: &decoded, Fixtures: &fixts}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if decoded.String() != outDirect.String() {
		t.Fatalf("лог декодера расходится с прямой сессией:\ngot:\n%s\nwant:\n%s", decoded.String(), outDirect.String())
	}
	if outTap.String() != outDirect.String() {
		t.Fatalf("лог клиента через тап расходится с прямой сессией")
	}

	// фикстуры: без AUTH_LOGIN обеих ног, с SERVER_LIST/KEY_PACKET и пр.
	var rows []map[string]any
	if err := json.Unmarshal(fixts.Bytes(), &rows); err != nil {
		t.Fatalf("фикстуры JSON: %v", err)
	}
	names := map[string]int{}
	for _, r := range rows {
		names[r["name"].(string)]++
	}
	if names["AUTH_LOGIN"] != 0 || names["REQUEST_AUTH_LOGIN"] != 0 {
		t.Fatalf("фикстуры содержат AuthLogin: %+v", names)
	}
	for _, want := range []string{"INIT", "SERVER_LIST", "KEY_PACKET", "CHAR_SELECT_INFO", "CHAR_SELECTED", "PROTOCOL_VERSION"} {
		if names[want] == 0 {
			t.Fatalf("в фикстурах нет %s: %+v", want, names)
		}
	}

	// original-канал rewrite: фикстура SERVER_LIST извлечена из оригинала —
	// порт записи равен порту GS сценария, а не слушателя тапа
	_, scenarioPortStr, _ := net.SplitHostPort(srv.GameAddr())
	scenarioPort, _ := strconv.Atoi(scenarioPortStr)
	sawOriginal := false
	for _, r := range rows {
		if r["name"] != "SERVER_LIST" {
			continue
		}
		payload, err := hex.DecodeString(r["payload"].(string))
		if err != nil || len(payload) < 11 {
			t.Fatalf("фикстура SERVER_LIST: payload %v (%v)", r["payload"], err)
		}
		if got := int32(binary.LittleEndian.Uint32(payload[7:11])); got == int32(scenarioPort) {
			sawOriginal = true
		}
	}
	if !sawOriginal {
		t.Fatal("фикстура SERVER_LIST не из оригинала rewrite (порт GS сценария не найден)")
	}
}
