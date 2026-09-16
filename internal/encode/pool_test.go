package encode

// Пул стейджа (P3.7b): датчик владения (Gets−Overflows)−Puts замкнут на всех
// путях — стационар, чурн, финальный батч (Recycle), дрен Unregister;
// трим бюджета spare; best-fit матчинг; кросс-клиентское переиспользование.

import (
	"testing"
)

func poolBalance(s *Stage) int64 {
	st := s.Stats().Pool
	return (st.Gets - st.Overflows) - st.Puts
}

// Баланс владения: полный жизненный цикл клиентов (включая кадр > 4096Б —
// overflow-ветвь Get и молчаливый отброс Put) закрывает датчик в ноль.
func TestStagePoolOwnershipBalance(t *testing.T) {
	t.Parallel()
	s, err := NewStage(256 << 10)
	if err != nil {
		t.Fatal(err)
	}
	run := func(id ClientID, frames [][]byte, takeAll bool) {
		c := s.Register(id, [8]byte{1})
		for _, f := range frames {
			s.Push(id, f, false)
		}
		if takeAll {
			// Один Take вынимает всю глубину; финальный батч — как defer
			// write-горутины: Recycle после последнего Take.
			next, _ := c.Take(nil)
			c.Recycle(next)
		}
		s.Unregister(id)
	}
	small := make([]byte, 38)
	big := make([]byte, 5000) // > 4096: overflow-слэб, Put отбрасывает молча
	run(1, [][]byte{small, small, big}, true)
	run(2, [][]byte{small}, false) // очередь дренирует Unregister
	run(3, [][]byte{big}, true)

	if bal := poolBalance(s); bal != 0 {
		t.Fatalf("баланс владения = %d; want 0 (слэб потерян/не возвращён)", bal)
	}
	// Gets реально работал (не вакуум): были и выдачи, и overflow.
	st := s.Stats().Pool
	if st.Gets == 0 {
		t.Fatal("Gets = 0: пул не задействован (вакуумный оракул)")
	}
	if st.Overflows == 0 {
		t.Fatal("Overflows = 0: overflow-ветвь не испытана")
	}
}

// Трим: burst сверх spareKeepBytes — излишек уходит в пул (Puts растёт),
// spare после Take в бюджете.
func TestStageSpareTrim(t *testing.T) {
	t.Parallel()
	s, err := NewStage(256 << 10)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Register(7, [8]byte{})
	// 70 кадров × 256-бакет: след ≈ 17.9 КиБ > бюджета 8 КиБ (32 слэба).
	frame := make([]byte, 254)
	for range 70 {
		s.Push(7, frame, false)
	}
	batch1, _ := c.Take(nil)
	if len(batch1) != 70 {
		t.Fatalf("батч %d кадров; want 70", len(batch1))
	}
	// Излишек уходит при триме ПРЕДЫДУЩЕГО батча: ещё один кадр → Take#2
	// тримит batch1 — 38 слэбов сверх бюджета в пул.
	s.Push(7, frame, false)
	batch2, _ := c.Take(batch1)
	c.mu.Lock()
	spareCap := c.spareCap
	c.mu.Unlock()
	if spareCap > spareKeepBytes {
		t.Fatalf("spareCap = %d > бюджета %d", spareCap, spareKeepBytes)
	}
	if puts := s.Stats().Pool.Puts; puts < 38 {
		t.Fatalf("Puts = %d; want ≥38 (излишек трима не ушёл в пул)", puts)
	}
	// Баланс замкнут после финала.
	c.Recycle(batch2)
	s.Unregister(7)
	if bal := poolBalance(s); bal != 0 {
		t.Fatalf("баланс после трим-чурна = %d; want 0", bal)
	}
}

// Best-fit: мелкий кадр не занимает крупный слэб spare.
func TestStageSpareBestFit(t *testing.T) {
	t.Parallel()
	s, err := NewStage(256 << 10)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Register(7, [8]byte{})
	big := make([]byte, 1000) // wireLen 1002 → бакет 1024
	small := make([]byte, 30) // wireLen 32 → бакет 128
	// Волна 1: два разных бакета в батче.
	s.Push(7, big, false)
	s.Push(7, small, false)
	w1, _ := c.Take(nil)
	// Волна 2: мисс по пустому spare (волна 1 у вызывающего) — два Get.
	s.Push(7, big, false)
	s.Push(7, small, false)
	w2, _ := c.Take(w1)
	// Take#2 припарковал волну 1: spare = [1024, 128].
	c.mu.Lock()
	has1024, has128 := false, false
	for _, sl := range c.spare {
		switch cap(sl) {
		case 1024:
			has1024 = true
		case 128:
			has128 = true
		}
	}
	c.mu.Unlock()
	if !has1024 || !has128 {
		t.Fatalf("spare после Take#2: 1024=%v 128=%v; want оба", has1024, has128)
	}
	// Волна 3 из spare: мелкий кадр обязан взять 128-бакет (best-fit),
	// не проедать 1024.
	s.Push(7, small, false)
	c.mu.Lock()
	smallTookBig := false
	for _, sl := range c.spare {
		if cap(sl) == 1024 {
			smallTookBig = true // 1024 всё ещё в spare → мелкий взял 128
		}
	}
	c.mu.Unlock()
	if !smallTookBig {
		t.Fatal("мелкий кадр проел 1024-бакет (first-fit вместо best-fit)")
	}
	s.Push(7, big, false)
	w3, _ := c.Take(w2)
	c.Recycle(w3)
	s.Unregister(7)
	if bal := poolBalance(s); bal != 0 {
		t.Fatalf("баланс best-fit = %d; want 0", bal)
	}
	// Gets: волна 1 (2) + волна 2 (2) = 4; волна 3 — из spare, без Get.
	if st := s.Stats().Pool; st.Gets != 4 {
		t.Fatalf("Gets = %d; want 4 (волна 3 обязана переиспользовать spare)", st.Gets)
	}
}

// Кросс-клиентское переиспользование: слэбы умершего клиента обслуживают
// нового — 0 аллокаций на цикл после прогрева (AllocsPerRun), датчик замкнут.
func TestStageCrossClientReuse(t *testing.T) {

	s, err := NewStage(256 << 10)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 100)
	// Прогрев: первый клиент берёт слэбы из пула и умирает с дреном.
	c1 := s.Register(1, [8]byte{})
	s.Push(1, frame, false)
	next, _ := c1.Take(nil)
	c1.Recycle(next)
	s.Unregister(1)
	if bal := poolBalance(s); bal != 0 {
		t.Fatalf("прогрев: баланс = %d", bal)
	}
	// Новый клиент: стационарный цикл writeLoop (push → Take(prev) → hold)
	// без аллокаций — слэб из пула, reuse-Get не аллоцирует, носитель
	// батча переиспользуется. Регистрация/выход — вне замера.
	c2 := s.Register(2, [8]byte{})
	s.Push(2, frame, false) // прогрев не паркует: очередь непуста
	prev, _ := c2.Take(nil) // носитель батча + слэб
	allocs := testing.AllocsPerRun(100, func() {
		s.Push(2, frame, false)
		prev, _ = c2.Take(prev)
	})
	if allocs != 0 {
		t.Fatalf("AllocsPerRun стационарного цикла = %v; want 0", allocs)
	}
	c2.Recycle(prev)
	s.Unregister(2)
	if bal := poolBalance(s); bal != 0 {
		t.Fatalf("баланс после кросс-клиентского цикла = %d; want 0", bal)
	}
}

// Recycle шва: финальный батч после close-Take уходит в пул (замыкание),
// двойной вызов не происходит (контракт однократности на вызывающем —
// здесь проверяем сам факт закрытия баланса).
func TestStageRecycleClosesBalance(t *testing.T) {
	t.Parallel()
	s, err := NewStage(256 << 10)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Register(9, [8]byte{})
	s.Push(9, make([]byte, 200), false)
	s.Close(9)
	var prev [][]byte
	next, close := c.Take(prev)
	if !close {
		t.Fatal("Take после Close не вернул close")
	}
	// Финальный батч у write-горутины; Recycle — как её defer.
	c.Recycle(next)
	s.Unregister(9)
	if bal := poolBalance(s); bal != 0 {
		t.Fatalf("баланс после Recycle+Unregister = %d; want 0", bal)
	}
}

// Датчик ловит потерю: слэб, не возвращённый Recycle, виден как дефицит
// (фальсифицируемость инварианта — не всегда-истина).
func TestStagePoolBalanceDetectsLeak(t *testing.T) {
	t.Parallel()
	s, err := NewStage(256 << 10)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Register(5, [8]byte{})
	s.Push(5, make([]byte, 200), false)
	next, _ := c.Take(nil)
	s.Unregister(5)
	_ = next // слэб «потерян» write-горутиной без Recycle
	if bal := poolBalance(s); bal <= 0 {
		t.Fatalf("потеря не видна датчиком: баланс = %d; want > 0", bal)
	}
	c.Recycle(next) // уборка теста
}

// Гонка Push×Unregister (замыкание мажора ревью): конкурентный push мёртвой
// записи не берёт слэб из пула — баланс замкнут под штормом.
func TestStagePushUnregisterRace(t *testing.T) {
	t.Parallel()
	s, err := NewStage(256 << 10)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 50)
	const rounds = 200
	for i := ClientID(1); i <= rounds; i++ {
		s.Register(i, [8]byte{})
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := ClientID(1); i <= rounds; i++ {
			s.Push(i, frame, false)
		}
	}()
	for i := ClientID(1); i <= rounds; i++ {
		s.Unregister(i)
	}
	<-done
	// Все записи сняты; пуш迟到 — dead-откат без слэба.
	for i := ClientID(1); i <= rounds; i++ {
		s.Push(i, frame, false)
	}
	if bal := poolBalance(s); bal != 0 {
		t.Fatalf("баланс после гонки = %d; want 0 (push взял слэб у мёртвой записи)", bal)
	}
}
