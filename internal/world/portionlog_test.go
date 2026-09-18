package world

import (
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/transport"
)

func newTestLog(t *testing.T, payloads bool, maxFile int64) *PortionLog {
	t.Helper()
	l, err := NewPortionLog(t.TempDir(), 7, 100*time.Millisecond, payloads, maxFile)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
	}
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return l
}

// Злые входы конструктора: пустой каталог, неположительный период и
// отрицательный размер файла отклоняются валидацией.
func TestPortionLogConfigValidation(t *testing.T) {
	dir := t.TempDir()
	bad := []struct {
		name    string
		dir     string
		period  time.Duration
		maxFile int64
	}{
		{name: "dir пустой", dir: "", period: time.Second, maxFile: 1 << 20},
		{name: "period=0", dir: dir, period: 0, maxFile: 1 << 20},
		{name: "period<0", dir: dir, period: -time.Second, maxFile: 1 << 20},
		{name: "maxFileBytes<0", dir: dir, period: time.Second, maxFile: -1},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewPortionLog(c.dir, 7, c.period, false, c.maxFile); err == nil {
				t.Errorf("NewPortionLog(%q, %v, %d) прошёл валидацию; want ошибка", c.dir, c.period, c.maxFile)
			}
		})
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
		if hdr.Region != 7 || hdr.Version != portionVersion || hdr.Payloads != payloads || hdr.PeriodNS == 0 {
			t.Fatalf("заголовок %+v", hdr)
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

// Ротация по размеру: следующий файл, цепочка читается по seq.
func TestPortionLogRotationAndChain(t *testing.T) {
	dir := t.TempDir()
	l, err := NewPortionLog(dir, 3, 100*time.Millisecond, false, 64)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
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
	_, steps, _, err := ReadPortionLogDir(dir, 3)
	if err != nil {
		t.Fatalf("ReadPortionLogDir: %v", err)
	}
	if len(steps) != 20 {
		t.Fatalf("цепочка вернула %d шагов; want 20", len(steps))
	}
	for i, st := range steps {
		if st.Tick != Tick(i) {
			t.Fatalf("порядок цепочки нарушен: steps[%d].Tick = %d", i, st.Tick)
		}
	}
}

// seq на рестарте: max существующих + 1 — сессии не смешиваются, старый файл цел.
func TestPortionLogRestartSeq(t *testing.T) {
	dir := t.TempDir()
	first, err := NewPortionLog(dir, 7, 100*time.Millisecond, false, 1<<20)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
	}
	if err := first.LogStep(StepInput{Tick: 1, Delta: 1}); err != nil {
		t.Fatalf("LogStep: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	second, err := NewPortionLog(dir, 7, 100*time.Millisecond, false, 1<<20)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
	}
	// после закрытия second файл больше не читается — ошибка ловится здесь,
	// а не молчаливым defer
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	if second.seq != first.seq+1 {
		t.Fatalf("seq новой сессии = %d; want %d (max существующих + 1)", second.seq, first.seq+1)
	}
	if err := second.LogStep(StepInput{Tick: 2, Delta: 0}); err != nil {
		t.Fatalf("LogStep: %v", err)
	}
	if err := second.w.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	_, steps, _, err := ReadPortionLogDir(dir, 7)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(steps) != 2 || steps[0].Tick != 1 || steps[1].Tick != 2 {
		t.Fatalf("сессии смешались: %+v", steps)
	}
}

// Оборванный хвост (ENOSPC/power-loss): ридер возвращает ErrTruncated после
// валидных записей, а не мусор.
func TestPortionLogTruncatedTail(t *testing.T) {
	dir := t.TempDir()
	l, err := NewPortionLog(dir, 7, 100*time.Millisecond, true, 1<<20)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
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
	l, err := NewPortionLog(dir, 7, 100*time.Millisecond, false, 1<<20)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
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
	l, err := NewPortionLog(dir, 9, 100*time.Millisecond, false, 1<<20)
	if err != nil {
		t.Fatalf("NewPortionLog: %v", err)
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

// v3: roundtrip отрезка движения и бакета (отрицательные значения включительно).
func TestPortionLogMovementRoundtrip(t *testing.T) {
	l := newTestLog(t, false, 1<<20)
	ent := Entity{Owner: 7, Pos: Position{X: -71338, Y: 258271, Z: -3104},
		Heading: 49152, Moving: true,
		MoveFrom: Position{X: -72000, Y: 258000, Z: -3200},
		MoveDist: 9007199, MoveDone: 1234567,
		Player: &Player{Rec: mkRec("acc", "Vasya", 0), SpeedBudget: -58000}}
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
}

// Реплей движения бит-в-бит: лог записанной сессии (входы письмами — рождения
// логируются) прогоняется через тот же Fold (период — из заголовка) — дамп
// равен живому; повтор — тоже; перестановка двух писем шага меняет дамп.
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
	hdr, steps, _, err := ReadPortionLogDir(r.log.dir, r.log.region)
	if err != nil {
		t.Fatalf("ReadPortionLogDir: %v", err)
	}
	if len(steps) < 10 {
		t.Fatalf("шагов в логе = %d; want ≥10", len(steps))
	}
	if hdr.PeriodNS != uint64(r.metro.period) {
		t.Fatalf("заголовок несёт период %d; want %d (метроном сессии)", hdr.PeriodNS, r.metro.period)
	}

	// replay — зеркалит шаг региона: fold писем, рождения применяются с ID из
	// лога (сортированная вставка), удаления изымаются
	replay := func(mutateSwap bool) []byte {
		st := newState()
		var ents []*Entity
		rules := testRules()
		rules.PeriodNS = int64(hdr.PeriodNS) // шов 1: реплей берёт период из заголовка лога
		for si, s := range steps {
			portions := make([]Portion, len(s.Portions))
			for i, p := range s.Portions {
				envs := p.Envs
				if mutateSwap && si == 2 && len(envs) >= 2 {
					envs[0], envs[1] = envs[1], envs[0]
				}
				portions[i] = Portion{Region: r.id, Tick: s.Tick, Envs: envs}
			}
			rng := rand.New(rand.NewPCG(uint64(r.id), uint64(s.Tick)))
			Fold(s.Tick, s.Delta, rng, st, ents, portions, s.Advisory, Env{Region: r.id, Rules: rules, GM: r.gm})
			for _, br := range s.Births {
				e := br.Ent
				e.Owner = r.id // Spawn актора ставит владельца — зеркалим
				idx := sort.Search(len(ents), func(i int) bool { return ents[i].ID >= br.ID })
				ents = append(ents, nil)
				copy(ents[idx+1:], ents[idx:])
				ents[idx] = &e
				if e.Player != nil { // материализация теней — зеркалит applyEffects актора
					st.ResolveBirth(e.Player.Rec.Account, e.Player.ConnID, e.ID)
				}
			}
			for _, rt := range s.Retires {
				for i := range ents {
					if ents[i].ID == rt.ID {
						ents = append(ents[:i], ents[i+1:]...)
						break
					}
				}
			}
		}
		return st.Dump(ents)
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
