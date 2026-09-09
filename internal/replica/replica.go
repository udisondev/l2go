// Package replica — ячеечная репликация/AoI у владельца наблюдателя
// (ADR-0004): снапшот-типы инкапсулированы здесь, игровая логика читает
// соседей только через advisory-API с логированием чтения в порцию (D4);
// прямой дереференс снапшотов невозможен физически (ADR-0003 §16).
// Реализация — фазы 3–4.
package replica

import "github.com/udisondev/l2go/internal/transport"

// CellID — ячейка глобальной сетки (шаг ≥ радиуса обзора, ADR-0004).
type CellID uint32

// Snapshot — непрозрачная запись сущности в per-owner SoA-блобе: поля
// недоступны вне пакета (эпоха/генерация блоба/маркеры переезда — ADR-0004).
type Snapshot struct {
	entity uint64
	epoch  uint64
}

// Entity возвращает вечный ID записи.
func (s Snapshot) Entity() transport.EntityID { return transport.EntityID(s.entity) }

// Epoch возвращает метку персиста/маркера записи (ADR-0003 §8).
func (s Snapshot) Epoch() uint64 { return s.epoch }

// AdvisoryInput — залогированное advisory-чтение: реплей-харнесс инъектирует
// эти значения как входы свёртки (D4/D6).
type AdvisoryInput struct {
	Cell   CellID
	Entity transport.EntityID
}

// Advisory — advisory-API чтения снапшотов соседей для игровой логики:
// реализация обязана логировать каждое чтение в порцию (D4).
type Advisory interface {
	Snapshot(cell CellID, id transport.EntityID) (Snapshot, bool)
}
