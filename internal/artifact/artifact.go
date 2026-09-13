// Package artifact — компилированный артефакт статики: контейнер с манифестами
// входов и счётчиками сборки, гео-секция из исходных байтов регионов. Сборка —
// атомарной записью с сравнением (идентичный артефакт не переписывается);
// загрузка — без копирования в кучу: внешний файл отображается mmap, embed-байты
// живут в rodata (за build-тегом embedded). Гео-лукапы поверх байтов артефакта
// наследуют представление и 0 аллокаций пути загрузки каталога.
package artifact

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
	CodeDecode   = "decode"
)

// Раскладка файла: заголовок, затем payload (meta, data-секция, geo-секция).
const (
	magicStr  = "L2A\x01"
	hdrSize   = 120
	hdrSumOff = 88 // поле payloadSHA — последнее в заголовке
	metaSize  = 40 // 10 счётчиков u32
	geoRecLen = 24 // запись индекса региона: rx u32, ry u32, off u64, len u64
)

// DecodeError — ошибка декодирования с кодом и офсетом.
type DecodeError struct {
	Code   string
	Offset int
	Msg    string
}

func (e *DecodeError) Error() string {
	return "artifact: " + e.Code + " @" + strconv.Itoa(e.Offset) + ": " + e.Msg
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
// назначения + rename (в том числе поверх живого отображения). Красная
// валидация исходников — обязанность вызывающего: Build получает уже
// проверенные данные и отчёты.
func Build(outPath string, st *data.Static, drep *data.Report, m *geo.Map, grep *geo.Report) (*BuildResult, error) {
	if st == nil || drep == nil {
		return nil, fmt.Errorf("artifact: Build требует статику и отчёт датапака")
	}
	t0 := time.Now()
	buf, meta := encodeArtifact(st, drep, m, grep)
	encodeDur := time.Since(t0)

	if existing, err := os.ReadFile(outPath); err == nil && bytes.Equal(existing, buf) {
		return &BuildResult{Meta: meta, Fresh: false, Encode: encodeDur}, nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(outPath), ".artifact-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("artifact: временный файл рядом с %s (путь по умолчанию относителен корню модуля; есть флаг -o): %w", outPath, err)
	}
	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, fmt.Errorf("artifact: запись %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return nil, fmt.Errorf("artifact: закрытие %s: %w", tmp.Name(), err)
	}
	writeDur := time.Since(t0) - encodeDur
	if err := os.Rename(tmp.Name(), outPath); err != nil {
		os.Remove(tmp.Name())
		return nil, fmt.Errorf("artifact: замена %s: %w", outPath, err)
	}
	return &BuildResult{Meta: meta, Fresh: true, Encode: encodeDur, Write: writeDur}, nil
}

// encodeArtifact собирает файл артефакта: заголовок (магия, версия, манифесты,
// длины секций, SHA-256 заголовка без поля суммы + payload), meta, data-секция,
// geo-секция (индекс + исходные байты регионов). Детерминирован байт-в-байт.
func encodeArtifact(st *data.Static, drep *data.Report, m *geo.Map, grep *geo.Report) ([]byte, Meta) {
	dataSec := data.EncodeStatic(st)

	var regions []struct {
		rx, ry int
		raw    []byte
	}
	if m != nil {
		m.EachRegion(func(rx, ry int, raw []byte) bool {
			regions = append(regions, struct {
				rx, ry int
				raw    []byte
			}{rx, ry, raw})
			return true
		})
	}
	geoSec := make([]byte, 0, 4+geoRecLen*len(regions))
	geoSec = binary.LittleEndian.AppendUint32(geoSec, uint32(len(regions)))
	// Офсеты — от конца индекса (байты регионов начинаются сразу за ним).
	off := uint64(0)
	for _, r := range regions {
		geoSec = binary.LittleEndian.AppendUint32(geoSec, uint32(r.rx))
		geoSec = binary.LittleEndian.AppendUint32(geoSec, uint32(r.ry))
		geoSec = binary.LittleEndian.AppendUint64(geoSec, off)
		geoSec = binary.LittleEndian.AppendUint64(geoSec, uint64(len(r.raw)))
		off += uint64(len(r.raw))
	}
	for _, r := range regions {
		geoSec = append(geoSec, r.raw...)
	}

	meta := Meta{
		Files:        uint32(drep.Files),
		Items:        uint32(drep.Items),
		Npcs:         uint32(drep.Npcs),
		Spawns:       uint32(drep.Spawns),
		Territories:  uint32(drep.Territories),
		Zones:        uint32(drep.Zones),
		Skills:       uint32(drep.Skills),
		SkillLevels:  uint32(drep.SkillLevels),
		DropItems:    uint32(drep.DropItems),
		Regions:      0,
		DataLen:      uint64(len(dataSec)),
		GeoLen:       uint64(len(geoSec)),
		DataManifest: drep.Manifest,
	}
	if grep != nil {
		meta.Regions = uint32(grep.Regions)
		meta.GeoManifest = grep.Manifest
	}

	buf := make([]byte, 0, hdrSize+metaSize+len(dataSec)+len(geoSec))
	buf = append(buf, magicStr...)
	buf = binary.LittleEndian.AppendUint32(buf, Version)
	buf = append(buf, meta.DataManifest[:]...)
	buf = append(buf, meta.GeoManifest[:]...)
	buf = binary.LittleEndian.AppendUint64(buf, meta.DataLen)
	buf = binary.LittleEndian.AppendUint64(buf, meta.GeoLen)
	buf = append(buf, make([]byte, sha256.Size)...) // место payloadSHA

	buf = binary.LittleEndian.AppendUint32(buf, meta.Files)
	buf = binary.LittleEndian.AppendUint32(buf, meta.Items)
	buf = binary.LittleEndian.AppendUint32(buf, meta.Npcs)
	buf = binary.LittleEndian.AppendUint32(buf, meta.Spawns)
	buf = binary.LittleEndian.AppendUint32(buf, meta.Territories)
	buf = binary.LittleEndian.AppendUint32(buf, meta.Zones)
	buf = binary.LittleEndian.AppendUint32(buf, meta.Skills)
	buf = binary.LittleEndian.AppendUint32(buf, meta.SkillLevels)
	buf = binary.LittleEndian.AppendUint32(buf, meta.DropItems)
	buf = binary.LittleEndian.AppendUint32(buf, meta.Regions)
	buf = append(buf, dataSec...)
	buf = append(buf, geoSec...)

	sum := sha256.New()
	sum.Write(buf[:hdrSumOff])
	sum.Write(buf[hdrSize:])
	copy(buf[hdrSumOff:hdrSize], sum.Sum(nil))
	return buf, meta
}

// Decode проверяет заголовок (магия, версия, длины — вычитанием, без
// суммирования), контрольную сумму (SHA-256 заголовка до поля суммы + payload:
// порча любого байта файла детектируется) и декодирует категории и гео;
// байты гео остаются поверх переданного слайса. Сверка: счётчики секции
// статики против meta, число регионов против индекса и meta.
func Decode(b []byte) (*data.Static, *geo.Map, *Meta, error) {
	meta, dataSec, geoSec, err := split(b)
	if err != nil {
		return nil, nil, nil, err
	}
	st, err := data.DecodeStatic(dataSec)
	if err != nil {
		return nil, nil, nil, &DecodeError{Code: CodeDecode, Offset: hdrSize + metaSize,
			Msg: err.Error()}
	}
	// Сверка заявленного (meta) с заголовком data-секции: первые шесть u32.
	for i, want := range []uint32{meta.Items, meta.Npcs, meta.Territories, meta.Spawns, meta.Zones, meta.Skills} {
		if got := binary.LittleEndian.Uint32(dataSec[i*4:]); got != want {
			return nil, nil, nil, &DecodeError{Code: CodeDecode, Offset: hdrSize + metaSize + i*4,
				Msg: fmt.Sprintf("счётчик секции %d = %d против meta %d", i, got, want)}
		}
	}
	regs, err := decodeGeoRegions(geoSec)
	if err != nil {
		return nil, nil, nil, err
	}
	if uint32(len(regs)) != meta.Regions {
		return nil, nil, nil, &DecodeError{Code: CodeDecode, Offset: hdrSize + metaSize + int(meta.DataLen),
			Msg: fmt.Sprintf("регионов декодировано %d против meta %d", len(regs), meta.Regions)}
	}
	m, err := geo.NewMapFromRegions(regs)
	if err != nil {
		return nil, nil, nil, &DecodeError{Code: CodeDup, Offset: hdrSize + metaSize + int(meta.DataLen),
			Msg: err.Error()}
	}
	return st, m, meta, nil
}

// split проверяет заголовок и режет payload; сверка контрольной суммы —
// после всех дешёвых проверок полей.
func split(b []byte) (*Meta, []byte, []byte, error) {
	if len(b) < hdrSize {
		return nil, nil, nil, &DecodeError{Code: CodeTrunc, Offset: len(b),
			Msg: fmt.Sprintf("файл %d байт короче заголовка %d", len(b), hdrSize)}
	}
	if string(b[:4]) != magicStr {
		return nil, nil, nil, &DecodeError{Code: CodeMagic, Offset: 0,
			Msg: fmt.Sprintf("магия %q не %q", b[:4], magicStr)}
	}
	if v := binary.LittleEndian.Uint32(b[4:8]); v != Version {
		return nil, nil, nil, &DecodeError{Code: CodeVersion, Offset: 4,
			Msg: fmt.Sprintf("версия %d не %d — пересобери артефакт", v, Version)}
	}
	// Длины — только вычитанием: суммы не вычисляются, переполнение невозможно.
	avail := uint64(len(b) - hdrSize)
	if avail < metaSize {
		return nil, nil, nil, &DecodeError{Code: CodeTrunc, Offset: len(b), Msg: "payload короче meta"}
	}
	avail -= metaSize
	dataLen := binary.LittleEndian.Uint64(b[72:80])
	if dataLen > avail {
		return nil, nil, nil, &DecodeError{Code: CodeTrunc, Offset: 72,
			Msg: fmt.Sprintf("dataLen %d против остатка %d", dataLen, avail)}
	}
	avail -= dataLen
	geoLen := binary.LittleEndian.Uint64(b[80:88])
	if geoLen > avail {
		return nil, nil, nil, &DecodeError{Code: CodeTrunc, Offset: 80,
			Msg: fmt.Sprintf("geoLen %d против остатка %d", geoLen, avail)}
	}
	avail -= geoLen
	if avail != 0 {
		return nil, nil, nil, &DecodeError{Code: CodeTail, Offset: hdrSize + metaSize + int(dataLen+geoLen),
			Msg: fmt.Sprintf("непотреблённый остаток файла: %d байт", avail)}
	}
	if !verifyChecksum(b) {
		return nil, nil, nil, &DecodeError{Code: CodeChecksum, Offset: hdrSumOff,
			Msg: "SHA-256 payload не сходится (порча при хранении/передаче)"}
	}
	meta := &Meta{
		DataLen: dataLen,
		GeoLen:  geoLen,
	}
	copy(meta.DataManifest[:], b[8:40])
	copy(meta.GeoManifest[:], b[40:72])
	meta.Files = binary.LittleEndian.Uint32(b[hdrSize:])
	meta.Items = binary.LittleEndian.Uint32(b[hdrSize+4:])
	meta.Npcs = binary.LittleEndian.Uint32(b[hdrSize+8:])
	meta.Spawns = binary.LittleEndian.Uint32(b[hdrSize+12:])
	meta.Territories = binary.LittleEndian.Uint32(b[hdrSize+16:])
	meta.Zones = binary.LittleEndian.Uint32(b[hdrSize+20:])
	meta.Skills = binary.LittleEndian.Uint32(b[hdrSize+24:])
	meta.SkillLevels = binary.LittleEndian.Uint32(b[hdrSize+28:])
	meta.DropItems = binary.LittleEndian.Uint32(b[hdrSize+32:])
	meta.Regions = binary.LittleEndian.Uint32(b[hdrSize+36:])
	return meta, b[hdrSize+metaSize : hdrSize+metaSize+int(dataLen)], b[hdrSize+metaSize+int(dataLen):], nil
}

// parseHeader проверяет магию, версию и длины и возвращает манифесты и длины
// секций (счётчики meta не читает).
func parseHeader(b []byte) (*Meta, error) {
	meta, _, _, err := split(b)
	return meta, err
}

// verifyChecksum сверяет SHA-256 заголовка (до поля суммы) + payload.
func verifyChecksum(b []byte) bool {
	if len(b) < hdrSize {
		return false
	}
	h := sha256.New()
	h.Write(b[:hdrSumOff])
	h.Write(b[hdrSize:])
	return bytes.Equal(h.Sum(nil), b[hdrSumOff:hdrSize])
}

// decodeGeoRegions разбирает индекс geo-секции и декодирует регионы поверх её
// байтов (валидация структуры — сам декод, один проход). Гварды офсет/длина —
// вычитанием; структурная ошибка региона заворачивается с офсетом индекса.
func decodeGeoRegions(geoSec []byte) ([]*geo.Region, error) {
	if len(geoSec) == 0 {
		return nil, nil
	}
	if len(geoSec) < 4 {
		return nil, &DecodeError{Code: CodeTrunc, Offset: 0, Msg: "geo-секция короче индекса"}
	}
	n := binary.LittleEndian.Uint32(geoSec)
	if uint64(n)*geoRecLen > uint64(len(geoSec)-4) {
		return nil, &DecodeError{Code: CodeRange, Offset: 0,
			Msg: fmt.Sprintf("счётчик регионов %d против остатка %d", n, len(geoSec)-4)}
	}
	indexEnd := 4 + int(n)*geoRecLen
	regs := make([]*geo.Region, 0, n)
	for i := 0; i < int(n); i++ {
		base := 4 + i*geoRecLen
		rx := int32(binary.LittleEndian.Uint32(geoSec[base:]))
		ry := int32(binary.LittleEndian.Uint32(geoSec[base+4:]))
		off := binary.LittleEndian.Uint64(geoSec[base+8:])
		ln := binary.LittleEndian.Uint64(geoSec[base+16:])
		// Вычитание: off/ln против байтов после индекса; u64→int только после
		// проверки границ.
		tail := uint64(len(geoSec) - indexEnd)
		if off > tail {
			return nil, &DecodeError{Code: CodeRange, Offset: base + 8,
				Msg: fmt.Sprintf("офсет региона %d против хвоста %d", off, tail)}
		}
		if ln > tail-off {
			return nil, &DecodeError{Code: CodeRange, Offset: base + 16,
				Msg: fmt.Sprintf("длина региона %d против остатка %d", ln, tail-off)}
		}
		reg, err := geo.DecodeRegion(int(rx), int(ry), geoSec[indexEnd+int(off):indexEnd+int(off)+int(ln)])
		if err != nil {
			return nil, &DecodeError{Code: CodeDecode, Offset: base,
				Msg: fmt.Sprintf("регион (%d, %d): %v", rx, ry, err)}
		}
		regs = append(regs, reg)
	}
	return regs, nil
}

// LoadFile отображает файл артефакта (владение отображением — до конца
// процесса, как у загрузки каталога геодаты) и декодирует его с временами
// фаз. Предусловие файла — как у геодаты: не усекать и не править по месту
// при живом отображении; пересборка build (tmp + rename) безопасна.
func LoadFile(path string) (*data.Static, *geo.Map, *Meta, Phases, error) {
	b, _, err := geo.MapFile(path)
	if err != nil {
		return nil, nil, nil, Phases{}, fmt.Errorf("artifact: %w", err)
	}
	var ph Phases
	t0 := time.Now()
	meta, dataSec, geoSec, err := split(b)
	ph.Verify = time.Since(t0)
	if err != nil {
		return nil, nil, nil, ph, err
	}
	t1 := time.Now()
	st, err := data.DecodeStatic(dataSec)
	ph.DecodeData = time.Since(t1)
	if err != nil {
		return nil, nil, nil, ph, &DecodeError{Code: CodeDecode, Offset: hdrSize + metaSize, Msg: err.Error()}
	}
	t2 := time.Now()
	regs, err := decodeGeoRegions(geoSec)
	if err != nil {
		return nil, nil, nil, ph, err
	}
	m, err := geo.NewMapFromRegions(regs)
	ph.DecodeGeo = time.Since(t2)
	if err != nil {
		return nil, nil, nil, ph, &DecodeError{Code: CodeDup, Offset: hdrSize + metaSize + int(meta.DataLen), Msg: err.Error()}
	}
	return st, m, meta, ph, nil
}
