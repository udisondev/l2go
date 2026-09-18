package world

// Регион-актор: ветка разрыва чата (мусорный Say2) — полный уход с реальным
// сохранением и развязкой (F1 реестра P3.11); двойной мусор одной пачки
// безвреден (прецедент двойного Logout: второй Remove no-op).

import (
	"encoding/binary"
	"testing"

	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/transport"
)

// enterChatPlayer — вход игрока в харнессе; возвращает EntityID из бинда.
func enterChatPlayer(t *testing.T, h *enterHarness, conn uint64, account string) transport.EntityID {
	t.Helper()
	rec := mkRec(account, account, 0)
	rec.X, rec.Y, rec.Z = int(syncPos.X), int(syncPos.Y), int(syncPos.Z)
	h.send(t, h.ctrlLetter(transport.KindEnterWorld,
		mustJSONEnter(conn, rec)))
	batch := h.gwBox.ExtractInto(h.gwToken, nil)
	h.gwBox.AckNotify()
	for _, env := range batch {
		if env.Kind != transport.KindConnBind {
			continue
		}
		m, err := transport.DecodeLetter[transport.ConnBindMsg](env.Payload)
		if err != nil || m.Conn != conn {
			t.Fatalf("бинд: %+v err=%v", m, err)
		}
		return m.Entity
	}
	t.Fatal("KindConnBind не найден")
	return 0
}

// garbageSay2 — кадр Say2 с типом вне таблицы канона («packet hack»-ветка).
func garbageSay2(id, from transport.EntityID) transport.Envelope {
	b := []byte{protocol.OpCSay2, 0, 0} // пустая строка
	b = binary.LittleEndian.AppendUint32(b, 999)
	return transport.Envelope{
		To: transport.Addr{Entity: id}, FromID: from,
		Kind: transport.KindClientFrame, Payload: b,
	}
}

// Мусорный Say2 → полный уход: ActionFailed и LeaveWorld доставлены, ConnClose
// шлюзу, OpSaveChar персисту, Residents→0 после живого шага (не вакуум —
// твин P3.10-F32).
func TestRegionSay2GarbageTypeFullLeave(t *testing.T) {
	h := newEnterHarness(t, DefaultConfig())
	id := enterChatPlayer(t, h, 7, "chatter")
	if n := h.r.Stats().Residents; n != 1 {
		t.Fatalf("Residents после входа = %d; want 1", n)
	}

	h.reg.Send(garbageSay2(id, h.gwID))
	waitTick(t, h.r)

	// ConnClose шлюзу (close-after-flush: кадры уже в стейдже).
	batch := h.gwBox.ExtractInto(h.gwToken, nil)
	h.gwBox.AckNotify()
	closed := false
	for _, env := range batch {
		if env.Kind == transport.KindConnClose {
			m, err := transport.DecodeLetter[transport.ConnRefMsg](env.Payload)
			if err != nil || m.Conn != 7 {
				t.Fatalf("ConnClose: %+v err=%v", m, err)
			}
			closed = true
		}
	}
	if !closed {
		t.Error("KindConnClose не отправлен шлюзу")
	}
	// Кадры: ActionFailed перед LeaveWorld (порядок пушей ветки).
	var sawAF, sawLW bool
	for _, p := range h.pushes.Snapshot() {
		if p.Client != 7 {
			continue
		}
		switch p.Frame[0] {
		case opActionFailed:
			sawAF = true
		case 0x7E: // LeaveWorld
			sawLW = true
		}
	}
	if !sawAF || !sawLW {
		t.Errorf("кадры разрыва: ActionFailed=%v LeaveWorld=%v; want оба", sawAF, sawLW)
	}
	// Персист: OpSaveChar с корреляцией сущности.
	pBatch := h.pBox.ExtractInto(h.pToken, nil)
	h.pBox.AckNotify()
	saved := false
	for _, env := range pBatch {
		if env.Kind != transport.KindPersistRequest {
			continue
		}
		req, err := persist.DecodeRequest(env.Payload)
		if err != nil {
			t.Fatalf("DecodeRequest: %v", err)
		}
		if req.Op == persist.OpSaveChar && req.Corr == uint64(id) {
			saved = true
		}
	}
	if !saved {
		t.Error("OpSaveChar не отправлен персисту")
	}
	// Уход состоялся живым шагом.
	if n := h.r.Stats().Residents; n != 0 {
		t.Errorf("Residents = %d; want 0 (призрак — разрыв без хвоста)", n)
	}
	if h.r.Stats().Failed != 0 {
		t.Errorf("Failed = %d; want 0 (без паник)", h.r.Stats().Failed)
	}
}

// Двойной мусорный Say2 одной пачкой — безвреден: регион жив, один файл
// сохранения, Residents→0 (второй Retire — no-op транспорта).
func TestRegionSay2DoubleGarbageSameBatch(t *testing.T) {
	h := newEnterHarness(t, DefaultConfig())
	id := enterChatPlayer(t, h, 8, "dblchatter")

	h.reg.Send(garbageSay2(id, h.gwID))
	h.reg.Send(garbageSay2(id, h.gwID))
	waitTick(t, h.r)

	if h.regionDone() {
		t.Fatal("Run завершился после двойного мусорного Say2")
	}
	if n := h.r.Stats().Residents; n != 0 {
		t.Errorf("Residents = %d; want 0", n)
	}
	if h.r.Stats().Failed != 0 {
		t.Errorf("Failed = %d; want 0", h.r.Stats().Failed)
	}
	// Запись SaveQ одна (юнит TestFoldSay2BatchInterleavingsTable); писем
	// может быть два — идемпотентный upsert одного снимка (прецедент двойного
	// Logout; S6: guard не обязателен). Все письма — одной корреляции/аккаунта.
	pBatch := h.pBox.ExtractInto(h.pToken, nil)
	h.pBox.AckNotify()
	saves := 0
	for _, env := range pBatch {
		if env.Kind != transport.KindPersistRequest {
			continue
		}
		req, err := persist.DecodeRequest(env.Payload)
		if err != nil {
			t.Fatalf("DecodeRequest: %v", err)
		}
		if req.Op != persist.OpSaveChar || req.Corr != uint64(id) || req.Account != "dblchatter" {
			t.Errorf("чужое письмо персиста: %+v", req)
		}
		saves++
	}
	if saves == 0 {
		t.Error("сохранение не отправлено вовсе")
	}
}
