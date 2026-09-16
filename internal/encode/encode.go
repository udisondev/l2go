// Package encode — стейтлес-стадия энкода исходящего: per-client FIFO
// готовых кадров (слэб на кадр, возврат слэбов обменом при Take), покадровый
// криптоблок, отдача батчей write-горутине conn. Работает вне тика; часов
// здесь нет. Слэб кадра — проводная запись [длина uint16 LE][кадр]:
// заголовок длины идёт открытым текстом, шифруется тело кадра.
//
// Источник слэбов — бакетный пул Stage (первый живой потребитель pkg/bufpool,
// решение P1.2): горячий цикл живого клиента — возвратный список spare
// (ни аллокаций, ни пул-операций), пул обслуживает границы — бёрст (мисс
// spare), смерть клиента и излишек сверх spareKeepBytes. Датчик владения
// (Gets−Overflows)−Puts замкнут: каждый слэб рождён Get и возвращён Put
// (финальный батч — Recycle шва conn, очередь/spare — Unregister).
package encode

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/udisondev/l2go/internal/crypto"
	"github.com/udisondev/l2go/pkg/bufpool"
)

// ClientID — идентификатор коннекта клиента (совпадает с conn.ConnID).
type ClientID = uint64

// spareKeepBytes — бюджет возвратного списка клиента: покрывает слиток входа
// (~1.5–2 КиБ) с запасом; излишек уходит в общий пул (иначе бёрст медленного
// читателя паркует до byteCap навсегда). Излишек сверх бюджета живёт
// pool-раундтрипом на каждый батч — осознанный трейд «память ↔ цикл»
// (ревью P3.7b, R8): стационарный след > 8 КиБ платит ~10–38 нс/слэб.
const spareKeepBytes = 8192

// entry — кадр очереди: слэб и марка крипты. Граница plain/crypt — атрибут
// элемента FIFO (KeyPacket идёт открытым текстом, последующие кадры — с
// маркой crypt): упорядочивание делает сама очередь, внеполосных
// переключений нет.
type entry struct {
	slab  []byte
	crypt bool
}

// client — запись клиента стейджа; реализует потребительский шов conn.Outbound.
// Take зовёт ровно одна write-горутина коннекта, Push — актор шлюза и регион
// (стационарные кадры): мьютекс сериализует очередь, паркинг — notify-токен
// cap-1 на переходе пусто→непусто.
type client struct {
	crypt *crypto.GameCrypt
	pool  *bufpool.Pool

	mu       sync.Mutex
	queue    []entry
	spare    [][]byte
	spareCap int // бегущая сумма cap слэбов spare (обновляется при изменениях)
	bytes    int
	dead     bool // Unregister дренировал запись: push откатывается без слэба
	closing  bool
	notify   chan struct{}
}

// Stage — карта per-client FIFO. Кап байтов на клиента ограничивает память
// медленного читателя: push сверх капа не встаёт в очередь, следующий Take
// возвращает close (TCP flow control — не замена: медленный читатель
// блокирует Write).
type Stage struct {
	mu      sync.Mutex
	clients map[ClientID]*client
	byteCap int
	pool    bufpool.Pool

	pushed   atomic.Uint64
	capDrops atomic.Uint64
}

// NewStage валидирует конфигурацию (нулевой кап запрещён — «0 = без лимита»
// молча ловушка) и возвращает пустую стадию.
func NewStage(byteCap int) (*Stage, error) {
	if byteCap <= 0 {
		return nil, fmt.Errorf("encode: byteCap = %d: нулевой кап запрещён", byteCap)
	}
	return &Stage{
		clients: make(map[ClientID]*client),
		byteCap: byteCap,
	}, nil
}

// Register публикует запись клиента с ключом сессии (ключ рождается в conn
// при accept) — до старта write-горутины: окна Take-до-регистрации нет.
// Возвращает клиентскую запись (шов conn.Outbound); повторная регистрация
// того же id — нарушение контракта вызывающего.
func (s *Stage) Register(id ClientID, key [8]byte) *client {
	c := &client{
		crypt:  crypto.NewGameCrypt(key),
		pool:   &s.pool,
		notify: make(chan struct{}, 1),
	}
	c.crypt.Enable()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.clients[id]; dup {
		panic(fmt.Sprintf("encode: Register(%d): клиент уже зарегистрирован", id))
	}
	s.clients[id] = c
	return c
}

// Unregister снимает запись клиента и возвращает её слэбы (очередь и spare)
// в общий пул — владение замкнуто, записи переживают смерть владельца.
// Повторный вызов — no-op. Дрен под c.mu после снятия из карты: конкурирующий
// push сверяет dead и откатывается БЕЗ взятия слэба из пула (иначе окно
// между поиском записи и push теряло бы слэб навсегда).
func (s *Stage) Unregister(id ClientID) {
	s.mu.Lock()
	c, ok := s.clients[id]
	if ok {
		delete(s.clients, id)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	c.mu.Lock()
	c.dead = true
	for _, e := range c.queue {
		s.pool.Put(e.slab)
	}
	c.queue = nil
	for _, sl := range c.spare {
		s.pool.Put(sl)
	}
	c.spare = nil
	c.spareCap = 0
	c.mu.Unlock()
}

// Push ставит кадр в FIFO клиента: слэб берётся из возвратного списка
// (нет подходящего — из общего пула, никогда не блокировать); crypt — марка
// кадра. Кадр сверх байтового капа не встаёт в очередь: следующий Take
// вернёт close. Неизвестный id — no-op (клиент уже ушёл); пустой кадр —
// no-op (в протоколе нет пустых).
func (s *Stage) Push(id ClientID, frame []byte, crypt bool) {
	s.mu.Lock()
	c, ok := s.clients[id]
	s.mu.Unlock()
	if !ok || len(frame) == 0 {
		return
	}
	accepted, dead := c.push(frame, crypt, s.byteCap)
	switch {
	case dead:
		// Запись дренирована Unregister: как неизвестный id — молча (это
		// не переполнение капа).
	case accepted:
		s.pushed.Add(1)
	default:
		s.capDrops.Add(1)
	}
}

// Close помечает соединение закрываемым после флеша очереди: стоящие кадры
// будут выданы, Take вернёт close. Неизвестный id — no-op.
func (s *Stage) Close(id ClientID) {
	s.mu.Lock()
	c, ok := s.clients[id]
	s.mu.Unlock()
	if !ok {
		return
	}
	c.close()
}

// Stats — снимок счётчиков стейджа, включая датчик владения пулом
// (Gets−Overflows)−Puts == 0 после тирдауна всех клиентов; тройка Load —
// неатомарна (контракт bufpool).
func (s *Stage) Stats() StageStats {
	s.mu.Lock()
	clients := len(s.clients)
	s.mu.Unlock()
	return StageStats{
		Clients:  clients,
		Pushed:   s.pushed.Load(),
		CapDrops: s.capDrops.Load(),
		Pool:     s.pool.Stats(),
	}
}

// StageStats — снимок метрик стейджа.
type StageStats struct {
	Clients  int
	Pushed   uint64
	CapDrops uint64
	Pool     bufpool.PoolStats
}

// Depth возвращает глубину FIFO клиента в байтах провода и кадрах
// (наблюдаемость backpressure; паркинг write-горутины её не меняет).
func (c *client) Depth() (bytes, frames int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes, len(c.queue)
}

// push исполняется у записи клиента; accepted=false — кадр не встал
// (сверх капа); dead=true — запись дренирована Unregister, полный abort:
// слэб не приобретается (ни из spare, ни pool.Get), кадр не встаёт в
// очередь — иначе слэб терялся бы у мёртвой записи навсегда.
func (c *client) push(frame []byte, crypt bool, byteCap int) (accepted, dead bool) {
	c.mu.Lock()
	if c.dead {
		c.mu.Unlock()
		return false, true
	}
	wireLen := 2 + len(frame)
	if c.bytes+wireLen > byteCap {
		c.closing = true
		c.mu.Unlock()
		c.wake()
		return false, false
	}
	slab := c.takeSpare(wireLen)
	slab[0] = byte(wireLen)
	slab[1] = byte(wireLen >> 8)
	copy(slab[2:], frame)
	wasEmpty := len(c.queue) == 0
	c.queue = append(c.queue, entry{slab: slab, crypt: crypt})
	c.bytes += wireLen
	c.mu.Unlock()
	if wasEmpty {
		c.wake()
	}
	return true, false
}

// takeSpare достаёт слэб наименьшего достаточного cap из возвратного списка
// (best-fit: мелкий кадр не занимает крупный бакет — first-fit кормил бы
// 4096-бакет мелочью и гнал следующий крупный кадр в пул); нет подходящего —
// из общего пула (мисс = бёрст/новый клиент, там и считается датчиком).
// Требует c.mu.
func (c *client) takeSpare(n int) []byte {
	best, bestCap := -1, 1<<30
	for i, sl := range c.spare {
		capSl := cap(sl)
		if capSl >= n && capSl < bestCap {
			best, bestCap = i, capSl
			if capSl == n {
				break // точное совпадение не улучшить
			}
		}
	}
	if best >= 0 {
		sl := c.spare[best]
		c.spare = append(c.spare[:best], c.spare[best+1:]...)
		c.spareCap -= cap(sl)
		return sl[:n]
	}
	return c.pool.Get(n)
}

// close помечает закрытие и будит паркинг write-горутины.
func (c *client) close() {
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	c.wake()
}

// wake — notify-токен cap-1: неблокирующая отправка, spurious безвреден.
func (c *client) wake() {
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

// Recycle возвращает write-горутине её финальный батч в общий пул — замыкание
// владения на выходе writeLoop (close/ошибка записи). Однократность на
// вызывающем: после вызова prev мёртв (второй вызов с тем же prev — двойной
// Put, пул не детектирует); nil-safe. Локов не берёт — прямой Put
// потокобезопасен, запись клиента уже не касается (Unregister дренирует
// очередь/spare — множества не пересекаются: prev не возвращался в spare).
func (c *client) Recycle(prev [][]byte) {
	for _, sl := range prev {
		c.pool.Put(sl)
	}
}

// Take возвращает write-горутине очередной батч (вся доступная глубина одним
// куском — один write на пробуждение) и вердикт close: записать батч и
// разорвать сокет (переполнение байтового капа медленным читателем или
// close-after-flush по инициативе шлюза — сокет не рвётся до флеша очереди).
// Prev — батч предыдущего вызова: его запись завершена к следующему Take,
// слэбы возвращаются в spare в бюджетом spareKeepBytes (излишек — в общий
// пул), носитель переиспользуется (0 аллокаций на стационарный цикл).
// Пусто и не закрывается — паркинг до следующего push/close. Криптоблок —
// последовательный Encrypt по телам кадров батча (счётчик и каскад покадровы;
// единый проход над склейкой запрещён протоколом). Паника криптоблока
// оставляет prev во владении вызывающего (возврат в spare — строго после
// криптоблока): раскрутка до Recycle не даёт двойного Put, лок отпущен
// defer'ом.
func (c *client) Take(prev [][]byte) (next [][]byte, close bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.queue) == 0 && !c.closing {
		c.mu.Unlock()
		<-c.notify
		c.mu.Lock()
	}
	// Криптоблок вперёд: до возврата prev в spare и до переиспользования
	// носителя prev (паника здесь не портит владение).
	for _, e := range c.queue {
		if e.crypt {
			// Пустое тело сюда не встаёт (push отсеивает пустые кадры);
			// отказ Encrypt — нарушение контракта программистом.
			if err := c.crypt.Encrypt(e.slab[2:]); err != nil {
				panic(fmt.Sprintf("encode: криптоблок кадра: %v", err))
			}
		}
	}
	// Возврат prev: в spare в пределах бюджета, излишек — в общий пул.
	for _, sl := range prev {
		if c.spareCap+cap(sl) <= spareKeepBytes {
			c.spare = append(c.spare, sl)
			c.spareCap += cap(sl)
			continue
		}
		c.pool.Put(sl)
	}
	next = prev[:0]
	for _, e := range c.queue {
		next = append(next, e.slab)
	}
	c.bytes = 0
	c.queue = c.queue[:0]
	close = c.closing
	return next, close
}
