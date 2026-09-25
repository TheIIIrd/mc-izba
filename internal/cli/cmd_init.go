package cli

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"izba/internal/config"
	"izba/internal/paths"
)

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	dir := fs.String("dir", ".", "каталог проекта")
	core := fs.String("core", "", "ядро: vanilla или paper")
	version := fs.String("version", "", "версия Minecraft или latest")
	port := fs.Int("port", 0, "порт сервера")
	memory := fs.String("memory", "", "объём памяти под кучу: auto, 4G, 6144M")
	offline := fs.Bool("offline", false,
		"отключить аутентификацию Mojang (опасно: зайти можно будет под любым ником)")
	force := fs.Bool("force", false, "перезаписать существующий конфиг")
	nonInteractive := fs.Bool("yes", false, "не задавать вопросов, использовать значения по умолчанию")
	if err := fs.Parse(args); err != nil {
		return err
	}

	layout, err := paths.New(*dir)
	if err != nil {
		return err
	}
	if paths.Exists(layout.Config()) && !*force {
		return fmt.Errorf("%s уже существует; используйте -force для перезаписи", layout.Config())
	}

	cfg := config.Default()
	interactive := !*nonInteractive

	// Значения из флагов имеют приоритет и отключают соответствующий вопрос.
	if *core != "" {
		cfg.Server.Core = *core
	} else if interactive {
		cfg.Server.Core = ask("Ядро сервера (vanilla/paper)", cfg.Server.Core)
	}
	if *version != "" {
		cfg.Server.Version = *version
	} else if interactive {
		cfg.Server.Version = ask("Версия Minecraft (latest или, например, 1.21.4)", cfg.Server.Version)
	}
	if *port != 0 {
		cfg.Server.Port = *port
	} else if interactive {
		raw := ask("Порт", strconv.Itoa(cfg.Server.Port))
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("порт должен быть числом, получено %q", raw)
		}
		cfg.Server.Port = n
	}
	if *memory != "" {
		cfg.Server.Memory = *memory
	} else if interactive {
		cfg.Server.Memory = ask("Память под кучу (auto, 4G, 6144M)", cfg.Server.Memory)
	}

	// Вопрос про online-mode в диалоге не задаётся намеренно: подавляющему
	// большинству нужен обычный режим, а отключение должно быть осознанным
	// действием, а не одним из ответов, которые пролистывают через Enter.
	if *offline {
		cfg.Server.OnlineMode = false
		fmt.Println("\nВНИМАНИЕ: аутентификация Mojang отключена.")
		fmt.Println("Зайти на сервер сможет любой под любым ником, включая ник администратора.")
		fmt.Println("Включите белый список и не открывайте порт в интернет.")
	}

	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := layout.EnsureBase(); err != nil {
		return err
	}
	if err := config.Save(layout.Config(), cfg); err != nil {
		return err
	}

	fmt.Printf("\nСоздан %s\n", layout.Config())
	fmt.Println("Следующий шаг: izba up")
	return nil
}

// ask задаёт вопрос с подставленным значением по умолчанию.
//
// Пустой ввод означает согласие с умолчанием, поэтому Enter проводит
// пользователя через весь диалог без единого решения.
func ask(question, def string) string {
	fmt.Printf("%s [%s]: ", question, def)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return def
	}
	answer := strings.TrimSpace(sc.Text())
	if answer == "" {
		return def
	}
	return answer
}

// confirm требует явного согласия. Всё, кроме перечисленных вариантов,
// считается отказом: молчаливое согласие здесь недопустимо.
func confirm(question string) bool {
	fmt.Printf("%s [y/N]: ", question)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(sc.Text())) {
	case "y", "yes", "д", "да":
		return true
	default:
		return false
	}
}
