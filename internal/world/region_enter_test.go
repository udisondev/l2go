package world

// Регион-актор: рождение игрока через контрольное письмо — бинд-письмо шлюзу,
// слиток в пулах шага (актор компонует после applyEffects), grace-экспирация
// с реальным сохранением, активность с SaveQ, shutdown-сохранитель.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/transport"
)

// pushCollector — коллектор пушей по шву FramePusher. Push зовётся и из
// phaseB горутины региона, и из recovered-доставки — доступ под мьютексом;
// тесты читают копию через Snapshot (гонка харнесса = та же находка, что и
// гонка прод-кода).
type pushCollector struct {
	mu     sync.Mutex
	pushes []FramePush
}

func (c *pushCollector) Push(id uint64, frame []byte, crypt bool) {
	c.mu.Lock()
	c.pushes = append(c.pushes, FramePush{Client: id, Frame: frame, Crypt: crypt})
	c.mu.Unlock()
}

// Snapshot возвращает копию накопленных пушей.
func (c *pushCollector) Snapshot() []FramePush {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]FramePush, len(c.pushes))
	copy(out, c.pushes)
	return out
}

// Reset очищает коллектор.
func (c *pushCollector) Reset() {
	c.mu.Lock()
	c.pushes = nil
	c.mu.Unlock()
}

type enterHarness struct {
	reg     *transport.Registry
	metro   *Metronome
	r       *Region
	binds   map[uint64]uint64 // кеш bind-писем (изымается один раз; playerEntity)
	rCancel context.CancelFunc
	rDone   chan struct{}
	pushes  *pushCollector
	gwBox   transport.Mailbox
	gwID    transport.EntityID
	gwToken uint64
	pBox    transport.Mailbox
	pID     transport.EntityID
	pToken  uint64
}

func newEnterHarness(t *testing.T, cfg Config) *enterHarness {
	t.Helper()
	return newEnterHarnessPusher(t, cfg, nil)
}

// newEnterHarnessPusher — харнесс с пушером-двойником, назначаемым ДО старта
// Run (подмена pusher при живой горутине региона — гонка харнесса).
func newEnterHarnessPusher(t *testing.T, cfg Config, pc FramePusher) *enterHarness {
	t.Helper()
	h := buildEnterHarness(t, cfg, pc)
	h.startRun(t)
	return h
}

// buildEnterHarness — харнесс без запуска горутин: точка вмешательства до
// старта Run (DeployNPCs разворота населения).
func buildEnterHarness(t *testing.T, cfg Config, pc FramePusher) *enterHarness {
	t.Helper()
	base := DefaultConfig()
	base.Hz = cfg.Hz
	base.GraceTicks = cfg.GraceTicks
	base.SaveRetryTicks = cfg.SaveRetryTicks
	base.LogMaxFileBytes = 1 << 20
	cfg = base
	if cfg.Hz == 0 {
		cfg.Hz = 50
	}
	if cfg.GraceTicks == 0 {
		cfg.GraceTicks = DefaultGraceTicks
	}
	if cfg.SaveRetryTicks == 0 {
		cfg.SaveRetryTicks = DefaultSaveRetryTicks
	}
	m, err := NewMetronome(cfg)
	if err != nil {
		t.Fatalf("метроном: %v", err)
	}
	reg := transport.NewRegistry(64)
	log, err := NewPortionLog(t.TempDir(), 1, false, cfg.LogMaxFileBytes)
	if err != nil {
		t.Fatalf("лог: %v", err)
	}
	collector := &pushCollector{}
	pusher := FramePusher(collector)
	if pc != nil {
		pusher = pc
	}
	r, err := NewRegion(m, reg, 1, cfg, log, pusher, emptyGeo)
	if err != nil {
		t.Fatalf("регион: %v", err)
	}
	h := &enterHarness{reg: reg, r: r, pushes: collector, metro: m}
	h.gwID = reg.Register(&h.gwBox)
	h.gwToken = uint64(h.gwID)
	if err := h.gwBox.Claim(h.gwToken); err != nil {
		t.Fatal(err)
	}
	h.pID = reg.Register(&h.pBox)
	h.pToken = uint64(h.pID)
	if err := h.pBox.Claim(h.pToken); err != nil {
		t.Fatal(err)
	}
	if err := r.Wire(h.gwID, h.pID); err != nil {
		t.Fatal(err)
	}
	return h
}

// startRun запускает метроном и регион (однократно).
func (h *enterHarness) startRun(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	rctx, rCancel := context.WithCancel(t.Context())
	h.rCancel = rCancel
	h.rDone = make(chan struct{})
	t.Cleanup(cancel)
	t.Cleanup(rCancel)
	go h.metro.Run(ctx)
	go func() { defer close(h.rDone); h.r.Run(rctx) }()
}

// send — письмо в ctrl-ящик региона + ожидание шага.
func (h *enterHarness) send(t *testing.T, env transport.Envelope) {
	t.Helper()
	h.reg.Send(env)
	waitTick(t, h.r)
}

// regionDone — выход Run (сохранитель исполнен).
func (h *enterHarness) regionDone() bool {
	select {
	case <-h.rDone:
		return true
	default:
		return false
	}
}

// waitForResidents — поллинг населения региона до want (бюджет).
func waitForResidents(t *testing.T, r *Region, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if r.Stats().Residents == want {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatalf("Residents = %d; want %d", r.Stats().Residents, want)
}

func waitTick(t *testing.T, r *Region) {
	t.Helper()
	target := r.Stats().DoneTick + 1
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if r.Stats().DoneTick >= target {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("шаг региона не наступил (doneTick=%d, stats=%+v)", r.Stats().DoneTick, r.Stats())
}

func (h *enterHarness) ctrlLetter(kind transport.Kind, payload []byte) transport.Envelope {
	return transport.Envelope{To: transport.Addr{Entity: h.r.CtrlID()}, FromID: h.gwID,
		Kind: kind, Payload: payload}
}

// Бинд-письмо: рождение игрока → KindConnBind с присвоенным ID шлюзу.
func TestRegionConnBindAfterSpawn(t *testing.T) {
	h := newEnterHarness(t, DefaultConfig())
	body, _ := transport.EncodeLetter(transport.EnterWorldMsg{
		Conn: 7, Account: "acc", Char: mustJSONChar(mkRec("acc", "Vasya", 0))})
	h.send(t, h.ctrlLetter(transport.KindEnterWorld, body))

	batch := h.gwBox.ExtractInto(h.gwToken, nil)
	h.gwBox.AckNotify()
	found := false
	for _, env := range batch {
		if env.Kind != transport.KindConnBind {
			continue
		}
		m, err := transport.DecodeLetter[transport.ConnBindMsg](env.Payload)
		if err != nil || m.Conn != 7 || m.Entity == 0 {
			t.Fatalf("бинд: %+v err=%v", m, err)
		}
		found = true
	}
	if !found {
		t.Fatal("KindConnBind не отправлен шлюзу")
	}
	if n := h.r.Stats().Residents; n != 1 {
		t.Fatalf("Residents = %d; want 1", n)
	}
}

// Слиток: 16 пушей с ObjectID-базой тем же шагом, что и бинд.
func TestRegionEnterWorldFramesComposedAfterApplyEffects(t *testing.T) {
	h := newEnterHarness(t, DefaultConfig())
	body, _ := transport.EncodeLetter(transport.EnterWorldMsg{
		Conn: 7, Account: "acc", Char: mustJSONChar(mkRec("acc", "Vasya", 0))})
	h.send(t, h.ctrlLetter(transport.KindEnterWorld, body))

	// шаг завершён — пуши уже исполнены фазой B
	if n := len(h.pushes.Snapshot()); n != 16 {
		t.Fatalf("пушей слитка %d; want 16", n)
	}
	if sp := h.pushes.Snapshot(); sp[0].Client != 7 || !sp[0].Crypt {
		t.Fatalf("первый пуш: %+v", h.pushes.Snapshot()[0])
	}
	// UserInfo: ObjectID = база + ID (ID ≥ 1)
	if sp := h.pushes.Snapshot(); sp[0].Frame[0] != 0x04 {
		t.Fatalf("первый кадр не UserInfo: %#x", sp[0].Frame[0])
	}
}

// Grace-экспирация с реальным сохранением: OpSaveChar в персист-ящике.
func TestRegionGraceExpiryRealTicks(t *testing.T) {
	h := newEnterHarness(t, Config{Hz: 100, GraceTicks: 3})
	body, _ := transport.EncodeLetter(transport.EnterWorldMsg{
		Conn: 7, Account: "acc", Char: mustJSONChar(mkRec("acc", "Vasya", 0))})
	h.send(t, h.ctrlLetter(transport.KindEnterWorld, body))
	ld, _ := transport.EncodeLetter(transport.ConnRefMsg{Conn: 7})
	h.send(t, h.ctrlLetter(transport.KindLinkDead, ld))

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h.r.Stats().Residents == 0 {
			break
		}
		time.Sleep(3 * time.Millisecond)
	}
	if n := h.r.Stats().Residents; n != 0 {
		t.Fatalf("Residents = %d после grace; want 0", n)
	}
	waitTick(t, h.r) // фаза B шага исполнена (happens-before писем)
	batch := h.pBox.ExtractInto(h.pToken, nil)
	h.pBox.AckNotify()
	saw := false
	for _, env := range batch {
		if env.Kind == transport.KindPersistRequest {
			saw = true
		}
	}
	if !saw {
		t.Fatal("экспирация не отправила сохранение персисту")
	}
}

// Активность: SaveQ держит регион шагающим; погашение очереди — сон.
func TestRegionActiveWhileSaveQPending(t *testing.T) {
	h := newEnterHarness(t, Config{Hz: 100, GraceTicks: 2, SaveRetryTicks: 2})
	body, _ := transport.EncodeLetter(transport.EnterWorldMsg{
		Conn: 7, Account: "acc", Char: mustJSONChar(mkRec("acc", "Vasya", 0))})
	h.send(t, h.ctrlLetter(transport.KindEnterWorld, body))
	ld, _ := transport.EncodeLetter(transport.ConnRefMsg{Conn: 7})
	h.send(t, h.ctrlLetter(transport.KindLinkDead, ld))

	waitTick(t, h.r) // экспирация/сохранение
	t1 := h.r.Stats().DoneTick
	time.Sleep(80 * time.Millisecond) // тайминг-окно наблюдения активности
	t2 := h.r.Stats().DoneTick
	if t2 <= t1 {
		t.Fatal("регион уснул с непустым SaveQ — IO-ретраи мертвы")
	}

	// ok-ответ (Corr из письма сохранения) гасит очередь — регион засыпает:
	// жителей нет, SaveQ пуст (после шага, разбуженного письмом).
	var corr uint64
	for _, env := range h.pBox.ExtractInto(h.pToken, nil) {
		if env.Kind != transport.KindPersistRequest {
			continue
		}
		req, err := persist.DecodeRequest(env.Payload)
		if err != nil {
			t.Fatalf("декод запроса: %v", err)
		}
		corr = req.Corr
	}
	h.pBox.AckNotify()
	if corr == 0 {
		t.Fatal("письмо сохранения не найдено")
	}
	reply, err := persist.EncodeReply(persist.Reply{
		Op: persist.OpSaveChar, Corr: corr, OK: true})
	if err != nil {
		t.Fatal(err)
	}
	h.reg.Send(transport.Envelope{To: transport.Addr{Entity: h.r.CtrlID()},
		FromID: h.pID, Kind: transport.KindPersistReply, Payload: reply})
	// Шаг, гасящий очередь, разбужен письмом; после него — затишье спящего
	// региона (жителей нет, SaveQ пуст): ждём остановки фазовых счётчиков.
	deadline := time.Now().Add(2 * time.Second)
	lastPh := h.r.Stats().PhaseAck
	for time.Now().Before(deadline) {
		time.Sleep(30 * time.Millisecond)
		ph := h.r.Stats().PhaseAck
		if ph == lastPh {
			break
		}
		lastPh = ph
	}

	t3 := h.r.Stats().DoneTick
	time.Sleep(80 * time.Millisecond) // тайминг-окно: тишина спящего региона
	t4 := h.r.Stats().DoneTick
	if t4 > t3+2 {
		t.Fatal("регион не уснул после погашения SaveQ (пустой шумит шагами)")
	}
}

// D5: финальный сохранитель — живой игрок и висящая SaveQ-запись получают
// OpSaveChar при отмене ctx региона (Run-defer, горутина региона).
func TestRegionShutdownSavesAndDrains(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Hz = 100
	cfg.GraceTicks = 2
	cfg.SaveRetryTicks = 2
	h := newEnterHarness(t, cfg)
	body, _ := transport.EncodeLetter(transport.EnterWorldMsg{
		Conn: 7, Account: "acc", Char: mustJSONChar(mkRec("acc", "Vasya", 0))})
	h.send(t, h.ctrlLetter(transport.KindEnterWorld, body))
	ld, _ := transport.EncodeLetter(transport.ConnRefMsg{Conn: 7})
	h.reg.Send(h.ctrlLetter(transport.KindLinkDead, ld)) // без ответа персиста

	// Ждём: экспирация → SaveQ зависла (персист молчит — ретраи живы).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.r.Stats().Residents > 0 {
		time.Sleep(3 * time.Millisecond)
	}
	if n := h.r.Stats().Residents; n != 0 {
		t.Fatalf("Residents = %d; want 0 (экспирация)", n)
	}

	h.rCancel() // TERM-путь: отмена ctx региона
	rDone := time.Now().Add(2 * time.Second)
	for time.Now().Before(rDone) && !h.regionDone() {
		time.Sleep(3 * time.Millisecond)
	}
	saves := 0
	for _, env := range h.pBox.ExtractInto(h.pToken, nil) {
		if env.Kind == transport.KindPersistRequest {
			saves++
		}
	}
	h.pBox.AckNotify()
	if saves < 2 {
		t.Fatalf("финальных сохранений %d; want ≥2 (живой до экспирации + дрен SaveQ)", saves)
	}
}

// M1-свидетель (S8-контроль): пачка [Enter,LinkDead,Enter] в один ctrl-дрен —
// Residents==1 (призрак не рождается), один бинд, финальное сохранение одно.
func TestRegionSameStepDisplacementNoGhost(t *testing.T) {
	h := newEnterHarness(t, Config{Hz: 100, GraceTicks: 3, SaveRetryTicks: 2})
	mkEnter := func(conn uint64) transport.Envelope {
		body, err := transport.EncodeLetter(transport.EnterWorldMsg{
			Conn: conn, Account: "acc", Char: mustJSONChar(mkRec("acc", "Vasya", 0))})
		if err != nil {
			t.Fatal(err)
		}
		return h.ctrlLetter(transport.KindEnterWorld, body)
	}
	ld, _ := transport.EncodeLetter(transport.ConnRefMsg{Conn: 1})
	h.reg.Send(mkEnter(1))
	h.reg.Send(h.ctrlLetter(transport.KindLinkDead, ld))
	h.reg.Send(mkEnter(2))
	waitTick(t, h.r)
	waitForResidents(t, h.r, 1)

	binds := 0
	for _, env := range h.gwBox.ExtractInto(h.gwToken, nil) {
		if env.Kind == transport.KindConnBind {
			binds++
		}
	}
	h.gwBox.AckNotify()
	if binds != 1 {
		t.Fatalf("биндов шлюзу %d; want 1 (только живой вход)", binds)
	}

	// Финальный сохранитель: ровно одно сохранение живой сущности (призрак
	// не рождён — финально сохранить нечего).
	h.rCancel()
	for !h.regionDone() {
		time.Sleep(2 * time.Millisecond)
	}
	saves := 0
	for _, env := range h.pBox.ExtractInto(h.pToken, nil) {
		if env.Kind == transport.KindPersistRequest {
			saves++
		}
	}
	h.pBox.AckNotify()
	if saves != 1 {
		t.Fatalf("финальных сохранений %d; want 1 (призрак не сохраняется)", saves)
	}
}
