// Фаза AoI региона: маппинг населения в AoI-записи, компоновка join-событий
// в клиентские кадры и advisory-обёртка с логированием чтений (D4).
package world

import (
	"fmt"

	"github.com/udisondev/l2go/internal/encode"
	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/replica"
	"github.com/udisondev/l2go/internal/transport"
)

// AdvisoryIn = replica.AdvisoryInput — обязательство P3.2 сведено (алиас:
// битовое представление порции и portionVersion не меняются).
type AdvisoryIn = replica.AdvisoryInput

// aoiCell — вырожденная сетка фазы 3: одна ячейка на регион.
const aoiCell replica.CellID = 0

// recordOf — сущность → AoI-запись (поля по потребителю CharInfo; NpcInfo-поля
// дополнит P3.10; Flags фаза 3 не порождает — синтетика тестов).
func recordOf(ent *Entity) replica.Record {
	rec := replica.Record{
		Entity: ent.ID,
		Cell:   aoiCell,
		X:      ent.Pos.X,
		Y:      ent.Pos.Y,
		Z:      ent.Pos.Z,
		Moving: ent.Moving,
	}
	if ent.Player == nil {
		rec.Kind = replica.RecordKindNPC
		return rec
	}
	rec.Kind = replica.RecordKindPlayer
	p := &ent.Player.Rec
	rec.Heading = int32(p.Heading)
	rec.Name = p.Name
	rec.Race = int32(p.Race)
	rec.Female = p.Sex == 1
	rec.BaseClass = int32(p.ClassID)
	rec.ClassID = int32(p.ClassID)
	rec.HairStyle = int32(p.HairStyle)
	rec.HairColor = int32(p.HairColor)
	rec.Face = int32(p.Face)
	return rec
}

// charInfoOf — AoI-запись игрока → CharInfo (шаблонные константы HumanFighter
// как у UserInfo P3.7; скорости/коллизии — константы golden-прецедента P3.5;
// MaxCp/CurCp=0 до формул статов — запись-отклонение О-6 реестра P3.8).
func charInfoOf(rec replica.Record) protocol.CharInfoData {
	return protocol.CharInfoData{
		X: rec.X, Y: rec.Y, Z: rec.Z,
		ObjID:      int32(encode.ObjectIDBase + uint64(rec.Entity)),
		Name:       rec.Name,
		Race:       rec.Race,
		Female:     rec.Female,
		BaseClass:  rec.BaseClass,
		MAtkSpd:    333,
		PAtkSpd:    300,
		RunSpd:     int32(persist.HumanFighter.RunSpd),
		WalkSpd:    int32(persist.HumanFighter.WalkSpd),
		SwimRunSpd: 50, SwimWalkSpd: 50,
		MoveMultiplier:        1.0,
		AttackSpeedMultiplier: 1.0,
		CollisionRadius:       9.0,
		CollisionHeight:       23.0,
		HairStyle:             rec.HairStyle,
		HairColor:             rec.HairColor,
		Face:                  rec.Face,
		Standing:              !rec.Moving,
		Running:               rec.Moving,
		ClassID:               rec.ClassID,
		Heading:               rec.Heading,
	}
}

// composeJoin — события join → кадры (чистая функция; пушится регионом в
// pendingPushes ПОСЛЕ Apply — слив только применённого стадинга). Update —
// каркас P3.9 (кадры наполнит движение); NPC-ввод — P3.10.
func (r *Region) composeJoin(events []replica.Event) []FramePush {
	pushes := make([]FramePush, 0, len(events))
	for _, ev := range events {
		switch ev.Kind {
		case replica.EventIntroduce:
			if ev.Target.Kind != replica.RecordKindPlayer {
				r.npcIntroduceSkipped.Add(1)
				continue
			}
			d := charInfoOf(ev.Target)
			dst := make([]byte, protocol.CharInfoSize(d))
			protocol.WriteCharInfo(dst, d)
			pushes = append(pushes, FramePush{Client: ev.Obs.ConnID, Frame: dst, Crypt: true})
		case replica.EventRemove:
			dst := make([]byte, protocol.DeleteObjectSize)
			protocol.WriteDeleteObject(dst, int32(encode.ObjectIDBase+uint64(ev.Target.Entity)))
			pushes = append(pushes, FramePush{Client: ev.Obs.ConnID, Frame: dst, Crypt: true})
		case replica.EventUpdate:
			// Позиционные кадры — P3.9; событие фиксируется юнит-тестом replica
		}
	}
	return pushes
}

// logAdviser — advisory-обёртка региона: каждое чтение дописывается в порцию
// шага (N чтений = N записей); контракт окна: чтения только из свёртки —
// механика шва бьёт верхнюю границу (после LogStep = паника шва), «до LogStep»
// — дисциплина потребителя (фаза 3 потребителей нет).
type logAdviser struct {
	src *replica.Publisher
	r   *Region
}

// Snapshot реализует replica.Advisory (шов у источника по ADR-0005).
func (a *logAdviser) Snapshot(cell replica.CellID, id transport.EntityID) (replica.Snapshot, bool) {
	if !a.r.advWindow {
		panic(fmt.Sprintf("world: advisory-чтение %d вне окна свёртки (после LogStep)", id))
	}
	snap, ok := a.src.Read(cell, id)
	if ok {
		a.r.adviseBuf = append(a.r.adviseBuf, AdvisoryIn{Cell: cell, Entity: id})
	}
	return snap, ok
}
