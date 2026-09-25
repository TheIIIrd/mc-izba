//go:build windows

package platform

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetDiskFreeSpaceExW  = kernel32.NewProc("GetDiskFreeSpaceExW")
)

// memoryStatusEx повторяет структуру MEMORYSTATUSEX из Windows API.
type memoryStatusEx struct {
	dwLength                uint32
	dwMemoryLoad            uint32
	ullTotalPhys            uint64
	ullAvailPhys            uint64
	ullTotalPageFile        uint64
	ullAvailPageFile        uint64
	ullTotalVirtual         uint64
	ullAvailVirtual         uint64
	ullAvailExtendedVirtual uint64
}

// TotalMemoryBytes вызывает GlobalMemoryStatusEx.
func TotalMemoryBytes() (uint64, error) {
	var st memoryStatusEx
	st.dwLength = uint32(unsafe.Sizeof(st))
	r, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&st)))
	if r == 0 {
		return 0, fmt.Errorf("GlobalMemoryStatusEx: %w", err)
	}
	return st.ullTotalPhys, nil
}

// FreeDiskBytes вызывает GetDiskFreeSpaceExW и возвращает объём, доступный
// текущему пользователю с учётом дисковых квот.
func FreeDiskBytes(path string) (uint64, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, fmt.Errorf("некорректный путь %q: %w", path, err)
	}
	var freeToCaller, total, totalFree uint64
	r, _, callErr := procGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&freeToCaller)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r == 0 {
		return 0, fmt.Errorf("GetDiskFreeSpaceExW(%s): %w", path, callErr)
	}
	return freeToCaller, nil
}

// IsPrivileged определяет, запущен ли процесс с правами администратора.
//
// Это эвристика: открытие \\.\PHYSICALDRIVE0 требует прав администратора и не
// имеет побочных эффектов. Полноценная проверка потребовала бы работы с
// токеном процесса через advapi32, что тянет заметный объём кода ради
// предупреждения. Ложноотрицательный результат здесь допустим: на Windows
// запуск от администратора считается предупреждением, а не ошибкой.
func IsPrivileged() (bool, error) {
	f, err := os.Open(`\\.\PHYSICALDRIVE0`)
	if err != nil {
		return false, nil
	}
	_ = f.Close()
	return true, nil
}
