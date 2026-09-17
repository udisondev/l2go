package world

// Гео-потребитель движения на синтетической геометрии (группа D тест-плана):
// билдер региона из публичного L2J-формата (flat-фон + complex-ячейки),
// сценарии стены/L-угла/моста перенесены из P2.4.

import (
	"encoding/binary"
	"testing"

	"github.com/udisondev/l2go/internal/geo"
)

// synthGeo — сборка региона гео в тестах мира: фон flat-0, поверх — complex-
// ячейки словом (h/8)<<4|NSWE (публичный формат; дублирует ~3 строки кодирования
// geo-тестов — экспорт тест-механики в прод-API отвергнут по codestyle §8).
type synthGeo struct {
	t      *testing.T
	rx, ry int
	cells  map[[2]int]uint16 // глобальные гео-координаты → слово
}

func newSynthGeo(t *testing.T) *synthGeo {
	t.Helper()
	return &synthGeo{t: t, rx: 16, ry: 16, cells: make(map[[2]int]uint16)}
}

// set задаёт complex-ячейку (высота кратна 8; NSWE — флаги направлений).
func (s *synthGeo) set(gx, gy, h int, nswe geo.NSWE) {
	s.t.Helper()
	if h%8 != 0 {
		s.t.Fatalf("synthGeo: высота %d не кратна 8", h)
	}
	s.cells[[2]int{gx, gy}] = uint16(h/8)<<4 | uint16(nswe)
}

// build кодирует регион (65536 блоков: flat=3 байта, complex=1+64×uint16 LE) и
// собирает карту. Блок становится complex при первой заданной ячейке; фон блока
// — открытый (высота 0, NSWE все), явные слова пишутся поверх (слово 0 —
// легальная глухая ячейка).
func (s *synthGeo) build() *geo.Map {
	s.t.Helper()
	const open = uint16(0x0F)
	var words [256 * 256][64]uint16
	var complex [256 * 256]bool
	for key, w := range s.cells {
		gx, gy := key[0], key[1]
		lx, ly := gx-s.rx*2048, gy-s.ry*2048
		if lx < 0 || lx >= 2048 || ly < 0 || ly >= 2048 {
			s.t.Fatalf("synthGeo: ячейка (%d, %d) вне региона (%d, %d)", gx, gy, s.rx, s.ry)
		}
		b := (lx>>3)<<8 | (ly >> 3)
		if !complex[b] {
			complex[b] = true
			for c := range words[b] {
				words[b][c] = open
			}
		}
		words[b][(lx&7)<<3|(ly&7)] = w
	}
	buf := make([]byte, 0, 256*256*3)
	for i := range 256 * 256 {
		if !complex[i] {
			buf = append(buf, 0, 0, 0) // flat, высота 0
			continue
		}
		buf = append(buf, 1)
		for _, w := range words[i] {
			buf = binary.LittleEndian.AppendUint16(buf, w)
		}
	}
	reg, err := geo.DecodeRegion(s.rx, s.ry, buf)
	if err != nil {
		s.t.Fatalf("synthGeo: DecodeRegion: %v", err)
	}
	m, err := geo.NewMapFromRegions([]*geo.Region{reg})
	if err != nil {
		s.t.Fatalf("synthGeo: NewMapFromRegions: %v", err)
	}
	return m
}

// atGeo — центр ячейки в мировых координатах.
func atGeo(gx, gy, z int) Position {
	return Position{X: int32(geo.GeoToWorldX(gx)), Y: int32(geo.GeoToWorldY(gy)), Z: int32(z)}
}

// wallColumn — глухая стена по гео-колонке gx (все направления сняты, высота 0):
// непроходима в обе стороны.
func (s *synthGeo) wallColumn(gx int, fromGy, toGy int) {
	for gy := fromGy; gy <= toGy; gy++ {
		s.set(gx, gy, 0, 0)
	}
}

func TestFoldMoveIntoWallClampsAndStops(t *testing.T) {
	sg := newSynthGeo(t)
	sg.set(16*2048+105, 16*2048+100, 0, geo.NSWEAll&^geo.East) // стена: восток закрыт
	gm := sg.build()
	from := atGeo(16*2048+100, 16*2048+100, 0)
	st := newState()
	ents := []*Entity{playerEnt(101, from.X, from.Y, from.Z)}
	// цель далеко за стеной
	foldM(10, 0, st, ents, gm, moveLetter(101, atGeo(16*2048+110, 16*2048+100, 0).X, from.Y, 0))
	e := ents[0]
	wantX := int32(geo.GeoToWorldX(16*2048 + 105)) // кламп — центр ячейки стены (встать можно, уйти востоком — нет)
	if !e.Moving || e.Dest.X != wantX {
		t.Fatalf("кламп: moving %v destX %d; want true, %d", e.Moving, e.Dest.X, wantX)
	}
	for i := range 20 {
		foldM(10+Tick(i), 1, st, ents, gm)
	}
	if e.Moving || e.Pos.X != wantX {
		t.Fatalf("прибытие к стене: moving %v posX %d; want false, %d", e.Moving, e.Pos.X, wantX)
	}
}

func TestFoldMoveDiagonalLWallImpassable(t *testing.T) {
	// L-угол канона P2.4: закрыты (101,100) и (100,101); диагональ из (100,100)
	// на (110,110) — срез невозможен (анти-корнер композит), кламп = исходная.
	sg := newSynthGeo(t)
	sg.set(16*2048+101, 16*2048+100, 0, 0)
	sg.set(16*2048+100, 16*2048+101, 0, 0)
	gm := sg.build()
	from := atGeo(16*2048+100, 16*2048+100, 0)
	st := newState()
	ents := []*Entity{playerEnt(101, from.X, from.Y, from.Z)}
	target := atGeo(16*2048+110, 16*2048+110, 0)
	res := foldM(10, 0, st, ents, gm, moveLetter(101, target.X, target.Y, 0))
	e := ents[0]
	if e.Moving || e.Dest != e.Pos {
		t.Fatalf("L-угол: движ = %v, dest %v; want без старта (кламп = исходная, срез закрыт)",
			e.Moving, e.Dest)
	}
	if _, ok := findPush(res, 7, opStopMove); !ok {
		t.Errorf("немедленный StopMove отсутствует")
	}
}

func TestFoldValidLocationReturnedPointReachable(t *testing.T) {
	// перенос свойства P2.4: возвращённая клампом точка достижима из from —
	// вторичный ValidLocation(from, res) ок и возвращает res же (идемпотент).
	sg := newSynthGeo(t)
	sg.wallColumn(16*2048+104, 16*2048+98, 16*2048+102)
	gm := sg.build()
	from := atGeo(16*2048+100, 16*2048+100, 0)
	checked := 0
	for dgx := -2; dgx <= 8; dgx++ {
		for dgy := -2; dgy <= 2; dgy++ {
			to := atGeo(16*2048+100+dgx, 16*2048+100+dgy, 0)
			res, _ := gm.ValidLocation(geo.Loc{X: int(from.X), Y: int(from.Y), Z: 0},
				geo.Loc{X: int(to.X), Y: int(to.Y), Z: 0})
			second, ok := gm.ValidLocation(geo.Loc{X: int(from.X), Y: int(from.Y), Z: 0},
				geo.Loc{X: res.X, Y: res.Y, Z: res.Z})
			if !ok || second.X != res.X || second.Y != res.Y {
				t.Errorf("пара (dgx=%d, dgy=%d): res %v недостижима повторно (second %v, ok %v)",
					dgx, dgy, res, second, ok)
			}
			checked++
		}
	}
	if checked < 50 {
		t.Fatalf("сетка пар не прошла: %d", checked)
	}
}

func TestFoldEmptyGeoMapNullRegionSemantics(t *testing.T) {
	// артефакт без гео-регионов: NullRegion канона — всё проходимо, дропов нет
	st := newState()
	ents := []*Entity{playerEnt(101, syncPos.X, syncPos.Y, syncPos.Z)}
	foldM(10, 0, st, ents, emptyGeo, moveLetter(101, syncPos.X+1000, syncPos.Y, 0))
	e := ents[0]
	if !e.Moving || e.Dest.X != syncPos.X+1000 || e.Dest.Z != 0 || st.DroppedFrames != 0 {
		t.Fatalf("пустая карта: moving %v dest %v drops %d; want true, цель как есть, 0",
			e.Moving, e.Dest, st.DroppedFrames)
	}
}

func TestFoldSlopeArrivalZFollowsRelief(t *testing.T) {
	// склон из P2.4: ступени по 40 юн в complex-ячейках — проход по рельефу;
	// прибытие ставит Z слоя цели (gm.NearestZ), не заявленный клиентом ноль.
	sg := newSynthGeo(t)
	for gy := 16*2048 + 100; gy <= 16*2048+102; gy++ {
		sg.set(16*2048+101, gy, (gy-16*2048-100)*40, geo.NSWEAll)
	}
	gm := sg.build()
	from := atGeo(16*2048+100, 16*2048+100, 0)
	st := newState()
	ents := []*Entity{playerEnt(101, from.X, from.Y, 0)}
	target := atGeo(16*2048+102, 16*2048+102, 0)
	foldM(10, 0, st, ents, gm, moveLetter(101, target.X, target.Y, 0))
	if !ents[0].Moving {
		t.Fatalf("движение по склону не начато (ступени ≤40 проходимы)")
	}
	for i := range 20 {
		foldM(10+Tick(i), 1, st, ents, gm)
	}
	if ents[0].Moving {
		t.Fatalf("прибытие не наступило")
	}
	wantZ := int32(gm.NearestZ(geo.Loc{X: int(target.X), Y: int(target.Y), Z: 0}))
	if ents[0].Pos.Z != wantZ {
		t.Fatalf("Z прибытия = %d; want %d (слой рельефа цели)", ents[0].Pos.Z, wantZ)
	}
}
