// Package platform изолирует различия между Linux, Windows и macOS.
//
// Правило: платформенно-зависимый код живёт только здесь. Остальной проект
// работает с этими функциями и не содержит проверок runtime.GOOS.
package platform

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
)

// ErrUnsupported возвращается, когда платформа не поддерживается. Вызывающий
// код обязан обрабатывать это как «проверку выполнить не удалось», а не как
// фатальную ошибку: лучше пропустить проверку свободной памяти, чем отказаться
// запускать сервер на экзотической ОС.
var ErrUnsupported = errors.New("платформа не поддерживается")

// AdoptiumOS возвращает значение параметра os для API Adoptium.
func AdoptiumOS() (string, error) {
	switch runtime.GOOS {
	case "linux":
		return "linux", nil
	case "windows":
		return "windows", nil
	case "darwin":
		return "mac", nil
	default:
		return "", fmt.Errorf("%w: GOOS=%s", ErrUnsupported, runtime.GOOS)
	}
}

// AdoptiumArch возвращает значение параметра architecture для API Adoptium.
func AdoptiumArch() (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return "x64", nil
	case "arm64":
		return "aarch64", nil
	case "arm":
		return "arm", nil
	case "386":
		return "x86", nil
	case "ppc64le":
		return "ppc64le", nil
	case "s390x":
		return "s390x", nil
	default:
		return "", fmt.Errorf("%w: GOARCH=%s", ErrUnsupported, runtime.GOARCH)
	}
}

// JavaBinary возвращает путь к исполняемому файлу java внутри JAVA_HOME.
func JavaBinary(javaHome string) string {
	name := "java"
	if runtime.GOOS == "windows" {
		name = "java.exe"
	}
	return filepath.Join(javaHome, "bin", name)
}

// IsWindows пригодится там, где различие сводится к одной строке текста
// (например, к подсказке в сообщении об ошибке).
func IsWindows() bool { return runtime.GOOS == "windows" }

// PrivilegeHint — как называется «слишком много прав» на текущей ОС.
func PrivilegeHint() string {
	if IsWindows() {
		return "от имени администратора"
	}
	return "от root"
}
