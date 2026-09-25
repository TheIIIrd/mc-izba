//go:build darwin

package platform

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// TotalMemoryBytes читает hw.memsize.
//
// В стандартной библиотеке Go для darwin есть syscall.SysctlUint32, но нет
// варианта на 64 бита, а hw.memsize на любой современной машине выходит за
// предел uint32. Тянуть golang.org/x/sys ради одного значения не хочется,
// поэтому вызывается системная утилита. Путь указан абсолютным намеренно:
// поиск по PATH позволил бы подсунуть свой sysctl.
func TotalMemoryBytes() (uint64, error) {
	out, err := exec.Command("/usr/sbin/sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0, fmt.Errorf("sysctl hw.memsize: %w", err)
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("не удалось разобрать вывод sysctl: %w", err)
	}
	return n, nil
}

// FreeDiskBytes возвращает место, доступное непривилегированному процессу.
func FreeDiskBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs(%s): %w", path, err)
	}
	return uint64(st.Bsize) * st.Bavail, nil
}
