package data

import "crypto/sha256"

// Entry — запись отчёта об ошибке загрузки данных.
type Entry struct {
	Category string
	File     string
	Line     int
	ID       ItemID
	Code     string
	Message  string
}

// Коды записей отчёта.
const (
	CodeXML    = "xml"    // малформленный или обрезанный XML
	CodeRoot   = "root"   // чужой корневой элемент
	CodeDupID  = "dup_id" // дубликат ID предмета
	CodeAttr   = "attr"   // отсутствует обязательный атрибут item
	CodeNumber = "number" // число вне домена или неразборчивое значение поля
	CodeLimit  = "limit"  // файл превышает потолок размера
)

// Report — итог загрузки: счётчики, перечень ошибок и манифест входов
// (SHA-256 по отсортированному составу фактически прочитанных файлов).
type Report struct {
	Files    int
	Items    int
	Errors   []Entry
	Manifest [sha256.Size]byte

	UnknownKeys      map[string]int
	UnknownTypes     map[string]int
	SkippedElements  map[string]int
	EmptyValues      int
	DupKeys          int
	UnnamedSets      int
	SkippedCustomDir int
}

// HasErrors сообщает, есть ли в отчёте ошибки целостности.
func (r *Report) HasErrors() bool {
	return len(r.Errors) > 0
}
