package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"izba/internal/resolve"
)

// LockVersion — версия схемы файла. Увеличивается при несовместимых
// изменениях, чтобы старый lock можно было распознать, а не молча
// неправильно прочитать.
const LockVersion = 1

// Lock фиксирует, что реально установлено.
//
// Это то, что делает сборку воспроизводимой: имея izba.toml и izba.lock,
// сервер можно поднять на другой машине в точно таком же составе.
type Lock struct {
	SchemaVersion int       `json:"schema_version"`
	UpdatedAt     time.Time `json:"updated_at"`
	CreatedBy     string    `json:"created_by"`

	Core *resolve.CoreResolution `json:"core,omitempty"`
	Java *resolve.JavaResolution `json:"java,omitempty"`

	// JavaHome заполняется только когда Java скачана самим izba.
	// Для внешней Java из конфига поле остаётся пустым.
	JavaHome string `json:"java_home,omitempty"`
}

// LoadLock читает lock-файл. Отсутствие файла не является ошибкой: это просто
// означает, что установка ещё не выполнялась.
func LoadLock(path string) (Lock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Lock{SchemaVersion: LockVersion}, nil
		}
		return Lock{}, err
	}

	var l Lock
	if err := json.Unmarshal(data, &l); err != nil {
		return Lock{}, fmt.Errorf("не удалось разобрать %s: %w", path, err)
	}
	if l.SchemaVersion > LockVersion {
		return Lock{}, fmt.Errorf(
			"%s создан более новой версией izba (схема %d, поддерживается %d)",
			path, l.SchemaVersion, LockVersion)
	}
	return l, nil
}

// SaveLock записывает lock-файл атомарно: сначала во временный файл рядом,
// затем переименованием. Прерывание на середине не оставит битый lock.
func SaveLock(path string, l Lock, version string) error {
	l.SchemaVersion = LockVersion
	l.UpdatedAt = time.Now().UTC()
	l.CreatedBy = "izba/" + version

	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	_ = os.Remove(path)
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("не удалось записать %s: %w", path, err)
	}
	return nil
}
