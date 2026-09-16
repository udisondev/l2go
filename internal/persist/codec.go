package persist

import (
	"encoding/json"
	"fmt"
)

// Операции запросов персист-актора.
const (
	OpCharList   = "charlist"
	OpCreateChar = "create"
	// OpSaveChar — upsert персонажа по слоту: записи чужих слотов аккаунта
	// сохраняются (отправитель знает только онлайн-персонажа; P3.7).
	OpSaveChar = "savechar"
)

// Request — запрос персист-актору (payload KindPersistRequest). Corr —
// непрозрачный корреляционный идентификатор отправителя (connID шлюза /
// EntityID региона), возвращаемый эхом в ответе: демультиплексация ответов
// при одном ящике отправителя.
type Request struct {
	Op        string     `json:"op"`
	Corr      uint64     `json:"corr"`
	Account   string     `json:"account"`
	Name      string     `json:"name,omitempty"`
	Sex       int        `json:"sex,omitempty"`
	HairStyle int        `json:"hair_style,omitempty"`
	HairColor int        `json:"hair_color,omitempty"`
	Face      int        `json:"face,omitempty"`
	Char      CharRecord `json:"char,omitempty"`
}

// Reply — ответ актора (payload KindPersistReply). Code — стабильный код
// отказа (пуст для ok), Err — человекочитаемый текст.
type Reply struct {
	Op     string       `json:"op"`
	Corr   uint64       `json:"corr"`
	OK     bool         `json:"ok"`
	Code   string       `json:"code,omitempty"`
	Err    string       `json:"err,omitempty"`
	Chars  []CharRecord `json:"chars,omitempty"`
	Record *CharRecord  `json:"record,omitempty"`
}

// EncodeRequest кодирует запрос в байты письма.
func EncodeRequest(r Request) ([]byte, error) {
	buf, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("persist: кодирование запроса %s: %w", r.Op, err)
	}
	return buf, nil
}

// DecodeRequest декодирует payload запроса; битый вход — ошибка, не паника.
func DecodeRequest(buf []byte) (Request, error) {
	var r Request
	if err := json.Unmarshal(buf, &r); err != nil {
		return r, fmt.Errorf("persist: разбор запроса: %w", err)
	}
	if r.Op == "" {
		// null и пустой объект проходят Unmarshal молча — это не запрос.
		return r, fmt.Errorf("persist: разбор запроса: пустая операция")
	}
	return r, nil
}

// EncodeReply кодирует ответ в байты письма.
func EncodeReply(r Reply) ([]byte, error) {
	buf, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("persist: кодирование ответа %s: %w", r.Op, err)
	}
	return buf, nil
}

// DecodeReply декодирует payload ответа.
func DecodeReply(buf []byte) (Reply, error) {
	var r Reply
	if err := json.Unmarshal(buf, &r); err != nil {
		return r, fmt.Errorf("persist: разбор ответа: %w", err)
	}
	if r.Op == "" {
		// null и пустой объект проходят Unmarshal молча — это не ответ.
		return r, fmt.Errorf("persist: разбор ответа: пустая операция")
	}
	return r, nil
}
