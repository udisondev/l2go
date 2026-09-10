// Сценарий-сервер — тестовый актив клиента: пара in-process слушателей
// (LoginServer-нога и GameServer-нога, обе 127.0.0.1:0), исполняющая
// записанные диалоги шагами «ожидай опкод → ответь кадром». Крипто-материалы
// фиксированы (детерминированные шифротексты → детерминированный golden):
// тестовая RSA-пара в testdata/scenario_test_key.der, Blowfish-ключ и
// XOR-ключ — константы. Переиспользуется cmd-прогоном и будущим pcap-конвейером.

package l2client

import (
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"runtime"
	"time"

	"github.com/udisondev/l2go/internal/crypto"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/protocol/fixture"
)

// Учётные данные и сессия golden-сценария.
const (
	ScenarioUser    = "testuser"
	ScenarioPass    = "secret"
	ScenarioSession = int32(0x12345678)
)

// scenarioBFKey — фиксированный динамический Blowfish-ключ сценария.
var scenarioBFKey = [16]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88,
	0x99, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0x0F, 0x1A}

// scenarioXORKey — фиксированный XOR-ключ первого Init-кадра сценария.
const scenarioXORKey = uint32(0x0BADC0DE)

// scenarioGameWire — фиксированные 8 случайных байт game-ключа сценария
// (значения совпадают с фикстурой KEY_PACKET — паттерн pattern(8, 31)).
var scenarioGameWire = [8]byte{0x1F, 0x44, 0x69, 0x8E, 0xB3, 0xD8, 0xFD, 0x22}

// сценарный дедлайн чтения: тестовые диалоги не висят вечно.
const scenarioTimeout = 10 * time.Second

// ExpectNone — шаг без ожидания входящего кадра (сервер инициирует).
const ExpectNone = -1

// LoginStep — шаг LS-ноги: Expect — ожидаемый опкод или ExpectNone, ответ
// wire-байтами, порча последнего байта шифротекста (битая чексумма).
type LoginStep struct {
	Expect  int
	Reply   []byte
	Corrupt bool
}

// LoginScript — диалог LS-ноги: Raw пишется вместо Init байт-в-байт (злые
// входы: мусор, обрыв посреди кадра; пустой срез — закрытие без Init), Close
// закрывает соединение после отправки, Modulus подменяет модулус собираемого
// Init (злой вход: вырожденный ключ).
type LoginScript struct {
	Init      []byte // nil — собранный Init сценария; непустое — сырые байты тела кадра
	Raw       []byte // пишется вместо Init байт-в-байт (без шифрования)
	Modulus   []byte // подмена модулуса собираемого Init
	Close     bool
	CheckAuth bool // проверять учётные данные REQUEST_AUTH_LOGIN
	Steps     []LoginStep
}

// GameStep — шаг GS-ноги: Expect — ожидаемый опкод или ExpectNone.
type GameStep struct {
	Expect int
	Reply  []byte
}

// GameScript — диалог GS-ноги.
type GameScript struct {
	Steps []GameStep
}

// ScenarioServer — пара слушателей с фиксированными крипто-материалами.
type ScenarioServer struct {
	ls, gs net.Listener
	priv   *rsa.PrivateKey
	errCh  chan error
}

// StartScenarioServer поднимает обе ноги на 127.0.0.1:0 и грузит тестовую
// RSA-пару.
func StartScenarioServer() (*ScenarioServer, error) {
	priv, err := loadScenarioKey()
	if err != nil {
		return nil, err
	}
	ls, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("слушатель LS: %w", err)
	}
	gs, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		ls.Close()
		return nil, fmt.Errorf("слушатель GS: %w", err)
	}
	return &ScenarioServer{ls: ls, gs: gs, priv: priv, errCh: make(chan error, 4)}, nil
}

// LoginAddr возвращает адрес LS-ноги.
func (s *ScenarioServer) LoginAddr() string { return s.ls.Addr().String() }

// GameAddr возвращает адрес GS-ноги.
func (s *ScenarioServer) GameAddr() string { return s.gs.Addr().String() }

// Close закрывает слушатели.
func (s *ScenarioServer) Close() {
	lsErr := s.ls.Close()
	gsErr := s.gs.Close()
	if lsErr != nil || gsErr != nil {
		slog.Debug("scenario: закрытие слушателей", "ls", lsErr, "gs", gsErr)
	}
}

// Err возвращает накопленные ошибки шагов (расхождение опкода, auth).
func (s *ScenarioServer) Err() error {
	select {
	case err := <-s.errCh:
		return err
	default:
		return nil
	}
}

func (s *ScenarioServer) fail(format string, args ...any) {
	s.errCh <- fmt.Errorf("сценарий: "+format, args...)
}

// RunLoginScript принимает одно соединение LS-ноги и исполняет диалог.
func (s *ScenarioServer) RunLoginScript(sc LoginScript) error {
	conn, err := s.ls.Accept()
	if err != nil {
		return fmt.Errorf("accept LS: %w", err)
	}
	defer conn.Close()
	crypt := crypto.NewLoginCrypt()

	// Init — в статической фазе движка; ключ ставится после отправки
	// (ответы сценария и расшифровка входящих — динамические). Raw != nil —
	// сырые байты вместо Init (злые входы; пустой срез — мгновенное закрытие).
	if sc.Raw != nil {
		if _, err := conn.Write(sc.Raw); err != nil {
			return fmt.Errorf("raw-запись: %w", err)
		}
	} else {
		initWire := sc.Init
		if initWire == nil {
			modulus := s.scrambledModulus()
			if len(sc.Modulus) == 128 {
				modulus = sc.Modulus
			}
			var w [protocol.InitSize]byte
			protocol.WriteInit(w[:], ScenarioSession, modulus, scenarioBFKey[:])
			initWire = w[:]
		}
		if err := writeInitFrame(conn, crypt, initWire); err != nil {
			return fmt.Errorf("отправка Init: %w", err)
		}
	}
	if sc.Close {
		return nil
	}
	if err := crypt.SetKey(scenarioBFKey[:]); err != nil {
		return fmt.Errorf("динамический ключ сценария: %w", err)
	}
	for _, st := range sc.Steps {
		if st.Expect != ExpectNone {
			frame, err := readFrame(conn, scenarioTimeout)
			if err != nil {
				return fmt.Errorf("шаг 0x%02X: %w", st.Expect, err)
			}
			if err := crypt.Decrypt(frame); err != nil {
				return fmt.Errorf("шаг 0x%02X: расшифровка: %w", st.Expect, err)
			}
			if len(frame) == 0 || frame[0] != byte(st.Expect) {
				op := -1
				if len(frame) > 0 {
					op = int(frame[0])
				}
				s.fail("ожидался опкод 0x%02X, пришёл 0x%02X", st.Expect, op)
				return nil
			}
			if st.Expect == protocol.OpRequestAuthLogin && sc.CheckAuth {
				if len(frame) < 129 {
					s.fail("auth: кадр %d Б короче блоба", len(frame))
					return nil
				}
				user, pass, err := decodeAuthBlock(s.priv, frame[1:129])
				if err != nil || user != ScenarioUser || pass != ScenarioPass {
					// расшифрованные учётные данные не печатаются
					s.fail("auth: учётные данные не совпали (err=%v)", err)
					return nil
				}
			}
		}
		if st.Reply != nil {
			if err := writeEncFrame(conn, crypt, st.Reply, st.Corrupt); err != nil {
				return fmt.Errorf("ответ 0x%02X: %w", st.Expect, err)
			}
		}
	}
	return nil
}

// RunGameScript принимает одно соединение GS-ноги и исполняет диалог:
// ProtocolVersion/KeyPacket — открытым текстом, дальше зеркальный GameCrypt.
func (s *ScenarioServer) RunGameScript(sc GameScript) error {
	conn, err := s.gs.Accept()
	if err != nil {
		return fmt.Errorf("accept GS: %w", err)
	}
	defer conn.Close()
	crypt := crypto.NewGameCrypt(scenarioGameWire)
	plain := true
	for _, st := range sc.Steps {
		if st.Expect != ExpectNone {
			frame, err := readFrame(conn, scenarioTimeout)
			if err != nil {
				return fmt.Errorf("шаг 0x%02X: %w", st.Expect, err)
			}
			if !plain {
				if err := crypt.Decrypt(frame); err != nil {
					return fmt.Errorf("шаг 0x%02X: расшифровка: %w", st.Expect, err)
				}
			}
			if len(frame) == 0 || frame[0] != byte(st.Expect) {
				op := -1
				if len(frame) > 0 {
					op = int(frame[0])
				}
				s.fail("game: ожидался опкод 0x%02X, пришёл 0x%02X", st.Expect, op)
				return nil
			}
		}
		if st.Reply != nil {
			payload := st.Reply
			if len(payload) == 0 {
				// пустое тело (запись length=2): шифровать нечего
				if err := writeRecord(conn, scenarioTimeout, payload); err != nil {
					return fmt.Errorf("ответ пустой: %w", err)
				}
			} else {
				if !plain {
					payload = append([]byte(nil), st.Reply...)
					if err := crypt.Encrypt(payload); err != nil {
						return fmt.Errorf("шифрование ответа: %w", err)
					}
				}
				if err := writeRecord(conn, scenarioTimeout, payload); err != nil {
					return fmt.Errorf("ответ 0x%02X: %w", st.Expect, err)
				}
			}
		}
		if st.Expect == protocol.OpProtocolVersion {
			crypt.Enable() // KeyPacket отправлен — дальше шифрованный обмен
			plain = false
		}
	}
	return nil
}

// GoldenLoginScript — записанный happy-path диалог LS-ноги: Init, GGAuth,
// LoginOk, ServerList (собирается по фактическому адресу GS-ноги), PlayOk.
func GoldenLoginScript(s *ScenarioServer) LoginScript {
	servers, chars := goldenServers(s.GameAddr())
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

// GoldenGameScript — записанный happy-path диалог GS-ноги: KeyPacket,
// CharSelectionInfo, CharSelected, два кадра стационарной фазы (типизированный
// каталогом и неизвестный), закрытие после Logout.
func GoldenGameScript() GameScript {
	return GameScript{Steps: []GameStep{
		{Expect: protocol.OpProtocolVersion, Reply: mustGSFixture("KEY_PACKET")},
		{Expect: protocol.OpAuthLogin, Reply: mustGSFixture("CHAR_SELECT_INFO")},
		{Expect: protocol.OpCharacterSelect, Reply: mustGSFixture("CHAR_SELECTED")},
		{Expect: ExpectNone, Reply: []byte{}},                 // пустое тело — фолбэк «??(0x??)»
		{Expect: ExpectNone, Reply: []byte{0x1C, 0x01, 0x02}}, // SUNRISE — имя каталога, hex-дамп
		{Expect: ExpectNone, Reply: []byte{0xBA, 0xAB, 0x01}}, // неизвестный опкод — «??»
		{Expect: protocol.OpLogout, Reply: nil},               // закрытие: клиент завершает флоу
	}}
}

// goldenServers — записи списка серверов сценария: адрес первой записи —
// фактический адрес GS-ноги (клиент набирает его после SelectServer).
func goldenServers(gameAddr string) ([]protocol.ServerListEntry, []protocol.ServerChars) {
	tcp, err := net.ResolveTCPAddr("tcp", gameAddr)
	if err != nil {
		// адрес собственным слушателем выдан — недостижимо, инвариант сценария
		panic(fmt.Sprintf("сценарий: адрес GS-ноги %q: %v", gameAddr, err))
	}
	var ip [4]byte
	copy(ip[:], tcp.IP.To4())
	servers := []protocol.ServerListEntry{
		{ID: 1, IP: ip, Port: int32(tcp.Port), CurrentPlayers: 42, MaxPlayers: 500, Status: 1, ServerType: 1},
		{ID: 2, IP: [4]byte{10, 0, 0, 5}, Port: 7778, AgeLimit: 15, PvP: true,
			CurrentPlayers: 7, MaxPlayers: 100, Status: 1, ServerType: 4, Brackets: true},
	}
	chars := []protocol.ServerChars{
		{ServerID: 1, CharCount: 3, DeleteTimes: []int32{3600}},
		{ServerID: 2},
	}
	return servers, chars
}

// decodeAuthBlock расшифровывает 128-байтовый блоб RequestAuthLogin тестовой
// парой сценария и возвращает учётные данные. Мусорный блоб не паникует:
// длина проверяется, поля читает конструктор представления.
func decodeAuthBlock(key *rsa.PrivateKey, ct []byte) (user, pass string, err error) {
	if len(ct) != 128 {
		return "", "", fmt.Errorf("блоб %d Б; want 128", len(ct))
	}
	m := new(big.Int).Exp(new(big.Int).SetBytes(ct), key.D, key.N)
	out := make([]byte, 128)
	m.FillBytes(out)
	v, ok := protocol.NewRequestAuthLoginPlainView(out)
	if !ok {
		return "", "", errors.New("расшифрованный блок короче полей")
	}
	return v.User(), v.Password(), nil
}

// scrambledModulus — скрэмблированный модулус тестовой пары (для Init).
func (s *ScenarioServer) scrambledModulus() []byte {
	mod := make([]byte, 128)
	s.priv.N.FillBytes(mod)
	sc, err := crypto.RSAScrambleModulus(mod)
	if err != nil {
		panic(fmt.Sprintf("сценарий: скрэмбл модулуса: %v", err)) // инвариант тестовой пары
	}
	return sc
}

func writeInitFrame(conn net.Conn, crypt *crypto.LoginCrypt, initWire []byte) error {
	buf := make([]byte, 2+len(initWire)+crypto.MaxFrameOverhead)
	n, err := crypt.EncryptInit(buf[2:], initWire, scenarioXORKey)
	if err != nil {
		return err
	}
	return writeRecord(conn, scenarioTimeout, buf[2:2+n])
}

func writeEncFrame(conn net.Conn, crypt *crypto.LoginCrypt, reply []byte, corrupt bool) error {
	buf := make([]byte, 2+len(reply)+crypto.MaxFrameOverhead)
	n, err := crypt.Encrypt(buf[2:], reply)
	if err != nil {
		return err
	}
	if corrupt && n > 0 {
		buf[2+n-1] ^= 0xFF // битая чексумма
	}
	return writeRecord(conn, scenarioTimeout, buf[2:2+n])
}

// loadScenarioKey читает тестовую RSA-пару. Ключ тестовый, не секрет;
// одноразовая генерация: rsa.GenerateKey(rand.Reader, 1024) →
// x509.MarshalPKCS1PrivateKey → testdata/scenario_test_key.der.
func loadScenarioKey() (*rsa.PrivateKey, error) {
	_, file, _, _ := runtime.Caller(0)
	der, err := os.ReadFile(file[:len(file)-len("scenario.go")] + "testdata/scenario_test_key.der")
	if err != nil {
		return nil, fmt.Errorf("чтение тестового ключа сценария: %w", err)
	}
	key, err := x509.ParsePKCS1PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("разбор тестового ключа сценария: %w", err)
	}
	return key, nil
}

// fixtureWire — wire-байты (опкод + payload) фикстуры P1.4 по файлу и имени.
// Паника допустима: сценарий — тестовая инфраструктура, отсутствующая
// фикстура — ошибка теста.
func fixtureWire(file, name string) []byte {
	rows, err := fixture.Load(file)
	if err != nil {
		panic(fmt.Sprintf("сценарий: фикстуры %s: %v", file, err))
	}
	for _, r := range rows {
		if r.Name != name {
			continue
		}
		w := make([]byte, 1+len(r.Payload))
		w[0] = byte(r.Op)
		copy(w[1:], r.Payload)
		return w
	}
	panic(fmt.Sprintf("сценарий: фикстура %s отсутствует", name))
}

// mustLSFixture/mustGSFixture — короткие имена golden-ответов сценария.
func mustLSFixture(name string) []byte { return fixtureWire("login", name) }

func mustGSFixture(name string) []byte { return fixtureWire("handshake", name) }

// wireGGAuth — wire-байты ответа GGAuth с данным кодом (для сценариев).
func wireGGAuth(response int32) []byte {
	w := make([]byte, protocol.GGAuthSize)
	protocol.WriteGGAuth(w, response)
	return w
}
