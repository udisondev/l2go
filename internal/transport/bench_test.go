package transport

import (
	"io"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

// Конкуренты выбора структуры карты (ADR-0003 §3): побеждает та, что в Registry;
// проигравшие живут здесь — числа воспроизводимы, мёртвого кода в поставке нет.

type plainTable struct {
	mu sync.RWMutex
	m  map[EntityID]*Mailbox
}

func (p *plainTable) get(id EntityID) *Mailbox {
	p.mu.RLock()
	box := p.m[id]
	p.mu.RUnlock()
	return box
}

func (p *plainTable) set(id EntityID, box *Mailbox) {
	p.mu.Lock()
	p.m[id] = box
	p.mu.Unlock()
}

type syncMapTable struct{ m sync.Map }

func (s *syncMapTable) get(id EntityID) *Mailbox {
	v, _ := s.m.Load(id)
	if v == nil {
		return nil
	}
	return v.(*Mailbox)
}

const (
	cowSegBits = 10
	cowSegSize = 1 << cowSegBits
	cowSegMask = cowSegSize - 1
)

// Сегментированный COW с прямым индексом по монотонному id: чтение — загрузка корня
// плюс две зависимые загрузки, без RMW; запись клонирует затронутый сегмент.
type cowTable struct {
	mu   sync.Mutex
	root atomic.Pointer[cowRoot]
}

type cowRoot struct {
	segs []*cowSeg
}

type cowSeg struct {
	slots [cowSegSize]*Mailbox
}

func (c *cowTable) get(id EntityID) *Mailbox {
	root := c.root.Load()
	if root == nil {
		return nil
	}
	i := int(id) >> cowSegBits
	if i >= len(root.segs) {
		return nil
	}
	return root.segs[i].slots[int(id)&cowSegMask]
}

func (c *cowTable) set(id EntityID, box *Mailbox) {
	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.root.Load()
	i := int(id) >> cowSegBits
	var segs []*cowSeg
	if old == nil || i >= len(old.segs) {
		n := i + 1
		if old != nil {
			n = len(old.segs) * 2
			if n <= i {
				n = i + 1
			}
		}
		segs = make([]*cowSeg, n)
		copy(segs, oldSegs(old))
	} else {
		segs = append(make([]*cowSeg, 0, len(old.segs)), oldSegs(old)...)
	}
	if segs[i] == nil {
		segs[i] = &cowSeg{}
	} else {
		cloned := &cowSeg{}
		*cloned = *segs[i]
		segs[i] = cloned
	}
	segs[i].slots[int(id)&cowSegMask] = box
	c.root.Store(&cowRoot{segs: segs})
}

func oldSegs(r *cowRoot) []*cowSeg {
	if r == nil {
		return nil
	}
	return r.segs
}

var benchSizes = []struct {
	name string
	n    int
}{
	{"Map0", 0},
	{"Map100", 100},
	{"Map10k", 10_000},
	{"Map50k", 50_000},
}

func idsUpTo(n int) []EntityID {
	ids := make([]EntityID, n)
	for i := range ids {
		ids[i] = EntityID(i + 1)
	}
	return ids
}

func BenchmarkEnqueue(b *testing.B) {
	r := NewRegistry(1 << 20)
	box := &Mailbox{}
	r.Register(box)
	if err := box.Claim(1); err != nil {
		b.Fatal(err)
	}
	env := Envelope{FromID: 1, Kind: KindClientFrame, Payload: payloadFixed[:]}
	var sink []Envelope
	b.ReportAllocs()
	for b.Loop() {
		box.enqueue(env)
		if box.length.Load() >= segCap {
			sink = box.extractInto(1, sink[:0])
		}
	}
	benchSink = sink
}

func BenchmarkEnqueueParallel(b *testing.B) {
	restore := slogDefaultSwapQuiet() // алерт глубины горячего ящика не должен рвать ряды
	b.Cleanup(restore)
	r := NewRegistry(1 << 20)
	box := &Mailbox{}
	r.Register(box)
	if err := box.Claim(1); err != nil {
		b.Fatal(err)
	}
	stop := make(chan struct{})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		var sink []Envelope
		for {
			select {
			case <-stop:
				return
			default:
			}
			if box.length.Load() > segCap*4 {
				sink = box.extractInto(1, sink[:0])
				continue
			}
			runtime.Gosched()
		}
	}()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		env := Envelope{FromID: 1, Kind: KindClientFrame, Payload: payloadFixed[:]}
		for pb.Next() {
			box.enqueue(env)
		}
	})
	close(stop)
	<-drained
	benchSink = nil
}

func BenchmarkExtract(b *testing.B) {
	b.Run("Empty", func(b *testing.B) {
		r := NewRegistry(8)
		box := &Mailbox{}
		r.Register(box)
		if err := box.Claim(1); err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		for b.Loop() {
			benchSink = box.extractInto(1, nil)
		}
	})
	b.Run("DeepCycle", func(b *testing.B) {
		r := NewRegistry(1 << 20)
		box := &Mailbox{}
		r.Register(box)
		if err := box.Claim(1); err != nil {
			b.Fatal(err)
		}
		for range 3 * segCap {
			box.enqueue(Envelope{FromID: 1, Kind: KindClientFrame, Payload: payloadFixed[:]})
		}
		var sink []Envelope
		b.ReportAllocs()
		for b.Loop() {
			sink = box.extractInto(1, sink[:0])
			for _, env := range sink {
				box.enqueue(env)
			}
		}
		benchSink = sink
	})
}

func BenchmarkSend(b *testing.B) {
	restore := slogDefaultSwapQuiet() // Map0: горячий ящик пересекает порог алерта глубины
	b.Cleanup(restore)
	for _, sz := range benchSizes {
		r := fillRegistry(sz.n)
		b.Run(sz.name, func(b *testing.B) {
			ids := idsUpTo(sz.n)
			if len(ids) == 0 {
				ids = []EntityID{1}
			}
			env := Envelope{FromID: 1, Kind: KindClientFrame, To: Addr{Entity: 1}, Payload: payloadFixed[:]}
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				env.To.Entity = ids[i%len(ids)]
				r.Send(env)
				i++
			}
		})
	}
}

func BenchmarkSendParallel(b *testing.B) {
	restore := slogDefaultSwapQuiet() // Map0: горячий ящик пересекает порог алерта глубины
	b.Cleanup(restore)
	for _, sz := range benchSizes {
		r := fillRegistry(sz.n)
		b.Run(sz.name, func(b *testing.B) {
			ids := idsUpTo(sz.n)
			if len(ids) == 0 {
				ids = []EntityID{1}
			}
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				env := Envelope{FromID: 1, Kind: KindClientFrame, Payload: payloadFixed[:]}
				i := 0
				for pb.Next() {
					env.To.Entity = ids[i%len(ids)]
					r.Send(env)
					i++
				}
			})
		})
	}
}

// Интерференция чтение×запись: параллельная отправка по заселённой карте,
// фон — фиксированный прирост массовым спавном (5 батчей по 12k, как P3.10),
// затем тишина: и интерференция, и чтение после роста. Фоновые Register —
// часть сценария интерференции: их аллокации входят в allocs/op Reading
// (читается как верхняя оценка пути, см. шапку baseline).
func BenchmarkSendUnderWrite(b *testing.B) {
	const n = 10_000
	const batches = 5
	r := fillRegistry(n)
	ids := idsUpTo(n)
	spawned := make(chan struct{})
	go func() {
		defer close(spawned)
		for range batches {
			for range 12_000 {
				r.Register(&Mailbox{})
			}
		}
	}()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		env := Envelope{FromID: 1, Kind: KindClientFrame, Payload: payloadFixed[:]}
		i := 0
		for pb.Next() {
			env.To.Entity = ids[i%len(ids)]
			r.Send(env)
			i++
		}
	})
	<-spawned
}

// Miss-путь отправки: RLock + промах + метрика; slog заглушен (цена лога —
// ответственность потребителя, не пути).
func BenchmarkSendMiss(b *testing.B) {
	r := fillRegistry(10_000)
	prev := slogDefaultSwapQuiet()
	b.Cleanup(prev)
	env := Envelope{FromID: 1, Kind: KindClientFrame, To: Addr{Entity: 1 << 40}, Payload: payloadFixed[:]}
	b.ReportAllocs()
	for b.Loop() {
		r.Send(env)
	}
}

func BenchmarkMapWrite(b *testing.B) {
	b.Run("Birth", func(b *testing.B) {
		r := fillRegistry(10_000)
		b.ReportAllocs()
		for b.Loop() {
			r.Register(&Mailbox{})
		}
	})
	b.Run("Retire", func(b *testing.B) {
		r := fillRegistry(10_000)
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			r.Retire(EntityID(i%10_000 + 1))
			i++
		}
	})
	b.Run("MassSpawn", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			r := NewRegistry(1024)
			for range 12_000 {
				r.Register(&Mailbox{})
			}
		}
	})
}

// Write-путь конкурентов выбора структуры: рождение и массовый спавн теми же
// числами, что и у поставки (свидетельство решения «sharded против COW»).
func BenchmarkMapWriteCompetitors(b *testing.B) {
	mk := map[string]func() (set func(EntityID, *Mailbox), get func(EntityID) *Mailbox){
		"Plain": func() (func(EntityID, *Mailbox), func(EntityID) *Mailbox) {
			p := &plainTable{m: make(map[EntityID]*Mailbox)}
			return p.set, p.get
		},
		"SyncMap": func() (func(EntityID, *Mailbox), func(EntityID) *Mailbox) {
			s := &syncMapTable{}
			return func(id EntityID, box *Mailbox) { s.m.Store(id, box) }, s.get
		},
		"COW": func() (func(EntityID, *Mailbox), func(EntityID) *Mailbox) {
			c := &cowTable{}
			return c.set, c.get
		},
	}
	for name, m := range mk {
		set, _ := m()
		b.Run(name+"/Birth", func(b *testing.B) {
			for i := 1; i <= 10_000; i++ {
				set(EntityID(i), &Mailbox{})
			}
			b.ResetTimer()
			b.ReportAllocs()
			next := 10_001
			for b.Loop() {
				set(EntityID(next), &Mailbox{})
				next++
			}
		})
		b.Run(name+"/MassSpawn", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				set, _ := m()
				for j := 1; j <= 12_000; j++ {
					set(EntityID(j), &Mailbox{})
				}
			}
		})
	}
}

func BenchmarkSendPlainTable(b *testing.B) {
	for _, sz := range benchSizes {
		tbl := fillCompetitor(sz.n, func() (set func(EntityID, *Mailbox), get func(EntityID) *Mailbox) {
			p := &plainTable{m: make(map[EntityID]*Mailbox)}
			return p.set, p.get
		})
		b.Run(sz.name, func(b *testing.B) {
			runSendParallel(b, tbl, sz.n)
		})
	}
}

func BenchmarkSendSyncMap(b *testing.B) {
	for _, sz := range benchSizes {
		tbl := fillCompetitor(sz.n, func() (set func(EntityID, *Mailbox), get func(EntityID) *Mailbox) {
			s := &syncMapTable{}
			return func(id EntityID, box *Mailbox) { s.m.Store(id, box) }, s.get
		})
		b.Run(sz.name, func(b *testing.B) {
			runSendParallel(b, tbl, sz.n)
		})
	}
}

func BenchmarkSendCOW(b *testing.B) {
	for _, sz := range benchSizes {
		tbl := fillCompetitor(sz.n, func() (set func(EntityID, *Mailbox), get func(EntityID) *Mailbox) {
			c := &cowTable{}
			return c.set, c.get
		})
		b.Run(sz.name, func(b *testing.B) {
			runSendParallel(b, tbl, sz.n)
		})
	}
}

func fillCompetitor(n int, mk func() (set func(EntityID, *Mailbox), get func(EntityID) *Mailbox)) func(EntityID) *Mailbox {
	if n < 1 {
		n = 1 // минимум один живой адресат — у всех кандидатов равный профиль
	}
	r := NewRegistry(1 << 20)
	set, get := mk()
	for i := 1; i <= n; i++ {
		box := &Mailbox{}
		set(r.Register(box), box)
	}
	return get
}

// slogDefaultSwapQuiet заглушает slog на время бенчмарка (miss-логи, алерт
// глубины горячего ящика); возвращает восстановитель.
func slogDefaultSwapQuiet() func() {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	prev := slog.Default()
	slog.SetDefault(quiet)
	return func() { slog.SetDefault(prev) }
}

func runSendParallel(b *testing.B, get func(EntityID) *Mailbox, n int) {
	ids := idsUpTo(n)
	if len(ids) == 0 {
		ids = []EntityID{1}
	}
	env := Envelope{FromID: 1, Kind: KindClientFrame, Payload: payloadFixed[:]}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if box := get(ids[i%len(ids)]); box != nil {
				box.enqueue(env)
			}
			i++
		}
	})
}

func fillRegistry(n int) *Registry {
	r := NewRegistry(1 << 20)
	if n < 1 {
		n = 1 // «пустая» карта: один адресат, фон таблицы минимален, мисс-путь не меряется
	}
	for i := 0; i < n; i++ {
		r.Register(&Mailbox{})
	}
	return r
}
