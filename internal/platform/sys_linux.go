//go:build linux

package platform

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// TotalMemoryBytes читает MemTotal из /proc/meminfo.
func TotalMemoryBytes() (uint64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, fmt.Errorf("не удалось прочитать /proc/meminfo: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("неожиданный формат строки MemTotal: %q", line)
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("не удалось разобрать MemTotal: %w", err)
		}
		return kb * 1024, nil
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("MemTotal не найден в /proc/meminfo")
}

// FreeDiskBytes возвращает место, доступное непривилегированному процессу
// (Bavail, а не Bfree: часть блоков зарезервирована под root).
func FreeDiskBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs(%s): %w", path, err)
	}
	return uint64(st.Bsize) * st.Bavail, nil
}
