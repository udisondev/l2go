// Реплей сессии региона D6: записанный лог порций прогоняется через ту же
// свёртку с инъекцией тиков из записей; канонический дамп сравнивается
// бит-в-бит (два прогона одного лога; дамп живого прогона). Исходящий поток
// не сравнивается (ADR-0004 «Цена»).

package world

import (
	"fmt"
	"math/rand/v2"
	"sort"

	"github.com/udisondev/l2go/internal/data"
	"github.com/udisondev/l2go/internal/geo"
)

// ReplayResult — итог реплея: канонический дамп свёртки на конце цепочки
// шагов (или на маркере паники — достоверность обрывается явно) и счётчики
// прогона.
type ReplayResult struct {
	Dump           []byte
	Steps          uint64
	Letters        uint64
	StoppedAtPanic bool
}

// Replay прогоняет записанную сессию через ту же свёртку. Правила и сид RNG —
// из заголовка лога и записей (PCG(region, tick), как в шаге региона);
// населения применяется из записей: рождения — с присвоенными ID (общий с
// актором хелпер stateBirth), удаления — снятием по ID. Вход (заголовок,
// кадры) не мутируется — вставка рождения глубокой копией записи. Исходящие
// письма и пуши свёртки отбрасываются. Маркер паники обрывает прогон: дамп
// достоверен до маркера, StoppedAtPanic = true. Паника внутри свёртки не
// ловится: производственный баг обязан быть воспроизводимым тестом (D6).
func Replay(hdr FileHeader, frames []LogFrame, static *data.Static, gm *geo.Map) (ReplayResult, error) {
	if !hdr.Payloads {
		return ReplayResult{}, fmt.Errorf("world: реплей: сессия записана без payloads (флаг включения — при записи)")
	}
	rules := Rules{
		GraceTicks:     int(hdr.GraceTicks),
		SaveRetryTicks: int(hdr.SaveRetryTicks),
		PeriodNS:       int64(hdr.PeriodNS),
		Persist:        hdr.Persist,
		Gateway:        hdr.Gateway,
		From:           hdr.CtrlFrom,
	}
	if !rules.valid() {
		return ReplayResult{}, fmt.Errorf("world: реплей: правила заголовка невалидны (период/окна/адресаты): %+v", hdr)
	}
	env := Env{Region: hdr.Region, Rules: rules, GM: gm, Static: static}
	st := newState()
	var ents []*Entity
	res := ReplayResult{}
	for i := range frames {
		f := &frames[i]
		if f.Panic != nil {
			res.StoppedAtPanic = true
			break
		}
		s := f.Step
		rng := rand.New(rand.NewPCG(uint64(hdr.Region), uint64(s.Tick)))
		foldRes := Fold(s.Tick, s.Delta, rng, st, ents, portionsOf(hdr.Region, s), s.Advisory, env)
		_ = foldRes // Out/Pushes отбрасываются: исходящий поток не сравнивается
		var err error
		if ents, err = replayBirths(st, ents, s, rules.GraceTicks); err != nil {
			return ReplayResult{}, err
		}
		ents = replayRetires(ents, s.Retires)
	}
	res.Steps = st.Steps
	res.Letters = st.Letters
	res.Dump = st.Dump(ents)
	return res, nil
}

// portionsOf — порции шага в порядке записей (порядок применения свёрткой
// сохранён из дрена).
func portionsOf(region RegionID, s *StepRecord) []Portion {
	if len(s.Portions) == 0 {
		return nil
	}
	ps := make([]Portion, len(s.Portions))
	for i := range s.Portions {
		ps[i] = Portion{Region: region, Tick: s.Tick, Envs: s.Portions[i].Envs}
	}
	return ps
}

// replayBirths — применения рождений шага к населению реплея: вставка по
// присвоенному ID (расхождение ID записи и сущности — ошибка разбора),
// связки State общим с актором хелпером.
func replayBirths(st *State, ents []*Entity, s *StepRecord, grace int) ([]*Entity, error) {
	for i := range s.Births {
		b := &s.Births[i]
		if b.Ent.ID != b.ID {
			return ents, fmt.Errorf("world: реплей: рождение %d: ID сущности %d расходится с записью", b.ID, b.Ent.ID)
		}
		ent := cloneEntity(&b.Ent)
		if ent.Player != nil {
			stateBirth(st, ent, s.Tick, grace)
		}
		ents = insertEntity(ents, ent)
	}
	return ents, nil
}

// replayRetires — снятие удалений шага (неизвестный ID — no-op, зеркально
// Remove актора).
func replayRetires(ents []*Entity, retires []Retire) []*Entity {
	for _, rt := range retires {
		i := sort.Search(len(ents), func(i int) bool { return ents[i].ID >= rt.ID })
		if i < len(ents) && ents[i].ID == rt.ID {
			ents = append(ents[:i], ents[i+1:]...)
		}
	}
	return ents
}

// insertEntity — вставка в сортированный по ID слайс (как Spawn актора).
func insertEntity(ents []*Entity, e *Entity) []*Entity {
	i := sort.Search(len(ents), func(i int) bool { return ents[i].ID >= e.ID })
	ents = append(ents, nil)
	copy(ents[i+1:], ents[i:])
	ents[i] = e
	return ents
}

// cloneEntity — глубокая копия сущности: вход реплея не мутируется
// (Fold пишет Beat/движение/бакеты; указатели Player/Npc и байты
// transfer-записей разделяются с кадрами ридера).
func cloneEntity(src *Entity) *Entity {
	e := *src
	if src.Player != nil {
		p := *src.Player
		e.Player = &p
	}
	if src.Npc != nil {
		n := *src.Npc
		e.Npc = &n
	}
	if len(src.Transfers) > 0 {
		tr := make([]TransferRecord, len(src.Transfers))
		for i, r := range src.Transfers {
			tr[i] = r
			tr[i].Payload = append([]byte(nil), r.Payload...)
			tr[i].Precondition = append([]byte(nil), r.Precondition...)
		}
		e.Transfers = tr
	}
	return &e
}
