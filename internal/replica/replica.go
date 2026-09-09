// Package replica — ячеечная репликация/Area of Interest у владельца
// наблюдателя: снапшот-типы инкапсулированы здесь, игровая логика читает
// соседей только через advisory-API с логированием чтения в порцию; прямой
// дереференс снапшотов невозможен физически.
package replica

import "github.com/udisondev/l2go/internal/transport"

// CellID — ячейка глобальной сетки (шаг ≥ радиуса обзора).
type CellID uint32

// Snapshot — непрозрачная запись сущности в per-owner SoA-блобе: поля
// недоступны вне пакета.
type Snapshot struct {
	entity uint64
	epoch  uint64
}

// Entity возвращает вечный ID записи.
func (s Snapshot) Entity() transport.EntityID { return transport.EntityID(s.entity) }

// Epoch возвращает метку персиста/маркера записи.
func (s Snapshot) Epoch() uint64 { return s.epoch }

// AdvisoryInput — залогированное advisory-чтение: реплей-харнесс инъектирует
// эти значения как входы свёртки.
type AdvisoryInput struct {
	Cell   CellID
	Entity transport.EntityID
}

// Advisory — advisory-API чтения снапшотов соседей для игровой логики:
// реализация обязана логировать каждое чтение в порцию.
type Advisory interface {
	Snapshot(cell CellID, id transport.EntityID) (Snapshot, bool)
}
