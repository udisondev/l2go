package login

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/udisondev/l2go/internal/crypto"
	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/protocol"
)

// maxFrameBytes — кэп заявленной длины кадра (F30): легитимные кадры login-флоу
// ≤194 Б, потолок жанра ServerList ~5.5 КиБ; заявленное сверх — протокольное
// нарушение, закрытие (дозатор не растит буфер).
const maxFrameBytes = 8 * 1024

// connState — окно ожиданий стейт-машины коннекта; пакет вне окна — закрытие.
type connState int

const (
	stateGG     connState = iota // ждём AuthGameGuard
	stateAuth                    // ждём RequestAuthLogin
	stateAuthed                  // после LoginOk: RequestServerList | RequestServerLogin
	statePlayed                  // после PlayOk: окно закрыто (F11)
)

// clientConn — горутина-на-коннект: крипта сессии и ключи после выдачи.
type clientConn struct {
	srv  *Server
	conn net.Conn
	lc   *crypto.LoginCrypt

	writeMu sync.Mutex // сериализация записей: kick из чужой горутины (F5)

	sessionID int32
	state     connState
	account   string // нормализованный логин после LoginOk
	loginOk1  int32
	loginOk2  int32
}

func newClientConn(s *Server, conn net.Conn) *clientConn {
	return &clientConn{srv: s, conn: conn, lc: crypto.NewLoginCrypt()}
}

// handle проводит коннект по флоу до закрытия; все отказы детерминированные.
func (cc *clientConn) handle() {
	defer cc.close()
	defer cc.forgetAccount()
	// Абсолютный дедлайн фазы хендшейка (F25): dribble байтами не продлевает.
	if err := cc.conn.SetDeadline(time.Now().Add(cc.srv.cfg.HandshakeTimeout)); err != nil {
		return
	}
	if !cc.sendInit() {
		return
	}

	buf := make([]byte, maxFrameBytes)
	for {
		frame, err := cc.readFrame(buf)
		if err != nil {
			return // обрыв/таймаут/мусор: закрытие без диагностики клиенту
		}
		switch cc.state {
		case stateGG:
			if !cc.handleGG(frame) {
				return
			}
		case stateAuth:
			if !cc.handleAuth(frame) {
				return
			}
		case stateAuthed:
			if !cc.handleAuthed(frame) {
				return
			}
		case statePlayed:
			return // окно закрыто: любой пакет — конец (F11)
		}
	}
}

// forgetAccount снимает связку authed-коннект ↔ аккаунт.
func (cc *clientConn) forgetAccount() {
	if cc.account != "" {
		cc.srv.forgetAuthed(cc.account, cc)
	}
}

// sendInit шлёт Init (RSA-модуль, BF-ключ, sessionID) и переводит крипту в
// динамическую фазу.
func (cc *clientConn) sendInit() bool {
	var bfKey [16]byte
	var sid [4]byte
	var xor [4]byte
	_, _ = rand.Read(bfKey[:]) // crypto/rand.Read не ошибается (Go 1.24+)
	_, _ = rand.Read(sid[:])
	_, _ = rand.Read(xor[:])
	cc.sessionID = int32(binary.LittleEndian.Uint32(sid[:]))

	var payload [protocol.InitSize]byte
	n := protocol.WriteInit(payload[:], cc.sessionID, cc.srv.scrambledN, bfKey[:])
	frame := make([]byte, protocol.InitSize+crypto.MaxFrameOverhead)
	fn, err := cc.lc.EncryptInit(frame, payload[:n], binary.LittleEndian.Uint32(xor[:]))
	if err != nil {
		slog.Error("login: шифрование Init", "err", err)
		return false
	}
	if !cc.writeRecord(frame[:fn]) {
		return false
	}
	if err := cc.lc.SetKey(bfKey[:]); err != nil {
		slog.Error("login: SetKey", "err", err)
		return false
	}
	return true
}

// readFrame читает одну запись [uint16 LE длина][кадр], расшифровывает на
// месте; после LoginOk дедлайн перезаводится только полным кадром (F25).
func (cc *clientConn) readFrame(buf []byte) ([]byte, error) {
	if cc.state >= stateAuthed {
		if err := cc.conn.SetReadDeadline(time.Now().Add(cc.srv.cfg.IdleTimeout)); err != nil {
			return nil, err
		}
	}
	var header [2]byte
	if _, err := io.ReadFull(cc.conn, header[:]); err != nil {
		return nil, err
	}
	n := int(binary.LittleEndian.Uint16(header[:]))
	if n < 2 {
		return nil, protocol.ErrFrameLength
	}
	if n-2 > maxFrameBytes {
		return nil, fmt.Errorf("login: заявленная длина кадра %d > кэпа %d: %w",
			n-2, maxFrameBytes, protocol.ErrFrameLength)
	}
	frame := buf[:n-2]
	if _, err := io.ReadFull(cc.conn, frame); err != nil {
		return nil, err
	}
	if err := cc.lc.Decrypt(frame); err != nil {
		return nil, err
	}
	return frame, nil
}

// writeRecord пишет кадр с рамкой; writeMu — от интерливинга с kick-записью.
func (cc *clientConn) writeRecord(frame []byte) bool {
	cc.writeMu.Lock()
	defer cc.writeMu.Unlock()
	var header [2]byte
	binary.LittleEndian.PutUint16(header[:], uint16(len(frame)+2))
	headerAndFrame := make([]byte, 0, len(frame)+2)
	headerAndFrame = append(headerAndFrame, header[:]...)
	headerAndFrame = append(headerAndFrame, frame...)
	if err := cc.conn.SetWriteDeadline(time.Now().Add(writeStageTimeout)); err != nil {
		return false
	}
	if _, err := cc.conn.Write(headerAndFrame); err != nil {
		return false
	}
	return true
}

// sendPacket шифрует и пишет payload (опкод+тело).
func (cc *clientConn) sendPacket(payload []byte) bool {
	frame := make([]byte, len(payload)+crypto.MaxFrameOverhead)
	n, err := cc.lc.Encrypt(frame, payload)
	if err != nil {
		return false
	}
	return cc.writeRecord(frame[:n])
}

// failClose — close-after-fail (канон): отказ клиенту и закрытие коннекта.
func (cc *clientConn) failClose(write func(dst []byte) int, reason string) {
	var payload [8]byte
	n := write(payload[:])
	_ = cc.sendPacket(payload[:n])
	slog.Warn("login: вход отклонён", "login", cc.masked(), "reason", reason,
		"addr", cc.conn.RemoteAddr().String())
}

// masked — логин для журнала (F18): без пароля; пустой до фазы Auth — прочерк.
func (cc *clientConn) masked() string {
	if cc.account == "" {
		return "-"
	}
	return cc.account
}

// closeWithFail — отказ из чужой горутины (kick старого коннекта, F5).
func (cc *clientConn) closeWithFail(reason protocol.LoginFailReason) {
	var payload [protocol.LoginFailSize]byte
	n := protocol.WriteLoginFail(payload[:], reason)
	_ = cc.sendPacket(payload[:n])
	cc.close()
}

func (cc *clientConn) close() {
	_ = cc.conn.Close()
}

// wake растормошивает заблокированные Read/Write (дрен при остановке).
func (cc *clientConn) wake(past time.Time) {
	_ = cc.conn.SetDeadline(past)
}

// handleGG — AuthGameGuard: сверка sessionID (канон: несовпадение — отказ).
func (cc *clientConn) handleGG(frame []byte) bool {
	if frame[0] != protocol.OpAuthGameGuard {
		return false
	}
	v, ok := protocol.NewAuthGameGuardView(frame)
	if !ok || v.SessionID() != cc.sessionID {
		cc.failClose(func(dst []byte) int {
			return protocol.WriteLoginFail(dst, protocol.ReasonAccessFailed)
		}, "authgameguard: чужой sessionID")
		return false
	}
	var payload [protocol.GGAuthSize]byte
	n := protocol.WriteGGAuth(payload[:], cc.sessionID)
	if !cc.sendPacket(payload[:n]) {
		return false
	}
	cc.state = stateAuth
	return true
}

// handleAuth — RequestAuthLogin: RSA-блоб → Verify → вердикты канона.
func (cc *clientConn) handleAuth(frame []byte) bool {
	if frame[0] != protocol.OpRequestAuthLogin {
		return false
	}
	v, ok := protocol.NewRequestAuthLoginView(frame)
	if !ok {
		return false
	}
	plain, err := crypto.RSADecryptNoPadding(cc.srv.key, v.RSABlock())
	if err != nil {
		// Не-расшифровываемый блок — протокольное нарушение, закрытие.
		slog.Warn("login: RSA-блоб не расшифрован", "addr", cc.conn.RemoteAddr().String())
		return false
	}
	pv, ok := protocol.NewRequestAuthLoginPlainView(plain)
	if !ok {
		return false
	}
	user, pass := pv.User(), pv.Password()
	// Учётные данные в журналы не пишутся (F16): логин ниже — после фазы,
	// вердикт без пароля.
	cc.account = strings.ToLower(user)

	switch cc.srv.accounts.Verify(user, pass) {
	case persist.VerdictOK:
		return cc.issueLoginOk()
	case persist.VerdictBanned:
		cc.failClose(func(dst []byte) int {
			return protocol.WriteAccountKicked(dst, protocol.KickPermanentlyBanned)
		}, "banned")
		return false
	default: // NoAccount/BadPassword: null-путь канона, существование не раскрывается
		cc.failClose(func(dst []byte) int {
			return protocol.WriteLoginFail(dst, protocol.ReasonUserOrPassWrong)
		}, "user_or_pass_wrong")
		return false
	}
}

// issueLoginOk — LoginOk с новой loginOk-парой; живая сессия того же аккаунта
// отклоняет обоих (канон ALREADY_ON_LS: замена со следующей попытки).
func (cc *clientConn) issueLoginOk() bool {
	k1, k2 := randInt32(), randInt32()
	if err := cc.srv.sessions.Put(cc.account, k1, k2); err != nil {
		if old := cc.srv.registerAuthed(cc.account, cc); old != nil {
			old.closeWithFail(protocol.ReasonAccountInUse)
		}
		cc.failClose(func(dst []byte) int {
			return protocol.WriteLoginFail(dst, protocol.ReasonAccountInUse)
		}, "account_in_use")
		return false
	}
	cc.srv.registerAuthed(cc.account, cc)
	cc.loginOk1, cc.loginOk2 = k1, k2
	var payload [protocol.LoginOkSize]byte
	n := protocol.WriteLoginOk(payload[:], k1, k2)
	if !cc.sendPacket(payload[:n]) {
		return false
	}
	// Хендшейк завершён: абсолютный дедлайн снимается, дальше — дедлайн
	// полного кадра (readFrame).
	if err := cc.conn.SetDeadline(time.Time{}); err != nil {
		return false
	}
	cc.state = stateAuthed
	return true
}

// handleAuthed — RequestServerList / RequestServerLogin со сверкой loginOk-пары.
func (cc *clientConn) handleAuthed(frame []byte) bool {
	switch frame[0] {
	case protocol.OpRequestServerList:
		v, ok := protocol.NewRequestServerListView(frame)
		if !ok || !cc.srv.sessions.CheckLoginPair(cc.account, v.LoginOkID1(), v.LoginOkID2()) {
			cc.failClose(func(dst []byte) int {
				return protocol.WriteLoginFail(dst, protocol.ReasonAccessFailed)
			}, "server_list: неверная пара loginOk")
			return false
		}
		return cc.sendServerList()
	case protocol.OpRequestServerLogin:
		v, ok := protocol.NewRequestServerLoginView(frame)
		if !ok || !cc.srv.sessions.CheckLoginPair(cc.account, v.LoginOkID1(), v.LoginOkID2()) {
			cc.failClose(func(dst []byte) int {
				return protocol.WriteLoginFail(dst, protocol.ReasonAccessFailed)
			}, "server_login: неверная пара loginOk")
			return false
		}
		return cc.issuePlayOk(v.ServerID())
	default:
		return false // вне ожидания
	}
}

// sendServerList строит список из реестра GS (дефолты записи — константы
// канона), lastServer — ID первого.
func (cc *clientConn) sendServerList() bool {
	entries := cc.srv.link.Servers()
	list := make([]protocol.ServerListEntry, 0, len(entries))
	for _, e := range entries {
		ip := net.ParseIP(e.Host)
		if ip == nil || ip.To4() == nil {
			slog.Warn("login: запись GS без IP-литерала пропущена", "host", e.Host, "name", e.Name)
			continue
		}
		var ip4 [4]byte
		copy(ip4[:], ip.To4())
		list = append(list, protocol.ServerListEntry{
			ID:         e.ID,
			IP:         ip4,
			Port:       e.Port,
			AgeLimit:   0,
			PvP:        defaultPvP,
			Status:     serverStatusUp,
			ServerType: serverTypeFree,
		})
	}
	var lastServer byte
	if len(list) > 0 {
		lastServer = list[0].ID
	}
	payload := make([]byte, protocol.ServerListSize(list, nil))
	n := protocol.WriteServerList(payload, list, nil, lastServer)
	return cc.sendPacket(payload[:n])
}

// issuePlayOk — PlayOk с новой playOk-парой; serverID вне реестра — PlayFail
// (причина канона Mobius RequestServerLogin) и закрытие (close-after-fail).
func (cc *clientConn) issuePlayOk(serverID byte) bool {
	for _, e := range cc.srv.link.Servers() {
		if e.ID != serverID {
			continue
		}
		p1, p2 := randInt32(), randInt32()
		if !cc.srv.sessions.SetPlayKeys(cc.account, p1, p2) {
			// Сессия умерла (TTL) — протокольный отказ.
			cc.failClose(func(dst []byte) int {
				return protocol.WriteLoginFail(dst, protocol.ReasonAccessFailed)
			}, "server_login: сессия истекла")
			return false
		}
		var payload [protocol.PlayOkSize]byte
		n := protocol.WritePlayOk(payload[:], p1, p2)
		if !cc.sendPacket(payload[:n]) {
			return false
		}
		cc.state = statePlayed // окно ожиданий закрыто (F11)
		return true
	}
	cc.failClose(func(dst []byte) int {
		return protocol.WritePlayFail(dst, protocol.ReasonServerOverloaded)
	}, "server_login: неизвестный сервер")
	return false
}

func randInt32() int32 {
	var b [4]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read не ошибается (Go 1.24+)
	return int32(binary.LittleEndian.Uint32(b[:]))
}
