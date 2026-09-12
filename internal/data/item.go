package data

// ItemID — идентификатор предмета датапака.
type ItemID int32

// Item — предмет датапака. Типизированные поля покрывают минимального
// потребителя игровых фаз (инвентарь, дроп, подбор); значения отсутствующих
// полей берутся из дефолтов датапака. Прочие параметры хранятся в исходном
// виде и доступны через Set. Записи не меняются после загрузки.
// Семантика разбора и дефолты портированы с L2J_Mobius (DocumentItem,
// DocumentBase.parseBeanSet, ItemTemplate.set), GPLv3.
type Item struct {
	ID           ItemID
	Name         string
	Type         string
	Weight       int64
	Price        int64
	Stackable    bool
	CrystalType  string
	CrystalCount int64
	Material     string
	BodyPart     string

	set map[string]string
}

// Set возвращает исходное значение параметра предмета по ключу. Ключами
// служат имена set-элементов датапака как есть и статов блока stats с
// префиксом "stat." (например "stat.pAtk").
func (it Item) Set(key string) (string, bool) {
	v, ok := it.set[key]
	return v, ok
}
