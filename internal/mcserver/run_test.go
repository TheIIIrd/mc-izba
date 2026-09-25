package mcserver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// Супервизор — самая слабо покрытая часть: настоящий сервер Minecraft в
// тестах не поднять. Вместо него используется сам тестовый бинарник,
// запущенный повторно в роли поддельного сервера. Приём переносим между
// платформами, в отличие от вызова sh или cmd.

const helperEnv = "IZBA_TEST_HELPER"

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		helperMain(mode)
		return
	}
	os.Exit(m.Run())
}

// helperMain изображает сервер Minecraft: печатает несколько строк лога и
// ведёт себя дальше в зависимости от режима.
func helperMain(mode string) {
	out := bufio.NewWriter(os.Stdout)
	emit := func(line string) {
		fmt.Fprintln(out, line)
		out.Flush()
	}

	emit("[00:00:00] [ServerMain/INFO]: Loading properties")

	switch mode {
	case "crash-before-ready":
		emit("[00:00:01] [Server thread/WARN]: **** FAILED TO BIND TO PORT!")
		os.Exit(1)

	case "ready", "ignore-stop":
		// Формат строки совпадает с настоящим: по ней определяется готовность.
		emit(`[00:00:05] [Server thread/INFO]: Done (5.123s)! For help, type "help"`)
	}

	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		cmd := strings.TrimSpace(sc.Text())
		emit("[00:00:10] [Server thread/INFO]: получена команда " + cmd)
		if cmd == "stop" && mode != "ignore-stop" {
			emit("[00:00:11] [Server thread/INFO]: Stopping server")
			os.Exit(0)
		}
	}
	// stdin закрылся — в режиме ignore-stop просто ждём, пока убьют.
	select {}
}

// startHelper запускает поддельный сервер в указанном режиме.
func startHelper(t *testing.T, mode string) *Process {
	t.Helper()
	t.Setenv(helperEnv, mode)

	proc, err := Start(RunOptions{
		JavaBin:    os.Args[0],
		JarPath:    "fake-server.jar",
		WorkDir:    t.TempDir(),
		JvmArgs:    []string{"-Xms1G"},
		ServerArgs: []string{"--nogui"},
		Output:     io.Discard,
	})
	if err != nil {
		t.Fatalf("не удалось запустить поддельный сервер: %v", err)
	}
	return proc
}

func TestWaitReadyDetectsDoneLine(t *testing.T) {
	proc := startHelper(t, "ready")
	defer proc.Stop(5 * time.Second)

	if err := proc.WaitReady(context.Background(), 30*time.Second); err != nil {
		t.Fatalf("готовность не определена: %v", err)
	}
}

// Процесс, завершившийся до строки Done, не должен считаться запущенным
// сервером: «процесс не упал» и «сервер поднялся» — разные вещи.
func TestWaitReadyFailsWhenProcessExits(t *testing.T) {
	proc := startHelper(t, "crash-before-ready")

	err := proc.WaitReady(context.Background(), 30*time.Second)
	if err == nil {
		t.Fatal("ожидалась ошибка: сервер завершился до готовности")
	}
	if !strings.Contains(err.Error(), "до готовности") {
		t.Errorf("непонятное сообщение: %v", err)
	}
}

func TestWaitReadyTimeout(t *testing.T) {
	proc := startHelper(t, "silent")
	defer proc.Stop(5 * time.Second)

	err := proc.WaitReady(context.Background(), 300*time.Millisecond)
	if !errors.Is(err, ErrStartTimeout) {
		t.Fatalf("ожидался ErrStartTimeout, получено: %v", err)
	}
}

func TestWaitReadyRespectsContext(t *testing.T) {
	proc := startHelper(t, "silent")
	defer proc.Stop(5 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := proc.WaitReady(ctx, 30*time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("ожидалась отмена контекста, получено: %v", err)
	}
}

// Корректная остановка идёт консольной командой stop: только так сервер
// сохраняет мир.
func TestStopIsGraceful(t *testing.T) {
	proc := startHelper(t, "ready")
	if err := proc.WaitReady(context.Background(), 30*time.Second); err != nil {
		t.Fatal(err)
	}

	if err := proc.Stop(10 * time.Second); err != nil {
		t.Fatalf("корректная остановка вернула ошибку: %v", err)
	}

	tail := strings.Join(proc.Tail(), "\n")
	if !strings.Contains(tail, "получена команда stop") {
		t.Errorf("команда stop не дошла до сервера, лог:\n%s", tail)
	}
	if !strings.Contains(tail, "Stopping server") {
		t.Errorf("сервер не сообщил об остановке, лог:\n%s", tail)
	}
}

// Зависший сервер должен быть убит по таймауту, но с явным предупреждением:
// молча терять несохранённый мир нельзя.
func TestStopKillsAfterTimeout(t *testing.T) {
	proc := startHelper(t, "ignore-stop")
	if err := proc.WaitReady(context.Background(), 30*time.Second); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	err := proc.Stop(300 * time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("принудительная остановка должна возвращать ошибку с предупреждением")
	}
	if !strings.Contains(err.Error(), "принудительно") {
		t.Errorf("сообщение не предупреждает о принудительной остановке: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("остановка заняла %s: таймаут не сработал", elapsed)
	}
}

// Повторный Stop не должен зависать или паниковать: его вызывают и по
// Ctrl+C, и из ветки обработки неудачного старта.
func TestStopIsIdempotent(t *testing.T) {
	proc := startHelper(t, "ready")
	if err := proc.WaitReady(context.Background(), 30*time.Second); err != nil {
		t.Fatal(err)
	}

	_ = proc.Stop(10 * time.Second)
	done := make(chan struct{})
	go func() {
		_ = proc.Stop(10 * time.Second)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("повторный Stop завис")
	}
}

// Запись в консоль и чтение лога не должны блокировать друг друга: на общем
// мьютексе это давало взаимоблокировку при зависшем сервере.
func TestConsoleAndLogDoNotBlockEachOther(t *testing.T) {
	proc := startHelper(t, "ready")
	if err := proc.WaitReady(context.Background(), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	defer proc.Stop(10 * time.Second)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			_ = proc.Command(fmt.Sprintf("say %d", i))
		}
		close(done)
	}()

	for i := 0; i < 200; i++ {
		_ = proc.Tail()
	}

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("запись в консоль и чтение лога заблокировали друг друга")
	}
}

// Переменные, подменяющие аргументы JVM, не должны доходить до сервера:
// иначе они переопределят рассчитанные флаги.
func TestFilterEnvDropsJavaToolOptions(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"JAVA_TOOL_OPTIONS=-Xmx128M",
		"_JAVA_OPTIONS=-Xmx64M",
		"HOME=/home/user",
	}
	out := filterEnv(in, "JAVA_TOOL_OPTIONS", "_JAVA_OPTIONS")

	joined := strings.Join(out, " ")
	if strings.Contains(joined, "JAVA_TOOL_OPTIONS") || strings.Contains(joined, "_JAVA_OPTIONS") {
		t.Errorf("переменные не вычищены: %v", out)
	}
	if !strings.Contains(joined, "PATH=") || !strings.Contains(joined, "HOME=") {
		t.Errorf("лишнее вычищено: %v", out)
	}
}
