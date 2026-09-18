package world

// Сетка ячеек AoI (P4.1): известность и стрим пар через границу ячеек
// (ближайшая к позициям харнесса граница — x=−73728, cellSize 8192),
// паник-окно Apply с уходом члена, перезаход у границы.

import (
	"testing"

	"github.com/udisondev/l2go/internal/encode"
	"github.com/udisondev/l2go/internal/protocol"
	"github.com/udisondev/l2go/internal/transport"
)

// TestRegionKnownSetAcrossCellBoundary — пара по разные стороны границы
// ячеек: взаимный ввод строится, расхождение за Exit рвёт известность.
func TestRegionKnownSetAcrossCellBoundary(t *testing.T) {
	_, r := newTestRegion(t, DefaultConfig())
	// x=−73729 — клетка 70, x=−73728 — клетка 71: 1 юнит между, d ≤ Enter
	west := Entity{Owner: r.id, HP: 100, Pos: Position{X: -73729, Y: 258271, Z: -3104},
		Player: &Player{ConnID: 1, SpeedBudget: speedCAP}}
	east := Entity{Owner: r.id, HP: 100, Pos: Position{X: -73728, Y: 258271, Z: -3104},
		Player: &Player{ConnID: 2, SpeedBudget: speedCAP}}
	if _, err := r.Spawn(west); err != nil {
		t.Fatalf("Spawn(west): %v", err)
	}
	if _, err := r.Spawn(east); err != nil {
		t.Fatalf("Spawn(east): %v", err)
	}
	r.step()
	intros := 0
	for _, p := range r.joinPushes {
		if len(p.Frame) > 0 && p.Frame[0] == protocol.OpCharInfo {
			intros++
		}
	}
	if intros != 2 {
		t.Fatalf("CharInfo через границу ячеек = %d; want 2 (взаимность)", intros)
	}
	// расхождение на 9k юнитов: цель покидает окно наблюдателя (> Exit);
	// прямая мутация жителя до старта Run — белое вмешательство харнесса
	for _, res := range r.residents {
		if res.ent.Player != nil && res.ent.Player.ConnID == 2 {
			res.ent.Pos.X += 9000
			res.ent.Dest = res.ent.Pos
		}
	}
	r.step()
	dels := 0
	for _, p := range r.joinPushes {
		if len(p.Frame) > 0 && p.Frame[0] == protocol.OpDeleteObject {
			dels++
		}
	}
	if dels != 2 {
		t.Fatalf("DeleteObject при расхождении = %d; want 2 (по одному каждому из пары)", dels)
	}
}

// TestRegionMovementStreamAcrossCellBoundary — стрим движения через границу:
// CharMoveToLocation на каждом шаге пересечения, известность не мерцает
// (ноль CharInfo/DeleteObject после первичного ввода), прибытие — StopMove.
func TestRegionMovementStreamAcrossCellBoundary(t *testing.T) {
	_, r := newTestRegion(t, DefaultConfig())
	obs := Entity{Owner: r.id, HP: 100, Pos: Position{X: -73729, Y: 258271, Z: -3104},
		Player: &Player{ConnID: 1, SpeedBudget: speedCAP}}
	start := Position{X: -73828, Y: 258271, Z: -3104} // западнее границы
	dest := Position{X: -73628, Y: 258271, Z: -3104}  // восточнее: 200 юнитов ≈ 17 тиков бега (11.5 юн/тик, 10 Гц)
	mover := Entity{Owner: r.id, HP: 100, Pos: start, Moving: true,
		Dest: dest, MoveFrom: start, MoveDist: distMilli(start, dest),
		Player: &Player{ConnID: 2, SpeedBudget: speedCAP}}
	if _, err := r.Spawn(obs); err != nil {
		t.Fatalf("Spawn(obs): %v", err)
	}
	if _, err := r.Spawn(mover); err != nil {
		t.Fatalf("Spawn(mover): %v", err)
	}
	r.step() // вводы
	moves, flicker, stops := 0, 0, 0
	for range 150 {
		r.metro.tick.Add(1)
		r.step()
		if len(r.joinPushes) == 0 {
			break // прибытие: стрим закончен
		}
		for _, p := range r.joinPushes {
			if len(p.Frame) == 0 {
				continue
			}
			switch p.Frame[0] {
			case protocol.OpCharMoveToLocation:
				moves++
			case protocol.OpStopMove:
				stops++
			case protocol.OpCharInfo, protocol.OpDeleteObject:
				flicker++
			}
		}
	}
	if moves < 12 {
		t.Fatalf("стрим пересечения границы = %d кадров; want ≥12 из ~17 тиков бега", moves)
	}
	if flicker != 0 {
		t.Fatalf("известность мерцает на границе: %d CharInfo/DeleteObject в стриме", flicker)
	}
	if stops != 1 {
		t.Fatalf("StopMove на прибытии = %d; want 1", stops)
	}
}

// TestRegionPanicInApplyDespawnReconcileCleanup — паник-окно Apply на шаге
// ухода члена (логаут): примирение следующего шага доезжает чистку хвоста —
// DeleteObject доставлен, фантома нет.
func TestRegionPanicInApplyDespawnReconcileCleanup(t *testing.T) {
	cfg := DefaultConfig()
	cfg.GraceTicks = 1
	h := newEnterHarness(t, cfg)
	h.enterConn(t, 7, mkRecAt("alice", "Alice", -73729, 258271))
	h.enterConn(t, 8, mkRecAt("bob", "Bob", -73728, 258271))
	bobID := playerEntity(h, 8)
	h.pushes.Reset()
	h.r.join.ForcePanicInApply.Store(true)
	// raw-отправка (без waitTick): ожидание на проваленных шагах зависло бы
	// до заморозки серии — прецедент enterConnRaw паник-инъекций
	h.reg.Send(transport.Envelope{
		To: transport.Addr{Entity: transport.EntityID(bobID)}, FromID: h.gwID,
		Kind: transport.KindClientFrame, Payload: []byte{protocol.OpLogout}})
	waitCond(t, h.r, func(st RegionStats) bool { return st.Failed >= 1 })
	h.r.join.ForcePanicInApply.Store(false)
	for range 6 {
		waitTick(t, h.r)
	}
	// оракул по кадрам после сброса коллектора: DeleteObject(bob) доставлен
	// примирением ровно один, повторных CharInfo(bob) нет — фантома нет
	chars, dels := objIDs(h.pushes)
	deleteCount, ghostIntro := 0, 0
	for _, id := range dels {
		if id == encode.ObjectIDBase+bobID {
			deleteCount++
		}
	}
	for _, id := range chars {
		if id == encode.ObjectIDBase+bobID {
			ghostIntro++
		}
	}
	if deleteCount != 1 {
		t.Fatalf("DeleteObject(bob) после паник-окна = %d; want 1 (чистка хвоста примирением)", deleteCount)
	}
	if ghostIntro != 0 {
		t.Fatalf("призрачный CharInfo(bob) после удаления = %d", ghostIntro)
	}
}

// TestRegionReenterNearCellBoundarySameKnownSet — перезаход у границы ячеек
// (вытеснение аккаунта): известность перестраивается на ту же пару позиций,
// призрачного CharInfo вытесненной сущности нет.
func TestRegionReenterNearCellBoundarySameKnownSet(t *testing.T) {
	h := newEnterHarness(t, DefaultConfig())
	h.enterConn(t, 7, mkRecAt("alice", "Alice", -73729, 258271))
	h.enterConn(t, 8, mkRecAt("bob", "Bob", -73728, 258271))
	oldBob := playerEntity(h, 8)
	if known := knownOf(h.pushes); len(known) != 2 {
		t.Fatalf("известность первого входа = %v; want 2", known)
	}
	h.pushes.Reset()
	h.enterConn(t, 9, mkRecAt("bob", "Bob2", -73728, 258271)) // вытеснение с нового конна
	chars, _ := objIDs(h.pushes)
	for _, id := range chars {
		if id == encode.ObjectIDBase+oldBob {
			t.Fatalf("призрачный CharInfo вытесненной сущности %d", oldBob)
		}
	}
	if known := knownOf(h.pushes); len(known) != 2 {
		t.Fatalf("известность после перезахода у границы = %v; want 2", known)
	}
}
