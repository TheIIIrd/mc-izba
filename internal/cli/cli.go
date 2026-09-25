// Package cli реализует интерфейс командной строки.
package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"izba/internal/mcserver"
	"izba/internal/netx"
)

const usage = `izba — сборщик сервера Minecraft.

Использование:
  izba <команда> [флаги]

Команды:
  init      создать izba.toml в текущем каталоге
  up        скачать всё необходимое, разложить и запустить сервер
  version   показать версию
  help      показать эту справку

Общие флаги:
  -dir <путь>   каталог проекта (по умолчанию текущий)

Подробнее о команде: izba <команда> -h
`

// Main — точка входа. Возвращает код завершения процесса.
func Main(args []string) int {
	if len(args) < 2 {
		fmt.Print(usage)
		return 2
	}

	cmd := args[1]
	rest := args[2:]

	var err error
	switch cmd {
	case "init":
		err = runInit(rest)
	case "up":
		err = runUp(rest)
	case "version", "-v", "--version":
		fmt.Printf("izba %s\n", netx.Version)
		return 0
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "неизвестная команда %q\n\n%s", cmd, usage)
		return 2
	}

	if err != nil {
		if errors.Is(err, mcserver.ErrPreflightFailed) {
			// Подробности уже выведены в виде списка находок.
			return 1
		}
		fmt.Fprintf(os.Stderr, "\nОшибка: %v\n", err)
		return 1
	}
	return 0
}

// printFindings выводит результаты проверок единым блоком.
func printFindings(title string, fs []mcserver.Finding) {
	if len(fs) == 0 {
		return
	}
	fmt.Printf("\n%s:\n", title)
	for _, f := range fs {
		fmt.Printf("  [%s] %s\n", f.Level, f.Message)
		if f.Hint != "" {
			for _, line := range strings.Split(f.Hint, "\n") {
				fmt.Printf("         %s\n", line)
			}
		}
	}
}
