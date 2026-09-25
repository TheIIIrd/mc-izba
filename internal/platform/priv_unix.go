//go:build linux || darwin

package platform

import "os"

// IsPrivileged сообщает, что процесс запущен от root.
//
// Проверяется эффективный uid: именно он определяет реальные полномочия при
// setuid-запуске.
func IsPrivileged() (bool, error) {
	return os.Geteuid() == 0, nil
}
