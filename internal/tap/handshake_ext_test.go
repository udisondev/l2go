package tap

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/udisondev/l2go/internal/crypto"
	"github.com/udisondev/l2go/internal/protocol"
)

// Запись журнала идёт в bytes.Buffer и не падает; «_ =» на писателе записей —
// идиома пакета (прецедент decode_test.go).

// extendedProtocolVersion — расширенный хендшейк 265 Б: опкод + версия +
// 260-байтовая таблица патченных клиентов (живое свидетельство KT3-3).
func extendedProtocolVersion(version int32, tableFill byte) []byte {
	frame := make([]byte, protocol.ProtocolVersionExtendedSize)
	frame[0] = protocol.OpProtocolVersion
	protocol.WriteD(frame[1:], version)
	for i := 5; i < len(frame); i++ {
		frame[i] = tableFill
	}
	return frame
}

// Расширенный ProtocolVersion (265 Б) классифицируется как game-хендшейк —
// имя из каталога, не hex.
func TestDecodeClassifiesExtendedProtocolVersion(t *testing.T) {
	t.Parallel()
	journal := buildJournal(t, func(jw *JournalWriter) {
		_ = jw.ConnOpen(1, "a:1", "b:2", 0)
		_ = jw.writeData(recData, 1, DirCtoS, 1, wireRecord(extendedProtocolVersion(protocol.ProtocolVersionInterlude, 0)))
	})
	var log bytes.Buffer
	if err := Decode(bytes.NewReader(journal), DecodeOptions{Log: &log}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	want := "→ PROTOCOL_VERSION version=746"
	if !strings.Contains(log.String(), want) {
		t.Errorf("расширенный хендшейк не классифицирован; want %q;\ngot:\n%s", want, log.String())
	}
}

// Сквозная расшифровка game-ноги за расширенным хендшейком: KeyPacket сеет
// оба движка, кадры обеих ног расшифровываются и получают имена каталога.
func TestDecodeExtendedHandshakeDecryptsGameLeg(t *testing.T) {
	t.Parallel()
	var key [8]byte
	for i := range key {
		key[i] = byte(i + 1)
	}
	keyPacket := make([]byte, protocol.KeyPacketSize)
	protocol.WriteKeyPacket(keyPacket, 1, key[:], true, 1)

	clientFrame := make([]byte, protocol.LogoutSize)
	protocol.WriteLogout(clientFrame)
	serverFrame := make([]byte, protocol.GSLoginFailSize)
	protocol.WriteGSLoginFail(serverFrame, protocol.GSReasonSystemErrorLoginLater)

	// каскады сторон сеются одинаково; каждый движок шифрует свою ногу
	cliCrypt := crypto.NewGameCrypt(key)
	cliCrypt.Enable()
	if err := cliCrypt.Encrypt(clientFrame); err != nil {
		t.Fatalf("encrypt client: %v", err)
	}
	srvCrypt := crypto.NewGameCrypt(key)
	srvCrypt.Enable()
	if err := srvCrypt.Encrypt(serverFrame); err != nil {
		t.Fatalf("encrypt server: %v", err)
	}

	journal := buildJournal(t, func(jw *JournalWriter) {
		_ = jw.ConnOpen(1, "a:1", "b:2", 0)
		_ = jw.writeData(recData, 1, DirCtoS, 1, wireRecord(extendedProtocolVersion(protocol.ProtocolVersionInterlude, 0)))
		_ = jw.writeData(recData, 1, DirStoC, 2, wireRecord(keyPacket))
		_ = jw.writeData(recData, 1, DirCtoS, 3, wireRecord(clientFrame))
		_ = jw.writeData(recData, 1, DirStoC, 4, wireRecord(serverFrame))
		_ = jw.ConnClose(1, nil)
	})
	var log, fixts bytes.Buffer
	if err := Decode(bytes.NewReader(journal), DecodeOptions{Log: &log, Fixtures: &fixts}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	for _, want := range []string{
		"→ PROTOCOL_VERSION version=746",
		"← KEY_PACKET result=1 encryption=true serverID=1 key=8",
		"→ LOGOUT",
		// GS LoginFail в логе — имя каталога + hex (типизированных полей нет);
		// оракул расшифровки — само имя
		"← LOGIN_FAIL hex=",
	} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("нет строки %q;\ngot:\n%s", want, log.String())
		}
	}
	if !strings.Contains(fixts.String(), `"name": "LOGOUT"`) {
		t.Errorf("фикстуры без LOGOUT:\n%s", fixts.String())
	}
}

// Маркер таблицы различает семейства хендшейка в логе: ровно у расширенного.
func TestDecodeProtocolVersionTableMarkerDifferential(t *testing.T) {
	t.Parallel()
	classic := make([]byte, protocol.ProtocolVersionSize)
	protocol.WriteProtocolVersion(classic, protocol.ProtocolVersionInterlude)

	decodeLog := func(t *testing.T, frame []byte) string {
		t.Helper()
		journal := buildJournal(t, func(jw *JournalWriter) {
			_ = jw.ConnOpen(1, "a:1", "b:2", 0)
			_ = jw.writeData(recData, 1, DirCtoS, 1, wireRecord(frame))
		})
		var log bytes.Buffer
		if err := Decode(bytes.NewReader(journal), DecodeOptions{Log: &log}); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		return log.String()
	}
	ext := decodeLog(t, extendedProtocolVersion(protocol.ProtocolVersionInterlude, 0))
	cls := decodeLog(t, classic)
	if !strings.Contains(ext, "table=260") {
		t.Errorf("расширенный хендшейк без маркера таблицы:\n%s", ext)
	}
	if strings.Contains(cls, "table=") {
		t.Errorf("классический хендшейк несёт маркер таблицы:\n%s", cls)
	}
}

// Probe-коннект живого клиента: классический ProtocolVersion 0xFFFFFFFE —
// версия читается, Decode молчит (регресс-пин; красный не требуется —
// поведение уже canon).
func TestDecodeProbeProtocolVersionFFFFFFFF(t *testing.T) {
	t.Parallel()
	frame := make([]byte, protocol.ProtocolVersionSize)
	protocol.WriteProtocolVersion(frame, -2)
	journal := buildJournal(t, func(jw *JournalWriter) {
		_ = jw.ConnOpen(1, "a:1", "b:2", 0)
		_ = jw.writeData(recData, 1, DirCtoS, 1, wireRecord(frame))
		_ = jw.ConnClose(1, nil)
	})
	var log bytes.Buffer
	if err := Decode(bytes.NewReader(journal), DecodeOptions{Log: &log}); err != nil {
		t.Fatalf("Decode: %v (want nil)", err)
	}
	if !strings.Contains(log.String(), "version=-2") {
		t.Errorf("probe-версия не прочитана:\n%s", log.String())
	}
}

// Строгая пара размеров хендшейка {5, 265}: прочие размеры с опкодом 0x00 —
// hex-деградация с нейтральным именем (толерантность не просачивается).
func TestDecodeProtocolVersionStrictSizes(t *testing.T) {
	t.Parallel()
	for _, size := range []int{4, 6, 264, 266} {
		t.Run(strconv.Itoa(size)+"Б", func(t *testing.T) {
			t.Parallel()
			frame := make([]byte, size)
			frame[0] = protocol.OpProtocolVersion
			journal := buildJournal(t, func(jw *JournalWriter) {
				_ = jw.ConnOpen(1, "a:1", "b:2", 0)
				_ = jw.writeData(recData, 1, DirCtoS, 1, wireRecord(frame))
			})
			var log bytes.Buffer
			if err := Decode(bytes.NewReader(journal), DecodeOptions{Log: &log}); err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if strings.Contains(log.String(), "PROTOCOL_VERSION") {
				t.Errorf("кадр %d Б получил игровое имя:\n%s", size, log.String())
			}
			if !strings.Contains(log.String(), "hex=") {
				t.Errorf("кадр %d Б без hex-деградации:\n%s", size, log.String())
			}
		})
	}
}

// Мусорная таблица расширенного хендшейка не роняет декодер: версия читается,
// остальное — байты без интерпретации.
func TestDecodeExtendedTableGarbageNoPanic(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("паника: %v", r)
		}
	}()
	journal := buildJournal(t, func(jw *JournalWriter) {
		_ = jw.ConnOpen(1, "a:1", "b:2", 0)
		_ = jw.writeData(recData, 1, DirCtoS, 1, wireRecord(extendedProtocolVersion(protocol.ProtocolVersionInterlude, 0xFF)))
	})
	var log bytes.Buffer
	if err := Decode(bytes.NewReader(journal), DecodeOptions{Log: &log}); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !strings.Contains(log.String(), "version=746") {
		t.Errorf("версия не прочитана на мусорной таблице:\n%s", log.String())
	}
}
