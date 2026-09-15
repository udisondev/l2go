package protocol

import "testing"

// Бенчмарки стационарной фазы (P3.5). Кластер движения — пер-тиковый путь
// репликации (каждый тик всем наблюдателям, P3.9); большие писатели —
// событие join-AoI (P3.8); кейсы Short/Long покрывают вариативность S-полей
// (реалистичные строки против длинного титула). Baseline — методика
// benchmarks/P3.5-protocol-baseline.txt.

// Кластер движения: CharMoveToLocation пишется каждый тик по каждому
// движущемуся объекту.
func BenchmarkWriteCharMoveToLocation(b *testing.B) {
	var dst [CharMoveToLocationSize]byte
	b.SetBytes(CharMoveToLocationSize)
	b.ReportAllocs()
	for b.Loop() {
		_ = WriteCharMoveToLocation(dst[:], 100500, 1000, 2000, 3000, 10, 20, 30)
	}
}

func BenchmarkWriteStopMove(b *testing.B) {
	var dst [StopMoveSize]byte
	b.SetBytes(StopMoveSize)
	b.ReportAllocs()
	for b.Loop() {
		_ = WriteStopMove(dst[:], 100500, 1000, 2000, 3000, 64)
	}
}

// Входной разбор: конструктор + все геттеры MoveToLocationView — кадр,
// который регион дренит с каждого клиента под лавиной (коалесинг P3.6).
func BenchmarkMoveToLocationView(b *testing.B) {
	src := []byte{byte(moveToLocation)}
	src = append(src, 0x64, 0, 0, 0, 0xC8, 0, 0, 0, 0x2C, 1, 0, 0,
		0x0A, 0, 0, 0, 0x14, 0, 0, 0, 0x1E, 0, 0, 0, 1, 0, 0, 0)
	b.SetBytes(MoveToLocationSize)
	b.ReportAllocs()
	for b.Loop() {
		v, ok := NewMoveToLocationView(src)
		if ok {
			_ = v.TargetX() + v.TargetY() + v.TargetZ()
			_ = v.OriginX() + v.OriginY() + v.OriginZ()
			_ = v.MovementMode()
		}
	}
}

// Большие писатели (join-AoI): типовой новичок Human Fighter с реалистичными
// именем и пустым титулом.
func benchWriteCharInfo(b *testing.B, d CharInfoData) {
	dst := make([]byte, CharInfoSize(d))
	b.SetBytes(int64(len(dst)))
	b.ReportAllocs()
	for b.Loop() {
		_ = WriteCharInfo(dst, d)
	}
}

func BenchmarkWriteCharInfoShort(b *testing.B) {
	d := tCharInfo
	d.Title = "Нуб" // реалистичный короткий титул (план: Short — реалистичные имя/титул)
	benchWriteCharInfo(b, d)
}

func BenchmarkWriteCharInfoLong(b *testing.B) {
	d := tCharInfo
	d.Title = "ИмяПерсонажа с длинным титулом и клановой приставкой — строка реального пакета"
	benchWriteCharInfo(b, d)
}

func benchWriteUserInfo(b *testing.B, d UserInfoData) {
	dst := make([]byte, UserInfoSize(d))
	b.SetBytes(int64(len(dst)))
	b.ReportAllocs()
	for b.Loop() {
		_ = WriteUserInfo(dst, d)
	}
}

func BenchmarkWriteUserInfoShort(b *testing.B) {
	d := tUserInfo
	d.Title = "Нуб"
	benchWriteUserInfo(b, d)
}

func BenchmarkWriteUserInfoLong(b *testing.B) {
	d := tUserInfo
	d.Title = "ИмяПерсонажа с длинным титулом и клановой приставкой — строка реального пакета"
	benchWriteUserInfo(b, d)
}

func benchWriteNpcInfo(b *testing.B, d NpcInfoData) {
	dst := make([]byte, NpcInfoSize(d))
	b.SetBytes(int64(len(dst)))
	b.ReportAllocs()
	for b.Loop() {
		_ = WriteNpcInfo(dst, d)
	}
}

func BenchmarkWriteNpcInfoShort(b *testing.B) { benchWriteNpcInfo(b, tNpcInfo) }

func BenchmarkWriteNpcInfoLong(b *testing.B) {
	d := tNpcInfo
	d.Title = "ИмяПерсонажа с длинным титулом и клановой приставкой — строка реального пакета"
	benchWriteNpcInfo(b, d)
}

// Клеймо 0 аллокаций фиксированных писателей стационарной фазы (машина,
// прецедент TestWriteAttackZeroAllocs; бенчмарк воротами не является).
func TestWorldWritersZeroAllocs(t *testing.T) {
	var buf [CharMoveToLocationSize]byte
	allocs := testing.AllocsPerRun(100, func() {
		_ = WriteCharMoveToLocation(buf[:], 1, 2, 3, 4, 5, 6, 7)
	})
	if allocs != 0 {
		t.Errorf("WriteCharMoveToLocation: %v аллокаций; want 0", allocs)
	}
	var stop [StopMoveSize]byte
	allocs = testing.AllocsPerRun(100, func() { _ = WriteStopMove(stop[:], 1, 2, 3, 4, 5) })
	if allocs != 0 {
		t.Errorf("WriteStopMove: %v аллокаций; want 0", allocs)
	}
	var tp [TeleportToLocationSize]byte
	allocs = testing.AllocsPerRun(100, func() { _ = WriteTeleportToLocation(tp[:], 1, 2, 3, 4, 5) })
	if allocs != 0 {
		t.Errorf("WriteTeleportToLocation: %v аллокаций; want 0", allocs)
	}
	var vl [ValidateLocationSize]byte
	allocs = testing.AllocsPerRun(100, func() { _ = WriteValidateLocation(vl[:], 1, 2, 3, 4, 5) })
	if allocs != 0 {
		t.Errorf("WriteValidateLocation: %v аллокаций; want 0", allocs)
	}
	var del [DeleteObjectSize]byte
	allocs = testing.AllocsPerRun(100, func() { _ = WriteDeleteObject(del[:], 1) })
	if allocs != 0 {
		t.Errorf("WriteDeleteObject: %v аллокаций; want 0", allocs)
	}
	var ml [MoveToLocationSize]byte
	allocs = testing.AllocsPerRun(100, func() { _ = WriteMoveToLocation(ml[:], 1, 2, 3, 4, 5, 6, 7) })
	if allocs != 0 {
		t.Errorf("WriteMoveToLocation: %v аллокаций; want 0", allocs)
	}
	var vp [ValidatePositionSize]byte
	allocs = testing.AllocsPerRun(100, func() { _ = WriteValidatePosition(vp[:], 1, 2, 3, 4, 5) })
	if allocs != 0 {
		t.Errorf("WriteValidatePosition: %v аллокаций; want 0", allocs)
	}
	var cm [CannotMoveAnymoreSize]byte
	allocs = testing.AllocsPerRun(100, func() { _ = WriteCannotMoveAnymore(cm[:], 1, 2, 3, 4) })
	if allocs != 0 {
		t.Errorf("WriteCannotMoveAnymore: %v аллокаций; want 0", allocs)
	}
	var ew [EnterWorldSize]byte
	allocs = testing.AllocsPerRun(100, func() { _ = WriteEnterWorld(ew[:]) })
	if allocs != 0 {
		t.Errorf("WriteEnterWorld: %v аллокаций; want 0", allocs)
	}
	var empty [EmptyItemListSize]byte
	allocs = testing.AllocsPerRun(100, func() {
		_ = WriteEmptyItemList(empty[:])
		_ = WriteEmptySkillList(empty[:])
		_ = WriteEmptyShortCutInit(empty[:])
		_ = WriteActionFailed(empty[:1])
	})
	if allocs != 0 {
		t.Errorf("пустые списки/ActionFailed: %v аллокаций; want 0", allocs)
	}
	say := make([]byte, Say2Size("привет", ChatGeneral, ""))
	allocs = testing.AllocsPerRun(100, func() { _ = WriteSay2(say, "привет", ChatGeneral, "") })
	if allocs != 0 {
		t.Errorf("WriteSay2: %v аллокаций; want 0", allocs)
	}
	cs := make([]byte, CreatureSaySize("Vasya", "Hello"))
	allocs = testing.AllocsPerRun(100, func() { _ = WriteCreatureSay(cs, 1, ChatGeneral, "Vasya", "Hello") })
	if allocs != 0 {
		t.Errorf("WriteCreatureSay: %v аллокаций; want 0", allocs)
	}
	tpl := make([]byte, CharTemplatesSize(1))
	oneTemplate := []CharTemplate{{Race: 0, ClassID: 0, Str: 40, Dex: 30, Con: 43, Int: 21, Wit: 11, Men: 25}}
	allocs = testing.AllocsPerRun(100, func() { _ = WriteCharTemplates(tpl[:], oneTemplate) })
	if allocs != 0 {
		t.Errorf("WriteCharTemplates: %v аллокаций; want 0", allocs)
	}
	var resp [CharCreateOkSize]byte
	allocs = testing.AllocsPerRun(100, func() {
		_ = WriteCharCreateOk(resp[:])
		_ = WriteCharCreateFail(resp[:], CharCreateReasonCreationFailed)
		_ = WriteCharDeleteFail(resp[:], CharDeleteReasonDeletionFailed)
	})
	if allocs != 0 {
		t.Errorf("ответы создания/удаления: %v аллокаций; want 0", allocs)
	}
	cc := CharacterCreateData{Name: "Newbie", Int: 21, Str: 40, Con: 43, Men: 25, Dex: 30, Wit: 11}
	ccBuf := make([]byte, CharacterCreateSize(cc))
	allocs = testing.AllocsPerRun(100, func() {
		_ = WriteCharacterCreate(ccBuf, cc)
		_ = WriteCharacterDelete(resp[:], 3)
		_ = WriteNewCharacter(resp[:1])
	})
	if allocs != 0 {
		t.Errorf("WriteCharacterCreate/Delete/NewCharacter: %v аллокаций; want 0", allocs)
	}
	var sm [SystemMessageSize]byte
	allocs = testing.AllocsPerRun(100, func() {
		_ = WriteSystemMessage(sm[:], SystemMessageWelcomeToTheWorldOfLineageII)
	})
	if allocs != 0 {
		t.Errorf("WriteSystemMessage: %v аллокаций; want 0", allocs)
	}
}
