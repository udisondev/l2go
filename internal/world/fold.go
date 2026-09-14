package world

import (
	"encoding/binary"
	"math/rand/v2"

	"github.com/udisondev/l2go/internal/transport"
)

// Birth — эффект рождения сущности: вычисляется свёрткой из писем шага,
// применяется актором после обхода дрена (Register + Claim + вставка в
// отсортированный слайс). Присвоенный EntityID регион дописывает в лог порций.
type Birth struct {
	Ent Entity
}

// Retire — эффект удаления сущности: актор исполняет Despawn → Retire →
// освобождение записи (контракт транспорта).
type Retire struct {
	ID transport.EntityID
}

// StepResult — эффекты шага свёртки: исходящие письма и изменения населения.
// Игровая логика фазы 3 возвращает пустые слайсы; производители — фазы 3.7+.
type StepResult struct {
	Out     []transport.Envelope
	Births  []Birth
	Retires []Retire
}

// AdvisoryIn — залогированный advisory-вход шага (шов replica; сводится с
// модулем репликации в фазе 3.8).
type AdvisoryIn struct {
	Cell   uint32
	Entity transport.EntityID
}

// State — счётчики свёртки: детерминированная функция входов. LastDelta — в
// тиках прошлого шага (наблюдаемость dt без настенных часов); Noise —
// аккумулятор RNG-потребления (wrapping-add: повторный шаг того же тика даёт
// другой вклад).
type State struct {
	Steps      uint64
	Letters    uint64
	KindCounts [transport.KindCount]uint64
	Noise      uint64
	LastDelta  uint64
}

// Fold — детерминированная функция шага: применяет порции к состоянию и
// населению (сортированный слайс ents — актор передаёт проекцию своих
// жителей, реплей строит из дампа и применённых эффектов), возвращает эффекты
// шага. delta — в тиках (конверсия во время — у потребителя через период из
// конфигурации/заголовка лога). Чистота исполнена так: fold не читает ничего,
// кроме аргументов — никакого I/O, настенных часов, глобального состояния;
// мутация переданных state/ents — владение вызывающего актора, детерминизм
// свёртки полисится реплей-гейтом бит-в-бит.
func Fold(tick Tick, delta uint64, rng *rand.Rand, st *State, ents []*Entity, portions []Portion, adv []AdvisoryIn) StepResult {
	st.Steps++
	st.LastDelta = delta
	st.Noise += rng.Uint64()
	res := StepResult{}
	for i := range portions {
		for _, env := range portions[i].Envs {
			st.Letters++
			if k := int(env.Kind); k >= 1 && k <= transport.KindCount {
				st.KindCounts[k-1]++
			}
		}
	}
	for _, e := range ents {
		e.Beat = tick
	}
	return res
}

// Dump — детерминированная сериализация состояния и населения: бит-в-бит
// сравнение в тестах, потребитель — реплей-гейт. Население — в порядке
// обхода (контракт вызывающего: сортированный слайс).
func (st *State) Dump(ents []*Entity) []byte {
	buf := make([]byte, 0, 48+40*len(ents))
	buf = binary.AppendUvarint(buf, st.Steps)
	buf = binary.AppendUvarint(buf, st.Letters)
	for k := range st.KindCounts {
		buf = binary.AppendUvarint(buf, st.KindCounts[k])
	}
	buf = binary.AppendUvarint(buf, st.Noise)
	buf = binary.AppendUvarint(buf, st.LastDelta)
	buf = binary.AppendUvarint(buf, uint64(len(ents)))
	for _, e := range ents {
		buf = appendEntity(buf, e)
	}
	return buf
}

// appendEntity — детерминированная сериализация сущности (поля по убыванию
// значимости; Servants и Transfers — полностью).
func appendEntity(buf []byte, e *Entity) []byte {
	buf = binary.AppendUvarint(buf, uint64(e.ID))
	buf = binary.AppendUvarint(buf, uint64(e.Owner))
	buf = binary.AppendUvarint(buf, uint64(e.Pos.X))
	buf = binary.AppendUvarint(buf, uint64(e.Pos.Y))
	buf = binary.AppendUvarint(buf, uint64(e.Pos.Z))
	buf = binary.AppendUvarint(buf, uint64(e.Dest.X))
	buf = binary.AppendUvarint(buf, uint64(e.Dest.Y))
	buf = binary.AppendUvarint(buf, uint64(e.Dest.Z))
	if e.Moving {
		buf = append(buf, 1)
	} else {
		buf = append(buf, 0)
	}
	if e.Dead {
		buf = append(buf, 1)
	} else {
		buf = append(buf, 0)
	}
	buf = binary.AppendUvarint(buf, uint64(e.HP))
	buf = binary.AppendUvarint(buf, uint64(e.Beat))
	for i := range e.Servants {
		if e.Servants[i].Alive {
			buf = append(buf, 1)
		} else {
			buf = append(buf, 0)
		}
		buf = binary.AppendUvarint(buf, uint64(e.Servants[i].Pos.X))
		buf = binary.AppendUvarint(buf, uint64(e.Servants[i].Pos.Y))
		buf = binary.AppendUvarint(buf, uint64(e.Servants[i].Pos.Z))
	}
	buf = binary.AppendUvarint(buf, uint64(len(e.Transfers)))
	for i := range e.Transfers {
		tr := &e.Transfers[i]
		buf = binary.AppendUvarint(buf, tr.ID)
		buf = append(buf, tr.Phase)
		buf = binary.AppendUvarint(buf, uint64(len(tr.Payload)))
		buf = append(buf, tr.Payload...)
		buf = binary.AppendUvarint(buf, uint64(len(tr.Precondition)))
		buf = append(buf, tr.Precondition...)
	}
	return buf
}
