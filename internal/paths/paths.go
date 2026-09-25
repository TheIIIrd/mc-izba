// Package paths описывает раскладку каталогов проекта.
//
// Всё, что создаёт izba, живёт внутри одной корневой папки. Система
// (PATH, реестр, systemd, домашний каталог) не затрагивается: удаление
// сервера — это удаление корневой папки.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// Имена файлов и подкаталогов вынесены в константы, чтобы раскладка была
// описана ровно в одном месте.
const (
	ConfigName = "izba.toml"
	LockName   = "izba.lock"

	serverDir   = "server"
	javaDir     = "java"
	backupsDir  = "backups"
	internalDir = ".izba"
)

// Layout — корень проекта и производные от него пути.
type Layout struct {
	Root string
}

// New возвращает раскладку для указанного корня, приводя путь к абсолютному.
func New(root string) (Layout, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return Layout{}, fmt.Errorf("не удалось определить абсолютный путь для %q: %w", root, err)
	}
	return Layout{Root: abs}, nil
}

func (l Layout) Config() string  { return filepath.Join(l.Root, ConfigName) }
func (l Layout) Lock() string    { return filepath.Join(l.Root, LockName) }
func (l Layout) Server() string  { return filepath.Join(l.Root, serverDir) }
func (l Layout) Java() string    { return filepath.Join(l.Root, javaDir) }
func (l Layout) Backups() string { return filepath.Join(l.Root, backupsDir) }

func (l Layout) Internal() string { return filepath.Join(l.Root, internalDir) }
func (l Layout) Cache() string    { return filepath.Join(l.Internal(), "cache") }
func (l Layout) Tmp() string      { return filepath.Join(l.Internal(), "tmp") }

// ServerJar — путь к ядру. Имя фиксированное независимо от того, ваниль это
// или Paper: стартовые скрипты и супервизор не должны зависеть от ядра.
func (l Layout) ServerJar() string { return filepath.Join(l.Server(), "server.jar") }

func (l Layout) Eula() string       { return filepath.Join(l.Server(), "eula.txt") }
func (l Layout) Properties() string { return filepath.Join(l.Server(), "server.properties") }
func (l Layout) Plugins() string    { return filepath.Join(l.Server(), "plugins") }

// EnsureBase создаёт каталоги, необходимые до начала установки.
//
// Права 0o700 выбраны намеренно: в рабочем каталоге лежат конфиги и (в
// будущем) пароль RCON, посторонним пользователям машины там делать нечего.
// На Windows права игнорируются, наследуется ACL родительского каталога.
func (l Layout) EnsureBase() error {
	dirs := []string{
		l.Root,
		l.Server(),
		l.Java(),
		l.Backups(),
		l.Internal(),
		l.Cache(),
		l.Tmp(),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("не удалось создать каталог %s: %w", d, err)
		}
	}
	return nil
}

// Exists — небольшой помощник, чтобы не плодить os.Stat по всему коду.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
