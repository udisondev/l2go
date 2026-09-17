package world

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/udisondev/l2go/internal/replica"
	"github.com/udisondev/l2go/internal/transport"
)

const (
	portionMagic   = "PL32"
	portionVersion = 3 // v3: сущность несёт отрезок движения и бакет игрока (P3.9)
	flagPayloads   = 1

	recStep  = 1
	recPanic = 2

	// defaultMaxFileBytes — ротация по размеру по умолчанию (64 МиБ).
	defaultMaxFileBytes = 64 << 20

	writeBufSize = 4 << 10
)

// ErrTruncated — оборванный хвост лога (ENOSPC/power-loss): валидные записи
// до места обрыва уже возвращены, кадр-неполноценец отброшен.
var ErrTruncated = errors.New("world: лог порций оборван (неполный кадр)")

// errLogDead — писатель уже отказал: повторные записи не предпринимаются
// (один отказ, не цикл), регион обязан заморозиться по первой ошибке.
var errLogDead = errors.New("world: писатель лога порций отказал")

// FileHeader — заголовок файла лога. Период метронома нужен читателю: тики
// переводятся во время потребителем (движение), реплей с другим дефолтом Hz
// разошёлся бы молча.
type FileHeader struct {
	Version  uint64
	Payloads bool
	Region   RegionID
	PeriodNS uint64
}

// AppliedBirth — рождение с присвоенным реестром ID: запись лога несёт ID
// явно — реплей воспроизводит население при любом порядке регистраций в
// реестре адресов.
type AppliedBirth struct {
	ID  transport.EntityID
	Ent *Entity
}

// BirthRecord — рождение, восстановленное из лога.
type BirthRecord struct {
	ID  transport.EntityID
	Ent Entity
}

// PortionRecord — порция шага в логе: заголовок пачки (ящик, водяной знак)
// и письма; порядок записей внутри шага = порядок применения свёрткой.
type PortionRecord struct {
	Box  transport.EntityID
	Mark uint64
	Envs []transport.Envelope
}

// StepInput — шаг для записи в лог: заполняется регионом после успешного
// применения (паник-шаг порцией не логируется).
type StepInput struct {
	Tick     Tick
	Delta    uint64
	Births   []AppliedBirth
	Retires  []Retire
	Portions []PortionRecord
	Advisory []AdvisoryIn
}

// StepRecord — шаг, восстановленный из лога.
type StepRecord struct {
	Tick     Tick
	Delta    uint64
	Births   []BirthRecord
	Retires  []Retire
	Portions []PortionRecord
	Advisory []AdvisoryIn
}

// PanicRecord — маркер сбойного шага: реплей-достоверность обрывается на
// маркере явно, расхождение детектируемо.
type PanicRecord struct {
	Tick  Tick
	Phase uint8
}

// PortionLog — писатель лога порций D5: файл на регион, ротация по размеру,
// seq на старте = max существующих + 1 (рестарт не затирает и не смешивает
// сессии). Буферизованная запись, fsync не требуется; закрытие — только из
// горутины региона (контракт: единственный писатель). Каждая запись — кадр
// {длина, crc32, тело}: ридер отличает оборванный хвост от валидных данных.
type PortionLog struct {
	dir      string
	region   RegionID
	period   time.Duration
	payloads bool
	maxFile  int64

	seq       int
	file      *os.File
	w         *bufio.Writer
	written   int64
	dead      bool
	headerLen int64 // размер заголовка файла (маркер «файл без кадров»)

	enc    []byte // переиспользуемый буфер тела кадра
	prefix []byte // переиспользуемый префикс кадра (длина + crc32)
}

// NewPortionLog создаёт писателя в каталоге dir (создаётся при отсутствии).
func NewPortionLog(dir string, region RegionID, period time.Duration, payloads bool, maxFileBytes int64) (*PortionLog, error) {
	if dir == "" {
		return nil, fmt.Errorf("world: каталог лога порций пуст")
	}
	if period <= 0 {
		return nil, fmt.Errorf("world: период метронома для лога порций = %v; want > 0", period)
	}
	if maxFileBytes < 0 {
		return nil, fmt.Errorf("world: размер файла лога порций = %d; want ≥ 0", maxFileBytes)
	}
	if maxFileBytes == 0 {
		maxFileBytes = defaultMaxFileBytes
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("world: каталог лога порций %s: %w", dir, err)
	}
	l := &PortionLog{
		dir:      dir,
		region:   region,
		period:   period,
		payloads: payloads,
		maxFile:  maxFileBytes,
		seq:      scanMaxSeq(dir, region) + 1,
	}
	if err := l.openFile(); err != nil {
		return nil, err
	}
	return l, nil
}

func portionFileName(dir string, region RegionID, seq int) string {
	return filepath.Join(dir, fmt.Sprintf("portion-%d-%d.log", region, seq))
}

func (l *PortionLog) openFile() error {
	f, err := os.OpenFile(portionFileName(l.dir, l.region, l.seq), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("world: открыть лог порций %s: %w", portionFileName(l.dir, l.region, l.seq), err)
	}
	l.file = f
	l.w = bufio.NewWriterSize(f, writeBufSize)
	hdr := l.encodeHeader()
	l.headerLen = int64(len(hdr))
	l.written = l.headerLen
	if _, err := l.w.Write(hdr); err != nil {
		return fmt.Errorf("world: заголовок лога порций: %w", err)
	}
	return nil
}

func (l *PortionLog) encodeHeader() []byte {
	buf := make([]byte, 0, 32)
	buf = append(buf, portionMagic...)
	buf = binary.AppendUvarint(buf, portionVersion)
	if l.payloads {
		buf = append(buf, flagPayloads)
	} else {
		buf = append(buf, 0)
	}
	buf = binary.AppendUvarint(buf, uint64(l.region))
	buf = binary.AppendUvarint(buf, uint64(l.period))
	return buf
}

// LogStep записывает шаг: вызывается регионом после успешного применения.
// Ошибка записи — заморозить регион (лог — обязательство D5, молчаливая дыра
// недопустима).
func (l *PortionLog) LogStep(s StepInput) error {
	body := l.encodeStep(s)
	return l.writeFrame(body)
}

// LogPanic пишет маркер сбойного шага. После отказа писателя — no-op:
// инцидент уже громкий (recover-журнал), повторная запись в отказавший лог
// не предпринимается.
func (l *PortionLog) LogPanic(tick Tick, phase uint8) error {
	if l.dead {
		return nil
	}
	buf := l.enc[:0]
	buf = append(buf, recPanic)
	buf = binary.AppendUvarint(buf, uint64(tick))
	buf = binary.AppendUvarint(buf, uint64(phase))
	l.enc = buf
	return l.writeFrame(buf)
}

func (l *PortionLog) writeFrame(body []byte) error {
	if l.dead {
		return errLogDead
	}
	frameLen := uvarintLen(uint64(len(body))) + 4 + len(body)
	// файл из одного заголовка не ротачивается: иначе негабаритный кадр
	// (крупнее maxFile) давал бы «файл на кадр» — один негабарит допустим
	if l.written > l.headerLen && l.written+int64(frameLen) > l.maxFile {
		if err := l.rotate(); err != nil {
			l.dead = true
			return err
		}
	}
	crc := crc32.ChecksumIEEE(body)
	l.prefix = binary.AppendUvarint(l.prefix[:0], uint64(len(body)))
	l.prefix = append(l.prefix,
		byte(crc), byte(crc>>8), byte(crc>>16), byte(crc>>24))
	if _, err := l.w.Write(l.prefix); err != nil {
		l.dead = true
		return fmt.Errorf("world: запись лога порций: %w", err)
	}
	if _, err := l.w.Write(body); err != nil {
		l.dead = true
		return fmt.Errorf("world: запись лога порций: %w", err)
	}
	l.written += int64(frameLen)
	return nil
}

func (l *PortionLog) rotate() error {
	if err := l.w.Flush(); err != nil {
		return fmt.Errorf("world: сброс лога порций при ротации: %w", err)
	}
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("world: закрытие лога порций при ротации: %w", err)
	}
	l.seq++
	return l.openFile()
}

// Close сбрасывает буфер и закрывает файл. Только горутина региона —
// конкурентное закрытие из другой горутины является гонкой по контракту.
func (l *PortionLog) Close() error {
	if l.file == nil {
		return nil
	}
	err := l.w.Flush()
	if cerr := l.file.Close(); err == nil {
		err = cerr
	}
	l.file = nil
	if err != nil {
		return fmt.Errorf("world: закрытие лога порций: %w", err)
	}
	return nil
}

func uvarintLen(v uint64) int {
	n := 1
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}

// encodeStep кодирует тело записи шага. Выставляет l.enc (внешний тест
// меряет аллокации encodeStepInto).
func (l *PortionLog) encodeStep(s StepInput) []byte {
	l.enc = l.encodeStepInto(l.enc[:0], s)
	return l.enc
}

func (l *PortionLog) encodeStepInto(buf []byte, s StepInput) []byte {
	buf = append(buf, recStep)
	buf = binary.AppendUvarint(buf, uint64(s.Tick))
	buf = binary.AppendUvarint(buf, s.Delta)
	buf = binary.AppendUvarint(buf, uint64(len(s.Births)))
	for _, b := range s.Births {
		buf = binary.AppendUvarint(buf, uint64(b.ID))
		buf = appendEntity(buf, b.Ent)
	}
	buf = binary.AppendUvarint(buf, uint64(len(s.Retires)))
	for _, r := range s.Retires {
		buf = binary.AppendUvarint(buf, uint64(r.ID))
	}
	buf = binary.AppendUvarint(buf, uint64(len(s.Portions)))
	for _, p := range s.Portions {
		buf = binary.AppendUvarint(buf, uint64(p.Box))
		buf = binary.AppendUvarint(buf, p.Mark)
		buf = binary.AppendUvarint(buf, uint64(len(p.Envs)))
		for _, env := range p.Envs {
			buf = appendEnvelope(buf, env, l.payloads)
		}
	}
	buf = binary.AppendUvarint(buf, uint64(len(s.Advisory)))
	for _, a := range s.Advisory {
		buf = binary.AppendUvarint(buf, uint64(a.Cell))
		buf = binary.AppendUvarint(buf, uint64(a.Entity))
	}
	return buf
}

func appendEnvelope(buf []byte, env transport.Envelope, payloads bool) []byte {
	buf = binary.AppendUvarint(buf, uint64(env.To.Entity))
	buf = binary.AppendVarint(buf, int64(env.To.Slot))
	buf = binary.AppendUvarint(buf, uint64(env.FromID))
	buf = binary.AppendUvarint(buf, uint64(env.Kind))
	buf = binary.AppendUvarint(buf, uint64(env.Attrs))
	if payloads {
		buf = binary.AppendUvarint(buf, uint64(len(env.Payload)))
		buf = append(buf, env.Payload...)
	}
	return buf
}

// listSeqFiles — файлы сессии региона по возрастанию seq.
func listSeqFiles(dir string, region RegionID) []string {
	files, err := filepath.Glob(filepath.Join(dir, fmt.Sprintf("portion-%d-*.log", region)))
	if err != nil {
		return nil // glob по шаблону не ошибается иначе как по I/O; пусто — нет сессии
	}
	seqs := make([]int, 0, len(files))
	bySeq := make(map[int]string, len(files))
	for _, f := range files {
		base := strings.TrimSuffix(filepath.Base(f), ".log")
		parts := strings.Split(base, "-")
		if len(parts) != 3 {
			continue
		}
		seq, err := strconv.Atoi(parts[2])
		if err != nil || seq <= 0 {
			continue
		}
		seqs = append(seqs, seq)
		bySeq[seq] = f
	}
	sort.Ints(seqs)
	ordered := make([]string, 0, len(seqs))
	for _, seq := range seqs {
		ordered = append(ordered, bySeq[seq])
	}
	return ordered
}

// fileSeq — seq из имени файла цепочки.
func fileSeq(path string) int {
	base := strings.TrimSuffix(filepath.Base(path), ".log")
	parts := strings.Split(base, "-")
	if len(parts) != 3 {
		return -1
	}
	seq, err := strconv.Atoi(parts[2])
	if err != nil {
		return -1
	}
	return seq
}

// scanMaxSeq — максимальный seq существующих файлов региона.
func scanMaxSeq(dir string, region RegionID) int {
	files := listSeqFiles(dir, region)
	if len(files) == 0 {
		return 0
	}
	seq, _ := strconv.Atoi(strings.Split(strings.TrimSuffix(filepath.Base(files[len(files)-1]), ".log"), "-")[2])
	return seq
}

// ReadPortionLogDir читает сессию региона: цепочку файлов по возрастанию seq.
// Заголовки файлов обязаны совпадать; оборванный хвост даёт ErrTruncated
// после валидных записей.
func ReadPortionLogDir(dir string, region RegionID) (FileHeader, []StepRecord, []PanicRecord, error) {
	files := listSeqFiles(dir, region)
	var hdr FileHeader
	var steps []StepRecord
	var panics []PanicRecord
	first := true
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return hdr, steps, panics, fmt.Errorf("world: чтение лога порций: %w", err)
		}
		if len(data) == 0 {
			continue // пустой файл: kill -9 между созданием и первым сбросом буфера
		}
		seq := fileSeq(path)
		fh, fs, fp, err := parseFile(data)
		if first {
			hdr = fh
			first = false
		} else if err == nil || errors.Is(err, ErrTruncated) {
			if fh != hdr {
				return hdr, steps, panics, fmt.Errorf("world: заголовок файла seq=%d расходится с началом цепочки: %+v против %+v", seq, fh, hdr)
			}
		}
		steps = append(steps, fs...)
		panics = append(panics, fp...)
		if err != nil {
			return hdr, steps, panics, err
		}
	}
	return hdr, steps, panics, nil
}

// parseFile разбирает один файл: заголовок + кадры до обрыва.
func parseFile(data []byte) (FileHeader, []StepRecord, []PanicRecord, error) {
	var hdr FileHeader
	if len(data) < len(portionMagic) || string(data[:len(portionMagic)]) != portionMagic {
		return hdr, nil, nil, fmt.Errorf("world: лог порций без магической сигнатуры")
	}
	pos := len(portionMagic)
	ver, n := binary.Uvarint(data[pos:])
	if n <= 0 {
		return hdr, nil, nil, fmt.Errorf("world: лог порций: версия не читается")
	}
	pos += n
	hdr.Version = ver
	if ver != portionVersion {
		return hdr, nil, nil, fmt.Errorf("world: лог порций версии %d не поддерживается (ожидается %d)",
			ver, portionVersion)
	}
	if pos >= len(data) {
		return hdr, nil, nil, fmt.Errorf("world: лог порций: флаги не читаются")
	}
	hdr.Payloads = data[pos]&flagPayloads != 0
	pos++
	region, n := binary.Uvarint(data[pos:])
	if n <= 0 {
		return hdr, nil, nil, fmt.Errorf("world: лог порций: регион не читается")
	}
	pos += n
	hdr.Region = RegionID(region)
	period, n := binary.Uvarint(data[pos:])
	if n <= 0 {
		return hdr, nil, nil, fmt.Errorf("world: лог порций: период не читается")
	}
	pos += n
	hdr.PeriodNS = period

	var steps []StepRecord
	var panics []PanicRecord
	for pos < len(data) {
		bodyLen, n := binary.Uvarint(data[pos:])
		if n <= 0 || bodyLen > uint64(len(data)) {
			return hdr, steps, panics, ErrTruncated
		}
		pos += n
		if pos+4 > len(data) {
			return hdr, steps, panics, ErrTruncated
		}
		want := binary.LittleEndian.Uint32(data[pos : pos+4])
		pos += 4
		if pos+int(bodyLen) > len(data) {
			return hdr, steps, panics, ErrTruncated
		}
		body := data[pos : pos+int(bodyLen)]
		if crc32.ChecksumIEEE(body) != want {
			return hdr, steps, panics, ErrTruncated
		}
		pos += int(bodyLen)
		if len(body) == 0 {
			continue
		}
		switch body[0] {
		case recStep:
			st, err := parseStep(body[1:], hdr.Payloads)
			if err != nil {
				return hdr, steps, panics, err
			}
			steps = append(steps, st)
		case recPanic:
			p, err := parsePanic(body[1:])
			if err != nil {
				return hdr, steps, panics, err
			}
			panics = append(panics, p)
		default:
			return hdr, steps, panics, fmt.Errorf("world: лог порций: неизвестный тип записи %d", body[0])
		}
	}
	return hdr, steps, panics, nil
}

type parseCursor struct {
	data []byte
	pos  int
	err  error
}

func (c *parseCursor) uvarint() uint64 {
	if c.err != nil {
		return 0
	}
	v, n := binary.Uvarint(c.data[c.pos:])
	if n <= 0 {
		c.err = fmt.Errorf("world: лог порций: uvarint на смещении %d", c.pos)
		return 0
	}
	c.pos += n
	return v
}

func (c *parseCursor) varint() int64 {
	if c.err != nil {
		return 0
	}
	v, n := binary.Varint(c.data[c.pos:])
	if n <= 0 {
		c.err = fmt.Errorf("world: лог порций: varint на смещении %d", c.pos)
		return 0
	}
	c.pos += n
	return v
}

func (c *parseCursor) byte() byte {
	if c.err != nil {
		return 0
	}
	if c.pos >= len(c.data) {
		c.err = fmt.Errorf("world: лог порций: байт на смещении %d", c.pos)
		return 0
	}
	b := c.data[c.pos]
	c.pos++
	return b
}

func (c *parseCursor) str() string {
	n := c.uvarint()
	return string(c.bytes(int(n)))
}

func (c *parseCursor) bytes(n int) []byte {
	if c.err != nil {
		return nil
	}
	if n < 0 || c.pos+n > len(c.data) {
		c.err = fmt.Errorf("world: лог порций: байты (%d) на смещении %d", n, c.pos)
		return nil
	}
	b := c.data[c.pos : c.pos+n]
	c.pos += n
	return b
}

func parseStep(body []byte, payloads bool) (StepRecord, error) {
	c := &parseCursor{data: body}
	st := StepRecord{
		Tick:  Tick(c.uvarint()),
		Delta: c.uvarint()}
	nbu := c.uvarint()
	if c.err == nil && nbu > uint64(len(body)) {
		c.err = fmt.Errorf("world: лог порций: рождений %d больше тела записи", nbu)
	}
	if c.err == nil {
		nb := int(nbu)
		st.Births = make([]BirthRecord, 0, nb)
		for range nb {
			var b BirthRecord
			b.ID = transport.EntityID(c.uvarint())
			b.Ent, c.err = parseEntity(c)
			if c.err != nil {
				break
			}
			st.Births = append(st.Births, b)
		}
	}
	nru := c.uvarint()
	if c.err == nil && nru > uint64(len(body)) {
		c.err = fmt.Errorf("world: лог порций: удалений %d больше тела записи", nru)
	}
	if c.err == nil {
		nr := int(nru)
		st.Retires = make([]Retire, 0, nr)
		for range nr {
			st.Retires = append(st.Retires, Retire{ID: transport.EntityID(c.uvarint())})
		}
	}
	npu := c.uvarint()
	if c.err == nil && npu > uint64(len(body)) {
		c.err = fmt.Errorf("world: лог порций: пачек %d больше тела записи", npu)
	}
	if c.err == nil {
		np := int(npu)
		st.Portions = make([]PortionRecord, 0, np)
		for range np {
			var p PortionRecord
			p.Box = transport.EntityID(c.uvarint())
			p.Mark = c.uvarint()
			neu := c.uvarint()
			if c.err != nil || neu > uint64(len(body)) {
				break
			}
			ne := int(neu)
			p.Envs = make([]transport.Envelope, 0, ne)
			for range ne {
				var env transport.Envelope
				env.To.Entity = transport.EntityID(c.uvarint())
				env.To.Slot = transport.Slot(c.varint())
				env.FromID = transport.EntityID(c.uvarint())
				env.Kind = transport.Kind(c.uvarint())
				env.Attrs = transport.Attrs(c.uvarint())
				if payloads {
					pl := int(c.uvarint())
					env.Payload = append([]byte(nil), c.bytes(pl)...)
				}
				if c.err != nil {
					break
				}
				p.Envs = append(p.Envs, env)
			}
			if c.err != nil {
				break
			}
			st.Portions = append(st.Portions, p)
		}
	}
	nau := c.uvarint()
	if c.err == nil && nau > uint64(len(body)) {
		c.err = fmt.Errorf("world: лог порций: advisory-входов %d больше тела записи", nau)
	}
	if c.err == nil {
		na := int(nau)
		st.Advisory = make([]AdvisoryIn, 0, na)
		for range na {
			st.Advisory = append(st.Advisory, AdvisoryIn{
				Cell:   replica.CellID(c.uvarint()),
				Entity: transport.EntityID(c.uvarint()),
			})
		}
	}
	return st, c.err
}

func parsePanic(body []byte) (PanicRecord, error) {
	c := &parseCursor{data: body}
	p := PanicRecord{
		Tick: Tick(c.uvarint())}
	ph := c.uvarint()
	if c.err == nil {
		if ph > math.MaxUint8 {
			return p, fmt.Errorf("world: лог порций: фаза паники %d за границей байта", ph)
		}
		p.Phase = uint8(ph)
	}
	return p, c.err
}

// parseEntity — восстановление сущности из сериализации appendEntity.
func parseEntity(c *parseCursor) (Entity, error) {
	var e Entity
	if c.err != nil {
		return e, c.err
	}
	e.ID = transport.EntityID(c.uvarint())
	e.Owner = RegionID(c.uvarint())
	e.Pos.X = int32(int64(c.uvarint()))
	e.Pos.Y = int32(int64(c.uvarint()))
	e.Pos.Z = int32(int64(c.uvarint()))
	e.Dest.X = int32(int64(c.uvarint()))
	e.Dest.Y = int32(int64(c.uvarint()))
	e.Dest.Z = int32(int64(c.uvarint()))
	e.Heading = int32(int64(c.uvarint()))
	e.MoveFrom.X = int32(int64(c.uvarint()))
	e.MoveFrom.Y = int32(int64(c.uvarint()))
	e.MoveFrom.Z = int32(int64(c.uvarint()))
	e.MoveDist = int64(c.uvarint())
	e.MoveDone = int64(c.uvarint())
	e.Moving = c.byte() != 0
	e.Dead = c.byte() != 0
	e.HP = int32(int64(c.uvarint()))
	e.Beat = Tick(c.uvarint())
	for i := range e.Servants {
		e.Servants[i].Alive = c.byte() != 0
		e.Servants[i].Pos.X = int32(int64(c.uvarint()))
		e.Servants[i].Pos.Y = int32(int64(c.uvarint()))
		e.Servants[i].Pos.Z = int32(int64(c.uvarint()))
	}
	ntu := c.uvarint()
	if c.err != nil {
		return e, c.err
	}
	if ntu > uint64(len(c.data)) {
		return e, fmt.Errorf("world: лог порций: transfer-записей %d больше тела", ntu)
	}
	nt := int(ntu)
	if nt > 0 {
		e.Transfers = make([]TransferRecord, 0, nt)
		for range nt {
			var tr TransferRecord
			tr.ID = c.uvarint()
			tr.Phase = c.byte()
			tr.Payload = append([]byte(nil), c.bytes(int(c.uvarint()))...)
			tr.Precondition = append([]byte(nil), c.bytes(int(c.uvarint()))...)
			if c.err != nil {
				return e, c.err
			}
			e.Transfers = append(e.Transfers, tr)
		}
	}
	if c.byte() == 0 {
		return e, c.err
	}
	e.Player, c.err = parsePlayer(c)
	return e, c.err
}

// parsePlayer — зеркало appendPlayer.
func parsePlayer(c *parseCursor) (*Player, error) {
	p := &Player{}
	r := &p.Rec
	r.Account = c.str()
	r.Name = c.str()
	r.Slot = int(c.uvarint())
	r.ClassID = int(c.uvarint())
	r.Race = int(c.uvarint())
	r.Sex = int(c.uvarint())
	r.HairStyle = int(c.uvarint())
	r.HairColor = int(c.uvarint())
	r.Face = int(c.uvarint())
	r.X = int(c.varint())
	r.Y = int(c.varint())
	r.Z = int(c.varint())
	r.Heading = int(c.varint())
	r.Level = int(c.uvarint())
	r.Exp = c.varint()
	r.HP = int(c.uvarint())
	r.MP = int(c.uvarint())
	r.CreatedUnix = c.varint()
	r.LastSeenUnix = c.varint()
	p.ConnID = c.uvarint()
	p.SpeedBudget = c.varint()
	p.PendingTeleport = c.byte() == 1
	p.EnterLeaving = c.byte() == 1
	return p, c.err
}
