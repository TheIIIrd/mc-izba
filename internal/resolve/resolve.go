// Package resolve превращает пожелание пользователя («Paper, последняя
// стабильная») в конкретный артефакт: точную версию, ссылку и контрольную сумму.
//
// Резолв намеренно отделён от скачивания. Это позволяет показать пользователю
// полный список того, что будет установлено, до того как начнётся загрузка,
// и делает сетевой слой тестируемым на замоканных ответах.
package resolve

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"izba/internal/netx"
)

// Core — идентификатор ядра сервера.
type Core string

const (
	CoreVanilla Core = "vanilla"
	CorePaper   Core = "paper"
)

// ParseCore разбирает значение из конфига.
func ParseCore(s string) (Core, error) {
	switch Core(strings.ToLower(strings.TrimSpace(s))) {
	case CoreVanilla:
		return CoreVanilla, nil
	case CorePaper:
		return CorePaper, nil
	default:
		return "", fmt.Errorf("неизвестное ядро %q (поддерживаются: vanilla, paper)", s)
	}
}

// LatestVersion — значение, означающее «последняя стабильная версия».
const LatestVersion = "latest"

// Artifact описывает один файл, который предстоит скачать.
type Artifact struct {
	URL      string      `json:"url"`
	Digest   netx.Digest `json:"digest"`
	Size     int64       `json:"size"`
	Filename string      `json:"filename"`
}

// CoreResolution — результат резолва ядра.
type CoreResolution struct {
	Core Core `json:"core"`
	// MinecraftVersion хранится строкой без попытки разобрать её на числа.
	// Mojang перешёл на схему вида 26.2 наряду с прежними 1.21.11, поэтому
	// любое сравнение «по semver» здесь ошибочно.
	MinecraftVersion string   `json:"minecraft_version"`
	Build            string   `json:"build,omitempty"` // номер сборки Paper
	JavaMajor        int      `json:"java_major"`
	Jar              Artifact `json:"jar"`
}

// JavaResolution — результат резолва среды выполнения Java.
type JavaResolution struct {
	Vendor    string   `json:"vendor"`
	Major     int      `json:"major"`
	Release   string   `json:"release"`
	ImageType string   `json:"image_type"` // jre или jdk
	Archive   Artifact `json:"archive"`
}

// orderedKeys возвращает ключи JSON-объекта в том порядке, в котором они
// встретились в исходном тексте.
//
// Fill v3 отдаёт версии объектом, где первый ключ соответствует самой свежей
// группе версий. Обычный Unmarshal в map этот порядок теряет, поэтому объект
// разбирается потоком токенов.
func orderedKeys(raw []byte, field string) ([]string, map[string]json.RawMessage, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, nil, fmt.Errorf("не удалось разобрать ответ: %w", err)
	}
	obj, ok := top[field]
	if !ok {
		return nil, nil, fmt.Errorf("в ответе отсутствует поле %q", field)
	}

	dec := json.NewDecoder(bytes.NewReader(obj))
	tok, err := dec.Token()
	if err != nil {
		return nil, nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, nil, fmt.Errorf("поле %q не является объектом", field)
	}

	var keys []string
	values := make(map[string]json.RawMessage)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, nil, fmt.Errorf("неожиданный ключ в объекте %q", field)
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, nil, err
		}
		keys = append(keys, key)
		values[key] = val
	}
	return keys, values, nil
}
