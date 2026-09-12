package data

import (
	"strings"
	"testing"
)

// TestDumpGoldenNpcSpawn фиксирует форму канонического дампа новых категорий
// (NPC с дроплистами и миньонами, территории, спавны): смена формата —
// сознательная правка эталона, тихий дрейф исключён. Сверка эквивалентности
// артефакта P2.7 опирается на этот канонический вид.
func TestDumpGoldenNpcSpawn(t *testing.T) {
	st, _ := loadSynth(t)
	dump := st.Dump()
	start := strings.Index(dump, "npc id=")
	if start < 0 {
		t.Fatalf("дамп без секции NPC:\n%s", dump)
	}
	got := dump[start:]
	want := `npc id=20550 name="Учебный орк" title="Разбойник" level=27 type="Monster" race="HUMANOID" aggro=500 clanHelp=300 aggressive=true collision=13/22.5 clans=[ORC GUARD] ignore=[20551] minions=[20551:2/4/120/1] drops=[drop{g70[9001 1-1@25 9003 2-4@50]g0.5[9002 1-1@100] i[9005 1-2@8.5]} spoil{i[9004 1-1@100]}] sets=[acquire.exp=500 acquire.sp=40 ai.aggroRange=500 ai.clanHelpRange=300 ai.isAggressive=true ai.type=AGGRESSIVE collision.height.grown=25 collision.height.normal=22.5 collision.radius.grown=15 collision.radius.normal=13 corpseTime=86400 equipment.rhand=127 exCrtEffect=false minions=Privates npc.level=27 npc.name=Учебный орк npc.title=Разбойник npc.type=Monster parameters.MoveAroundSocial=80 parameters.MoveAroundTeritory=0 sex=MALE shots.soul=2 shots.spirit=2 stats.attack.accuracy=4.75 stats.attack.attackSpeed=253 stats.attack.critical=4 stats.attack.distance=80 stats.attack.magical=55.6 stats.attack.physical=123.4 stats.attack.random=30 stats.attack.range=40 stats.attack.type=SWORD stats.attack.width=120 stats.con=43 stats.defence.magical=108.5 stats.defence.physical=148.7 stats.dex=30 stats.hitTime=390 stats.int=21 stats.men=10 stats.speed.run.ground=140 stats.speed.walk.ground=60 stats.str=40 stats.vitals.hp=613.5 stats.vitals.hpRegen=3.5 stats.vitals.mp=200.1 stats.vitals.mpRegen=1.5 stats.wit=20]
npc id=20551 name="Орк-приспешник" title="" level=25 type="Monster" race="ORC" aggro=0 clanHelp=0 aggressive=false collision=10/18 clans=[] ignore=[] minions=[] drops=[drop{g100[9003 56398-11510@100]}] sets=[ai.aggroRange=0 ai.clanHelpRange=0 ai.isAggressive=false collision.height.normal=18 collision.radius.normal=10 npc.level=25 npc.name=Орк-приспешник npc.type=Monster sex=MALE stats.str=40 stats.vitals.hp=300.2 stats.vitals.hpRegen=3 stats.vitals.mp=100 stats.vitals.mpRegen=1]
npc id=29019 name="Учебный босс" title="" level=79 type="GrandBoss" race="DRAGON" aggro=0 clanHelp=0 aggressive=false collision=100/200 clans=[] ignore=[] minions=[] drops=[drop{g100[9001 12-29@200]} spoil{i[9003 1-1@148.76]}] sets=[collision.height.normal=200 collision.radius.normal=100 npc.level=79 npc.name=Учебный босс npc.type=GrandBoss]
npc id=30080 name="Торговец без уровня" title="" level=85 type="Merchant" race="" aggro=0 clanHelp=0 aggressive=false collision=0/0 clans=[] ignore=[] minions=[] drops=[] sets=[npc.name=Торговец без уровня npc.type=Merchant]
npc id=30081 name="" title="" level=40 type="CustomThing" race="" aggro=0 clanHelp=0 aggressive=false collision=0/0 clans=[] ignore=[] minions=[] drops=[] sets=[npc.level=40 npc.type=CustomThing]
terr name="synth_both" minZ=-100 maxZ=100 nodes=[1,1 2,2 3,3] banned=[{-50,50 [1,1 2,2]}] sets=[]
terr name="synth_territory" minZ=-3800 maxZ=-3400 nodes=[70780,125060 71852,124640 72660,125432] banned=[] sets=[]
spawn npc=30080 point=147456,22576,-1989,16384 terr="" count=1 respawn=60 sets=[]
spawn npc=20550 point=100,200,-300,-1 terr="" count=1 respawn=0 sets=[]
spawn npc=20551 point=- terr="synth_territory" count=3 respawn=22 sets=[]
spawn npc=20550 point=50,60,0,0 terr="synth_both" count=2 respawn=30 sets=[chaseRange=2000 respawnRandom=5]
spawn npc=29019 point=10,20,30,5 terr="synth_both" count=1 respawn=1 sets=[periodOfDay=day]
spawn npc=80000 point=83485,147998,-3407,23509 terr="" count=1 respawn=60 sets=[]
`
	if got != want {
		t.Errorf("секция NPC/территорий/спавнов отлична от эталона:\n---got---\n%s---want---\n%s", got, want)
	}
}
