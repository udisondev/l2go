package world

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/udisondev/l2go/internal/geo"
	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/transport"
)

func newTestLog(t *testing.T, payloads bool, maxFile int64) *PortionLog {
	t.Helper()
	l, err := NewPortionLog(t.TempDir(), 7, payloads, maxFile)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
	}
	if err := l.SetRules(testRules()); err != nil {
		t.Fatalf("SetRules: %v", err)
	}
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return l
}

// Злые входы конструктора: пустой каталог и отрицательный размер файла
// отклоняются валидацией (правила — в SetRules, см. отдельную таблицу).
func TestPortionLogConfigValidation(t *testing.T) {
	dir := t.TempDir()
	bad := []struct {
		name    string
		dir     string
		maxFile int64
	}{
		{name: "dir пустой", dir: "", maxFile: 1 << 20},
		{name: "maxFileBytes<0", dir: dir, maxFile: -1},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewPortionLog(c.dir, 7, false, c.maxFile); err == nil {
				t.Errorf("NewPortionLog(%q, %d) прошёл валидацию; want ошибка", c.dir, c.maxFile)
			}
		})
	}
}

// SetRules — до первой записи кадра: нулевые период/окна/адресаты
// отвергаются именованными ошибками (реплей с такими правилами недостоверен).
func TestPortionLogSetRulesRejectsNonPositive(t *testing.T) {
	good := testRules()
	for _, c := range []struct {
		name  string
		mut   func(*Rules)
		field string
	}{
		{"PeriodNS=0", func(r *Rules) { r.PeriodNS = 0 }, "PeriodNS"},
		{"GraceTicks=0", func(r *Rules) { r.GraceTicks = 0 }, "GraceTicks"},
		{"SaveRetryTicks=0", func(r *Rules) { r.SaveRetryTicks = 0 }, "SaveRetryTicks"},
		{"Persist=0", func(r *Rules) { r.Persist = 0 }, "Persist"},
		{"Gateway=0", func(r *Rules) { r.Gateway = 0 }, "Gateway"},
		{"From=0", func(r *Rules) { r.From = 0 }, "From"},
	} {
		t.Run(c.name, func(t *testing.T) {
			l, err := NewPortionLog(t.TempDir(), 7, false, 1<<20)
			if err != nil {
				t.Fatalf("NewPortionLog: %v", err)
			}
			r := good
			c.mut(&r)
			if err := l.SetRules(r); err == nil {
				t.Errorf("SetRules(%s=0) прошёл валидацию; want ошибка", c.field)
			}
		})
	}
}

// Кадр без реплей-контракта (SetRules/Wire не пройдены) — ошибка записи:
// регион обязан заморозиться действующим механизмом, файл остаётся пуст.
func TestPortionLogRequiresSetRulesBeforeFrame(t *testing.T) {
	l, err := NewPortionLog(t.TempDir(), 7, true, 1<<20)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
	}
	if err := l.LogStep(StepInput{Tick: 1, Delta: 1}); err == nil {
		t.Fatal("LogStep без SetRules прошёл молча; want ошибка")
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	hdr, frames, err := ReadPortionFrames(l.dir, 7)
	if err != nil || hdr.Version != 0 || len(frames) != 0 {
		t.Fatalf("после отказа: hdr=%+v frames=%d err=%v; want пустая сессия", hdr, len(frames), err)
	}
}

// Заголовок ленивый: сессия без шагов не пишет заголовка вовсе — файл 0 байт,
// чтение цепочки не ломается (ридер пропускает пустые файлы).
func TestPortionLogHeaderLazyEmptySession(t *testing.T) {
	dir := t.TempDir()
	l, err := NewPortionLog(dir, 7, true, 1<<20)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
	}
	if err := l.SetRules(testRules()); err != nil {
		t.Fatalf("SetRules: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "portion-7-1.log"))
	if err != nil {
		t.Fatalf("os.Stat: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("файл сессии без шагов = %d байт; want 0 (заголовок ленивый)", info.Size())
	}
	hdr, frames, err := ReadPortionFrames(dir, 7)
	if err != nil || hdr.Version != 0 || len(frames) != 0 {
		t.Fatalf("пустая сессия: hdr=%+v frames=%d err=%v", hdr, len(frames), err)
	}
}

func sampleStep(tick Tick, delta uint64) (steps []StepInput) {
	return []StepInput{{
		Tick: tick, Delta: delta,
		Births:  []AppliedBirth{{ID: 42, Ent: &Entity{ID: 42, Owner: 7, HP: 100, Beat: tick}}},
		Retires: []Retire{{ID: 9}},
		Portions: []PortionRecord{
			{Box: 1, Mark: 3, Envs: []transport.Envelope{
				{To: transport.Addr{Entity: 2, Slot: transport.SlotSelf}, FromID: 1, Kind: transport.KindAggro, Attrs: transport.AttrBound, Payload: []byte{1, 2, 3}},
				{FromID: 1, Kind: transport.KindEnterWorld},
			}},
		},
		Advisory: []AdvisoryIn{{Cell: 5, Entity: 11}},
	}}
}

// Раундтрип формата: запись → чтение → реконструкция порций, оба режима
// payloads; порядок порций = порядок применения.
func TestPortionLogRoundtrip(t *testing.T) {
	for _, payloads := range []bool{false, true} {
		l := newTestLog(t, payloads, 1<<20)
		for _, s := range sampleStep(10, 1) {
			if err := l.LogStep(s); err != nil {
				t.Fatalf("LogStep(payloads=%v): %v", payloads, err)
			}
		}
		if err := l.LogPanic(11, 2); err != nil {
			t.Fatalf("LogPanic: %v", err)
		}
		hdr, steps, panics, err := readAll(t, l)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		// версия — литералом: откат bump должен краснеть, а не следовать константе;
		// v6 несёт реплей-контракт: sessionID и правила свёртки
		wantRules := testRules()
		if hdr.Region != 7 || hdr.Version != 6 || hdr.Payloads != payloads || hdr.Session == 0 ||
			hdr.PeriodNS != uint64(wantRules.PeriodNS) || hdr.GraceTicks != uint64(wantRules.GraceTicks) ||
			hdr.SaveRetryTicks != uint64(wantRules.SaveRetryTicks) || hdr.Persist != wantRules.Persist ||
			hdr.Gateway != wantRules.Gateway || hdr.CtrlFrom != wantRules.From {
			t.Fatalf("заголовок %+v; want контракт %+v", hdr, wantRules)
		}
		if len(steps) != 1 || len(panics) != 1 {
			t.Fatalf("записей: steps=%d panics=%d; want 1 и 1", len(steps), len(panics))
		}
		st := steps[0]
		if st.Tick != 10 || st.Delta != 1 {
			t.Errorf("граница шага = (%d, %d); want (10, 1)", st.Tick, st.Delta)
		}
		if len(st.Births) != 1 || st.Births[0].ID != 42 || st.Births[0].Ent.HP != 100 {
			t.Errorf("рождения в логе: %+v", st.Births)
		}
		if len(st.Retires) != 1 || st.Retires[0].ID != 9 {
			t.Errorf("удаления в логе: %+v", st.Retires)
		}
		if len(st.Portions) != 1 || st.Portions[0].Box != 1 || st.Portions[0].Mark != 3 || len(st.Portions[0].Envs) != 2 {
			t.Fatalf("пачки в логе: %+v", st.Portions)
		}
		env := st.Portions[0].Envs[0]
		if env.To.Entity != 2 || env.FromID != 1 || env.Kind != transport.KindAggro || env.Attrs != transport.AttrBound {
			t.Errorf("заголовок письма: %+v", env)
		}
		if payloads {
			if string(env.Payload) != "\x01\x02\x03" {
				t.Errorf("payload при включённых payloads = %v", env.Payload)
			}
		} else if env.Payload != nil {
			t.Errorf("payload при выключенных payloads = %v; want nil", env.Payload)
		}
		if len(st.Advisory) != 1 || st.Advisory[0].Cell != 5 || st.Advisory[0].Entity != 11 {
			t.Errorf("advisory-входы: %+v", st.Advisory)
		}
		if panics[0].Tick != 11 || panics[0].Phase != 2 {
			t.Errorf("маркер паники: %+v", panics[0])
		}
	}
}

func readAll(t *testing.T, l *PortionLog) (FileHeader, []StepRecord, []PanicRecord, error) {
	t.Helper()
	if err := l.w.Flush(); err != nil { // тест читает при живом писателе
		t.Fatalf("flush: %v", err)
	}
	hdr, steps, panics, err := ReadPortionLogDir(l.dir, l.region)
	return hdr, steps, panics, err
}

// Запись границы шага — на каждый шаг: пустые шаги тоже пишутся (сходимость
// реплея по Steps/Noise/Beat).
func TestPortionLogEveryStepWritten(t *testing.T) {
	l := newTestLog(t, false, 1<<20)
	for i := range 5 {
		if err := l.LogStep(StepInput{Tick: Tick(i), Delta: 1}); err != nil {
			t.Fatalf("LogStep: %v", err)
		}
	}
	_, steps, _, err := readAll(t, l)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(steps) != 5 {
		t.Fatalf("записей %d; want 5 (пустые шаги пишутся)", len(steps))
	}
}

// Ротация по размеру: следующий файл, цепочка читается по seq; заголовок
// ротированного файла несёт тот же sessionID и те же правила (бит-в-бит).
func TestPortionLogRotationAndChain(t *testing.T) {
	dir := t.TempDir()
	l, err := NewPortionLog(dir, 3, false, 64)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
	}
	if err := l.SetRules(testRules()); err != nil {
		t.Fatalf("SetRules: %v", err)
	}
	for i := range 20 {
		s := StepInput{Tick: Tick(i), Delta: 1,
			Portions: []PortionRecord{{Box: 1, Mark: uint64(i), Envs: []transport.Envelope{{FromID: 5, Kind: transport.KindXP}}}}}
		if err := l.LogStep(s); err != nil {
			t.Fatalf("LogStep: %v", err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "portion-3-*.log"))
	if err != nil || len(files) < 2 {
		t.Fatalf("ротация не создала цепочку: %v (%v)", files, err)
	}
	var firstHdr FileHeader
	for fi, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		fh, _, err := parseFile(raw)
		if err != nil {
			t.Fatalf("parseFile %s: %v", path, err)
		}
		if fi == 0 {
			firstHdr = fh
		} else if fh != firstHdr {
			t.Fatalf("заголовок ротации разошёлся: %+v против %+v", fh, firstHdr)
		}
	}
	hdr, frames, err := ReadPortionFrames(dir, 3)
	if err != nil {
		t.Fatalf("ReadPortionFrames: %v", err)
	}
	if hdr != firstHdr {
		t.Fatalf("заголовок цепочки %+v ≠ первого файла %+v", hdr, firstHdr)
	}
	if len(frames) != 20 {
		t.Fatalf("цепочка вернула %d кадров; want 20", len(frames))
	}
	for i, f := range frames {
		if f.Step == nil || f.Step.Tick != Tick(i) {
			t.Fatalf("порядок цепочки нарушен: frames[%d] = %+v", i, f)
		}
	}
}

// seq на рестарте: max существующих + 1 — файлы не затираются; вторая сессия
// в каталоге читается ГРОМКОЙ ошибкой (sessionID различает прогоны: тик
// метронома рестартует с нуля, склейка дала бы недостоверный дамп).
func TestPortionLogSecondSessionSameDirRejected(t *testing.T) {
	dir := t.TempDir()
	first, err := NewPortionLog(dir, 7, true, 1<<20)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
	}
	if err := first.SetRules(testRules()); err != nil {
		t.Fatalf("SetRules: %v", err)
	}
	if err := first.LogStep(StepInput{Tick: 1, Delta: 1}); err != nil {
		t.Fatalf("LogStep: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	second, err := NewPortionLog(dir, 7, true, 1<<20)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
	}
	if second.seq != first.seq+1 {
		t.Fatalf("seq новой сессии = %d; want %d (max существующих + 1)", second.seq, first.seq+1)
	}
	if err := second.SetRules(testRules()); err != nil {
		t.Fatalf("SetRules: %v", err)
	}
	if err := second.LogStep(StepInput{Tick: 2, Delta: 0}); err != nil {
		t.Fatalf("LogStep: %v", err)
	}
	if err := second.w.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if _, _, _, err := ReadPortionLogDir(dir, 7); err == nil {
		t.Fatal("прогоны после рестарта склеены молча; want ошибка цепочки (sessionID)")
	}
}

// Оборванный хвост (ENOSPC/power-loss): ридер возвращает ErrTruncated после
// валидных записей, а не мусор.
func TestPortionLogTruncatedTail(t *testing.T) {
	dir := t.TempDir()
	l, err := NewPortionLog(dir, 7, true, 1<<20)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
	}
	if err := l.SetRules(testRules()); err != nil {
		t.Fatalf("SetRules: %v", err)
	}
	for i := range 3 {
		if err := l.LogStep(StepInput{Tick: Tick(i), Delta: 1}); err != nil {
			t.Fatalf("LogStep: %v", err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	path := filepath.Join(dir, "portion-7-1.log")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("os.Stat: %v", err)
	}
	if err := os.Truncate(path, info.Size()-3); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	_, steps, _, err := ReadPortionLogDir(dir, 7)
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("err = %v; want ErrTruncated", err)
	}
	if len(steps) != 2 {
		t.Fatalf("валидных записей %d; want 2", len(steps))
	}
}

// Кодирование записи — в переиспользуемый буфер писателя: 0 аллокаций на
// запись вне роста буфера.
func TestPortionLogEncodeZeroAlloc(t *testing.T) {
	l := newTestLog(t, false, 1<<20)
	s := sampleStep(5, 1)[0]
	l.encodeStep(s) // прогрев ёмкости
	allocs := testing.AllocsPerRun(20, func() {
		l.enc = l.encodeStepInto(l.enc[:0], s)
	})
	if allocs != 0 {
		t.Fatalf("аллокаций на запись = %.0f; want 0 (вне роста буфера)", allocs)
	}
}

// E1: версия цепочки проверяется строго — v1/мусор дают явную ошибку.
func TestPortionLogVersionStrict(t *testing.T) {
	dir := t.TempDir()
	l, err := NewPortionLog(dir, 7, false, 1<<20)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
	}
	if err := l.SetRules(testRules()); err != nil {
		t.Fatalf("SetRules: %v", err)
	}
	if err := l.LogStep(StepInput{Tick: 1}); err != nil {
		t.Fatalf("LogStep: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "portion-7-*.log"))
	if len(files) != 1 {
		t.Fatalf("файлов %d", len(files))
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	// подменить версию на 1 (байты после магии — uvarint версии)
	patched := append([]byte(nil), raw...)
	patched[len(portionMagic)] = 1
	if err := os.WriteFile(files[0], patched, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ReadPortionLogDir(dir, 7); err == nil {
		t.Fatal("цепочка v1 прочитана v2-читателем молча")
	}
	// мусорная версия
	patched[len(portionMagic)] = 0x7f
	if err := os.WriteFile(files[0], patched, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ReadPortionLogDir(dir, 7); err == nil {
		t.Fatal("мусорная версия прочитана молча")
	}
}

// E2: roundtrip Player с отрицательными int64 (X/Exp/CreatedUnix).
func TestPortionLogPlayerRoundtrip(t *testing.T) {
	dir := t.TempDir()
	l, err := NewPortionLog(dir, 9, false, 1<<20)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
	}
	if err := l.SetRules(testRules()); err != nil {
		t.Fatalf("SetRules: %v", err)
	}
	ent := Entity{Owner: 9, Pos: Position{X: -71338, Y: 258271, Z: -3104}, HP: 80,
		Player: &Player{Rec: mkRec("acc", "Vasya", 0)}}
	ent.Player.Rec.X = -71338
	ent.Player.Rec.Exp = -42
	ent.Player.Rec.CreatedUnix = -5
	ent.Player.Rec.LastSeenUnix = -7
	ent.Player.ConnID = 12345
	ent.Player.PendingTeleport = true
	if err := l.LogStep(StepInput{Tick: 3, Births: []AppliedBirth{{ID: 77, Ent: &ent}}}); err != nil {
		t.Fatalf("LogStep: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, steps, _, err := ReadPortionLogDir(dir, 9)
	if err != nil {
		t.Fatalf("ReadPortionLogDir: %v", err)
	}
	if len(steps) != 1 || len(steps[0].Births) != 1 {
		t.Fatalf("шаги/рождения: %d/%d", len(steps), len(steps[0].Births))
	}
	got := steps[0].Births[0].Ent
	if got.Player == nil {
		t.Fatal("Player не восстановлен")
	}
	g := got.Player
	if g.Rec.Account != "acc" || g.Rec.Name != "Vasya" || g.Rec.Exp != -42 ||
		g.Rec.CreatedUnix != -5 || g.Rec.LastSeenUnix != -7 || g.Rec.X != -71338 ||
		g.ConnID != 12345 || !g.PendingTeleport || g.EnterLeaving {
		t.Fatalf("Player roundtrip: %+v", g.Rec)
	}
}

// v3: roundtrip отрезка движения и бакета (отрицательные значения включительно);
// v5: спам-бакет чата пережив roundtrip тем же varint (F6 реестра P3.11).
func TestPortionLogMovementRoundtrip(t *testing.T) {
	l := newTestLog(t, false, 1<<20)
	ent := Entity{Owner: 7, Pos: Position{X: -71338, Y: 258271, Z: -3104},
		Heading: 49152, Moving: true,
		MoveFrom: Position{X: -72000, Y: 258000, Z: -3200},
		MoveDist: 9007199, MoveDone: 1234567,
		Player: &Player{Rec: mkRec("acc", "Vasya", 0), SpeedBudget: -58000, ChatBudget: 500}}
	if err := l.LogStep(StepInput{Tick: 3, Births: []AppliedBirth{{ID: 77, Ent: &ent}}}); err != nil {
		t.Fatalf("LogStep: %v", err)
	}
	_, steps, _, err := readAll(t, l)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := steps[0].Births[0].Ent
	if got.Heading != 49152 || !got.Moving ||
		got.MoveFrom != (Position{X: -72000, Y: 258000, Z: -3200}) ||
		got.MoveDist != 9007199 || got.MoveDone != 1234567 {
		t.Fatalf("отрезок roundtrip: %+v", got)
	}
	if got.Player == nil || got.Player.SpeedBudget != -58000 {
		t.Fatalf("бакет roundtrip: %+v", got.Player)
	}
	if got.Player.ChatBudget != 500 {
		t.Fatalf("чат-бакет roundtrip: %+v; want 500", got.Player.ChatBudget)
	}
	// истощённый бакет (0) — тоже переживает: пустое varint-кодирование.
	l2 := newTestLog(t, false, 1<<20)
	ent2 := ent
	ent2.Player = &Player{Rec: mkRec("acc", "Vasya", 0), ChatBudget: 0}
	if err := l2.LogStep(StepInput{Tick: 1, Births: []AppliedBirth{{ID: 78, Ent: &ent2}}}); err != nil {
		t.Fatalf("LogStep(0): %v", err)
	}
	_, steps2, _, err := readAll(t, l2)
	if err != nil {
		t.Fatalf("read(0): %v", err)
	}
	if p := steps2[0].Births[0].Ent.Player; p == nil || p.ChatBudget != 0 {
		t.Fatalf("чат-бакет 0 roundtrip: %+v", p)
	}
}

// Реплей движения бит-в-бит: лог записанной сессии (входы письмами — рождения
// логируются с Owner и присвоенными ID) прогоняется через Replay — дамп равен
// живому; повтор — тоже; перестановка двух писем шага меняет дамп.
func TestPortionLogReplayMovementDigest(t *testing.T) {
	r, _ := moveRegion(t)
	// входы письмами: рождения попадают в лог с присвоенными ID
	r.reg.Send(transport.Envelope{To: transport.Addr{Entity: r.CtrlID()}, FromID: 901,
		Kind:    transport.KindEnterWorld,
		Payload: mustJSONEnter(1, mkRecAt("aca", "Hero", int(syncPos.X), int(syncPos.Y)))})
	r.reg.Send(transport.Envelope{To: transport.Addr{Entity: r.CtrlID()}, FromID: 901,
		Kind:    transport.KindEnterWorld,
		Payload: mustJSONEnter(2, mkRecAt("bca", "Bobby", int(syncPos.X)+100, int(syncPos.Y)))})
	stepN(r, 1)
	a := r.residents[0].ent.ID
	stepN(r, 1) // знакомство
	b := make([]byte, 29)
	writeMoveFrame(b, syncPos.X+230, syncPos.Y, syncPos.Z, syncPos.X, syncPos.Y, syncPos.Z)
	sendToBox(r, a, b)
	b2 := make([]byte, 29)
	writeMoveFrame(b2, syncPos.X-300, syncPos.Y, syncPos.Z, syncPos.X, syncPos.Y, syncPos.Z)
	sendToBox(r, a, b2) // ретаргет той же пачки: порядок писем определяет итог
	v := make([]byte, 21)
	v[0] = 0x48
	lePut32(v[1:], int32(syncPos.X)+5) // близкий честный отчёт
	lePut32(v[5:], int32(syncPos.Y))
	lePut32(v[9:], syncPos.Z)
	sendToBox(r, a, v)
	stepN(r, 12) // старт, advance, прибытие

	live := r.state.Dump(r.entsProj())
	if err := r.log.w.Flush(); err != nil { // тест читает при живом писателе
		t.Fatalf("flush: %v", err)
	}
	hdr, frames, err := ReadPortionFrames(r.log.dir, r.log.region)
	if err != nil {
		t.Fatalf("ReadPortionFrames: %v", err)
	}
	if len(frames) < 10 {
		t.Fatalf("шагов в логе = %d; want ≥10", len(frames))
	}
	if hdr.PeriodNS != uint64(r.metro.period) {
		t.Fatalf("заголовок несёт период %d; want %d (метроном сессии)", hdr.PeriodNS, r.metro.period)
	}

	replay := func(mutateSwap bool) []byte {
		fr := frames
		if mutateSwap { // перестановка двух клиентских кадров одной пачки
			// (ретаргет движения: порядок писем определяет итоговый Dest);
			// вызывается последним — разделяемые Envs после этого не читаются
			fr = append([]LogFrame(nil), frames...)
			for _, f := range fr {
				if f.Step == nil {
					continue
				}
				for pi := range f.Step.Portions {
					envs := f.Step.Portions[pi].Envs
					for ei := 0; ei+1 < len(envs); ei++ {
						if envs[ei].Kind == transport.KindClientFrame && envs[ei+1].Kind == transport.KindClientFrame {
							envs[ei], envs[ei+1] = envs[ei+1], envs[ei]
							return ReplayDump(t, hdr, fr, r.gm)
						}
					}
				}
			}
		}
		return ReplayDump(t, hdr, fr, r.gm)
	}
	d1, d2 := replay(false), replay(false)
	if string(d1) != string(d2) {
		t.Fatal("повторный реплей разошёлся")
	}
	if string(d1) != string(live) {
		t.Fatalf("реплей ≠ живой дамп (первое расхождение ищется побайтово): len %d vs %d", len(d1), len(live))
	}
	if d3 := replay(true); string(d3) == string(d1) {
		t.Fatal("перестановка двух писем шага не меняет дамп — детектор слеп")
	}
}

// ReplayDump — прогон кадров через Replay с фаталью по ошибке (хелпер тестов).
func ReplayDump(t *testing.T, hdr FileHeader, fr []LogFrame, gm *geo.Map) []byte {
	t.Helper()
	res, err := Replay(hdr, fr, nil, gm)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	return res.Dump
}

// mustJSONEnter — письмо входа (коннект + запись) для ручных сценариев.
func mustJSONEnter(conn uint64, rec persist.CharRecord) []byte {
	env, err := transport.EncodeLetter(transport.EnterWorldMsg{
		Conn: conn, Account: rec.Account, Char: mustJSONChar(rec)})
	if err != nil {
		panic("тест: кодирование EnterWorldMsg: " + err.Error())
	}
	return env
}

// lePut32 — LE int32 в буфер (тестовый писатель полей кадра).
func lePut32(dst []byte, v int32) {
	dst[0] = byte(v)
	dst[1] = byte(v >> 8)
	dst[2] = byte(v >> 16)
	dst[3] = byte(v >> 24)
}
