package protocol

import "testing"

// Путь реестра ADR-0005 «пакет-писатель»: 40-байтовый пакет на стеке
// вызывающего, 0 аллокаций.
func BenchmarkWriteAttack(b *testing.B) {
	var dst [AttackSize]byte
	hit := AttackHit{TargetID: 200, Damage: 1500, Flags: HitFlagCrit}
	b.SetBytes(AttackSize)
	b.ReportAllocs()
	for b.Loop() {
		_ = WriteAttack(dst[:], 100, hit, 10, 20, 30, 40, 50, 60)
	}
}

// Геттеры представления над буфером (нулевая копия).
func BenchmarkAttackView(b *testing.B) {
	src := attackViewBytes()
	v, ok := NewAttackView(src)
	if !ok {
		b.Fatal("NewAttackView")
	}
	b.SetBytes(AttackSize)
	b.ReportAllocs()
	for b.Loop() {
		_ = v.AttackerID() + v.TargetID() + v.Damage()
		_ = v.AttackX() + v.AttackY() + v.AttackZ()
		_ = v.ExtraHits()
	}
}

func BenchmarkWriteReadD(b *testing.B) {
	var buf [4]byte
	b.SetBytes(4)
	b.ReportAllocs()
	for b.Loop() {
		WriteD(buf[:], 123456)
		_, _ = ReadD(buf[:], 0)
	}
}

func BenchmarkWriteReadSShort(b *testing.B) {
	s := "ИмяПерсонажа" // 12 рун, 12 юнитов + терминатор
	buf := make([]byte, LenS(s))
	b.SetBytes(int64(len(buf)))
	b.ReportAllocs()
	for b.Loop() {
		WriteS(buf, s)
		_, _, _ = ReadS(buf, 0)
	}
}

func BenchmarkWriteReadSLong(b *testing.B) {
	s := "ИмяПерсонажа с длинным титулом и клановой приставкой — строка реального пакета"
	buf := make([]byte, LenS(s))
	b.SetBytes(int64(len(buf)))
	b.ReportAllocs()
	for b.Loop() {
		WriteS(buf, s)
		_, _, _ = ReadS(buf, 0)
	}
}

func BenchmarkLenS(b *testing.B) {
	s := "ИмяПерсонажа"
	b.ReportAllocs()
	for b.Loop() {
		_ = LenS(s)
	}
}
