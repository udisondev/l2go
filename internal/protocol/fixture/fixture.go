// Package fixture — golden-фикстуры пакетов протокола: байтовые векторы с
// мета-полями для тестов кодирования/декодирования (golden encode/decode,
// сценарный сервер, разбор дампов). Payload — байты после опкода (для
// Ex-семейств — после байтов sub); опкод и sub хранятся полями, в байты не
// дублируются.
package fixture

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Direction — направление пакета.
type Direction string

// Направления каталога опкодов.
const (
	LoginClient Direction = "login-client" // C → LoginServer
	LoginServer Direction = "login-server" // LoginServer → C
	GameClient  Direction = "game-client"  // C → GameServer
	GameServer  Direction = "game-server"  // GameServer → C
)

// Origin — происхождение вектора: generated (собран по внешнему оракулу
// формата/полей) или captured (живой трафик).
const (
	OriginGenerated = "generated"
	OriginCaptured  = "captured"
)

// Fixture — один байтовый вектор.
type Fixture struct {
	Dir     Direction
	Op      uint16 // опкод (для Ex-семейств — опкод семейства: 0xD0/0xFE)
	Sub     uint16 // sub Ex-семейства; 0 — нет sub
	Name    string
	Payload []byte // тело после опкода (и байтов sub)
	Origin  string
}

// Load читает testdata/<name>.json. Путь анкеруется по исходнику пакета:
// потребители исполняются из разных рабочих каталогов. Имя заперто в
// testdata: абсолютные пути и обход «..» — ошибка.
func Load(name string) ([]Fixture, error) {
	if filepath.IsAbs(name) || strings.Contains(name, "..") {
		return nil, fmt.Errorf("fixture: имя %q вне testdata", name)
	}
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "testdata", name+".json")

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("fixture: чтение %s: %w", path, err)
	}
	var rows []struct {
		Dir     string `json:"dir"`
		Op      uint16 `json:"op"`
		Sub     uint16 `json:"sub"`
		Name    string `json:"name"`
		Payload string `json:"payload"`
		Origin  string `json:"origin"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("fixture: разбор %s: %w", path, err)
	}
	if rows == nil {
		return nil, fmt.Errorf("fixture: %s: пустой файл или null-JSON", path)
	}
	out := make([]Fixture, 0, len(rows))
	for _, r := range rows {
		payload, err := hex.DecodeString(r.Payload)
		if err != nil {
			return nil, fmt.Errorf("fixture: %s: payload %q: %w", path, r.Name, err)
		}
		out = append(out, Fixture{
			Dir: Direction(r.Dir), Op: r.Op, Sub: r.Sub,
			Name: r.Name, Payload: payload, Origin: r.Origin,
		})
	}
	return out, nil
}
