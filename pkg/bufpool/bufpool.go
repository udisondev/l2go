// Package bufpool — бакетный пул байтовых буферов для крупных пакетов; малые
// пакеты живут на стеке вызывающего, Put обязателен на горячих путях.
//
// Контракт владения: после Get буфер принадлежит вызывающему до Put; Put
// передаёт владение безусловно — в том числе при отбраковке чужой ёмкости
// (буфер уходит в GC и не возвращается); после Put любое использование любого
// алиаса запрещено (копия заголовка, подслайс — тоже); двойной Put одного
// буфера запрещён — пул не детектирует дубликат и молча выдаст массив двоим;
// Put принимает буфер, полученный из Get, целиком (проекция с урезанной
// ёмкостью — нарушение); передача буфера в другую горутину — только вместе с
// единственным владением. Подслайс, пережива́ющий буфер (в том числе у
// асинхронного потребителя), — копировать.
//
// Выдача: len равен size, cap равен размеру бакета, весь cap нулевой. Свыше
// 4096 байт — прямая аллокация (Overflows). Верхнюю границу size валидирует
// вызывающий (размер кадра может приходить из недоверенного поля провода);
// append за cap аннулирует членство в пуле — датчик деградации: дрейф Gets и
// Puts. При всплеске Put содержимое бакетов закрепляется до GC (+victim-цикл):
// датчик — Stats, позиция по памяти — GOMEMLIMIT. Значение Pool не копировать
// после первого использования; нулевое значение работоспособно.
//
// Семантика Stats: Gets считает все выдачи (включая overflow), Overflows —
// подмножество, Puts — только принятые в бакеты; nil и чужая ёмкость счётчики
// не растят; снимок из трёх Load — не атомарная тройка.
//
// Порт udisondev/interlude@34fe4c86 (pkg/bufpool/bufpool.go). Отклонения от
// референса: slog убран (наблюдаемость — Stats); хранение в бакетах
// pointer-shaped (*[N]byte, без бокса слайса в any); имя Pool и работоспособное
// нулевое значение (референс: BytePool/NewBytePool); паника-контракт Get при
// size < 1 (референс молча аллоцировал/паниковал без диагностики); статистика
// агрегатная (референс: per-bucket массивы); clear по всей ёмкости (референс:
// по len). Бакеты 128–4096 — по распределению размеров пакетов Interlude
// (≈65% ≤128, ≈99.9% ≤4096).
package bufpool

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// Сетка бакетов — именованные константы: единый источник правды для Get, Put
// и тестов. Массив-справочник с функциями поиска не заводится: длина типов
// *[N]byte не выводима из массива (в Go нет дженериков по длине массива), он
// стал бы четвёртой репликацией; рассинхрон лесенок Get/Put ловится
// покомпонентной сходимостью стресс-теста.
const (
	bucket128  = 128
	bucket256  = 256
	bucket512  = 512
	bucket1024 = 1024
	bucket2048 = 2048
	bucket4096 = 4096
)

// Pool — бакетный пул байтовых буферов. Нулевое значение работоспособно;
// копировать после первого использования нельзя.
type Pool struct {
	p128, p256, p512, p1024, p2048, p4096 sync.Pool
	gets, puts, overflows                 atomic.Int64
}

// PoolStats — снимок счётчиков пула (не атомарная тройка).
type PoolStats struct {
	Gets      int64
	Puts      int64
	Overflows int64
}

// Stats возвращает снимок счётчиков.
func (p *Pool) Stats() PoolStats {
	return PoolStats{
		Gets:      p.gets.Load(),
		Puts:      p.puts.Load(),
		Overflows: p.overflows.Load(),
	}
}

// Get возвращает буфер длины size: ёмкостью бакета и полностью нулевой при
// size ≤ 4096, либо прямой аллокацией (Overflows) свыше. Паникует при
// size < 1 — нарушение программного контракта; верхнюю границу валидирует
// вызывающий.
func (p *Pool) Get(size int) []byte {
	if size < 1 {
		panic(fmt.Sprintf("bufpool: Get: size должен быть ≥ 1, получен %d", size))
	}
	p.gets.Add(1)
	switch {
	case size <= bucket128:
		if v := p.p128.Get(); v != nil {
			b := v.(*[bucket128]byte)[:size]
			clear(b[:bucket128]) // reuse-буфер обязан быть нулевым по всему cap
			return b
		}
		return new([bucket128]byte)[:size] // fresh-массив уже нулевой
	case size <= bucket256:
		if v := p.p256.Get(); v != nil {
			b := v.(*[bucket256]byte)[:size]
			clear(b[:bucket256])
			return b
		}
		return new([bucket256]byte)[:size]
	case size <= bucket512:
		if v := p.p512.Get(); v != nil {
			b := v.(*[bucket512]byte)[:size]
			clear(b[:bucket512])
			return b
		}
		return new([bucket512]byte)[:size]
	case size <= bucket1024:
		if v := p.p1024.Get(); v != nil {
			b := v.(*[bucket1024]byte)[:size]
			clear(b[:bucket1024])
			return b
		}
		return new([bucket1024]byte)[:size]
	case size <= bucket2048:
		if v := p.p2048.Get(); v != nil {
			b := v.(*[bucket2048]byte)[:size]
			clear(b[:bucket2048])
			return b
		}
		return new([bucket2048]byte)[:size]
	case size <= bucket4096:
		if v := p.p4096.Get(); v != nil {
			b := v.(*[bucket4096]byte)[:size]
			clear(b[:bucket4096])
			return b
		}
		return new([bucket4096]byte)[:size]
	default:
		p.overflows.Add(1)
		return make([]byte, size)
	}
}

// Put возвращает буфер, полученный из Get, в бакет по его ёмкости. Nil и чужая
// ёмкость отбрасываются молча. После Put буфер и его алиасы использовать
// нельзя.
func (p *Pool) Put(b []byte) {
	if b == nil {
		return
	}
	switch cap(b) {
	case bucket128:
		p.p128.Put((*[bucket128]byte)(b[:bucket128:bucket128]))
	case bucket256:
		p.p256.Put((*[bucket256]byte)(b[:bucket256:bucket256]))
	case bucket512:
		p.p512.Put((*[bucket512]byte)(b[:bucket512:bucket512]))
	case bucket1024:
		p.p1024.Put((*[bucket1024]byte)(b[:bucket1024:bucket1024]))
	case bucket2048:
		p.p2048.Put((*[bucket2048]byte)(b[:bucket2048:bucket2048]))
	case bucket4096:
		p.p4096.Put((*[bucket4096]byte)(b[:bucket4096:bucket4096]))
	default:
		return
	}
	p.puts.Add(1)
}
