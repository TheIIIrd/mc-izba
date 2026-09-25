//go:build !linux && !darwin && !windows

package platform

// На платформах вне списка поддерживаемых предстартовые проверки пропускаются
// с предупреждением, а не блокируют запуск.

func TotalMemoryBytes() (uint64, error) { return 0, ErrUnsupported }

func FreeDiskBytes(string) (uint64, error) { return 0, ErrUnsupported }

func IsPrivileged() (bool, error) { return false, ErrUnsupported }
