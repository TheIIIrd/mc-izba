// Package config читает и пишет izba.toml — декларативное описание того,
// какой сервер нужен пользователю.
//
// Конфиг описывает намерение («последний стабильный Paper»), а не результат.
// Что именно получилось установить, фиксируется в izba.lock.
package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config — содержимое izba.toml.
type Config struct {
	Server     ServerSection     `toml:"server"`
	Java       JavaSection       `toml:"java"`
	Runtime    RuntimeSection    `toml:"runtime"`
	Properties map[string]string `toml:"properties"`
}

// ServerSection описывает само ядро.
type ServerSection struct {
	Core    string `toml:"core"`
	Version string `toml:"version"`
	Port    int    `toml:"port"`
	Memory  string `toml:"memory"`

	// OnlineMode включает проверку учётных записей через сервисы Mojang.
	//
	// Вынесено в отдельное поле, а не оставлено в [properties], потому что
	// это единственная настройка, от которой зависит, сможет ли посторонний
	// зайти на сервер под ником администратора. Такое решение должно
	// приниматься явно и быть видно в конфиге с первого взгляда.
	OnlineMode bool `toml:"online_mode"`
}

// JavaSection позволяет не скачивать Java, а использовать уже имеющуюся.
type JavaSection struct {
	// Path указывает на JAVA_HOME. Пустое значение означает, что izba
	// скачает Temurin в каталог проекта.
	Path string `toml:"path"`
}

// RuntimeSection управляет запуском.
type RuntimeSection struct {
	// StartTimeout — сколько ждать строки Done в логе. Первый запуск с
	// генерацией мира на слабой машине занимает минуты.
	StartTimeout string `toml:"start_timeout"`
	// ExtraJvmArgs добавляются последними и могут переопределить расчётные
	// флаги. Небезопасные параметры отсекаются при валидации.
	ExtraJvmArgs []string `toml:"extra_jvm_args"`
}

// Default возвращает конфиг со значениями по умолчанию.
func Default() Config {
	return Config{
		Server: ServerSection{
			Core:       "paper",
			Version:    "latest",
			Port:       25565,
			Memory:     "auto",
			OnlineMode: true,
		},
		Runtime: RuntimeSection{
			StartTimeout: "5m",
		},
		Properties: map[string]string{
			"motd":        "Сервер собран с помощью izba",
			"max-players": "20",
			"difficulty":  "normal",
		},
	}
}

// Load читает конфиг с диска и валидирует его.
func Load(path string) (Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, fmt.Errorf("конфиг %s не найден: выполните izba init", path)
		}
		return Config{}, err
	}

	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("ошибка в %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		// Опечатка в имени ключа не должна тихо приводить к значению по
		// умолчанию: пользователь будет уверен, что настройка применена.
		var keys []string
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		return Config{}, fmt.Errorf("в %s есть неизвестные ключи: %s", path, strings.Join(keys, ", "))
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("ошибка в %s: %w", path, err)
	}
	return cfg, nil
}

// Save записывает конфиг. Права 0o600: рядом будут лежать секреты.
func Save(path string, cfg Config) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	header := "# Конфигурация izba.\n" +
		"# Описывает желаемое состояние сервера. Точные версии и контрольные\n" +
		"# суммы установленного фиксируются в izba.lock.\n\n"
	if _, err := f.WriteString(header); err != nil {
		return err
	}
	if err := toml.NewEncoder(f).Encode(cfg); err != nil {
		return err
	}
	return f.Close()
}

// Опасные флаги JVM. Удалённый отладчик или незащищённый JMX превращают
// игровой сервер в готовую точку удалённого исполнения кода, а в чужих
// конфигах с форумов они встречаются регулярно.
var forbiddenJvmArgs = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^-agentlib:jdwp`),
	regexp.MustCompile(`(?i)^-Xrunjdwp`),
	regexp.MustCompile(`(?i)^-Dcom\.sun\.management\.jmxremote`),
}

// Validate проверяет значения и возвращает понятное сообщение об ошибке.
func (c Config) Validate() error {
	switch strings.ToLower(c.Server.Core) {
	case "vanilla", "paper":
	default:
		return fmt.Errorf("server.core = %q: поддерживаются только vanilla и paper", c.Server.Core)
	}

	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port = %d: порт должен быть в диапазоне 1..65535", c.Server.Port)
	}
	if c.Server.Port < 1024 {
		return fmt.Errorf("server.port = %d: порты ниже 1024 требуют повышенных прав, "+
			"а izba не работает с ними намеренно", c.Server.Port)
	}

	// Два источника истины для одной настройки — гарантированная путаница,
	// поэтому дублирование отвергается, а не разрешается «по приоритету».
	for key := range c.Properties {
		if strings.EqualFold(strings.TrimSpace(key), "online-mode") {
			return fmt.Errorf(
				"online-mode задан в [properties]: используйте server.online_mode, " +
					"иначе получилось бы два несогласованных источника одной настройки")
		}
	}

	if _, err := c.MemoryBytes(); err != nil {
		return err
	}
	if _, err := c.StartTimeout(); err != nil {
		return err
	}

	for _, arg := range c.Runtime.ExtraJvmArgs {
		for _, re := range forbiddenJvmArgs {
			if re.MatchString(strings.TrimSpace(arg)) {
				return fmt.Errorf(
					"runtime.extra_jvm_args содержит %q: этот флаг открывает удалённый доступ "+
						"к JVM и не допускается", arg)
			}
		}
	}
	return nil
}

// MemoryAuto означает, что объём памяти рассчитывается автоматически.
const MemoryAuto int64 = 0

var memRe = regexp.MustCompile(`^(\d+)\s*([kmgtKMGT]?)[bB]?$`)

// MemoryBytes разбирает server.memory.
//
// Возвращает MemoryAuto для значения "auto".
func (c Config) MemoryBytes() (int64, error) {
	raw := strings.TrimSpace(strings.ToLower(c.Server.Memory))
	if raw == "" || raw == "auto" {
		return MemoryAuto, nil
	}

	m := memRe.FindStringSubmatch(raw)
	if m == nil {
		return 0, fmt.Errorf("server.memory = %q: ожидается auto или число с суффиксом, например 6G или 4096M", c.Server.Memory)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("server.memory = %q: %w", c.Server.Memory, err)
	}

	var mult int64 = 1
	switch strings.ToLower(m[2]) {
	case "k":
		mult = 1 << 10
	case "m":
		mult = 1 << 20
	case "g":
		mult = 1 << 30
	case "t":
		mult = 1 << 40
	}
	bytes := n * mult

	const minHeap = 512 << 20
	if bytes < minHeap {
		return 0, fmt.Errorf("server.memory = %q: минимум 512M", c.Server.Memory)
	}
	return bytes, nil
}

// StartTimeout разбирает runtime.start_timeout.
func (c Config) StartTimeout() (time.Duration, error) {
	raw := strings.TrimSpace(c.Runtime.StartTimeout)
	if raw == "" {
		return 5 * time.Minute, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("runtime.start_timeout = %q: %w", raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("runtime.start_timeout = %q: должно быть положительным", raw)
	}
	return d, nil
}
