package mcserver

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	gib int64 = 1 << 30
	mib int64 = 1 << 20
)

// AutoMemory подбирает размер кучи по объёму оперативной памяти машины.
//
// Правило: системе оставляется четверть памяти, но не меньше 1 ГиБ и не
// больше 4 ГиБ. Результат ограничен сверху 12 ГиБ, потому что за пределами
// примерно 32 ГиБ JVM теряет сжатые указатели, а на игровом сервере огромная
// куча обычно означает лишь более долгие паузы сборщика мусора.
//
// total == 0 означает, что объём памяти определить не удалось; в этом случае
// возвращается консервативное значение.
func AutoMemory(total int64) int64 {
	const fallback = 2 * gib
	if total <= 0 {
		return fallback
	}

	reserve := total / 4
	if reserve < gib {
		reserve = gib
	}
	if reserve > 4*gib {
		reserve = 4 * gib
	}

	heap := total - reserve
	if heap < gib {
		heap = gib
	}
	if heap > 12*gib {
		heap = 12 * gib
	}
	return heap
}

// FormatMemoryFlag переводит байты в форму, понятную JVM.
func FormatMemoryFlag(bytes int64) string {
	if bytes%gib == 0 {
		return fmt.Sprintf("%dG", bytes/gib)
	}
	return fmt.Sprintf("%dM", bytes/mib)
}

// FlagsInput — всё, что нужно для расчёта аргументов JVM.
type FlagsInput struct {
	HeapBytes        int64
	Core             string // vanilla или paper
	MinecraftVersion string
	ExtraArgs        []string
}

// BuildFlags возвращает аргументы JVM.
//
// За основу взят общеизвестный набор флагов G1 для серверов Minecraft
// (так называемые флаги Aikar). Для куч больше 12 ГиБ часть параметров
// меняется согласно тому же набору.
func BuildFlags(in FlagsInput) []string {
	heap := FormatMemoryFlag(in.HeapBytes)

	// Xms равен Xmx намеренно: сервер всё равно быстро дорастает до предела,
	// а постепенное расширение кучи даёт лишние паузы.
	args := []string{
		"-Xms" + heap,
		"-Xmx" + heap,
		"-XX:+UseG1GC",
		"-XX:+ParallelRefProcEnabled",
		"-XX:MaxGCPauseMillis=200",
		"-XX:+UnlockExperimentalVMOptions",
		"-XX:+DisableExplicitGC",
		"-XX:+AlwaysPreTouch",
		"-XX:G1HeapWastePercent=5",
		"-XX:G1MixedGCCountTarget=4",
		"-XX:G1MixedGCLiveThresholdPercent=90",
		"-XX:G1RSetUpdatingPauseTimePercent=5",
		"-XX:SurvivorRatio=32",
		"-XX:+PerfDisableSharedMem",
		"-XX:MaxTenuringThreshold=1",
	}

	if in.HeapBytes >= 12*gib {
		args = append(args,
			"-XX:G1NewSizePercent=40",
			"-XX:G1MaxNewSizePercent=50",
			"-XX:G1HeapRegionSize=16M",
			"-XX:G1ReservePercent=15",
			"-XX:InitiatingHeapOccupancyPercent=20",
		)
	} else {
		args = append(args,
			"-XX:G1NewSizePercent=30",
			"-XX:G1MaxNewSizePercent=40",
			"-XX:G1HeapRegionSize=8M",
			"-XX:G1ReservePercent=20",
			"-XX:InitiatingHeapOccupancyPercent=15",
		)
	}

	if mitigation, ok := Log4ShellFlag(in.MinecraftVersion); ok {
		args = append(args, mitigation)
	}

	// Пользовательские аргументы идут последними: в JVM при конфликте
	// побеждает более поздний флаг.
	args = append(args, in.ExtraArgs...)
	return args
}

// classicVersionRe разбирает версии старой схемы вида 1.18 или 1.16.5.
//
// Mojang перешёл на схему вида 26.2, поэтому строки, не подходящие под этот
// шаблон, считаются заведомо новыми и в проверках уязвимостей не участвуют.
var classicVersionRe = regexp.MustCompile(`^1\.(\d+)(?:\.(\d+))?$`)

func parseClassicVersion(v string) (minor, patch int, ok bool) {
	m := classicVersionRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return 0, 0, false
	}
	minor, _ = strconv.Atoi(m[1])
	if m[2] != "" {
		patch, _ = strconv.Atoi(m[2])
	}
	return minor, patch, true
}

// Log4ShellFlag возвращает смягчающий флаг для версий, уязвимых к
// CVE-2021-44228, если этот флаг для них вообще действует.
//
// Флаг formatMsgNoLookups появился в Log4j 2.10, то есть работает начиная
// примерно с Minecraft 1.12. Для более старых версий нужен подменённый
// конфиг Log4j, поэтому там возвращается false, а пользователь получает
// отдельное предупреждение (см. Log4ShellWarning).
func Log4ShellFlag(version string) (string, bool) {
	minor, patch, ok := parseClassicVersion(version)
	if !ok {
		return "", false
	}
	vulnerable := minor >= 12 && (minor < 18 || (minor == 18 && patch == 0))
	if !vulnerable {
		return "", false
	}
	return "-Dlog4j2.formatMsgNoLookups=true", true
}

// Log4ShellWarning возвращает текст предупреждения для версий, где одного
// флага недостаточно, и пустую строку в остальных случаях.
func Log4ShellWarning(version string) string {
	minor, patch, ok := parseClassicVersion(version)
	if !ok {
		return ""
	}
	switch {
	case minor >= 8 && minor < 12:
		return fmt.Sprintf(
			"Minecraft %s использует уязвимую версию Log4j (CVE-2021-44228), "+
				"и флага -Dlog4j2.formatMsgNoLookups для неё недостаточно: требуется подменённый "+
				"конфиг Log4j от Mojang. Автоматически это пока не делается", version)
	case minor >= 12 && (minor < 18 || (minor == 18 && patch == 0)):
		return fmt.Sprintf(
			"Minecraft %s уязвим к Log4Shell (CVE-2021-44228); добавлен смягчающий флаг "+
				"-Dlog4j2.formatMsgNoLookups=true. Надёжнее обновиться на 1.18.1 или новее", version)
	default:
		return ""
	}
}
