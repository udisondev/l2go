package gateway

import (
	"encoding/binary"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/udisondev/l2go/internal/crypto"
	"github.com/udisondev/l2go/internal/protocol"
)

// rawConn — сырой TCP-клиент злых входов: пишет проводные записи, читает.
type rawConn struct {
	t    *testing.T
	conn net.Conn
}

func dialRaw(t *testing.T, addr string) *rawConn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &rawConn{t: t, conn: conn}
}

func (r *rawConn) write(body []byte) {
	r.t.Helper()
	rec := make([]byte, 2+len(body))
	binary.LittleEndian.PutUint16(rec, uint16(len(rec)))
	copy(rec[2:], body)
	if _, err := r.conn.Write(rec); err != nil {
		r.t.Fatalf("запись кадра: %v", err)
	}
}

// readFrameErr читает кадр с таймаутом; ошибка несёт класс: EOF/RST —
// разрыв, os.ErrDeadlineExceeded — коннект жив и молчит (дефект для
// ассертов close-after-fail).
func (r *rawConn) readFrameErr(timeout time.Duration) ([]byte, error) {
	r.t.Helper()
	_ = r.conn.SetReadDeadline(time.Now().Add(timeout))
	head := make([]byte, 2)
	if _, err := readFull(r.conn, head); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint16(head)
	body := make([]byte, n-2)
	if _, err := readFull(r.conn, body); err != nil {
		return nil, err
	}
	return body, nil
}

// expectDead: коннект обязан разорваться (EOF/RST) именно до дедлайна —
// таймаут чтения означает живой молчащий коннект.
func (r *rawConn) expectDead(stage string) {
	r.t.Helper()
	if _, err := r.readFrameErr(2 * time.Second); err == nil ||
		errors.Is(err, os.ErrDeadlineExceeded) {
		r.t.Fatalf("%s: коннект жив (err=%v); want разрыв EOF/RST", stage, err)
	}
}

// readFrame — кадр или nil при любом сбое (для «ответ обязан прийти»).
func (r *rawConn) readFrame(timeout time.Duration) []byte {
	r.t.Helper()
	body, err := r.readFrameErr(timeout)
	if err != nil {
		return nil
	}
	return body
}

func readFull(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// Чужая версия протокола: KeyPacket(result=0) + разрыв.
func TestEvilWrongProtocolVersion(t *testing.T) {
	h := newHarness(t, alwaysValid(), 2*time.Second, true)
	r := dialRaw(t, h.addr)

	wire := make([]byte, protocol.ProtocolVersionSize)
	protocol.WriteProtocolVersion(wire, 999)
	r.write(wire)

	frame := r.readFrame(2 * time.Second)
	if frame == nil {
		t.Fatal("KeyPacket не получен")
	}
	v, ok := protocol.NewKeyPacketView(frame)
	result := byte(0xFF)
	if ok {
		result = v.Result()
	}
	if !ok || result != 0 {
		t.Fatalf("KeyPacket = ok:%v result:%d; want result 0 (отклонение версии)", ok, result)
	}
	r.expectDead("после отклонения версии")
}

// AuthLogin до ProtocolVersion — кадр вне окна: GSLoginFail + разрыв.
func TestEvilAuthLoginOutOfWindow(t *testing.T) {
	h := newHarness(t, alwaysValid(), 2*time.Second, true)
	r := dialRaw(t, h.addr)

	wire := make([]byte, protocol.AuthLoginSize("tester"))
	protocol.WriteAuthLogin(wire, "tester", 1, 2, 3, 4)
	r.write(wire)

	frame := r.readFrame(2 * time.Second)
	if frame == nil || frame[0] != protocol.OpGSLoginFail {
		t.Fatalf("ответ вне окна = %v; want GSLoginFail", frame)
	}
	r.expectDead("после GSLoginFail вне окна")
}

// Слот вне домена (−1) на аутентифицированном входе — GSLoginFail, не паника.
func TestEvilCharacterSelectSlot(t *testing.T) {
	h := newHarness(t, alwaysValid(), 2*time.Second, true)
	gc := dialClient(t, h.addr)
	if err := gc.Handshake(); err != nil {
		t.Fatal(err)
	}
	if _, err := gc.Auth(testEndpoint(), "tester"); err != nil {
		t.Fatal(err)
	}
	if err := gc.SelectChar(-1); err == nil {
		t.Fatal("слот −1 принят при пустом списке")
	}
	if err := gc.SelectChar(0); err == nil {
		t.Fatal("слот 0 принят при пустом списке")
	}
}

// Стационарная фаза: неизвестный опкод — дроп с метрикой, коннект жив;
// коалесцируемый MoveToLocation под лавиной сверх капа — последний
// побеждает (замена старого, не дроп нового).
func TestStationaryInboxPolicy(t *testing.T) {
	g := newTestGateway(t)
	gc := &gconn{id: 1, phase: phWorld, entity: 7}
	g.conns[1] = gc

	move := make([]byte, protocol.MoveToLocationSize)
	protocol.WriteMoveToLocation(move, 9, 9, 9, 0, 0, 0, 1)

	for i := 0; i < g.cfg.InboxCap+5; i++ {
		g.onStationaryFrame(gc, move)
	}
	if len(gc.inbox) != 1 {
		t.Errorf("MoveToLocation-кадров в inbox %d; want 1 (замена, не рост)", len(gc.inbox))
	}
	if st := g.Stats(); st.Coalesced < 5 {
		t.Errorf("Coalesced = %d; want ≥5", st.Coalesced)
	}

	other := make([]byte, protocol.ValidatePositionSize)
	protocol.WriteValidatePosition(other, 1, 2, 3, 4, 0)
	for i := 0; i < g.cfg.InboxCap; i++ { // move занимает слот: InboxCap−1 влезут
		g.onStationaryFrame(gc, other)
	}
	g.onStationaryFrame(gc, other) // кап: дроп нового некоалесцируемого
	if st := g.Stats(); st.InboxDropped != 2 {
		t.Errorf("InboxDropped = %d; want 2", st.InboxDropped)
	}

	g.onStationaryFrame(gc, []byte{0x77}) // неизвестный опкод
	if st := g.Stats(); st.UnknownOp != 1 {
		t.Errorf("UnknownOp = %d; want 1", st.UnknownOp)
	}
	if len(gc.inbox) != g.cfg.InboxCap {
		t.Errorf("inbox после мусорного опкода = %d; want неизменным (%d)", len(gc.inbox), g.cfg.InboxCap)
	}
}

// Повторный AuthLogin при валидации в полёте — вне окна, разрыв (шторм
// валидаций на LS исключён).
func TestEvilAuthLoginStorm(t *testing.T) {
	gate := make(chan struct{})
	v := alwaysValid()
	v.gate = gate
	h := newHarness(t, v, 2*time.Second, true)

	r := dialRaw(t, h.addr)
	wire := make([]byte, protocol.ProtocolVersionSize)
	protocol.WriteProtocolVersion(wire, protocol.ProtocolVersionInterlude)
	r.write(wire)
	keyPacket := r.readFrame(2 * time.Second)
	if keyPacket == nil {
		t.Fatal("KeyPacket не получен")
	}
	kv, ok := protocol.NewKeyPacketView(keyPacket)
	if !ok {
		t.Fatal("KeyPacket обрезан")
	}
	var key [8]byte
	copy(key[:], kv.Key())
	crypt := crypto.NewGameCrypt(key)
	crypt.Enable()

	auth := make([]byte, protocol.AuthLoginSize("tester"))
	protocol.WriteAuthLogin(auth, "tester", 1, 2, 3, 4)
	if err := crypt.Encrypt(auth); err != nil {
		t.Fatal(err)
	}
	r.write(auth)
	clone := make([]byte, len(auth))
	copy(clone, auth)
	r.write(clone) // вторая попытка при валидации в полёте

	frame := r.readFrame(2 * time.Second)
	if frame == nil {
		t.Fatal("ответ не получен")
	}
	if err := crypt.Decrypt(frame); err != nil {
		t.Fatalf("расшифровка ответа: %v", err)
	}
	if frame[0] != protocol.OpGSLoginFail {
		t.Fatalf("повторный AuthLogin в окне = опкод 0x%02X; want GSLoginFail", frame[0])
	}
	r.expectDead("после повторного AuthLogin в окне")
	close(gate)
	waitFor(t, "единственная валидация", func() bool { return v.calls.Load() == 1 })
	if got := v.calls.Load(); got != 1 {
		t.Errorf("ValidateSession вызовов %d; want 1 (шторм отсечён)", got)
	}
}

// Ветки домена создания и удаления (канон-ответы, коннект жив):
// CharacterDelete → CharDeleteFail; чужая раса → CreationFailed; имя 17
// символов → NameTooLong (0x03); недопустимый символ → IncorrectName (0x04).
func TestEvilCreateDomainBranches(t *testing.T) {
	h := newHarness(t, alwaysValid(), 2*time.Second, true)
	r := dialRaw(t, h.addr)

	wire := make([]byte, protocol.ProtocolVersionSize)
	protocol.WriteProtocolVersion(wire, protocol.ProtocolVersionInterlude)
	r.write(wire)
	kp := r.readFrame(2 * time.Second)
	kv, ok := protocol.NewKeyPacketView(kp)
	if !ok {
		t.Fatal("KeyPacket обрезан")
	}
	var key [8]byte
	copy(key[:], kv.Key())
	crypt := crypto.NewGameCrypt(key)
	crypt.Enable()
	enc := func(b []byte) {
		if err := crypt.Encrypt(b); err != nil {
			t.Fatal(err)
		}
		r.write(b)
	}
	expect := func(stage string, op byte) []byte {
		frame := r.readFrame(2 * time.Second)
		if frame == nil {
			t.Fatalf("%s: ответ не получен", stage)
		}
		if err := crypt.Decrypt(frame); err != nil {
			t.Fatalf("%s: расшифровка: %v", stage, err)
		}
		if frame[0] != op {
			t.Fatalf("%s: опкод 0x%02X; want 0x%02X", stage, frame[0], op)
		}
		return frame
	}

	// Вход: AuthLogin → CharSelectionInfo (фаза списка).
	auth := make([]byte, protocol.AuthLoginSize("tester"))
	protocol.WriteAuthLogin(auth, "tester", 1, 2, 3, 4)
	enc(auth)
	expect("AuthLogin", protocol.OpCharSelectInfo)

	// Delete: CharDeleteFail, коннект жив.
	del := make([]byte, protocol.CharacterDeleteSize)
	protocol.WriteCharacterDelete(del, 0)
	enc(del)
	fail := expect("CharacterDelete", protocol.OpCharDeleteFail)
	dv, ok := protocol.NewCharDeleteFailView(fail)
	if !ok || dv.Reason() != protocol.CharDeleteReasonDeletionFailed {
		t.Errorf("CharDeleteFail = ok:%v reason:%d; want DeletionFailed", ok, dv.Reason())
	}

	// Имя 17 символов валидного алфавита → 0x03.
	long := protocol.CharacterCreateData{Name: "Abcdefghijklmnopq", Race: 0, ClassID: 0}
	create := make([]byte, protocol.CharacterCreateSize(long))
	protocol.WriteCharacterCreate(create, long)
	enc(create)
	expectFailReason(t, expect("create длинное имя", protocol.OpCharCreateFail), 0x03)

	// Чужая раса → CreationFailed (0x00).
	elf := protocol.CharacterCreateData{Name: "Elfhero", Race: 2, ClassID: 1}
	create = make([]byte, protocol.CharacterCreateSize(elf))
	protocol.WriteCharacterCreate(create, elf)
	enc(create)
	expectFailReason(t, expect("create чужая раса", protocol.OpCharCreateFail), 0x00)

	// Коннект жив: NewChar получает CharTemplates.
	nc := make([]byte, protocol.NewCharacterSize)
	protocol.WriteNewCharacter(nc)
	enc(nc)
	expect("NewChar после отказов", protocol.OpCharTemplates)
}

func expectFailReason(t *testing.T, frame []byte, want byte) {
	t.Helper()
	v, ok := protocol.NewCharCreateFailView(frame)
	if !ok || byte(v.Reason()) != want {
		t.Errorf("CharCreateFail = ok:%v reason:0x%02X; want 0x%02X", ok, v.Reason(), want)
	}
}
