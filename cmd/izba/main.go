// Команда izba собирает и запускает сервер Minecraft.
package main

import (
	"os"

	"izba/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args))
}
