// Package artifact — компилированный артефакт статики: сборка (парсинг исходников
// уже выполнен владельцами категорий), контейнер с манифестами входов и два
// транспорта загрузки без копирования в кучу (mmap внешнего файла и встроенные
// байты за build-тегом embedded).
package artifact

import (
	"errors"
	"time"

	"github.com/udisondev/l2go/internal/data"
	"github.com/udisondev/l2go/internal/geo"
)

// Version — версия формата контейнера; несовпадение = артефакт устарел,
// пересобирается командой build (миграций нет как класса).
const Version = 1

// Коды ошибок декодирования контейнера.
const (
	CodeMagic    = "magic"
	CodeVersion  = "version"
	CodeChecksum = "checksum"
	CodeTrunc    = "trunc"
	CodeTail     = "tail"
	CodeRange    = "range"
	CodeDup      = "dup"
	CodeMeta     = "meta"
	CodeDecode   = "decode"
)

// DecodeError — ошибка декодирования с кодом и офсетом.
type DecodeError struct {
	Code   string
	Offset int
	Msg    string
}

func (e *DecodeError) Error() string {
	return "artifact: " + e.Code + " @" + itoa(e.Offset) + ": " + e.Msg
}

func itoa(n int) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// Meta — манифесты входов и счётчики сборки.
type Meta struct {
	Files, Items, Npcs, Spawns, Territories, Zones uint32
	Skills, SkillLevels, DropItems, Regions        uint32
	DataLen, GeoLen                                uint64
	DataManifest, GeoManifest                      [32]byte
}

// Phases — времена фаз загрузки (для отчёта команды load).
type Phases struct {
	Verify     time.Duration
	DecodeData time.Duration
	DecodeGeo  time.Duration
}

// BuildResult — итог сборки артефакта.
type BuildResult struct {
	Meta   Meta
	Fresh  bool // файл переписан; false — «актуален», байт-идентичен существующему
	Encode time.Duration
	Write  time.Duration
}

// Build кодирует статику и атомарно записывает артефакт: сравнение с
// существующим файлом (идентичен — без записи), os.CreateTemp в каталоге
// назначения + rename. Красная валидация исходников — обязанность вызывающего
// (артефакт не трогается).
func Build(outPath string, st *data.Static, drep *data.Report, m *geo.Map, grep *geo.Report) (*BuildResult, error) {
	return nil, errors.New("artifact: Build не реализован")
}

// Decode проверяет заголовок (магия, версия, длины вычитанием), контрольную
// сумму (SHA-256 заголовка до поля суммы + payload) и декодирует категории и
// гео; байты гео остаются поверх переданного слайса.
func Decode(b []byte) (*data.Static, *geo.Map, *Meta, error) {
	return nil, nil, nil, errors.New("artifact: Decode не реализован")
}

// LoadFile отображает файл артефакта (владение отображением — до конца
// процесса) и декодирует его.
func LoadFile(path string) (*data.Static, *geo.Map, *Meta, Phases, error) {
	return nil, nil, nil, Phases{}, errors.New("artifact: LoadFile не реализован")
}

// parseHeader проверяет магию, версию и длины (вычитанием) и возвращает
// манифесты и длины секций из заголовка.
func parseHeader(b []byte) (*Meta, error) {
	return nil, errors.New("artifact: parseHeader не реализован")
}

// verifyChecksum сверяет SHA-256 заголовка (до поля суммы) + payload.
func verifyChecksum(b []byte) bool { return false }

// decodeGeoRegions разбирает индекс geo-секции и декодирует регионы.
func decodeGeoRegions(geoSec []byte) ([]*geo.Region, error) {
	return nil, errors.New("artifact: decodeGeoRegions не реализован")
}
