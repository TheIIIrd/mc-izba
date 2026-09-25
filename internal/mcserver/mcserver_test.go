package mcserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutoMemory(t *testing.T) {
	cases := []struct {
		name  string
		total int64
		want  int64
	}{
		{"память неизвестна", 0, 2 * gib},
		{"2 ГиБ", 2 * gib, 1 * gib},    // резерв 1 ГиБ
		{"8 ГиБ", 8 * gib, 6 * gib},    // резерв 2 ГиБ
		{"16 ГиБ", 16 * gib, 12 * gib}, // резерв 4 ГиБ
		{"64 ГиБ", 64 * gib, 12 * gib}, // ограничение сверху
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AutoMemory(c.total); got != c.want {
				t.Errorf("AutoMemory(%d) = %d, ожидалось %d", c.total, got, c.want)
			}
		})
	}
}

// Запрошенная куча никогда не должна превышать физическую память.
func TestAutoMemoryNeverExceedsTotal(t *testing.T) {
	for _, total := range []int64{gib, 2 * gib, 3 * gib, 6 * gib, 32 * gib} {
		if got := AutoMemory(total); got > total {
			t.Errorf("при %d байт памяти выделено %d", total, got)
		}
	}
}

func TestFormatMemoryFlag(t *testing.T) {
	if got := FormatMemoryFlag(6 * gib); got != "6G" {
		t.Errorf("получено %q, ожидалось 6G", got)
	}
	if got := FormatMemoryFlag(1536 * mib); got != "1536M" {
		t.Errorf("получено %q, ожидалось 1536M", got)
	}
}

func TestBuildFlagsHeapConsistency(t *testing.T) {
	args := BuildFlags(FlagsInput{HeapBytes: 4 * gib, Core: "paper", MinecraftVersion: "1.21.4"})
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "-Xms4G") || !strings.Contains(joined, "-Xmx4G") {
		t.Errorf("Xms и Xmx должны совпадать: %s", joined)
	}
	if !strings.Contains(joined, "-XX:+UseG1GC") {
		t.Errorf("отсутствует G1GC: %s", joined)
	}
	if strings.Contains(joined, "formatMsgNoLookups") {
		t.Errorf("для 1.21.4 смягчение Log4Shell не нужно: %s", joined)
	}
}

func TestBuildFlagsLargeHeapVariant(t *testing.T) {
	small := strings.Join(BuildFlags(FlagsInput{HeapBytes: 4 * gib}), " ")
	large := strings.Join(BuildFlags(FlagsInput{HeapBytes: 16 * gib}), " ")

	if !strings.Contains(small, "G1HeapRegionSize=8M") {
		t.Errorf("для малой кучи ожидался регион 8M: %s", small)
	}
	if !strings.Contains(large, "G1HeapRegionSize=16M") {
		t.Errorf("для большой кучи ожидался регион 16M: %s", large)
	}
}

// Пользовательские аргументы должны идти последними, иначе они не смогут
// переопределить расчётные флаги.
func TestBuildFlagsExtraArgsGoLast(t *testing.T) {
	args := BuildFlags(FlagsInput{HeapBytes: 2 * gib, ExtraArgs: []string{"-Dfoo=bar"}})
	if args[len(args)-1] != "-Dfoo=bar" {
		t.Errorf("пользовательский аргумент не последний: %v", args)
	}
}

func TestLog4ShellDetection(t *testing.T) {
	cases := []struct {
		version  string
		wantFlag bool
		wantWarn bool
	}{
		{"1.16.5", true, true},
		{"1.17.1", true, true},
		{"1.18", true, true},     // 1.18 == 1.18.0, уязвима
		{"1.18.1", false, false}, // исправлено
		{"1.21.4", false, false},
		{"1.8.9", false, true}, // флаг не поможет, но предупредить надо
		{"26.2", false, false}, // новая схема версий, заведомо не затронута
		{"latest", false, false},
	}
	for _, c := range cases {
		t.Run(c.version, func(t *testing.T) {
			_, ok := Log4ShellFlag(c.version)
			if ok != c.wantFlag {
				t.Errorf("Log4ShellFlag(%q) = %v, ожидалось %v", c.version, ok, c.wantFlag)
			}
			warn := Log4ShellWarning(c.version) != ""
			if warn != c.wantWarn {
				t.Errorf("Log4ShellWarning(%q) наличие = %v, ожидалось %v", c.version, warn, c.wantWarn)
			}
		})
	}
}

// --- server.properties ----------------------------------------------------

func TestSecureDefaults(t *testing.T) {
	p := SecureDefaults(25565, true)
	for key, want := range map[string]string{
		"online-mode":  "true",
		"enable-rcon":  "false",
		"enable-query": "false",
	} {
		if p[key] != want {
			t.Errorf("%s = %q, ожидалось %q", key, p[key], want)
		}
	}
}

// Кириллический MOTD должен пережить запись и чтение: файл читается Java как
// ISO-8859-1, поэтому значения экранируются в \uXXXX.
func TestPropertiesRoundTripNonASCII(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.properties")

	in := Properties{"motd": "Привет, мир! 🎮", "max-players": "20"}
	if err := WriteProperties(path, in); err != nil {
		t.Fatal(err)
	}

	out, err := ReadProperties(path)
	if err != nil {
		t.Fatal(err)
	}
	if out["motd"] != in["motd"] {
		t.Errorf("MOTD не пережил запись и чтение: %q != %q", out["motd"], in["motd"])
	}
}

func TestPropertiesFileIsASCIIOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.properties")
	if err := WriteProperties(path, Properties{"motd": "Привет"}); err != nil {
		t.Fatal(err)
	}
	data := readFile(t, path)
	for i := 0; i < len(data); i++ {
		if data[i] > 127 {
			t.Fatalf("в файле остался не-ASCII байт на позиции %d", i)
		}
	}
}

func TestAuditProperties(t *testing.T) {
	// Отключённая аутентификация мимо конфига — регрессия безопасности:
	// кто-то правил server.properties, и запуск нужно остановить.
	t.Run("online-mode=false без подтверждения в конфиге — ошибка", func(t *testing.T) {
		fs := AuditProperties(Properties{"online-mode": "false"}, false)
		if !HasErrors(fs) {
			t.Fatalf("ожидалась блокирующая находка, получено %+v", fs)
		}
	})

	// Тот же файл, но выбор зафиксирован в izba.toml: это осознанное
	// решение пользователя, блокировать его на каждом запуске неправильно.
	t.Run("online-mode=false с подтверждением — предупреждение", func(t *testing.T) {
		fs := AuditProperties(Properties{"online-mode": "false"}, true)
		if len(fs) == 0 {
			t.Fatal("предупредить всё равно нужно")
		}
		if HasErrors(fs) {
			t.Fatalf("осознанный выбор не должен блокировать запуск: %+v", fs)
		}
	})

	t.Run("RCON с пустым паролем — ошибка", func(t *testing.T) {
		fs := AuditProperties(Properties{"enable-rcon": "true", "rcon.password": ""}, false)
		if !HasErrors(fs) {
			t.Fatalf("ожидалась блокирующая находка, получено %+v", fs)
		}
	})

	t.Run("query — предупреждение", func(t *testing.T) {
		fs := AuditProperties(Properties{"enable-query": "true"}, false)
		if len(fs) == 0 || HasErrors(fs) {
			t.Fatalf("ожидалось предупреждение без блокировки, получено %+v", fs)
		}
	})

	t.Run("безопасные настройки — тишина", func(t *testing.T) {
		if fs := AuditProperties(SecureDefaults(25565, true), false); len(fs) != 0 {
			t.Fatalf("на безопасных настройках находок быть не должно: %+v", fs)
		}
	})
}

// Значение online-mode должно приходить из конфига, а не оставаться
// захардкоженным в наборе умолчаний.
func TestSecureDefaultsRespectsOfflineMode(t *testing.T) {
	if got := SecureDefaults(25565, false)["online-mode"]; got != "false" {
		t.Errorf("online-mode = %q, ожидалось false", got)
	}
}

func TestMergeOverridesDefaults(t *testing.T) {
	merged := SecureDefaults(25565, true).Merge(map[string]string{"Max-Players": "40"})
	if merged["max-players"] != "40" {
		t.Errorf("ключи должны приводиться к нижнему регистру и переопределяться: %q", merged["max-players"])
	}
}

// --- EULA -----------------------------------------------------------------

func TestEulaLifecycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "eula.txt")

	if EulaAccepted(path) {
		t.Fatal("отсутствующий файл не должен считаться согласием")
	}
	if err := WriteEulaAccepted(path); err != nil {
		t.Fatal(err)
	}
	if !EulaAccepted(path) {
		t.Fatal("после записи согласие должно распознаваться")
	}
}

func TestEulaFalseIsNotAccepted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "eula.txt")
	writeFileT(t, path, "#comment\neula=false\n")
	if EulaAccepted(path) {
		t.Fatal("eula=false не является согласием")
	}
}

// --- диагностика ----------------------------------------------------------

func TestDiagnose(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{"порт занят", "[Server thread/WARN]: **** FAILED TO BIND TO PORT!", "порт"},
		{"нехватка памяти", "java.lang.OutOfMemoryError: Java heap space", "память"},
		{"несовместимая java", "java.lang.UnsupportedClassVersionError: x", "Java"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := Diagnose([]string{c.line}, t.TempDir())
			if len(fs) == 0 {
				t.Fatalf("ситуация не распознана: %q", c.line)
			}
			if !strings.Contains(fs[0].Message, c.want) {
				t.Errorf("сообщение %q не содержит %q", fs[0].Message, c.want)
			}
			if fs[0].Hint == "" {
				t.Error("находка без подсказки бесполезна пользователю")
			}
		})
	}
}

// --- вспомогательное ------------------------------------------------------

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
