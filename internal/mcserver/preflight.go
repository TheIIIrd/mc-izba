package mcserver

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"izba/internal/platform"
)

// Level — серьёзность находки.
type Level int

const (
	LevelInfo Level = iota
	LevelWarn
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelError:
		return "ОШИБКА"
	case LevelWarn:
		return "ВНИМАНИЕ"
	default:
		return "ИНФО"
	}
}

// Finding — результат одной проверки.
//
// Hint обязателен по смыслу: сообщение без указания, что делать, заставляет
// пользователя идти искать ответ на форуме.
type Finding struct {
	Level   Level
	Message string
	Hint    string
}

// HasErrors сообщает, есть ли среди находок блокирующие.
func HasErrors(fs []Finding) bool {
	for _, f := range fs {
		if f.Level == LevelError {
			return true
		}
	}
	return false
}

// EulaURL — ссылка, которую обязан увидеть пользователь.
const EulaURL = "https://aka.ms/MinecraftEULA"

// EulaAccepted проверяет, принято ли соглашение в файле eula.txt.
func EulaAccepted(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "eula") &&
			strings.EqualFold(strings.TrimSpace(v), "true") {
			return true
		}
	}
	return false
}

// WriteEulaAccepted фиксирует согласие пользователя.
//
// Вызывать эту функцию можно только после явного подтверждения. Принимать
// лицензионное соглашение за пользователя недопустимо.
func WriteEulaAccepted(path string) error {
	content := fmt.Sprintf(
		"# Соглашение Minecraft EULA принято через izba %s\n"+
			"# Текст соглашения: %s\n"+
			"eula=true\n",
		time.Now().UTC().Format(time.RFC3339), EulaURL)
	return os.WriteFile(path, []byte(content), 0o600)
}

// PreflightInput — данные для предстартовых проверок.
type PreflightInput struct {
	Port      int
	HeapBytes int64
	DiskPath  string
	// NeedDiskBytes — оценка того, сколько места потребуется под установку
	// и первый мир.
	NeedDiskBytes int64
}

// Preflight выполняет проверки, которые дешевле сделать до запуска, чем
// разбирать потом по логам.
func Preflight(in PreflightInput) []Finding {
	var out []Finding

	out = append(out, checkPrivileges()...)
	out = append(out, checkMemory(in.HeapBytes)...)
	out = append(out, checkDisk(in.DiskPath, in.NeedDiskBytes)...)
	out = append(out, checkPort(in.Port)...)

	return out
}

func checkPrivileges() []Finding {
	priv, err := platform.IsPrivileged()
	if err != nil || !priv {
		return nil
	}

	// На Unix это блокирующая ошибка: сервер Minecraft исполняет код плагинов
	// с правами своего процесса, и root здесь означает полный доступ к машине
	// при первом же вредоносном плагине.
	if !platform.IsWindows() {
		return []Finding{{
			Level:   LevelError,
			Message: "izba запущен от root",
			Hint: "Создайте отдельного пользователя и запускайте сервер от него:\n" +
				"  sudo adduser --system --group --home /opt/minecraft minecraft\n" +
				"  sudo chown -R minecraft:minecraft <каталог проекта>\n" +
				"  sudo -u minecraft izba up",
		}}
	}
	return []Finding{{
		Level:   LevelWarn,
		Message: "izba запущен от имени администратора",
		Hint:    "Для работы сервера это не требуется; обычная учётная запись безопаснее",
	}}
}

func checkMemory(heap int64) []Finding {
	totalRaw, err := platform.TotalMemoryBytes()
	if err != nil {
		return []Finding{{
			Level:   LevelInfo,
			Message: "объём оперативной памяти определить не удалось, проверка пропущена",
			Hint:    "Убедитесь сами, что памяти хватает под выбранный размер кучи",
		}}
	}

	total := int64(totalRaw)

	// Куче нужен запас сверху: метапространство, стеки потоков, прямые буферы
	// и сама ОС живут вне -Xmx.
	const overhead int64 = 1 << 30
	if heap+overhead > total {
		return []Finding{{
			Level: LevelError,
			Message: fmt.Sprintf(
				"запрошено %s под кучу при %s физической памяти",
				humanBytes(heap), humanBytes(total)),
			Hint: "JVM требует память сверх -Xmx. Уменьшите server.memory или поставьте auto",
		}}
	}
	if heap+2*overhead > total {
		return []Finding{{
			Level: LevelWarn,
			Message: fmt.Sprintf(
				"под кучу отдано %s из %s: системе остаётся мало",
				humanBytes(heap), humanBytes(total)),
			Hint: "При нехватке памяти ядро может убить процесс сервера",
		}}
	}
	return nil
}

func checkDisk(path string, need int64) []Finding {
	if path == "" {
		return nil
	}
	free, err := platform.FreeDiskBytes(path)
	if err != nil {
		return []Finding{{
			Level:   LevelInfo,
			Message: "свободное место на диске определить не удалось, проверка пропущена",
		}}
	}
	if int64(free) < need {
		return []Finding{{
			Level: LevelError,
			Message: fmt.Sprintf("на диске свободно %s, требуется около %s",
				humanBytes(int64(free)), humanBytes(need)),
			Hint: "Освободите место или выберите другой каталог для проекта",
		}}
	}
	return nil
}

func checkPort(port int) []Finding {
	addr := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return []Finding{{
			Level:   LevelError,
			Message: fmt.Sprintf("порт %d занят или недоступен: %v", port, err),
			Hint:    portHint(port),
		}}
	}
	_ = ln.Close()
	return nil
}

func portHint(port int) string {
	if platform.IsWindows() {
		return fmt.Sprintf("Посмотрите, кто занял порт:\n  netstat -ano | findstr :%d\n"+
			"Затем найдите процесс по PID в диспетчере задач", port)
	}
	return fmt.Sprintf("Посмотрите, кто занял порт:\n  ss -ltnp 'sport = :%d'", port)
}

// humanBytes форматирует размер для сообщений пользователю.
func humanBytes(b int64) string {
	switch {
	case b >= gib:
		return fmt.Sprintf("%.1f ГиБ", float64(b)/float64(gib))
	case b >= mib:
		return fmt.Sprintf("%.0f МиБ", float64(b)/float64(mib))
	default:
		return fmt.Sprintf("%d Б", b)
	}
}

// ErrPreflightFailed возвращается, когда проверки нашли блокирующую проблему.
var ErrPreflightFailed = errors.New("предстартовые проверки не пройдены")
