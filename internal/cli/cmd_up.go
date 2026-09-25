package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"izba/internal/config"
	"izba/internal/install"
	"izba/internal/mcserver"
	"izba/internal/netx"
	"izba/internal/paths"
	"izba/internal/platform"
	"izba/internal/resolve"
)

func runUp(args []string) error {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	dir := fs.String("dir", ".", "каталог проекта")
	acceptEula := fs.Bool("accept-eula", false,
		"подтвердить согласие с Minecraft EULA без интерактивного вопроса")
	noStart := fs.Bool("no-start", false, "только установить, не запускать сервер")
	force := fs.Bool("force", false, "продолжить, несмотря на предупреждения")
	if err := fs.Parse(args); err != nil {
		return err
	}

	layout, err := paths.New(*dir)
	if err != nil {
		return err
	}
	cfg, err := config.Load(layout.Config())
	if err != nil {
		return err
	}
	if err := layout.EnsureBase(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client := netx.NewClient(nil)

	// --- Этап 1: резолв -------------------------------------------------
	fmt.Println("Определяю версии...")

	coreKind, err := resolve.ParseCore(cfg.Server.Core)
	if err != nil {
		return err
	}
	vanilla := resolve.Vanilla{Client: client}

	var coreRes resolve.CoreResolution
	switch coreKind {
	case resolve.CoreVanilla:
		coreRes, err = vanilla.Resolve(ctx, cfg.Server.Version)
	case resolve.CorePaper:
		coreRes, err = resolve.Paper{Client: client, Vanilla: vanilla}.Resolve(ctx, cfg.Server.Version)
	}
	if err != nil {
		return err
	}

	// Память нужна до запуска, чтобы показать её в плане.
	heap, err := cfg.MemoryBytes()
	if err != nil {
		return err
	}
	if heap == config.MemoryAuto {
		total, memErr := platform.TotalMemoryBytes()
		if memErr != nil {
			total = 0
		}
		heap = mcserver.AutoMemory(int64(total))
	}

	// --- Этап 2: Java ---------------------------------------------------
	javaHome := cfg.Java.Path
	var javaRes *resolve.JavaResolution
	if javaHome == "" {
		jr, err := resolve.Java{Client: client}.Resolve(ctx, coreRes.JavaMajor)
		if err != nil {
			return err
		}
		javaRes = &jr
	}

	// --- Этап 3: показать план ------------------------------------------
	printPlan(coreRes, javaRes, javaHome, heap, cfg.Server.Port, cfg.Server.OnlineMode)

	if w := mcserver.Log4ShellWarning(coreRes.MinecraftVersion); w != "" {
		printFindings("Безопасность версии", []mcserver.Finding{{
			Level:   mcserver.LevelWarn,
			Message: w,
		}})
	}

	// --- Этап 4: EULA ---------------------------------------------------
	if !mcserver.EulaAccepted(layout.Eula()) {
		if !*acceptEula {
			fmt.Printf("\nДля запуска сервера требуется согласие с Minecraft EULA.\n")
			fmt.Printf("Текст соглашения: %s\n", mcserver.EulaURL)
			if !confirm("Вы прочитали соглашение и принимаете его?") {
				return fmt.Errorf("без согласия с EULA сервер запустить нельзя")
			}
		}
		if err := mcserver.WriteEulaAccepted(layout.Eula()); err != nil {
			return err
		}
	}

	// --- Этап 5: предстартовые проверки ---------------------------------
	need := coreRes.Jar.Size + 2<<30 // ядро плюс запас под мир и логи
	if javaRes != nil {
		need += javaRes.Archive.Size * 3 // архив плюс распакованное содержимое
	}
	findings := mcserver.Preflight(mcserver.PreflightInput{
		Port:          cfg.Server.Port,
		HeapBytes:     heap,
		DiskPath:      layout.Root,
		NeedDiskBytes: need,
	})
	printFindings("Предстартовые проверки", findings)
	if mcserver.HasErrors(findings) && !*force {
		fmt.Fprintln(os.Stderr, "\nУстановка остановлена. Исправьте проблемы выше или используйте -force.")
		return mcserver.ErrPreflightFailed
	}

	// --- Этап 6: конфигурация сервера -----------------------------------
	propFindings, err := ensureProperties(layout, cfg)
	if err != nil {
		return err
	}
	printFindings("Настройки сервера", propFindings)
	if mcserver.HasErrors(propFindings) && !*force {
		fmt.Fprintln(os.Stderr, "\nЗапуск остановлен. Исправьте настройки выше или используйте -force.")
		return mcserver.ErrPreflightFailed
	}

	// --- Этап 7: установка ----------------------------------------------
	fmt.Println("\nУстановка...")
	inst := install.Installer{
		Client: client,
		Layout: layout,
		Log:    func(f string, a ...any) { fmt.Printf(f+"\n", a...) },
	}

	if inst.CoreUpToDate(coreRes) {
		fmt.Println("  ядро: актуально")
	} else if err := inst.InstallCore(ctx, coreRes); err != nil {
		return err
	}

	if javaRes != nil {
		home, err := inst.InstallJava(ctx, *javaRes)
		if err != nil {
			return err
		}
		javaHome = home
	}

	javaBin := platform.JavaBinary(javaHome)
	if _, err := os.Stat(javaBin); err != nil {
		return fmt.Errorf("исполняемый файл Java не найден по пути %s: %w", javaBin, err)
	}

	// --- Этап 8: lock ---------------------------------------------------
	lock, err := config.LoadLock(layout.Lock())
	if err != nil {
		return err
	}
	lock.Core = &coreRes
	lock.Java = javaRes
	if javaRes != nil {
		lock.JavaHome = javaHome
	} else {
		lock.JavaHome = ""
	}
	if err := config.SaveLock(layout.Lock(), lock, netx.Version); err != nil {
		return err
	}

	if *noStart {
		fmt.Println("\nГотово. Сервер не запускался (-no-start).")
		return nil
	}

	// --- Этап 9: запуск -------------------------------------------------
	return startServer(ctx, layout, cfg, coreRes, javaBin, heap)
}

func printPlan(core resolve.CoreResolution, java *resolve.JavaResolution, javaHome string,
	heap int64, port int, onlineMode bool) {
	fmt.Println("\nБудет установлено:")
	build := ""
	if core.Build != "" {
		build = " (сборка " + core.Build + ")"
	}
	fmt.Printf("  ядро:   %s %s%s\n", core.Core, core.MinecraftVersion, build)
	if java != nil {
		fmt.Printf("  java:   %s %s (%s)\n", java.Vendor, java.Release, java.ImageType)
	} else {
		fmt.Printf("  java:   используется указанная в конфиге (%s)\n", javaHome)
	}
	fmt.Printf("  память: %s\n", mcserver.FormatMemoryFlag(heap))
	fmt.Printf("  порт:   %d\n", port)
	if onlineMode {
		fmt.Printf("  режим:  online (аутентификация Mojang включена)\n")
	} else {
		fmt.Printf("  режим:  OFFLINE — аутентификация отключена\n")
	}
}

// ensureProperties создаёт server.properties при первом запуске и проверяет
// существующий на опасные настройки.
//
// Возвращает находки, чтобы решение о блокировке запуска принималось в одном
// месте вместе с предстартовыми проверками.
func ensureProperties(layout paths.Layout, cfg config.Config) ([]mcserver.Finding, error) {
	path := layout.Properties()

	if !paths.Exists(path) {
		props := mcserver.SecureDefaults(cfg.Server.Port, cfg.Server.OnlineMode).Merge(cfg.Properties)
		if err := mcserver.WriteProperties(path, props); err != nil {
			return nil, err
		}
		fmt.Printf("  создан %s\n", path)
		return mcserver.AuditProperties(props, !cfg.Server.OnlineMode), nil
	}

	// Существующий файл не перезаписывается: пользователь мог править его
	// руками, и молчаливая потеря настроек хуже расхождения с конфигом.
	// Вместо перезаписи сообщаем о расхождениях.
	props, err := mcserver.ReadProperties(path)
	if err != nil {
		return nil, err
	}

	findings := mcserver.AuditProperties(props, !cfg.Server.OnlineMode)

	if p := props["server-port"]; p != "" && p != strconv.Itoa(cfg.Server.Port) {
		findings = append(findings, mcserver.Finding{
			Level: mcserver.LevelWarn,
			Message: fmt.Sprintf("server.properties указывает порт %s, а izba.toml — %d",
				p, cfg.Server.Port),
			Hint: "Побеждает значение из server.properties; приведите их в соответствие",
		})
	}

	// Обратное расхождение: конфиг разрешает offline-режим, а в файле он
	// включён. Ошибкой это не является, но пользователь должен знать, что
	// его настройка не применилась.
	if fileOnline := !strings.EqualFold(props["online-mode"], "false"); fileOnline != cfg.Server.OnlineMode {
		findings = append(findings, mcserver.Finding{
			Level: mcserver.LevelWarn,
			Message: fmt.Sprintf(
				"server.online_mode в izba.toml = %t, а в server.properties online-mode = %t",
				cfg.Server.OnlineMode, fileOnline),
			Hint: "izba не перезаписывает существующий server.properties, поэтому " +
				"действует значение из него. Исправьте нужный файл вручную",
		})
	}

	return findings, nil
}

func startServer(ctx context.Context, layout paths.Layout, cfg config.Config,
	core resolve.CoreResolution, javaBin string, heap int64) error {

	timeout, err := cfg.StartTimeout()
	if err != nil {
		return err
	}

	jvmArgs := mcserver.BuildFlags(mcserver.FlagsInput{
		HeapBytes:        heap,
		Core:             string(core.Core),
		MinecraftVersion: core.MinecraftVersion,
		ExtraArgs:        cfg.Runtime.ExtraJvmArgs,
	})

	fmt.Printf("\nЗапуск сервера (ожидание готовности до %s)...\n\n", timeout)

	proc, err := mcserver.Start(mcserver.RunOptions{
		JavaBin:    javaBin,
		JarPath:    layout.ServerJar(),
		WorkDir:    layout.Server(),
		JvmArgs:    jvmArgs,
		ServerArgs: []string{"--nogui"},
		Output:     os.Stdout,
	})
	if err != nil {
		return err
	}

	if err := proc.WaitReady(ctx, timeout); err != nil {
		printFindings("Диагностика", mcserver.Diagnose(proc.Tail(), layout.Server()))
		// Даже при неудачном старте процесс нужно завершить корректно.
		_ = proc.Stop(30 * time.Second)
		return fmt.Errorf("сервер не запустился: %w", err)
	}

	fmt.Printf("\n--- Сервер готов. Порт %d. Консоль доступна ниже, Ctrl+C для остановки. ---\n\n",
		cfg.Server.Port)

	// Ввод пользователя пробрасывается в консоль сервера.
	go forwardConsole(proc)

	select {
	case <-ctx.Done():
		fmt.Println("\nОстанавливаю сервер...")
		if err := proc.Stop(2 * time.Minute); err != nil {
			return err
		}
		fmt.Println("Сервер остановлен, мир сохранён.")
		return nil
	case <-waitChan(proc):
		err := proc.Wait()
		if err != nil {
			printFindings("Диагностика", mcserver.Diagnose(proc.Tail(), layout.Server()))
			return fmt.Errorf("сервер завершился: %w", err)
		}
		fmt.Println("\nСервер завершил работу.")
		return nil
	}
}

func waitChan(p *mcserver.Process) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		_ = p.Wait()
		close(ch)
	}()
	return ch
}

func forwardConsole(p *mcserver.Process) {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		if err := p.Command(sc.Text()); err != nil {
			return
		}
	}
}
