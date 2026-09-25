// Package netx — единственная точка выхода в сеть.
//
// Два правила, которые здесь закреплены в коде, а не в договорённостях:
//
//  1. Разрешён только https и только хосты из allowlist. Это касается и
//     редиректов: каждый переход проверяется отдельно, иначе разрешённый хост
//     мог бы увести загрузку куда угодно.
//  2. Файл, скачанный без сверки хеша, не попадает на диск под своим именем.
package netx

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Version подставляется при сборке через -ldflags и попадает в User-Agent.
var Version = "0.1.0-dev"

// ContactURL обязателен для PaperMC: их API отклоняет обобщённые User-Agent и
// требует ссылку для связи. Значение переопределяется при сборке.
var ContactURL = "https://github.com/TheIIIrd/mc-izba"

// UserAgent собирается по формату, который требует PaperMC.
func UserAgent() string {
	return fmt.Sprintf("izba/%s (%s)", Version, ContactURL)
}

// DefaultAllowedHosts — полный список хостов, к которым izba обращается.
//
// Хосты githubusercontent нужны потому, что ссылки Adoptium ведут на релизы
// GitHub и оттуда редиректятся на объектное хранилище. Убрать их нельзя,
// не сломав установку Java.
var DefaultAllowedHosts = []string{
	// Mojang: манифест версий и серверные jar.
	"piston-meta.mojang.com",
	"piston-data.mojang.com",
	"launchermeta.mojang.com",
	"launcher.mojang.com",

	// PaperMC: сервис Fill v3 и его объектное хранилище.
	"fill.papermc.io",
	"fill-data.papermc.io",

	// Adoptium: метаданные и сами архивы JRE/JDK.
	"api.adoptium.net",
	"github.com",
	"objects.githubusercontent.com",
	"release-assets.githubusercontent.com",

	// Modrinth: плагины и моды (задействуется на следующем этапе).
	"api.modrinth.com",
	"cdn.modrinth.com",
}

// ErrHostNotAllowed возвращается при попытке обратиться к хосту вне списка.
var ErrHostNotAllowed = errors.New("хост не входит в список разрешённых")

// Client — HTTP-клиент с фиксированным allowlist.
type Client struct {
	http    *http.Client
	allowed map[string]struct{}

	// insecureOK снимает требование https. Ставится только из тестов, где
	// httptest поднимает обычный http-сервер.
	insecureOK bool
}

// NewClient создаёт клиент. Пустой список означает DefaultAllowedHosts.
func NewClient(hosts []string) *Client {
	if len(hosts) == 0 {
		hosts = DefaultAllowedHosts
	}
	allowed := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		allowed[strings.ToLower(h)] = struct{}{}
	}

	c := &Client{allowed: allowed}
	c.http = &http.Client{
		// Таймаут на запрос целиком не ставим: архив JRE весит под 200 МБ и
		// на медленном канале качается долго. Ограничиваем фазу установления
		// соединения и заголовков в транспорте.
		Transport: &http.Transport{
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
			ForceAttemptHTTP2:     true,
			Proxy:                 http.ProxyFromEnvironment,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("слишком много редиректов")
			}
			return c.checkURL(req.URL)
		},
	}
	return c
}

// checkURL проверяет схему и хост.
func (c *Client) checkURL(u *url.URL) error {
	if u.Scheme != "https" && !(c.insecureOK && u.Scheme == "http") {
		return fmt.Errorf("разрешён только https, получено %q для %s", u.Scheme, u.Redacted())
	}
	if _, ok := c.allowed[strings.ToLower(u.Hostname())]; !ok {
		return fmt.Errorf("%w: %s", ErrHostNotAllowed, u.Hostname())
	}
	return nil
}

// AllowHost добавляет хост в список. Нужен для тестов на httptest и для
// будущего явного согласия пользователя на сторонний источник.
func (c *Client) AllowHost(host string) {
	c.allowed[strings.ToLower(host)] = struct{}{}
}

// AllowInsecureForTest снимает требование https. Нужен тестам, где httptest
// поднимает обычный http-сервер.
//
// Метод экспортирован потому, что им пользуются тесты соседних пакетов, а
// значит он попадает и в обычный бинарник. Чтобы отключение https нельзя
// было вызвать случайно или из чужого кода, метод проверяет, что программа
// запущена как тест, и иначе паникует: лучше остановиться, чем молча
// разрешить загрузку по открытому каналу.
func (c *Client) AllowInsecureForTest() {
	if flag.Lookup("test.v") == nil {
		panic("netx: AllowInsecureForTest вызван вне тестов")
	}
	c.insecureOK = true
}

func (c *Client) do(ctx context.Context, rawURL string) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("некорректный URL %q: %w", rawURL, err)
	}
	if err := c.checkURL(u); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent())
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("запрос к %s не удался: %w", u.Redacted(), err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%s вернул статус %d: %s",
			u.Redacted(), resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp, nil
}

// GetJSON выполняет GET и разбирает тело как JSON.
//
// Размер ответа ограничен: метаданные заведомо небольшие, а неограниченное
// чтение в память из внешнего источника — плохая идея.
func (c *Client) GetJSON(ctx context.Context, rawURL string, out any) error {
	resp, err := c.do(ctx, rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	const maxJSON = 32 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJSON))
	if err != nil {
		return fmt.Errorf("не удалось прочитать ответ %s: %w", rawURL, err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("не удалось разобрать JSON из %s: %w", rawURL, err)
	}
	return nil
}

// GetRaw возвращает сырое тело ответа. Нужен там, где важен порядок ключей
// JSON-объекта и стандартный Unmarshal в map не подходит.
func (c *Client) GetRaw(ctx context.Context, rawURL string) ([]byte, error) {
	resp, err := c.do(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	const maxBody = 32 << 20
	return io.ReadAll(io.LimitReader(resp.Body, maxBody))
}

// Digest — ожидаемая контрольная сумма файла.
type Digest struct {
	Algo string // "sha1" или "sha256"
	Hex  string
}

// IsZero сообщает, что сумма не задана. Такие загрузки запрещены.
func (d Digest) IsZero() bool { return d.Algo == "" || d.Hex == "" }

func (d Digest) newHash() (hash.Hash, error) {
	switch strings.ToLower(d.Algo) {
	case "sha256":
		return sha256.New(), nil
	case "sha1":
		// sha1 остаётся только потому, что Mojang публикует именно его.
		// Для проверки целостности загрузки по https этого достаточно.
		return sha1.New(), nil
	default:
		return nil, fmt.Errorf("неизвестный алгоритм хеширования %q", d.Algo)
	}
}

// ProgressFunc вызывается по мере загрузки; total равен -1, если размер
// заранее неизвестен.
type ProgressFunc func(done, total int64)

// DownloadVerified скачивает файл, считает хеш на лету и только при совпадении
// перемещает его по целевому пути.
//
// При несовпадении временный файл удаляется: частично скачанный или подменённый
// файл не должен остаться на диске даже как мусор.
func (c *Client) DownloadVerified(ctx context.Context, rawURL string, want Digest, dest string, progress ProgressFunc) error {
	if want.IsZero() {
		return fmt.Errorf("отказ скачивать %s без контрольной суммы", rawURL)
	}
	h, err := want.newHash()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}

	resp, err := c.do(ctx, rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".download-*.part")
	if err != nil {
		return fmt.Errorf("не удалось создать временный файл: %w", err)
	}
	tmpName := tmp.Name()
	// Подстраховка: если функция завершится ошибкой в любой точке ниже,
	// временный файл не переживёт вызов.
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	total := resp.ContentLength
	var done int64
	buf := make([]byte, 256<<10)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := tmp.Write(buf[:n]); err != nil {
				return fmt.Errorf("ошибка записи во временный файл: %w", err)
			}
			h.Write(buf[:n])
			done += int64(n)
			if progress != nil {
				progress(done, total)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("ошибка чтения из %s: %w", rawURL, readErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want.Hex) {
		return fmt.Errorf(
			"контрольная сумма не совпала для %s:\n  ожидалось (%s): %s\n  получено:        %s\n"+
				"Файл удалён. Причиной может быть повреждение при загрузке или подмена на стороне сети",
			rawURL, want.Algo, want.Hex, got)
	}

	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("не удалось сбросить файл на диск: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("не удалось закрыть временный файл: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	// На Windows Rename не перезаписывает существующий файл, поэтому цель
	// снимается заранее.
	_ = os.Remove(dest)
	if err := os.Rename(tmpName, dest); err != nil {
		return fmt.Errorf("не удалось переместить файл в %s: %w", dest, err)
	}
	return nil
}

// VerifyFile пересчитывает хеш уже лежащего на диске файла. Используется для
// кеша: повторно скачивать проверенный файл не нужно, но и доверять ему
// без проверки нельзя.
func VerifyFile(path string, want Digest) error {
	if want.IsZero() {
		return errors.New("контрольная сумма не задана")
	}
	h, err := want.newHash()
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want.Hex) {
		return fmt.Errorf("контрольная сумма %s не совпала: ожидалось %s, получено %s", path, want.Hex, got)
	}
	return nil
}
