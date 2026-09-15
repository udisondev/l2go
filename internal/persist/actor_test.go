package persist

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/udisondev/l2go/internal/transport"
)

// testEnv — контур актора: реестр, отправитель с ящиком, актор, звонок.
type testEnv struct {
	t        *testing.T
	reg      *transport.Registry
	actor    *Actor
	doorbell chan struct{}
	sender   transport.Mailbox
	senderID transport.EntityID
	dir      string

	ctx     context.Context
	cancel  context.CancelFunc
	replyCh chan Reply
	done    chan struct{}
}

func newTestEnv(t *testing.T, cfg Config) *testEnv {
	t.Helper()
	if cfg.Dir == "" {
		cfg.Dir = t.TempDir()
	}
	ctx, cancel := context.WithCancel(t.Context())
	env := &testEnv{
		t:        t,
		reg:      transport.NewRegistry(0),
		doorbell: make(chan struct{}, 1),
		dir:      cfg.Dir,
		ctx:      ctx,
		cancel:   cancel,
		replyCh:  make(chan Reply, 64),
		done:     make(chan struct{}),
	}
	env.senderID = env.reg.Register(&env.sender)
	a, err := New(cfg, env.reg, env.doorbell)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	env.actor = a
	return env
}

func (e *testEnv) start() {
	go func() {
		defer close(e.done)
		e.actor.Run(e.ctx)
	}()
	// читатель ответов отправителя: декодирует и раздаёт по replyCh
	go func() {
		token := uint64(e.senderID)
		if err := e.sender.Claim(token); err != nil {
			return
		}
		for {
			batch := e.sender.ExtractInto(token, nil)
			e.sender.AckNotify()
			for _, env := range batch {
				if env.Kind != transport.KindPersistReply {
					continue
				}
				reply, err := DecodeReply(env.Payload)
				if err != nil {
					e.t.Errorf("DecodeReply() error = %v", err)
					return
				}
				select {
				case e.replyCh <- reply:
				case <-e.done:
					return
				}
			}
			select {
			case <-e.sender.Notify():
			case <-e.done:
				return
			}
		}
	}()
}

func (e *testEnv) send(req Request) {
	payload, err := EncodeRequest(req)
	if err != nil {
		e.t.Fatalf("EncodeRequest() error = %v", err)
	}
	e.reg.Send(transport.Envelope{
		To:      transport.Addr{Entity: e.actor.id},
		FromID:  e.senderID,
		Kind:    transport.KindPersistRequest,
		Payload: payload,
	})
}

func (e *testEnv) ask(req Request, timeout time.Duration) Reply {
	e.t.Helper()
	e.send(req)
	select {
	case r := <-e.replyCh:
		return r
	case <-time.After(timeout):
		e.t.Fatalf("ответ на %s corr=%d не пришёл за %v", req.Op, req.Corr, timeout)
		return Reply{}
	}
}

func (e *testEnv) stop() {
	e.cancel()
	<-e.done
}

func TestActorCreateListSnapshot(t *testing.T) {
	env := newTestEnv(t, Config{DrainTimeout: 5 * time.Second, PanicLimit: 3})
	env.start()
	defer env.stop()

	rep := env.ask(Request{Op: OpCreateChar, Corr: 1, Account: "Player1",
		Name: "Vasya", Sex: 0, HairStyle: 1, HairColor: 1, Face: 1}, 5*time.Second)
	if !rep.OK || rep.Record == nil {
		t.Fatalf("CreateChar = %+v; want OK с записью", rep)
	}
	rec := rep.Record
	if rec.Slot != 0 || rec.Level != 1 || rec.HP != HumanFighter.BaseHP ||
		rec.X != HumanFighter.StartX || rec.Name != "Vasya" || rec.Account != "player1" {
		t.Errorf("созданная запись = %+v; want слот 0, уровень 1, позиция/статы шаблона, player1/Vasya", rec)
	}

	list := env.ask(Request{Op: OpCharList, Corr: 2, Account: "player1"}, 5*time.Second)
	if !list.OK || len(list.Chars) != 1 || list.Chars[0].Name != "Vasya" {
		t.Fatalf("CharList = %+v", list)
	}

	rec.X += 500
	snap := env.ask(Request{Op: OpSaveSnapshot, Corr: 3, Account: "player1",
		Chars: []CharRecord{*rec}}, 5*time.Second)
	if !snap.OK {
		t.Fatalf("SaveSnapshot = %+v", snap)
	}
	list = env.ask(Request{Op: OpCharList, Corr: 4, Account: "player1"}, 5*time.Second)
	if len(list.Chars) != 1 || list.Chars[0].X != HumanFighter.StartX+500 {
		t.Errorf("после снимка list = %+v; want X=%d", list.Chars, HumanFighter.StartX+500)
	}
	if list.Chars[0].LastSeenUnix == 0 {
		t.Error("lastSeen не проставлен пишущей стороной")
	}

	// второй персонаж — слот 1; дубликат имени — отказ (включая другой регистр
	// и другой аккаунт)
	rep2 := env.ask(Request{Op: OpCreateChar, Corr: 5, Account: "player1",
		Name: "Petya", Sex: 1, HairStyle: 2, HairColor: 0, Face: 0}, 5*time.Second)
	if !rep2.OK || rep2.Record.Slot != 1 {
		t.Fatalf("второй CreateChar = %+v; want слот 1", rep2)
	}
	dup := env.ask(Request{Op: OpCreateChar, Corr: 6, Account: "player1",
		Name: "VASYA", Sex: 0}, 5*time.Second)
	if dup.OK {
		t.Error("дубликат имени (другой регистр) принят")
	}
	dupAcc := env.ask(Request{Op: OpCreateChar, Corr: 7, Account: "other",
		Name: "vasya", Sex: 0}, 5*time.Second)
	if dupAcc.OK {
		t.Error("имя другого аккаунта занято повторно")
	}
	// слоты 2–6, затем 8-й — отказ
	for i := 0; i < 5; i++ {
		r := env.ask(Request{Op: OpCreateChar, Corr: uint64(8 + i), Account: "player1",
			Name: string(rune('A' + i))}, 5*time.Second)
		if !r.OK {
			t.Fatalf("создание №%d отклонено: %+v", i+3, r)
		}
	}
	eight := env.ask(Request{Op: OpCreateChar, Corr: 99, Account: "player1",
		Name: "OneTooMany"}, 5*time.Second)
	if eight.OK {
		t.Error("8-й персонаж принят")
	}
	// злой логин — детерминированный отказ с эхом корреляции
	evil := env.ask(Request{Op: OpCharList, Corr: 100, Account: "../evil"}, 5*time.Second)
	if evil.OK || evil.Corr != 100 {
		t.Errorf("CharList(злой логин) = %+v; want отказ, corr=100", evil)
	}
	stats := env.actor.Stats()
	if stats.Handled < 10 || stats.Replies < 10 {
		t.Errorf("счётчики актора: Handled=%d Replies=%d; want ≥10/≥10",
			stats.Handled, stats.Replies)
	}
}

func TestActorEvilRequests(t *testing.T) {
	env := newTestEnv(t, Config{DrainTimeout: time.Second, PanicLimit: 3})
	env.start()
	defer env.stop()

	evil := []Request{
		{Op: OpCreateChar, Corr: 1, Account: "../x", Name: "Ok"},
		{Op: OpCreateChar, Corr: 2, Account: "acc", Name: "../x"},
		{Op: OpCreateChar, Corr: 3, Account: "acc", Name: "Ok", Sex: 9},
		{Op: OpCreateChar, Corr: 4, Account: "acc", Name: "Ok", Face: 3},
		{Op: OpSaveSnapshot, Corr: 5, Account: "acc",
			Chars: []CharRecord{{Account: "acc", Name: "Ok", ClassID: 88, Level: 1, HP: 1, MP: 1}}},
		{Op: "unknown-op", Corr: 6, Account: "acc"},
	}
	for _, req := range evil {
		rep := env.ask(req, 5*time.Second)
		if rep.OK {
			t.Errorf("злой запрос %+v принят", req)
		}
		if rep.Corr != req.Corr {
			t.Errorf("эхо corr = %d; want %d", rep.Corr, req.Corr)
		}
	}
	list := env.ask(Request{Op: OpCharList, Corr: 7, Account: "acc"}, 5*time.Second)
	if !list.OK || len(list.Chars) != 0 {
		t.Errorf("после злых запросов list = %+v; want пусто", list.Chars)
	}
}

func TestActorConcurrentSenders(t *testing.T) {
	env := newTestEnv(t, Config{DrainTimeout: 5 * time.Second, PanicLimit: 3})
	env.start()
	defer env.stop()

	const senders = 8
	const createsPerSender = 7 // лимит канона: слоты 0–6
	const perSender = 10       // 7 create + 3 charlist
	var wg sync.WaitGroup
	for s := 0; s < senders; s++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			var box transport.Mailbox
			id := env.reg.Register(&box)
			token := uint64(id)
			_ = box.Claim(token)
			account := "acc" + string(rune('A'+s))
			next := func(n int) Request {
				if n < createsPerSender {
					return Request{Op: OpCreateChar, Corr: uint64(100 + n), Account: account,
						Name: "Char" + string(rune('A'+s)) + string(rune('a'+n)), Sex: 0}
				}
				return Request{Op: OpCharList, Corr: uint64(100 + n), Account: account}
			}
			env.reg.Send(transport.Envelope{
				To:      transport.Addr{Entity: env.actor.id},
				FromID:  id,
				Kind:    transport.KindPersistRequest,
				Payload: mustEncode(t, next(0)),
			})
			deadline := time.After(30 * time.Second)
			got := 0
			for got < perSender {
				batch := box.ExtractInto(token, nil)
				box.AckNotify()
				for _, e := range batch {
					reply, err := DecodeReply(e.Payload)
					if err != nil {
						t.Errorf("DecodeReply() error = %v", err)
						return
					}
					if !reply.OK {
						t.Errorf("ответ не OK: %+v", reply)
						return
					}
					if reply.Op == OpCreateChar && reply.Record == nil {
						t.Error("create без записи")
						return
					}
					got++
					if got < perSender {
						env.reg.Send(transport.Envelope{
							To:      transport.Addr{Entity: env.actor.id},
							FromID:  id,
							Kind:    transport.KindPersistRequest,
							Payload: mustEncode(t, next(got)),
						})
					}
				}
				if got >= perSender {
					break
				}
				select {
				case <-box.Notify():
				case <-deadline:
					t.Errorf("отправитель %d: ответов %d из %d", s, got, perSender)
					return
				}
			}
		}(s)
	}
	wg.Wait()
	list := env.ask(Request{Op: OpCharList, Corr: 999, Account: "accA"}, 5*time.Second)
	if !list.OK || len(list.Chars) != createsPerSender {
		t.Errorf("у accA персонажей %d; want %d", len(list.Chars), createsPerSender)
	}
}

// mustEncode вызывается в том числе из побочных горутин: FailNow там
// некорректен — ошибка, не фаталь.
func mustEncode(t *testing.T, r Request) []byte {
	t.Helper()
	buf, err := EncodeRequest(r)
	if err != nil {
		t.Errorf("EncodeRequest() error = %v", err)
		return nil
	}
	return buf
}

func TestActorTickFallback(t *testing.T) {
	env := newTestEnv(t, Config{DrainTimeout: time.Second, PanicLimit: 3})
	env.start()
	defer env.stop()

	// письмо + звонок: актор просыпается от звонка и обрабатывает
	env.send(Request{Op: OpCharList, Corr: 5, Account: "acc"})
	env.doorbell <- struct{}{}
	select {
	case r := <-env.replyCh:
		if r.Corr != 5 || !r.OK {
			t.Errorf("ответ по звонку = %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("звонок не разбудил актора")
	}

	// бездействующий звонок — обработок не добавляет
	before := env.actor.Stats().Handled
	for i := 0; i < 3; i++ {
		env.doorbell <- struct{}{}
		time.Sleep(10 * time.Millisecond) // пейсинг звонков, не синхронизация: cap-1 сериализует сам
	}
	if after := env.actor.Stats().Handled; after != before {
		t.Errorf("пустые звонки обработали письма: %d → %d", before, after)
	}
}

func TestActorGracefulDrain(t *testing.T) {
	env := newTestEnv(t, Config{DrainTimeout: 5 * time.Second, PanicLimit: 3})
	env.start()

	// письма отправлены до отмены — надёжный класс обязан быть обработан
	// финальным дренированием (T3), независимо от гонки уведомлений
	for i := 0; i < 20; i++ {
		env.send(Request{Op: OpSaveSnapshot, Corr: uint64(i), Account: "acc",
			Chars: []CharRecord{mkChar("acc", "Vasya", 0)}})
	}
	env.cancel()
	<-env.done

	buf, err := os.ReadFile(filepath.Join(env.dir, "chars", "acc.json"))
	if err != nil {
		t.Fatalf("снимок не сохранён финальным дренированием: %v", err)
	}
	if !strings.Contains(string(buf), "Vasya") {
		t.Error("в сохранённом файле нет персонажа")
	}
}

func TestActorDrainTimeoutDropsRemainder(t *testing.T) {
	env := newTestEnv(t, Config{DrainTimeout: time.Nanosecond, PanicLimit: 3})
	gate := make(chan struct{})
	env.actor.chars.testBlockWrite = gate
	env.start()

	// первое письмо застревает в записи; пока актор занят — ещё два в очереди
	env.send(Request{Op: OpSaveSnapshot, Corr: 0, Account: "acc",
		Chars: []CharRecord{mkChar("acc", "Vasya", 0)}})
	waitHandled(t, env.actor, 1)
	env.send(Request{Op: OpSaveSnapshot, Corr: 1, Account: "acc",
		Chars: []CharRecord{mkChar("acc", "Vasya", 0)}})
	env.send(Request{Op: OpSaveSnapshot, Corr: 2, Account: "acc",
		Chars: []CharRecord{mkChar("acc", "Vasya", 0)}})
	env.cancel()
	close(gate) // письмо 1 дописывается, выход по ctx дропает остаток

	select {
	case <-env.done:
	case <-time.After(10 * time.Second):
		t.Fatal("Done не закрылся после отмены")
	}
	if drops := env.actor.box.Stats().FinalReliable; drops == 0 {
		t.Error("остаток очереди по таймауту дрена не получил классовый дроп")
	}
}

// waitHandled ждёт, пока актор возьмёт n писем в обработку.
func waitHandled(t *testing.T, a *Actor, n uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for a.Stats().Handled < n {
		if time.Now().After(deadline) {
			t.Fatalf("обработано %d из %d за таймаут", a.Stats().Handled, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitPanics ждёт, пока счётчик паник актора достигнет n (паника не рождает
// ответа — синхронизация по метрике).
func (e *testEnv) waitPanics(t *testing.T, n uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for e.actor.Stats().Panics < n {
		if time.Now().After(deadline) {
			t.Fatalf("паник %d из %d за таймаут", e.actor.Stats().Panics, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestActorPanicRecover(t *testing.T) {
	env := newTestEnv(t, Config{DrainTimeout: time.Second, PanicLimit: 3})
	env.start()
	defer env.stop()

	env.actor.testPanicOp = OpCreateChar
	env.send(Request{Op: OpCreateChar, Corr: 1, Account: "acc", Name: "Boom"})
	env.waitPanics(t, 1)
	// Негативное окно: «нет ответа» за 200мс — вспомогательный ассерт;
	// основной оракул ветки — классовый дроп остатка ниже (FinalReliable).
	select {
	case rep := <-env.replyCh:
		t.Fatalf("паникующее письмо получило ответ: %+v", rep)
	case <-time.After(200 * time.Millisecond):
	}
	if drops := env.actor.box.Stats().FinalReliable; drops == 0 {
		t.Error("паникующее письмо не получило классовый дроп остатка")
	}
	// актор жив: следующий запрос обрабатывается, серия сброшена
	env.actor.testPanicOp = ""
	rep := env.ask(Request{Op: OpCharList, Corr: 2, Account: "acc"}, 5*time.Second)
	if !rep.OK {
		t.Fatalf("после паники актор мёртв: %+v", rep)
	}
	if env.actor.Stats().Panics != 1 {
		t.Error("серия паник не сбросилась успешным шагом")
	}
}

func TestActorPanicSeriesCrash(t *testing.T) {
	env := newTestEnv(t, Config{DrainTimeout: time.Second, PanicLimit: 3})
	crashed := make(chan any, 1)
	go func() {
		defer func() { crashed <- recover() }()
		env.actor.Run(env.ctx)
	}()
	env.actor.testPanicOp = OpCreateChar
	// серия растёт по пачкам: одна паника — одна пачка (остаток пачки после
	// паники дропается целиком)
	for i := 1; i <= 3; i++ {
		env.send(Request{Op: OpCreateChar, Corr: uint64(i), Account: "acc", Name: "Boom"})
		env.waitPanics(t, uint64(i))
	}
	select {
	case r := <-crashed:
		if r == nil {
			t.Fatal("серия паник не уронила актор (let-it-crash не исполнен)")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("актор не упал после серии паник")
	}
}

func TestActorConfigValidation(t *testing.T) {
	door := make(chan struct{}, 1)
	reg := transport.NewRegistry(0)
	if _, err := New(Config{}, reg, door); err == nil {
		t.Error("Config{} = nil; want ошибка валидации")
	}
	if _, err := New(Config{Dir: t.TempDir()}, reg, door); err == nil {
		t.Error("без DrainTimeout = nil; want ошибка")
	}
	if _, err := New(Config{Dir: t.TempDir(), DrainTimeout: time.Second}, reg, door); err == nil {
		t.Error("без PanicLimit = nil; want ошибка")
	}
	if _, err := New(Config{Dir: t.TempDir(), DrainTimeout: time.Second, PanicLimit: 3},
		reg, nil); err == nil {
		t.Error("без doorbell = nil; want ошибка (тик-фолбэк обязателен)")
	}
}

// TestActorRetryAfterWriteError — F3: индекс имён занимается только после
// успешной записи; retry того же имени после ошибки идемпотентен.
func TestActorRetryAfterWriteError(t *testing.T) {
	env := newTestEnv(t, Config{DrainTimeout: time.Second, PanicLimit: 3})
	env.start()
	defer env.stop()

	env.actor.chars.testFailRename = func() error {
		return errors.New("инъекция: диск внезапно полон")
	}
	rep := env.ask(Request{Op: OpCreateChar, Corr: 1, Account: "acc",
		Name: "Vasya", Sex: 0}, 5*time.Second)
	if rep.OK || rep.Err == "" {
		t.Fatalf("CreateChar при сбое записи = %+v; want отказ с причиной", rep)
	}
	if stats := env.actor.Stats(); stats.WriteErrs != 1 {
		t.Errorf("WriteErrs = %d; want 1", stats.WriteErrs)
	}
	// занято ли имя? повтор после снятия инъекции должен пройти
	env.actor.chars.testFailRename = nil
	rep2 := env.ask(Request{Op: OpCreateChar, Corr: 2, Account: "acc",
		Name: "Vasya", Sex: 0}, 5*time.Second)
	if !rep2.OK || rep2.Record == nil {
		t.Fatalf("retry CreateChar тем же именем = %+v; want OK (индекс не занялся до rename)", rep2)
	}
	list := env.ask(Request{Op: OpCharList, Corr: 3, Account: "acc"}, 5*time.Second)
	if !list.OK || len(list.Chars) != 1 {
		t.Errorf("после retry list = %+v; want одна запись", list.Chars)
	}
}

// TestActorDemultiplexFIFO — F22/FIFO: один отправитель, несколько запросов
// в полёте с разными corr; ответы различимы по эху и приходят в порядке
// запросов отправителя.
func TestActorDemultiplexFIFO(t *testing.T) {
	env := newTestEnv(t, Config{DrainTimeout: time.Second, PanicLimit: 3})
	env.start()
	defer env.stop()

	rep1 := env.ask(Request{Op: OpCreateChar, Corr: 11, Account: "acc",
		Name: "First", Sex: 0}, 5*time.Second)
	if !rep1.OK {
		t.Fatalf("создание = %+v", rep1)
	}
	// три запроса в полёте без ожидания между отправками
	env.send(Request{Op: OpCharList, Corr: 21, Account: "acc"})
	env.send(Request{Op: OpCreateChar, Corr: 22, Account: "acc", Name: "Second", Sex: 1})
	env.send(Request{Op: OpCharList, Corr: 23, Account: "acc"})
	want := []uint64{21, 22, 23}
	got := make([]uint64, 0, len(want))
	for range want {
		select {
		case r := <-env.replyCh:
			if !r.OK {
				t.Fatalf("ответ corr=%d не OK: %+v", r.Corr, r)
			}
			got = append(got, r.Corr)
		case <-time.After(5 * time.Second):
			t.Fatalf("ответы не пришли: %v из %v", got, want)
		}
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("порядок/эхо corr: got %v; want %v", got, want)
			break
		}
	}
}

// TestActorSnapshotGuards — слоты и пустой снимок: отправитель не доверяется.
func TestActorSnapshotGuards(t *testing.T) {
	env := newTestEnv(t, Config{DrainTimeout: time.Second, PanicLimit: 3})
	env.start()
	defer env.stop()

	base := mkChar("acc", "Vasya", 0)
	base.ClassID, base.Race, base.Level = 0, 0, 1
	dupSlot := mkChar("acc", "Petya", 0)
	dupSlot.ClassID, dupSlot.Level = 0, 1
	rep := env.ask(Request{Op: OpSaveSnapshot, Corr: 1, Account: "acc",
		Chars: []CharRecord{base, dupSlot}}, 5*time.Second)
	if rep.OK {
		t.Error("снимок с дубликатом слота принят")
	}
	var many []CharRecord
	for i := 0; i <= 7; i++ {
		r := mkChar("acc", string(rune('A'+i)), i)
		r.ClassID, r.Level = 0, 1
		many = append(many, r)
	}
	rep = env.ask(Request{Op: OpSaveSnapshot, Corr: 2, Account: "acc", Chars: many}, 5*time.Second)
	if rep.OK {
		t.Error("снимок с 8 персонажами принят")
	}
	// пустой снимок при непустом аккаунте — отказ
	ok := env.ask(Request{Op: OpCreateChar, Corr: 3, Account: "acc2", Name: "Solo", Sex: 0}, 5*time.Second)
	if !ok.OK {
		t.Fatalf("создание = %+v", ok)
	}
	rep = env.ask(Request{Op: OpSaveSnapshot, Corr: 4, Account: "acc2", Chars: nil}, 5*time.Second)
	if rep.OK {
		t.Error("пустой снимок при непустом аккаунте принят (стёр бы персонажей)")
	}
	// при пустом аккаунте пустой снимок легитимен (нечего стирать)
	rep = env.ask(Request{Op: OpSaveSnapshot, Corr: 5, Account: "acc3", Chars: nil}, 5*time.Second)
	if !rep.OK {
		t.Errorf("пустой снимок пустого аккаунта = %+v; want OK", rep)
	}
}
