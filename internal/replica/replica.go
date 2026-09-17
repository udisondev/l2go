// Package replica — ячеечная репликация/Area of Interest у владельца
// наблюдателя: per-owner SoA-блоб с публикацией после шага, событийный join
// и advisory-чтения. Снапшот-типы инкапсулированы здесь: игровая логика читает
// соседей только через Advisory с логированием чтения в порцию (D4), прямой
// дереференс невозможен физически. Библиотека внутри региона-владельца:
// собственных горутин нет (ADR-0005), мутации — только горутиной владельца,
// внешним читателям виден лишь атомарный свап закоммиченного поколения.
package replica

import "github.com/udisondev/l2go/internal/transport"

// CellID — ячейка глобальной сетки (шаг ≥ радиуса обзора). Фаза 3 —
// вырожденная сетка: одна ячейка на регион (тип присутствует, подъём — фаза 4).
type CellID uint32

// Flags — входы предиката видимости пары; авторитетен владелец цели в момент
// публикации. Фаза 3 — единственный синтетический флаг шва (переоценка
// предиката тестируется им); инвиз/GM — фазы 4+.
type Flags uint8

// FlagHidden — цель скрыта от наблюдателей (синтетический флаг фазы 3).
const FlagHidden Flags = 1 << 0

// Visible — единый предикат пары (наблюдатель, цель) для репликации (Join) и
// advisory-потребителей (ADR-0004 ось 4: одна функция, расхождение «клиент
// видит, моб не видит» не может возникнуть как деталь реализации). Радиус в
// предикат не входит (членство), координаты не нужны (гео-LOS — документированное
// исключение, OQ-8). Расширение входов (LOS/детект, фазы 4+) — эволюцией
// сигнатуры с обоими потребителями разом.
func Visible(obs, tgt Flags) bool {
	return tgt&FlagHidden == 0
}

// Snapshot — непрозрачное advisory-чтение записи: поля недоступны вне пакета,
// аксессоры возвращают копии примитивов. Эпоха записи появится с переездами
// фазы 4 — тогда и вернётся аксессор.
type Snapshot struct {
	entity transport.EntityID
	x, y   int32
	z      int32
	kind   RecordKind
	flags  Flags
}

// Entity возвращает вечный ID записи.
func (s Snapshot) Entity() transport.EntityID { return s.entity }

// Pos возвращает позицию записи.
func (s Snapshot) Pos() (x, y, z int32) { return s.x, s.y, s.z }

// Kind возвращает тип записи.
func (s Snapshot) Kind() RecordKind { return s.kind }

// Flags возвращает входы предиката записи.
func (s Snapshot) Flags() Flags { return s.flags }

// AdvisoryInput — залогированное advisory-чтение: реплей-харнесс инъектирует
// эти значения как входы свёртки (мир логирует каждое чтение в порцию шага).
type AdvisoryInput struct {
	Cell   CellID
	Entity transport.EntityID
}

// Advisory — advisory-API чтения снапшотов соседей для игровой логики:
// реализация обязана логировать каждое чтение в порцию (D4). Шов у источника
// по предписанию ADR-0005.
type Advisory interface {
	Snapshot(cell CellID, id transport.EntityID) (Snapshot, bool)
}

// MembershipHeader — заголовок членства per-owner блоба: генерация блоба и
// издатели в состоянии переезда (hold-last: источник старой эпохи держится до
// появления новой — анти-мигание на стыке). Фаза 3 маркеры не порождает
// (переездов нет) — поле Moving всегда пусто.
type MembershipHeader struct {
	Generation uint64
	Moving     []transport.EntityID
}

// GroundItem — AoI-запись лёгкого класса: предмет на земле не является
// сущностью транспорта (decay — состояние региона позиции; подбор — transfer).
// Публикация предметов — фаза 5; тип присутствует по решению ADR-0004 ось 3.
type GroundItem struct {
	ID         uint64
	X, Y, Z    int32
	TemplateID uint32
	Count      uint32
}
