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

// recordOf — сущность → AoI-запись (поля по потребителям CharInfo/NpcInfo;
// живой Heading и клампнутая Dest — источники кадров движения P3.9; NPC-поля
// — из скина рождения P3.10; Flags фаза 3 не порождает — синтетика тестов).
func recordOf(ent *Entity) replica.Record {
	rec := replica.Record{
		Entity:  ent.ID,
		Cell:    aoiCell,
		X:       ent.Pos.X,
		Y:       ent.Pos.Y,
		Z:       ent.Pos.Z,
		DestX:   ent.Dest.X,
		DestY:   ent.Dest.Y,
		DestZ:   ent.Dest.Z,
		Heading: ent.Heading,
		Moving:  ent.Moving,
	}
	if ent.Player == nil {
		rec.Kind = replica.RecordKindNPC
		if s := ent.Npc; s != nil {
			rec.TemplateID = s.TemplateID
			rec.Name = s.Name
			rec.Title = s.Title
			rec.Attackable = s.Attackable
			rec.CollisionRadius = s.CollisionRadius
			rec.CollisionHeight = s.CollisionHeight
			rec.RunSpd = s.RunSpd
			rec.WalkSpd = s.WalkSpd
			rec.SwimRunSpd = s.SwimRunSpd
			rec.SwimWalkSpd = s.SwimWalkSpd
			rec.PAtkSpd = s.PAtkSpd
			rec.MAtkSpd = s.MAtkSpd
			rec.MoveMultiplier = s.MoveMultiplier
			rec.AttackSpeedMultiplier = s.AttackSpeedMultiplier
			rec.RHand = s.RHand
			rec.LHand = s.LHand
		}
		return rec
	}
	rec.Kind = replica.RecordKindPlayer
	p := &ent.Player.Rec
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

// charInfoOf — AoI-запись игрока → CharInfo (константы шаблона HumanFighter —
// единый источник с UserInfo; MaxCp/CurCp=0 до формул статов — запись-
// отклонение О-6 реестра P3.8). Standing = факт позы (запись стоит);
// Running — режим run/walk канона,walk-режим — с формулами статов.
func charInfoOf(rec replica.Record) protocol.CharInfoData {
	return protocol.CharInfoData{
		X: rec.X, Y: rec.Y, Z: rec.Z,
		ObjID:                 int32(encode.ObjectIDBase + uint64(rec.Entity)),
		Name:                  rec.Name,
		Race:                  rec.Race,
		Female:                rec.Female,
		BaseClass:             rec.BaseClass,
		MAtkSpd:               int32(persist.HumanFighter.BaseMAtkSpd),
		PAtkSpd:               int32(persist.HumanFighter.BasePAtkSpd),
		RunSpd:                int32(persist.HumanFighter.RunSpd),
		WalkSpd:               int32(persist.HumanFighter.WalkSpd),
		SwimRunSpd:            int32(persist.HumanFighter.SwimSpd),
		SwimWalkSpd:           int32(persist.HumanFighter.SwimSpd),
		MoveMultiplier:        1.0,
		AttackSpeedMultiplier: 1.0,
		CollisionRadius:       persist.HumanFighter.CollisionR,
		CollisionHeight:       persist.HumanFighter.CollisionH,
		HairStyle:             rec.HairStyle,
		HairColor:             rec.HairColor,
		Face:                  rec.Face,
		Standing:              !rec.Moving,
		Running:               true,
		ClassID:               rec.ClassID,
		Heading:               rec.Heading,
	}
}

// composeMoveFrame — CharMoveToLocation из записи: сервер-авторитетный стрим,
// одна позиция на кадр (расхождение с каноном L2J «один кадр на интент»
// осознанное: r1 «сервер бродкастит свою позицию»; полоса 29 Б × 10 Гц × пары,
// пересмотр — фаза 4 по нагрузке).
func composeMoveFrame(rec replica.Record) []byte {
	dst := make([]byte, protocol.CharMoveToLocationSize)
	protocol.WriteCharMoveToLocation(dst, int32(encode.ObjectIDBase+uint64(rec.Entity)),
		rec.DestX, rec.DestY, rec.DestZ, rec.X, rec.Y, rec.Z)
	return dst
}

// composeStopFrame — StopMove из записи (прибытие/остановка/коррекция).
func composeStopFrame(rec replica.Record) []byte {
	dst := make([]byte, protocol.StopMoveSize)
	protocol.WriteStopMove(dst, int32(encode.ObjectIDBase+uint64(rec.Entity)),
		rec.X, rec.Y, rec.Z, rec.Heading)
	return dst
}

// npcInfoOf — AoI-запись NPC → NpcInfo (полный скоростной блок из скина
// рождения; константы канона — displayId+10⁶, nameAbove и хвост — писатель
// protocol; Running=false — NPC фазы 3 не бежит при спавне, канон).
func npcInfoOf(rec replica.Record) protocol.NpcInfoData {
	return protocol.NpcInfoData{
		ObjID:                 int32(encode.ObjectIDBase + uint64(rec.Entity)),
		DisplayID:             rec.TemplateID,
		Attackable:            rec.Attackable,
		X:                     rec.X,
		Y:                     rec.Y,
		Z:                     rec.Z,
		Heading:               rec.Heading,
		MAtkSpd:               rec.MAtkSpd,
		PAtkSpd:               rec.PAtkSpd,
		RunSpd:                rec.RunSpd,
		WalkSpd:               rec.WalkSpd,
		SwimRunSpd:            rec.SwimRunSpd,
		SwimWalkSpd:           rec.SwimWalkSpd,
		MoveMultiplier:        rec.MoveMultiplier,
		AttackSpeedMultiplier: rec.AttackSpeedMultiplier,
		CollisionRadius:       rec.CollisionRadius,
		CollisionHeight:       rec.CollisionHeight,
		Name:                  rec.Name,
		Title:                 rec.Title,
		RHand:                 rec.RHand,
		LHand:                 rec.LHand,
	}
}

// composeJoin — события join → кадры (пушится регионом в pendingPushes ПОСЛЕ
// Apply — слив только применённого стадинга). Ввод движущегося — CharInfo +
// CharMoveToLocation (describeState канона); апдейт — стрим/стоп; NPC-ввод —
// NpcInfo (P3.10; счётчик вводов — метрика живого прогона).
func (r *Region) composeJoin(events []replica.Event) []FramePush {
	pushes := make([]FramePush, 0, len(events))
	for _, ev := range events {
		switch ev.Kind {
		case replica.EventIntroduce:
			if ev.Target.Kind != replica.RecordKindPlayer {
				d := npcInfoOf(ev.Target)
				dst := make([]byte, protocol.NpcInfoSize(d))
				protocol.WriteNpcInfo(dst, d)
				pushes = append(pushes, FramePush{Client: ev.Obs.ConnID, Frame: dst, Crypt: true})
				r.npcIntroduced.Add(1)
				continue
			}
			d := charInfoOf(ev.Target)
			dst := make([]byte, protocol.CharInfoSize(d))
			protocol.WriteCharInfo(dst, d)
			pushes = append(pushes, FramePush{Client: ev.Obs.ConnID, Frame: dst, Crypt: true})
			if ev.Target.Moving {
				pushes = append(pushes, FramePush{Client: ev.Obs.ConnID, Frame: composeMoveFrame(ev.Target), Crypt: true})
			}
		case replica.EventRemove:
			dst := make([]byte, protocol.DeleteObjectSize)
			protocol.WriteDeleteObject(dst, int32(encode.ObjectIDBase+uint64(ev.Target.Entity)))
			pushes = append(pushes, FramePush{Client: ev.Obs.ConnID, Frame: dst, Crypt: true})
		case replica.EventUpdate:
			if ev.Target.Moving {
				pushes = append(pushes, FramePush{Client: ev.Obs.ConnID, Frame: composeMoveFrame(ev.Target), Crypt: true})
			} else {
				pushes = append(pushes, FramePush{Client: ev.Obs.ConnID, Frame: composeStopFrame(ev.Target), Crypt: true})
			}
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
	// логируется КАЖДОЕ чтение, включая промах: ok — часть ответа источника,
	// ветвление будущего потребителя по нему обязано быть воспроизводимым в реплее
	a.r.adviseBuf = append(a.r.adviseBuf, AdvisoryIn{Cell: cell, Entity: id})
	return snap, ok
}
