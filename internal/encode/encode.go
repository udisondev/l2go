// Package encode — стейтлес-стадия энкода исходящего: per-client FIFO
// готовых кадров (слэб на кадр, возврат слэбов обменом при Take), покадровый
// криптоблок, отдача батчей write-горутине conn. Работает вне тика; часов
// здесь нет. Слэб кадра — проводная запись [длина uint16 LE][кадр]:
// заголовок длины идёт открытым текстом, шифруется тело кадра.
package encode

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/udisondev/l2go/internal/crypto"
)

// ClientID — идентификатор коннекта клиента (совпадает с conn.ConnID).
type ClientID = uint64

// entry — кадр очереди: слэб и марка крипты. Граница plain/crypt — атрибут
// элемента FIFO (KeyPacket идёт открытым текстом, последующие кадры — с
// маркой crypt): упорядочивание делает сама очередь, внеполосных
// переключений нет.
type entry struct {
	slab  []byte
	crypt bool
}

// client — запись клиента стейджа; реализует потребительский шов conn.Outbound.
// Take зовёт ровно одна write-горутина коннекта, Push — актор шлюза (второй
// пушер стационарных кадров появится вместе с миром): мьютекс сериализует
// очередь, паркинг — notify-токен cap-1 на переходе пусто→непусто.
type client struct {
	crypt *crypto.GameCrypt

	mu      sync.Mutex
	queue   []entry
	spare   [][]byte
	bytes   int
	closing bool
	notify  chan struct{}
}

// Stage — карта per-client FIFO. Кап байтов на клиента ограничивает память
// медленного читателя: push сверх капа не встаёт в очередь, следующий Take
// возвращает ActClose (TCP flow control — не замена: медленный читатель
// блокирует Write).
type Stage struct {
	mu      sync.Mutex
	clients map[ClientID]*client
	byteCap int

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

// Unregister снимает запись клиента; слэбы уходят сборщику (per-client пул
// не переживает владельца). Повторный вызов — no-op.
func (s *Stage) Unregister(id ClientID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.clients, id)
}

// Push ставит кадр в FIFO клиента: слэб собирается из возвратного пула
// (нет свободного — свежая аллокация, никогда не блокировать); crypt — марка
// кадра. Кадр сверх байтового капа не встаёт в очередь: следующий Take
// вернёт ActClose. Неизвестный id — no-op (клиент уже ушёл); пустой кадр —
// no-op (в протоколе нет пустых).
func (s *Stage) Push(id ClientID, frame []byte, crypt bool) {
	s.mu.Lock()
	c, ok := s.clients[id]
	s.mu.Unlock()
	if !ok || len(frame) == 0 {
		return
	}
	if c.push(frame, crypt, s.byteCap) {
		s.pushed.Add(1)
	} else {
		s.capDrops.Add(1)
	}
}

// Close помечает соединение закрываемым после флеша очереди: стоящие кадры
// будут выданы, Take вернёт ActClose. Неизвестный id — no-op.
func (s *Stage) Close(id ClientID) {
	s.mu.Lock()
	c, ok := s.clients[id]
	s.mu.Unlock()
	if !ok {
		return
	}
	c.close()
}

// Stats — снимок счётчиков стейджа (глубина per-client — Depth записи).
func (s *Stage) Stats() StageStats {
	s.mu.Lock()
	clients := len(s.clients)
	s.mu.Unlock()
	return StageStats{
		Clients:  clients,
		Pushed:   s.pushed.Load(),
		CapDrops: s.capDrops.Load(),
	}
}

// StageStats — снимок метрик стейджа.
type StageStats struct {
	Clients  int
	Pushed   uint64
	CapDrops uint64
}

// Depth возвращает глубину FIFO клиента в байтах провода и кадрах
// (наблюдаемость backpressure; паркинг write-горутины её не меняет).
func (c *client) Depth() (bytes, frames int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes, len(c.queue)
}

// push исполняется у записи клиента; false — кадр не встал (сверх капа).
func (c *client) push(frame []byte, crypt bool, byteCap int) bool {
	c.mu.Lock()
	wireLen := 2 + len(frame)
	if c.bytes+wireLen > byteCap {
		c.closing = true
		c.mu.Unlock()
		c.wake()
		return false
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
	return true
}

// takeSpare достаёт слэб достаточной ёмкости из возвратного пула; нет
// свободного — свежая аллокация (никогда не блокировать). Требует c.mu.
func (c *client) takeSpare(n int) []byte {
	for i, sl := range c.spare {
		if cap(sl) >= n {
			c.spare = append(c.spare[:i], c.spare[i+1:]...)
			return sl[:n]
		}
	}
	return make([]byte, n)
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

// Take возвращает write-горутине очередной батч (вся доступная глубина одним
// куском — один write на пробуждение) и вердикт close: записать батч и
// разорвать сокет (переполнение байтового капа медленным читателем или
// close-after-flush по инициативе шлюза — сокет не рвётся до флеша очереди).
// Prev — батч предыдущего вызова: его запись завершена к следующему Take,
// слэбы возвращаются в пул, носитель переиспользуется (0 аллокаций на
// стационарный цикл). Пусто и не закрывается — паркинг до следующего
// push/close. Криптоблок — последовательный Encrypt по телам кадров батча
// (счётчик и каскад покадровы; единый проход над склейкой запрещён
// протоколом).
func (c *client) Take(prev [][]byte) (next [][]byte, close bool) {
	c.mu.Lock()
	for len(c.queue) == 0 && !c.closing {
		c.mu.Unlock()
		<-c.notify
		c.mu.Lock()
	}
	c.spare = append(c.spare, prev...)
	next = prev[:0]
	for _, e := range c.queue {
		if e.crypt {
			// Пустое тело сюда не встаёт (push отсеивает пустые кадры);
			// отказ Encrypt — нарушение контракта программистом.
			if err := c.crypt.Encrypt(e.slab[2:]); err != nil {
				panic(fmt.Sprintf("encode: криптоблок кадра: %v", err))
			}
		}
		next = append(next, e.slab)
	}
	c.bytes = 0
	c.queue = c.queue[:0]
	close = c.closing
	c.mu.Unlock()
	return next, close
}
