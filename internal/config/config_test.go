package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"izba/internal/netx"
	"izba/internal/resolve"
)

func TestMemoryBytes(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"auto", MemoryAuto, false},
		{"", MemoryAuto, false},
		{"6G", 6 << 30, false},
		{"4096M", 4096 << 20, false},
		{"2g", 2 << 30, false},
		{"2GB", 2 << 30, false},
		{"256M", 0, true}, // ниже минимума
		{"много", 0, true},
		{"-4G", 0, true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			cfg := Default()
			cfg.Server.Memory = c.in
			got, err := cfg.MemoryBytes()
			if c.wantErr {
				if err == nil {
					t.Fatalf("ожидалась ошибка для %q", c.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("неожиданная ошибка: %v", err)
			}
			if got != c.want {
				t.Errorf("получено %d, ожидалось %d", got, c.want)
			}
		})
	}
}

func TestValidateRejectsPrivilegedPort(t *testing.T) {
	cfg := Default()
	cfg.Server.Port = 80
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "1024") {
		t.Fatalf("порты ниже 1024 должны отвергаться, получено: %v", err)
	}
}

func TestValidateRejectsUnknownCore(t *testing.T) {
	cfg := Default()
	cfg.Server.Core = "forge"
	if err := cfg.Validate(); err == nil {
		t.Fatal("неподдерживаемое ядро должно отвергаться")
	}
}

// Отладочный порт JVM — готовая точка удалённого исполнения кода, и такие
// флаги легко переползают из чужих конфигов копипастой.
func TestValidateRejectsRemoteDebugFlags(t *testing.T) {
	for _, arg := range []string{
		"-agentlib:jdwp=transport=dt_socket,server=y,address=5005",
		"-Xrunjdwp:transport=dt_socket",
		"-Dcom.sun.management.jmxremote.port=9010",
	} {
		t.Run(arg, func(t *testing.T) {
			cfg := Default()
			cfg.Runtime.ExtraJvmArgs = []string{arg}
			if err := cfg.Validate(); err == nil {
				t.Fatalf("флаг %q должен отвергаться", arg)
			}
		})
	}
}

func TestValidateAllowsHarmlessJvmArgs(t *testing.T) {
	cfg := Default()
	cfg.Runtime.ExtraJvmArgs = []string{"-Dfile.encoding=UTF-8", "-XX:+UseStringDeduplication"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("безобидные флаги не должны блокироваться: %v", err)
	}
}

// online-mode должен задаваться ровно одним способом, иначе два источника
// истины рано или поздно разойдутся.
func TestValidateRejectsOnlineModeInProperties(t *testing.T) {
	cfg := Default()
	cfg.Properties["online-mode"] = "false"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "server.online_mode") {
		t.Fatalf("дублирование online-mode должно отвергаться, получено: %v", err)
	}
}

func TestOnlineModeDefaultsToTrue(t *testing.T) {
	if !Default().Server.OnlineMode {
		t.Fatal("по умолчанию аутентификация должна быть включена")
	}
}

// Ключ отсутствует в файле — значение остаётся включённым, а не
// обнуляется в false нулевым значением Go.
func TestOnlineModeStaysTrueWhenAbsentInFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "izba.toml")
	if err := os.WriteFile(path, []byte("[server]\ncore = \"vanilla\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Server.OnlineMode {
		t.Fatal("отсутствие ключа не должно выключать аутентификацию")
	}
}

func TestOnlineModeCanBeDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "izba.toml")
	if err := os.WriteFile(path, []byte("[server]\nonline_mode = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.OnlineMode {
		t.Fatal("online_mode = false не применился")
	}
}

func TestStartTimeout(t *testing.T) {
	cfg := Default()
	cfg.Runtime.StartTimeout = "90s"
	d, err := cfg.StartTimeout()
	if err != nil {
		t.Fatal(err)
	}
	if d != 90*time.Second {
		t.Errorf("получено %v", d)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "izba.toml")

	in := Default()
	in.Server.Core = "vanilla"
	in.Server.Version = "1.21.4"
	in.Server.Memory = "6G"
	in.Properties["motd"] = "Привет"

	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if out.Server.Core != "vanilla" || out.Server.Version != "1.21.4" || out.Server.Memory != "6G" {
		t.Errorf("значения не совпали: %+v", out.Server)
	}
	if out.Properties["motd"] != "Привет" {
		t.Errorf("кириллица в properties потерялась: %q", out.Properties["motd"])
	}
}

// Опечатка в имени ключа не должна тихо приводить к значению по умолчанию:
// пользователь будет уверен, что настройка применена.
func TestLoadRejectsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "izba.toml")
	content := "[server]\ncore = \"paper\"\nmemry = \"6G\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "неизвестные ключи") {
		t.Fatalf("опечатка в ключе должна приводить к ошибке, получено: %v", err)
	}
}

func TestLoadMissingFileGivesHint(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "нет.toml"))
	if err == nil || !strings.Contains(err.Error(), "izba init") {
		t.Fatalf("сообщение должно подсказывать следующий шаг, получено: %v", err)
	}
}

func TestLockRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "izba.lock")

	l, err := LoadLock(path)
	if err != nil {
		t.Fatalf("отсутствие lock-файла не должно быть ошибкой: %v", err)
	}

	l.Core = &resolve.CoreResolution{
		Core:             resolve.CorePaper,
		MinecraftVersion: "1.21.4",
		Build:            "48",
		JavaMajor:        21,
		Jar: resolve.Artifact{
			URL:    "https://fill-data.papermc.io/v1/objects/x/paper.jar",
			Digest: netx.Digest{Algo: "sha256", Hex: "abc"},
		},
	}
	if err := SaveLock(path, l, "0.1.0-test"); err != nil {
		t.Fatal(err)
	}

	got, err := LoadLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Core == nil || got.Core.Build != "48" || got.Core.Jar.Digest.Hex != "abc" {
		t.Fatalf("lock прочитан неверно: %+v", got.Core)
	}
	if got.SchemaVersion != LockVersion {
		t.Errorf("версия схемы: %d", got.SchemaVersion)
	}
}

func TestLockRejectsNewerSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "izba.lock")
	if err := os.WriteFile(path, []byte(`{"schema_version": 99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLock(path); err == nil {
		t.Fatal("lock более новой схемы должен отвергаться, а не читаться частично")
	}
}
