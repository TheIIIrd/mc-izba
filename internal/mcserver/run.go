package mcserver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RunOptions описывает, как запустить сервер.
type RunOptions struct {
	JavaBin    string
	JarPath    string
	WorkDir    string
	JvmArgs    []string
	ServerArgs []string

	// Output — куда дублировать вывод сервера (обычно os.Stdout).
	Output io.Writer
}

// Process — запущенный сервер.
type Process struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser

	readyOnce sync.Once
	ready     chan struct{}

	waitOnce sync.Once
	waitErr  error
	waited   chan struct{}

	// stdinMu защищает запись в консоль сервера и намеренно отделён от mu.
	//
	// Общий мьютекс приводил бы к взаимоблокировке: запись в stdin блокируется,
	// если сервер перестал читать команды, а pump на каждую строку лога берёт
	// тот же мьютекс. Заблокированный pump перестаёт вычитывать stdout, труба
	// заполняется, сервер встаёт на записи — и разобрать это по симптомам
	// почти невозможно.
	stdinMu sync.Mutex

	mu       sync.Mutex
	tail     []string
	stopping bool
}

// tailSize — сколько последних строк лога держать в памяти для диагностики.
const tailSize = 80

// doneMarker — строка, по которой сервер считается поднявшимся.
//
// Проверять только «процесс не завершился» недостаточно: сервер может
// работать минуту и упасть на инициализации мира.
const doneMarker = ": Done ("

// ErrStartTimeout возвращается, если готовность не наступила вовремя.
var ErrStartTimeout = errors.New("сервер не сообщил о готовности за отведённое время")

// Start запускает процесс сервера.
//
// Контекст сюда намеренно не передаётся в exec.CommandContext: отмена должна
// приводить к корректной остановке через консольную команду stop, иначе мир
// не сохранится.
func Start(opts RunOptions) (*Process, error) {
	if opts.Output == nil {
		opts.Output = io.Discard
	}

	args := append([]string{}, opts.JvmArgs...)
	args = append(args, "-jar", opts.JarPath)
	args = append(args, opts.ServerArgs...)

	cmd := exec.Command(opts.JavaBin, args...)
	cmd.Dir = opts.WorkDir
	// Окружение наследуется как есть, но JAVA_TOOL_OPTIONS вычищается:
	// эта переменная незаметно подмешивает аргументы в JVM и способна
	// переопределить рассчитанные флаги.
	cmd.Env = filterEnv(os.Environ(), "JAVA_TOOL_OPTIONS", "_JAVA_OPTIONS")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("не удалось запустить %s: %w", opts.JavaBin, err)
	}

	p := &Process{
		cmd:    cmd,
		stdin:  stdin,
		ready:  make(chan struct{}),
		waited: make(chan struct{}),
	}

	go p.pump(stdout, opts.Output)
	go func() {
		err := cmd.Wait()
		p.waitOnce.Do(func() {
			p.waitErr = err
			close(p.waited)
		})
	}()

	return p, nil
}

// filterEnv убирает указанные переменные окружения.
func filterEnv(env []string, drop ...string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		name, _, _ := strings.Cut(e, "=")
		skip := false
		for _, d := range drop {
			if strings.EqualFold(name, d) {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, e)
		}
	}
	return out
}

// pump читает вывод сервера: дублирует его пользователю, ищет признак
// готовности и запоминает хвост для диагностики.
func (p *Process) pump(r io.Reader, out io.Writer) {
	sc := bufio.NewScanner(r)
	// Строки стектрейсов бывают длинными, стандартного буфера мало.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := sc.Text()
		fmt.Fprintln(out, line)

		p.mu.Lock()
		p.tail = append(p.tail, line)
		if len(p.tail) > tailSize {
			p.tail = p.tail[len(p.tail)-tailSize:]
		}
		p.mu.Unlock()

		if strings.Contains(line, doneMarker) {
			p.readyOnce.Do(func() { close(p.ready) })
		}
	}
}

// WaitReady ждёт готовности сервера, его завершения или таймаута.
func (p *Process) WaitReady(ctx context.Context, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-p.ready:
		return nil
	case <-p.waited:
		return fmt.Errorf("сервер завершился до готовности: %w", p.exitError())
	case <-timer.C:
		return ErrStartTimeout
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Process) exitError() error {
	select {
	case <-p.waited:
		if p.waitErr == nil {
			return errors.New("процесс завершился с кодом 0")
		}
		return p.waitErr
	default:
		return errors.New("процесс ещё работает")
	}
}

// Command отправляет команду в консоль сервера.
func (p *Process) Command(cmd string) error {
	p.stdinMu.Lock()
	defer p.stdinMu.Unlock()
	_, err := io.WriteString(p.stdin, cmd+"\n")
	return err
}

// Tail возвращает последние строки лога.
func (p *Process) Tail() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.tail))
	copy(out, p.tail)
	return out
}

// Wait ждёт завершения процесса.
func (p *Process) Wait() error {
	<-p.waited
	return p.waitErr
}

// Stop останавливает сервер корректно.
//
// Последовательность важна: команда stop заставляет сервер сохранить мир и
// выгрузить плагины. Убийство процесса этого не делает и грозит повреждением
// региона, в котором в этот момент находился игрок. Kill остаётся только
// как крайняя мера по истечении таймаута.
func (p *Process) Stop(timeout time.Duration) error {
	p.mu.Lock()
	already := p.stopping
	p.stopping = true
	p.mu.Unlock()

	select {
	case <-p.waited:
		return p.waitErr
	default:
	}

	if !already {
		if err := p.Command("stop"); err != nil {
			// Канал ввода мог закрыться вместе с упавшим процессом.
			// Это не повод отказываться от ожидания.
			_ = err
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-p.waited:
		return p.waitErr
	case <-timer.C:
		_ = p.cmd.Process.Kill()
		<-p.waited
		return fmt.Errorf("сервер не завершился за %s и был принудительно остановлен; "+
			"возможна потеря последних изменений мира", timeout)
	}
}

// Diagnose разбирает хвост лога и объясняет типовые причины падения.
//
// Смысл в том, чтобы пользователь получил ответ сразу, а не шёл искать
// стектрейс в поиске.
func Diagnose(lines []string, workDir string) []Finding {
	var out []Finding
	joined := strings.Join(lines, "\n")
	lower := strings.ToLower(joined)

	switch {
	case strings.Contains(lower, "failed to bind to port"):
		out = append(out, Finding{
			Level:   LevelError,
			Message: "сервер не смог занять порт",
			Hint: "Порт уже используется другим процессом (возможно, предыдущим " +
				"экземпляром сервера). Освободите порт или измените server.port",
		})
	case strings.Contains(lower, "outofmemoryerror"):
		out = append(out, Finding{
			Level:   LevelError,
			Message: "JVM исчерпала выделенную память",
			Hint: "Увеличьте server.memory, если физическая память позволяет, " +
				"либо уменьшите view-distance и количество загруженных чанков",
		})
	case strings.Contains(lower, "you need to agree to the eula"):
		out = append(out, Finding{
			Level:   LevelError,
			Message: "соглашение EULA не принято",
			Hint:    "Запустите izba up ещё раз и подтвердите согласие",
		})
	case strings.Contains(lower, "unsupported class file major version"),
		strings.Contains(lower, "unsupportedclassversionerror"):
		out = append(out, Finding{
			Level:   LevelError,
			Message: "версия Java не подходит для этого ядра",
			Hint:    "Уберите java.path из конфига, чтобы izba подобрал и скачал нужную версию",
		})
	}

	// Свежий отчёт о сбое почти всегда называет виновника.
	if report := latestCrashReport(workDir); report != "" {
		out = append(out, Finding{
			Level:   LevelInfo,
			Message: "найден свежий отчёт о сбое",
			Hint:    "Посмотрите " + report + ": в его начале обычно указан виновный мод или плагин",
		})
	}
	return out
}

// latestCrashReport возвращает самый свежий файл из crash-reports, если он
// появился за последние несколько минут.
func latestCrashReport(workDir string) string {
	dir := filepath.Join(workDir, "crash-reports")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	var newest string
	var newestTime time.Time
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newestTime) {
			newestTime = info.ModTime()
			newest = filepath.Join(dir, e.Name())
		}
	}
	if newest == "" || time.Since(newestTime) > 10*time.Minute {
		return ""
	}
	return newest
}
