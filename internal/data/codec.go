package data

import "errors"

// EncodeStatic кодирует статику в детерминированный плоский little-endian
// формат (обход отображений по отсортированным ключам; байты двух кодирований
// одной статики идентичны).
func EncodeStatic(s *Static) []byte {
	return nil
}

// DecodeStatic декодирует формат EncodeStatic. Малформленный вход — ошибка с
// кодом и офсетом, не паника: границы проверяются на каждом чтении, ёмкости не
// выделяются вперёд больше остатка, дубликаты ключей — ошибка, хвост секции —
// ошибка.
func DecodeStatic(b []byte) (*Static, error) {
	return nil, errors.New("data: DecodeStatic не реализован")
}
