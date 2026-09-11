// Rewrite-ветка login-ноги: подмена адреса ServerList на слушателя game-ноги
// тапа. Init проходит static-фазу (DecryptInit → SetKey), дальше динамический
// Blowfish. Любое расхождение формата — skip: кадр идёт нетронутым.

package tap

import (
	"encoding/binary"
	"fmt"

	"github.com/udisondev/l2go/internal/crypto"
	"github.com/udisondev/l2go/internal/protocol"
)

// loginRewriter — крипто-состояние S→C login-ноги.
type loginRewriter struct {
	crypt    *crypto.LoginCrypt
	initSeen bool
	gameIP   [4]byte
	gamePort int32
}

func newLoginRewriter(gameIP [4]byte, gamePort int32) *loginRewriter {
	return &loginRewriter{crypt: crypto.NewLoginCrypt(), gameIP: gameIP, gamePort: gamePort}
}

// process обрабатывает запись S→C: возвращает байты для пересылки клиенту и
// (только при сработавшем rewrite) оригинал для записи recOriginal.
// Ошибка — не модифицировать кадр (вызывающий делает skip с slog).
func (r *loginRewriter) process(rec []byte) (out, orig []byte, err error) {
	if len(rec) < 3 {
		return nil, nil, fmt.Errorf("кадр %d байт < 3", len(rec))
	}
	work := make([]byte, len(rec)-2)
	copy(work, rec[2:])
	if !r.initSeen {
		if err := r.crypt.DecryptInit(work); err != nil {
			return nil, nil, fmt.Errorf("init: %w", err)
		}
		v, ok := protocol.NewInitView(work)
		if !ok {
			return nil, nil, fmt.Errorf("init: обрезанное тело (%d байт)", len(work))
		}
		if err := r.crypt.SetKey(v.BlowfishKey()); err != nil {
			return nil, nil, fmt.Errorf("init: %w", err)
		}
		r.initSeen = true
		return rec, nil, nil
	}
	if err := r.crypt.Decrypt(work); err != nil {
		return nil, nil, fmt.Errorf("расшифровка: %w", err)
	}
	if len(work) == 0 || work[0] != protocol.OpServerList {
		return rec, nil, nil
	}
	v, ok := protocol.NewServerListView(work)
	if !ok {
		return nil, nil, fmt.Errorf("ServerList: обрезанное тело (%d Б)", len(work))
	}
	if v.Count() != 1 {
		return nil, nil, fmt.Errorf("ServerList: серверов %d (rewrite — только для 1)", v.Count())
	}
	entry, ok := v.Server(0)
	if !ok {
		return nil, nil, fmt.Errorf("ServerList: запись 0 обрезана")
	}
	var chars []protocol.ServerChars
	if n, has := v.CharsCount(); has {
		for i := 0; i < n; i++ {
			c, ok := v.Chars(i)
			if !ok {
				return nil, nil, fmt.Errorf("ServerList: счётчик %d обрезан", i)
			}
			chars = append(chars, c)
		}
	}
	entry.IP = r.gameIP
	entry.Port = r.gamePort
	servers := []protocol.ServerListEntry{entry}
	payload := make([]byte, protocol.ServerListSize(servers, chars))
	protocol.WriteServerList(payload, servers, chars, v.LastServer())
	frame := make([]byte, len(payload)+crypto.MaxFrameOverhead)
	n, err := r.crypt.Encrypt(frame, payload)
	if err != nil {
		return nil, nil, fmt.Errorf("шифровка ServerList: %w", err)
	}
	frame = frame[:n]
	outRec := make([]byte, 2+len(frame))
	binary.LittleEndian.PutUint16(outRec, uint16(len(frame)+2))
	copy(outRec[2:], frame)
	orig = make([]byte, len(rec))
	copy(orig, rec)
	return outRec, orig, nil
}
