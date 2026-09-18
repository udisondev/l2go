package world

// Разворачивание NPC-населения из спавнов статики (группа B тест-плана):
// письмо KindDeployNPCs → Births свёртки. Статика — мини-датапак через
// fstest.MapFS (категории-заглушки обязательны: Load читает все каталоги).

import (
	"bytes"
	"fmt"
	"testing"
	"testing/fstest"

	"github.com/udisondev/l2go/internal/data"
	"github.com/udisondev/l2go/internal/geo"
	"github.com/udisondev/l2go/internal/persist"
	"github.com/udisondev/l2go/internal/replica"
	"github.com/udisondev/l2go/internal/transport"
)

// miniStatic — синтетический датапак под сценарий: NPC у центра среза
// (1000,1000) радиуса 10000.
func miniStatic(t *testing.T) *data.Static {
	t.Helper()
	npcs := `<?xml version="1.0" encoding="UTF-8"?>
<list>
	<npc id="20550" level="27" type="Monster" name="Орк" title="Разбойник">
		<collision><radius normal="13" /><height normal="22.5" /></collision>
		<stats><speed><walk ground="60" /><run ground="140" /></speed>
			<attack attackSpeed="253" /></stats>
	</npc>
	<npc id="20551" level="25" type="Monster" name="Орк-приспешник" />
	<npc id="30080" type="Merchant" name="Торговец" />
	<npc id="29001" level="40" type="RaidBoss" name="Рейд" />
	<npc id="29002" level="40" type="SiegeGuard" name="Гвардия" />
</list>`
	spawns := `<?xml version="1.0" encoding="UTF-8"?>
<list enabled="true">
	<spawn name="Points">
		<npc id="20550" x="1000" y="2000" z="-300" heading="16384" respawnDelay="60" />
		<npc id="20550" x="500" y="500" z="-300" count="2" respawnDelay="60" />
		<npc id="30080" x="30000" y="30000" z="-100" respawnDelay="60" />
		<npc id="29001" x="1500" y="1500" z="-100" respawnDelay="60" />
		<npc id="29002" x="1600" y="1600" z="-100" respawnDelay="60" />
		<npc id="80000" x="1800" y="1800" z="-100" respawnDelay="60" />
	</spawn>
	<spawn zone="near_terr">
		<territory minZ="-1000" maxZ="0">
			<node x="500" y="500" />
			<node x="2500" y="500" />
			<node x="2500" y="2500" />
		</territory>
		<npc id="20551" count="3" respawnDelay="22" />
	</spawn>
	<spawn zone="banned_terr">
		<territory minZ="-1000" maxZ="0">
			<node x="5000" y="5000" />
			<node x="7000" y="5000" />
			<node x="7000" y="7000" />
		</territory>
		<banned_territory minZ="-2000" maxZ="2000">
			<node x="4900" y="4900" />
			<node x="7100" y="4900" />
			<node x="7100" y="7100" />
		</banned_territory>
		<npc id="20550" count="1" respawnDelay="22" />
	</spawn>
	<spawn zone="far_terr">
		<territory minZ="-1000" maxZ="0">
			<node x="60000" y="60000" />
			<node x="64000" y="60000" />
			<node x="64000" y="64000" />
		</territory>
		<npc id="20550" count="1" respawnDelay="22" />
	</spawn>
</list>`
	return mkStatic(t, npcs, spawns)
}

// mkStatic — загрузка мини-датапака из XML-текстов (общий с бенчами:
// категории-заглушки обязательны — Load читает все каталоги).
func mkStatic(t *testing.T, npcs, spawns string) *data.Static {
	t.Helper()
	return staticOfXML(t, npcs, spawns)
}

// staticOfXML — сборка статики из XML-текстов (тесты и бенчи).
func staticOfXML(tb testing.TB, npcs, spawns string) *data.Static {
	tb.Helper()
	fsys := fstest.MapFS{
		"stats/items/.keep":   {},
		"stats/skills/.keep":  {},
		"zones/.keep":         {},
		"stats/npcs/npcs.xml": {Data: []byte(npcs)},
		"spawns/synth.xml":    {Data: []byte(spawns)},
	}
	st, rep, err := data.Load(fsys)
	if err != nil {
		tb.Fatalf("staticOfXML: %v", err)
	}
	if rep.HasErrors() {
		tb.Fatalf("staticOfXML: датапак красный: %v", rep.Errors)
	}
	return st
}

// testSliceRadius — радиус среза сценариев: покрывает точки (1000,2000),
// (500,500), (1500,1500) и полигоны near_terr/banned_terr; far_* и торговца
// (30000,30000) оставляет вне.
const testSliceRadius = int32(10000)

func deployLetter(t *testing.T, msg transport.NPCDeployMsg) transport.Envelope {
	t.Helper()
	return transport.Envelope{
		To: transport.Addr{Entity: 1}, FromID: 1, Kind: transport.KindDeployNPCs,
		Payload: mustLetter(t, msg),
	}
}

func mustLetter(t *testing.T, v any) []byte {
	t.Helper()
	body, err := transport.EncodeLetter(v)
	if err != nil {
		t.Fatalf("encode letter: %v", err)
	}
	return body
}

// B1: точечный спавн Monster в срезе — рождение с полным скином; heading
// датапака; скорости из raw-bag.
func TestFoldDeployNPCsPointSpawnAndSkin(t *testing.T) {
	st, static := &State{}, miniStatic(t)
	var res StepResult
	deploySpawns(1, 100, st, static, transport.NPCDeployMsg{CenterX: 1000, CenterY: 1000, Radius: testSliceRadius}, emptyGeo, &res)
	var orc *Birth
	for i := range res.Births {
		b := &res.Births[i]
		if b.Ent.Npc != nil && b.Ent.Npc.TemplateID == 20550 && b.Ent.Pos.X == 1000 {
			orc = b
		}
	}
	if orc == nil {
		t.Fatalf("точечный 20550 в (1000,2000) не развёрнут; births=%d", len(res.Births))
	}
	if orc.Ent.Pos.Y != 2000 || orc.Ent.Heading != 16384 {
		t.Errorf("точка/heading = %d/%d; want 2000/16384", orc.Ent.Pos.Y, orc.Ent.Heading)
	}
	if orc.Ent.HP != 1 {
		t.Errorf("HP = %d; want 1 (бессмертные, поле не потребляется)", orc.Ent.HP)
	}
	skin := orc.Ent.Npc
	if skin.Name != "Орк" || skin.Title != "Разбойник" || !skin.Attackable {
		t.Errorf("скин = %+v; want name/title/attackable", skin)
	}
	if skin.RunSpd != 140 || skin.WalkSpd != 60 || skin.PAtkSpd != 253 {
		t.Errorf("скорости raw-bag = %d/%d/%d; want 140/60/253", skin.RunSpd, skin.WalkSpd, skin.PAtkSpd)
	}
	if skin.SwimRunSpd != 140 || skin.SwimWalkSpd != 60 {
		t.Errorf("swim-fallback = %d/%d; want 140/60", skin.SwimRunSpd, skin.SwimWalkSpd)
	}
	if skin.MAtkSpd != 333 || skin.MoveMultiplier != 1.0 || skin.AttackSpeedMultiplier != 1.1 {
		t.Errorf("дефолты канона = MAtkSpd %d, mult %v/%v; want 333, 1.0/1.1",
			skin.MAtkSpd, skin.MoveMultiplier, skin.AttackSpeedMultiplier)
	}
}

// B2: гео-коррекция Z точечного Monster — |Δz|<300 применяется, |Δz|=300
// (граница) — нет; не-Monster не корректируется.
func TestFoldDeployNPCsMonsterZCorrectionBoundary(t *testing.T) {
	gx, gy := geo.GeoToWorldX(32800), geo.GeoToWorldY(32800) // внутри synthGeo-региона (flat-0)
	npcs := `<?xml version="1.0" encoding="UTF-8"?>
<list>
	<npc id="20550" level="27" type="Monster" name="Орк" />
	<npc id="30080" type="Merchant" name="Торговец" />
</list>`
	spawns := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<list enabled="true">
	<spawn name="Geo">
		<npc id="20550" x="%d" y="%d" z="-200" respawnDelay="60" />
		<npc id="20550" x="%d" y="%d" z="-300" respawnDelay="60" />
		<npc id="30080" x="%d" y="%d" z="-200" respawnDelay="60" />
	</spawn>
</list>`, gx, gy, gx+100, gy, gx+200, gy)
	static := mkStatic(t, npcs, spawns)
	gm := newSynthGeo(t).build()
	st := &State{}
	var res StepResult
	deploySpawns(1, 100, st, static, transport.NPCDeployMsg{CenterX: int32(gx), CenterY: int32(gy), Radius: 1000}, gm, &res)
	z := map[int32][]int32{}
	for i := range res.Births {
		b := &res.Births[i]
		z[b.Ent.Npc.TemplateID] = append(z[b.Ent.Npc.TemplateID], b.Ent.Pos.Z)
	}
	if got := z[20550]; len(got) != 2 || got[0] != 0 || got[1] != -300 {
		t.Errorf("Z монстров = %v; want [0 -300] (коррекция |Δz|<300, граница 300 — нет)", got)
	}
	if got := z[30080]; len(got) != 1 || got[0] != -200 {
		t.Errorf("Z торговца = %v; want [-200] (не-Monster без коррекции)", got)
	}
}

// B3: территориальный спавн — ровно count позиций в полигоне, Z в диапазоне.
func TestFoldDeployNPCsTerritorialBoundsAndCount(t *testing.T) {
	st, static := &State{}, miniStatic(t)
	var res StepResult
	deploySpawns(1, 100, st, static, transport.NPCDeployMsg{CenterX: 1000, CenterY: 1000, Radius: testSliceRadius}, emptyGeo, &res)
	terr, ok := static.Territory("near_terr")
	if !ok {
		t.Fatalf("территория near_terr не загружена")
	}
	n := 0
	for i := range res.Births {
		b := &res.Births[i]
		if b.Ent.Npc == nil || b.Ent.Npc.TemplateID != 20551 {
			continue
		}
		n++
		if !terr.Contains(b.Ent.Pos.X, b.Ent.Pos.Y, b.Ent.Pos.Z) {
			t.Errorf("позиция (%d,%d,%d) вне территории/диапазона", b.Ent.Pos.X, b.Ent.Pos.Y, b.Ent.Pos.Z)
		}
		if b.Ent.Heading < 0 || b.Ent.Heading >= 65536 {
			t.Errorf("heading %d вне [0,65536)", b.Ent.Heading)
		}
	}
	if n != 3 {
		t.Errorf("рождений 20551 = %d; want 3 (count)", n)
	}
}

// B4: banned-территория исключает точки; целиком banned → 0 + skip.
func TestFoldDeployNPCsBannedExcluded(t *testing.T) {
	st, static := &State{}, miniStatic(t)
	var res StepResult
	deploySpawns(1, 100, st, static, transport.NPCDeployMsg{CenterX: 1000, CenterY: 1000, Radius: testSliceRadius}, emptyGeo, &res)
	for i := range res.Births {
		b := &res.Births[i]
		if b.Ent.Pos.X > 4900 && b.Ent.Pos.X < 7100 && b.Ent.Pos.Y > 4900 && b.Ent.Pos.Y < 7100 {
			t.Errorf("позиция (%d,%d) попала в banned-полигон", b.Ent.Pos.X, b.Ent.Pos.Y)
		}
	}
	if st.NPCSkipped == 0 {
		t.Errorf("NPCSkipped = 0; want > 0 (территория целиком в banned)")
	}
}

// B5: точечный count=2 — две сущности ровно в одной точке (стак канона).
func TestFoldDeployNPCsPointCountStack(t *testing.T) {
	st, static := &State{}, miniStatic(t)
	var res StepResult
	deploySpawns(1, 100, st, static, transport.NPCDeployMsg{CenterX: 1000, CenterY: 1000, Radius: testSliceRadius}, emptyGeo, &res)
	n := 0
	for i := range res.Births {
		b := &res.Births[i]
		if b.Ent.Npc != nil && b.Ent.Npc.TemplateID == 20550 && b.Ent.Pos.X == 500 {
			n++
			if b.Ent.Pos.Y != 500 {
				t.Errorf("стак-копия сместилась: y=%d; want 500", b.Ent.Pos.Y)
			}
		}
	}
	if n != 2 {
		t.Errorf("рождений в точке (500,500) = %d; want 2 (count)", n)
	}
}

// B6: границы фильтра среза — точка в радиусе входит, вне — нет; int32-края
// без переполнения.
func TestFoldDeployNPCsSliceFilterBoundaries(t *testing.T) {
	st, static := &State{}, miniStatic(t)
	var res StepResult
	deploySpawns(1, 100, st, static, transport.NPCDeployMsg{CenterX: 1000, CenterY: 1000, Radius: testSliceRadius}, emptyGeo, &res)
	sawOrc, sawMerchant := false, false
	for i := range res.Births {
		if res.Births[i].Ent.Npc == nil {
			continue
		}
		if res.Births[i].Ent.Npc.TemplateID == 20550 && res.Births[i].Ent.Pos.X == 1000 {
			sawOrc = true // d = 1000 ≤ 10000
		}
		if res.Births[i].Ent.Npc.TemplateID == 30080 {
			sawMerchant = true // d ≈ 40900 > 10000
		}
	}
	if !sawOrc {
		t.Errorf("спавн на d=1000 внутри радиуса не развёрнут")
	}
	if sawMerchant {
		t.Errorf("спавн на d≈40900 вне радиуса развёрнут")
	}
	// int32-края: только точечная статика — фильтр в int64, без переполнения.
	edge := int32(1 << 30)
	pointOnly := mkStatic(t,
		`<?xml version="1.0" encoding="UTF-8"?>
<list>
	<npc id="20550" level="27" type="Monster" name="Орк" />
</list>`,
		fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<list enabled="true">
	<spawn name="Edge">
		<npc id="20550" x="%d" y="%d" z="-200" respawnDelay="60" />
		<npc id="20550" x="-%d" y="%d" z="-200" respawnDelay="60" />
	</spawn>
</list>`, edge-100, edge, edge, edge))
	st2, res2 := &State{}, StepResult{}
	deploySpawns(1, 100, st2, pointOnly, transport.NPCDeployMsg{
		CenterX: edge, CenterY: edge, Radius: edge}, emptyGeo, &res2)
	if len(res2.Births) != 1 || res2.Births[0].Ent.Pos.X != edge-100 {
		t.Fatalf("крайний срез развёрнул %d (want 1 ближняя точка; дальняя отфильтрована без переполнения)",
			len(res2.Births))
	}
}

// B7: таблица пропусков — без определения, fake-id, RaidBoss, SiegeGuard,
// территория целиком в banned.
func TestFoldDeployNPCsSkipsTable(t *testing.T) {
	st, static := &State{}, miniStatic(t)
	var res StepResult
	deploySpawns(1, 100, st, static, transport.NPCDeployMsg{CenterX: 1000, CenterY: 1000, Radius: testSliceRadius}, emptyGeo, &res)
	for i := range res.Births {
		switch res.Births[i].Ent.Npc.TemplateID {
		case 80000, 29001, 29002:
			t.Errorf("неразворачиваемый шаблон %d рождён", res.Births[i].Ent.Npc.TemplateID)
		}
	}
	if got, want := st.NPCSkipped, uint64(4); got < want { // fake-id, RaidBoss, SiegeGuard, banned_terr
		t.Errorf("NPCSkipped = %d; want ≥ %d", got, want)
	}
}

// B8: heading территориальных — бросок Rnd.get(61794) из RNG разворота.
func TestFoldDeployNPCsHeadingRollAndMask(t *testing.T) {
	st, static := &State{}, miniStatic(t)
	var res StepResult
	deploySpawns(1, 100, st, static, transport.NPCDeployMsg{CenterX: 1000, CenterY: 1000, Radius: testSliceRadius}, emptyGeo, &res)
	for i := range res.Births {
		if b := &res.Births[i]; b.Ent.Npc != nil && b.Ent.Npc.TemplateID == 20551 {
			if b.Ent.Heading >= 61794 {
				t.Errorf("территориальный heading %d ≥ 61794; want бросок Rnd.get(61794)", b.Ent.Heading)
			}
		}
	}
	// Заданный датапаком heading маскируется в домен [0,65536).
	var res2 StepResult
	deploySpawns(1, 100, &State{}, static, transport.NPCDeployMsg{CenterX: 1000, CenterY: 1000, Radius: testSliceRadius}, emptyGeo, &res2)
	if len(res2.Births) > 0 && res2.Births[0].Ent.Heading < 0 {
		t.Errorf("отрицательный heading после маски")
	}
}

// B9: дефолты скоростей без raw-bag — канон, нулей в кадре нет; Merchant
// не атакуем.
func TestFoldDeployNPCsNpcSkinSpeedBlockTable(t *testing.T) {
	st, static := &State{}, miniStatic(t)
	var res StepResult
	deploySpawns(1, 100, st, static, transport.NPCDeployMsg{CenterX: 30000, CenterY: 30000, Radius: testSliceRadius}, emptyGeo, &res)
	for i := range res.Births {
		if b := &res.Births[i]; b.Ent.Npc != nil && b.Ent.Npc.TemplateID == 30080 {
			s := b.Ent.Npc
			if s.RunSpd != 120 || s.WalkSpd != 50 || s.PAtkSpd != 300 || s.MAtkSpd != 333 {
				t.Errorf("дефолты = %d/%d/%d/%d; want 120/50/300/333", s.RunSpd, s.WalkSpd, s.PAtkSpd, s.MAtkSpd)
			}
			if s.SwimRunSpd != 120 || s.SwimWalkSpd != 50 {
				t.Errorf("swim-fallback дефолтов = %d/%d; want 120/50", s.SwimRunSpd, s.SwimWalkSpd)
			}
			if s.Attackable {
				t.Errorf("Merchant attackable; want false")
			}
			return
		}
	}
	t.Fatalf("торговец вне среза не развёрнут — дефолты не проверены")
}

// B10: детерминизм — два вызова одного сида идентичны; смена тика меняет.
func TestFoldDeployNPCsDeterministicBitExact(t *testing.T) {
	run := func(tick Tick) ([]Birth, []byte) {
		st, static := &State{}, miniStatic(t)
		var res StepResult
		deploySpawns(1, tick, st, static, transport.NPCDeployMsg{CenterX: 1000, CenterY: 1000, Radius: testSliceRadius}, emptyGeo, &res)
		return res.Births, st.Dump(nil)
	}
	a1, d1 := run(100)
	a2, d2 := run(100)
	if !equalBirths(a1, a2) || !bytes.Equal(d1, d2) {
		t.Fatalf("два прогона одного сида дали разные рождения/счётчики")
	}
	a3, _ := run(101)
	if equalBirths(a1, a3) {
		t.Fatalf("смена тика не сменила броски (сид не влияет)")
	}
}

// B11: разворот не потребляет общий RNG шага — Noise следующего шага
// одинаков с разворотом и без.
func TestFoldDeployNPCsIsolatedRNG(t *testing.T) {
	static := miniStatic(t)
	noiseAfter := func(withDeploy bool) uint64 {
		st := &State{}
		var portions []Portion
		if withDeploy {
			portions = foldPortions(100, deployLetter(t, transport.NPCDeployMsg{CenterX: 1000, CenterY: 1000, Radius: 100}))
		}
		Fold(100, 1, stepRNG(1, 100), st, nil, portions, nil, testEnv(static))
		Fold(101, 1, stepRNG(1, 101), st, nil, nil, nil, testEnv(static))
		return st.Noise
	}
	if noiseAfter(true) != noiseAfter(false) {
		t.Fatalf("Noise разошёлся: разворот потребил общий RNG шага")
	}
}

// B12: валидация письма — чужой FromID, Radius≤0, битый payload, Static=nil,
// повторное письмо → dead-letter, рождения нет.
func TestFoldDeployNPCsLetterValidation(t *testing.T) {
	static := miniStatic(t)
	foreign := deployLetter(t, transport.NPCDeployMsg{CenterX: 1000, CenterY: 1000, Radius: 100})
	foreign.FromID = 42
	cases := []struct {
		name string
		env  transport.Envelope
	}{
		{"чужой FromID", foreign},
		{"radius 0", deployLetter(t, transport.NPCDeployMsg{Radius: 0})},
		{"radius <0", deployLetter(t, transport.NPCDeployMsg{Radius: -5})},
		{"битый payload", transport.Envelope{To: transport.Addr{Entity: 1}, FromID: 1,
			Kind: transport.KindDeployNPCs, Payload: []byte("{")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &State{}
			res := Fold(100, 1, stepRNG(1, 100), st, nil, foldPortions(100, tc.env), nil, testEnv(static))
			if st.DeadLetters != 1 || len(res.Births) != 0 {
				t.Errorf("DeadLetters=%d Births=%d; want 1/0", st.DeadLetters, len(res.Births))
			}
		})
	}
	// Static=nil: письмо валидно, но разворачивать нечем.
	st := &State{}
	env0 := deployLetter(t, transport.NPCDeployMsg{CenterX: 1000, CenterY: 1000, Radius: 100})
	res := Fold(100, 1, stepRNG(1, 100), st, nil, foldPortions(100, env0), nil, testEnv(nil))
	if st.DeadLetters != 1 || len(res.Births) != 0 {
		t.Errorf("Static=nil: DeadLetters=%d Births=%d; want 1/0", st.DeadLetters, len(res.Births))
	}
	// Повторное письмо после разворота — dead-letter.
	st = &State{}
	res = Fold(100, 1, stepRNG(1, 100), st, nil, foldPortions(100, env0, env0), nil, testEnv(static))
	if st.DeadLetters != 1 {
		t.Errorf("повторное письмо: DeadLetters=%d; want 1", st.DeadLetters)
	}
	if len(res.Births) == 0 {
		t.Errorf("первое письмо не развернулось")
	}
}

// B13: разворот и вход игрока одной пачкой — оба рождения, порядок писем.
func TestFoldDeployNPCsSameBatchWithEnterWorld(t *testing.T) {
	static := miniStatic(t)
	deploy := deployLetter(t, transport.NPCDeployMsg{CenterX: 1000, CenterY: 1000, Radius: 100})
	enter := transport.Envelope{
		To: transport.Addr{Entity: 1}, FromID: testRules().Gateway, Kind: transport.KindEnterWorld,
		Payload: mustLetter(t, transport.EnterWorldMsg{Conn: 7, Account: "acc",
			Char: []byte(`{"account":"acc","name":"Char","x":1000,"y":1000,"z":0,"heading":0}`)}),
	}
	st := &State{}
	res := Fold(100, 1, stepRNG(1, 100), st, nil, foldPortions(100, deploy, enter), nil, testEnv(static))
	if len(res.Births) < 2 {
		t.Fatalf("Births = %d; want ≥2 (NPC + игрок)", len(res.Births))
	}
	if res.Births[0].Ent.Npc == nil {
		t.Errorf("первое рождение — не NPC (порядок писем нарушен)")
	}
	if res.Births[len(res.Births)-1].Ent.Player == nil {
		t.Errorf("последнее рождение — не игрок")
	}
}

// B15: разворот жив, шаги без писем — NPC-сущности SoA-noop.
func TestFoldNPCStandingSoANoop(t *testing.T) {
	static := miniStatic(t)
	st := &State{}
	res := Fold(100, 1, stepRNG(1, 100), st, nil,
		foldPortions(100, deployLetter(t, transport.NPCDeployMsg{CenterX: 1000, CenterY: 1000, Radius: 100})),
		nil, testEnv(static))
	ents := make([]*Entity, 0, len(res.Births))
	for i := range res.Births {
		e := res.Births[i].Ent
		ents = append(ents, &e)
	}
	before := cloneEnts(ents)
	for n := Tick(101); n <= 110; n++ {
		r := Fold(n, 1, stepRNG(1, n), st, ents, nil, nil, testEnv(static))
		if len(r.Pushes) != 0 || len(r.Out) != 0 || len(r.Retires) != 0 {
			t.Fatalf("тик %d: NPC породил эффекты", n)
		}
	}
	for i := range ents {
		e, b := ents[i], before[i]
		if e.Player != nil {
			continue
		}
		if e.Pos != b.Pos || e.Heading != b.Heading || e.HP != b.HP || e.Moving {
			t.Errorf("NPC %d изменился: %+v → %+v", e.ID, b, e)
		}
		if e.Beat != 110 {
			t.Errorf("Beat = %d; want 110", e.Beat)
		}
	}
}

// B16: NullRegion (карта без гео) — Z территориальных клампится в диапазон.
func TestFoldDeployNPCsNullRegionSemantics(t *testing.T) {
	st, static := &State{}, miniStatic(t)
	var res StepResult
	deploySpawns(1, 100, st, static, transport.NPCDeployMsg{CenterX: 1000, CenterY: 1000, Radius: testSliceRadius}, emptyGeo, &res)
	for i := range res.Births {
		b := &res.Births[i]
		if b.Ent.Npc != nil && b.Ent.Npc.TemplateID == 20551 {
			if b.Ent.Pos.Z < -1000 || b.Ent.Pos.Z > 0 {
				t.Fatalf("Z %d вне [MinZ,MaxZ] на пустой гео", b.Ent.Pos.Z)
			}
		}
	}
}

func equalBirths(a, b []Birth) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := &a[i].Ent, &b[i].Ent
		if x.Pos != y.Pos || x.Heading != y.Heading || x.HP != y.HP {
			return false
		}
		if (x.Npc == nil) != (y.Npc == nil) {
			return false
		}
		if x.Npc != nil && *x.Npc != *y.Npc {
			return false
		}
	}
	return true
}

func cloneEnts(ents []*Entity) []Entity {
	out := make([]Entity, len(ents))
	for i, e := range ents {
		out[i] = *e
	}
	return out
}

// C1: recordOf NPC-ветки — все NPC-поля Record равны скину, Player-поля нули
// (и наоборот) — шов скин→запись фальсифицирован по полям.
func TestRecordOfNPCFields(t *testing.T) {
	skin := &NpcSkin{
		TemplateID: 20550, Name: "Орк", Title: "Разбойник", Attackable: true,
		CollisionRadius: 13, CollisionHeight: 22.5,
		RunSpd: 140, WalkSpd: 60, SwimRunSpd: 140, SwimWalkSpd: 60,
		PAtkSpd: 253, MAtkSpd: 333, MoveMultiplier: 1.0, AttackSpeedMultiplier: 1.1,
		RHand: 127, LHand: 42,
	}
	_, reg := newTestRegion(t, DefaultConfig())
	rec := reg.recordOf(&Entity{ID: 9, Owner: 1, Pos: Position{X: 1, Y: 2, Z: 3},
		Heading: 77, Npc: skin})
	want := replica.Record{
		Entity: 9, Cell: reg.grid.CellOf(1, 2), X: 1, Y: 2, Z: 3, Heading: 77,
		Kind:       replica.RecordKindNPC,
		TemplateID: skin.TemplateID, Name: skin.Name, Title: skin.Title,
		Attackable:      skin.Attackable,
		CollisionRadius: skin.CollisionRadius, CollisionHeight: skin.CollisionHeight,
		RunSpd: skin.RunSpd, WalkSpd: skin.WalkSpd,
		SwimRunSpd: skin.SwimRunSpd, SwimWalkSpd: skin.SwimWalkSpd,
		PAtkSpd: skin.PAtkSpd, MAtkSpd: skin.MAtkSpd,
		MoveMultiplier: skin.MoveMultiplier, AttackSpeedMultiplier: skin.AttackSpeedMultiplier,
		RHand: skin.RHand, LHand: skin.LHand,
	}
	if rec != want {
		t.Fatalf("recordOf(NPC) = %+v; want %+v", rec, want)
	}
	// NPC-поля нули у игрока.
	ent := playerEntForRecord()
	prec := reg.recordOf(ent)
	if prec.Kind != replica.RecordKindPlayer || prec.TemplateID != 0 || prec.Title != "" ||
		prec.Attackable || prec.RunSpd != 0 || prec.RHand != 0 {
		t.Fatalf("recordOf(игрок) несёт NPC-поля: %+v", prec)
	}
}

func playerEntForRecord() *Entity {
	return &Entity{ID: 10, Player: &Player{Rec: persistRec()}}
}

func persistRec() persist.CharRecord {
	return persist.CharRecord{Account: "a", Name: "N", ClassID: 0, Race: 0,
		Level: 1, HP: 80, MP: 30, X: 1, Y: 2, Z: 3}
}

// Пустой срез — валидное письмо без спавнов в радиусе: не dead-letter,
// разворот исполнен, регион жив (корнер «0»).
func TestFoldDeployNPCsEmptySlice(t *testing.T) {
	static := miniStatic(t)
	st := &State{}
	res := Fold(100, 1, stepRNG(1, 100), st, nil,
		foldPortions(100, deployLetter(t, transport.NPCDeployMsg{CenterX: 200000, CenterY: 200000, Radius: 100})),
		nil, testEnv(static))
	if st.DeadLetters != 0 || len(res.Births) != 0 {
		t.Fatalf("пустой срез: DeadLetters=%d Births=%d; want 0/0", st.DeadLetters, len(res.Births))
	}
	if !st.NPCDeployedOnce {
		t.Fatalf("разворот не отмечен исполненным")
	}
}

// B6-точно: d==Radius входит, Radius+1 — нет; полигон в углу квадрата за
// кругом разворачивается (консервативность bbox-фильтра).
func TestFoldDeployNPCsSliceFilterExactBoundary(t *testing.T) {
	const npcOnly = `<?xml version="1.0" encoding="UTF-8"?>
<list>
	<npc id="20550" level="27" type="Monster" name="Орк" />
</list>`
	spawns := `<?xml version="1.0" encoding="UTF-8"?>
<list enabled="true">
	<spawn name="Edge">
		<npc id="20550" x="11000" y="1000" z="-300" respawnDelay="60" />
		<npc id="20550" x="11001" y="1000" z="-300" respawnDelay="60" />
	</spawn>
	<spawn zone="corner">
		<territory minZ="-1000" maxZ="0">
			<node x="10800" y="10800" />
			<node x="11000" y="10800" />
			<node x="11000" y="11000" />
		</territory>
		<npc id="20550" count="1" respawnDelay="22" />
	</spawn>
</list>`
	static := mkStatic(t, npcOnly, spawns)
	st, res := &State{}, StepResult{}
	deploySpawns(1, 100, st, static, transport.NPCDeployMsg{CenterX: 1000, CenterY: 1000, Radius: 10000}, emptyGeo, &res)
	point, corner := 0, 0
	for i := range res.Births {
		p := res.Births[i].Ent.Pos
		switch {
		case p.X == 11000 && p.Y == 1000:
			point++ // d=10000 == Radius: входит
		case p.X == 11001 && p.Y == 1000:
			t.Errorf("точка d=Radius+1 развёрнута (фильтр нестрогий)")
		case p.X >= 10800 && p.Y >= 10800:
			corner++ // полигон в квадрате, но вне круга
		}
	}
	if point != 1 {
		t.Errorf("точка на границе радиуса: %d; want 1", point)
	}
	if corner == 0 {
		t.Errorf("полигон за кругом (в квадрате) не развёрнут — консервативность потеряна")
	}
}

// B8-точно: заданные heading за доменом маскируются — 70000→4464, 65535→65535.
func TestFoldDeployNPCsHeadingMaskValues(t *testing.T) {
	npcs := `<?xml version="1.0" encoding="UTF-8"?>
<list>
	<npc id="20550" level="27" type="Monster" name="Орк" />
</list>`
	spawns := `<?xml version="1.0" encoding="UTF-8"?>
<list enabled="true">
	<spawn name="Heads">
		<npc id="20550" x="100" y="100" z="0" heading="70000" respawnDelay="60" />
		<npc id="20550" x="200" y="100" z="0" heading="65535" respawnDelay="60" />
	</spawn>
</list>`
	static := mkStatic(t, npcs, spawns)
	st, res := &State{}, StepResult{}
	deploySpawns(1, 100, st, static, transport.NPCDeployMsg{CenterX: 0, CenterY: 0, Radius: 1000}, emptyGeo, &res)
	heads := map[int32]int32{}
	for i := range res.Births {
		heads[res.Births[i].Ent.Pos.X] = res.Births[i].Ent.Heading
	}
	if got := heads[100]; got != 70000&0xFFFF {
		t.Errorf("heading 70000 → %d; want %d", got, 70000&0xFFFF)
	}
	if got := heads[200]; got != 65535 {
		t.Errorf("heading 65535 → %d; want 65535", got)
	}
}

// B4-доп: banned-исключение 3D — banned-полигон с узким Z-диапазоном режет
// только точки его высоты (2D-фильтр прошёл бы всё или ничего).
func TestFoldDeployNPCsBannedZRange(t *testing.T) {
	npcs := `<?xml version="1.0" encoding="UTF-8"?>
<list>
	<npc id="20550" level="27" type="Monster" name="Орк" />
</list>`
	spawns := `<?xml version="1.0" encoding="UTF-8"?>
<list enabled="true">
	<spawn zone="zr">
		<territory minZ="-1000" maxZ="0">
			<node x="0" y="0" />
			<node x="2000" y="0" />
			<node x="2000" y="2000" />
		</territory>
		<banned_territory minZ="-100" maxZ="100">
			<node x="0" y="0" />
			<node x="2000" y="0" />
			<node x="2000" y="1000" />
			<node x="0" y="1000" />
		</banned_territory>
		<npc id="20550" count="16" respawnDelay="22" />
	</spawn>
</list>`
	static := mkStatic(t, npcs, spawns)
	st, res := &State{}, StepResult{}
	deploySpawns(1, 100, st, static, transport.NPCDeployMsg{CenterX: 1000, CenterY: 1000, Radius: 10000}, emptyGeo, &res)
	// Пустая гео: гео-Z = midZ = -500 — вне banned-полосы [-100,100]: весь
	// banned-полигон прозрачен по Z, рождения живут ВНУТРИ его 2D-площади —
	// 2D-деградация фильтра (Contains(x,y,0)) выжила бы точки только
	// смещённым перебросом за пределы banned-полигона (y>1000).
	if len(res.Births) != 16 {
		t.Fatalf("births=%d; want 16 (banned с чужим Z-диапазоном не режет)", len(res.Births))
	}
	insideBanned2D := 0
	for i := range res.Births {
		p := res.Births[i].Ent.Pos
		if p.Z < -1000 || p.Z > 0 {
			t.Fatalf("Z %d вне территории", p.Z)
		}
		if p.X >= 0 && p.X <= 2000 && p.Y >= 0 && p.Y <= 1000 {
			insideBanned2D++
		}
	}
	if insideBanned2D == 0 {
		t.Fatalf("ни одного рождения в 2D-площади banned — 3D-семантика не фальсифицирована (переброс выместил точки)")
	}
}
