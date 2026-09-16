// Package replica — ячеечная репликация/Area of Interest у владельца
// наблюдателя: снапшот-типы инкапсулированы здесь, игровая логика читает
// соседей только через advisory-API с логированием чтения в порцию; прямой
// дереференс снапшотов невозможен физически.
package replica

import "github.com/udisondev/l2go/internal/transport"

// CellID — ячейка глобальной сетки (шаг ≥ радиуса обзора).
type CellID uint32

// Snapshot — значение advisory-чтения записи блоба: вечный id, позиция и
// эпоха (факт успеха чтения — ok-параметр метода Advisory, дубль полем не
// кодируется). Поля недоступны вне пакета — доступ методами.
type Snapshot struct {
	entity uint64
	x      int32
	y      int32
	z      int32
	epoch  uint64
}

// Entity возвращает вечный ID записи.
func (s Snapshot) Entity() transport.EntityID { return transport.EntityID(s.entity) }

// Pos возвращает позицию записи.
func (s Snapshot) Pos() (x, y, z int32) { return s.x, s.y, s.z }

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

// MembershipHeader — заголовок членства per-owner блоба: генерация блоба и
// издатели в состоянии переезда (источник старой эпохи держится до появления
// новой — анти-мигание на стыке).
type MembershipHeader struct {
	Generation uint64
	Moving     []transport.EntityID
}

// Lookup — чтение записи из блоба по вечному id: линейный скан по колонке
// id (профиль чтения — редкие per-кандидат advisory-запросы; структура под
// O(1) пересматривается с первым тиковым потребителем фазы 4 — осознанное
// отклонение от реестра ADR-0005, записано в реестре задачи P3.8).
// Вызов — из горутины региона-владельца или читателя опубликованного блоба
// (Load): блоб иммутабелен.
func Lookup(b *Blob, id transport.EntityID) (Snapshot, bool) {
	if b == nil || id == 0 {
		return Snapshot{}, false
	}
	base := b.cols()
	for slot := range b.Len() {
		if !b.IsMember(slot) {
			continue
		}
		if transport.EntityID(b.nums[base+colID*b.n+slot]) != id {
			continue
		}
		return Snapshot{
			entity: uint64(id),
			x:      b.X(slot), y: b.Y(slot), z: b.Z(slot),
			epoch: b.Epoch(slot),
		}, true
	}
	return Snapshot{}, false
}

// Published — advisory-API над последней публикацией: обёртка региона
// (владелец Load-указателя) реализует интерфейс вызовом Lookup.
type Published struct {
	load func() *Blob
}

// NewPublished — advisory над источником публикаций (атомарный Load).
func NewPublished(load func() *Blob) *Published { return &Published{load: load} }

// Snapshot implements Advisory.
func (p *Published) Snapshot(cell CellID, id transport.EntityID) (Snapshot, bool) {
	return Lookup(p.load(), id)
}

// GroundItem — AoI-запись лёгкого класса: предмет на земле не является
// сущностью транспорта (decay — состояние региона позиции; подбор — transfer).
type GroundItem struct {
	ID         uint64
	X, Y, Z    int32
	TemplateID uint32
	Count      uint32
}
