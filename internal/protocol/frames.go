// Разрез байтового потока на кадры провода: запись [uint16 LE длина всей
// записи][тело = длина−2]. Чистая функция над буфером вызывающего — общая с
// будущими проводами сервера; не путать с crypto-кадром (шифроблок над
// payload: паддинг/чексумма — там, длина-префикс — здесь).

package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ErrFrameIncomplete — в буфере нет полного кадра: дочитать и вызвать
// NextFrame снова (прецедент io.ErrUnexpectedEOF).
var ErrFrameIncomplete = errors.New("кадр неполный")

// ErrFrameLength — объявленная длина записи меньше заголовка: протокольное
// нарушение, ожидание данных бессмысленно.
var ErrFrameLength = errors.New("длина кадра меньше заголовка")

// NextFrame извлекает первый кадр из b. Тело возвращается срезом в b
// (нулевая копия, буфер у вызывающего); потреблено всегда len(body)+2.
func NextFrame(b []byte) ([]byte, error) {
	if len(b) < 2 {
		return nil, ErrFrameIncomplete
	}
	n := int(binary.LittleEndian.Uint16(b))
	if n < 2 {
		return nil, fmt.Errorf("protocol: NextFrame: длина кадра %d: %w", n, ErrFrameLength)
	}
	if n > len(b) {
		return nil, ErrFrameIncomplete
	}
	return b[2:n], nil
}
