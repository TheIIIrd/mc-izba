// Package archive распаковывает архивы, полученные из сети.
//
// Архив из интернета — это недоверенные данные, и распаковка наивным циклом
// по записям является классической дырой. Здесь закрыты три типовые атаки:
//
//   - zip slip: имя вида ../../etc/cron.d/x или абсолютный путь выводит запись
//     за пределы целевого каталога;
//   - symlink escape: ссылка, указывающая наружу, превращает последующую
//     запись в перезапись произвольного файла;
//   - архивная бомба: несколько килобайт разворачиваются в терабайты.
//
// Дополнительно распаковка идёт во временный каталог и переносится на место
// целиком, чтобы после сбоя не оставалось наполовину установленной Java.
package archive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Limits ограничивают результат распаковки.
type Limits struct {
	MaxTotalBytes int64 // суммарный размер распакованного
	MaxFiles      int   // количество записей
	MaxFileBytes  int64 // размер одного файла
}

// DefaultLimits рассчитаны на архив JDK: он самый крупный из того, что
// распаковывает izba (около 200 МБ и порядка 20 тысяч файлов).
func DefaultLimits() Limits {
	return Limits{
		MaxTotalBytes: 2 << 30, // 2 ГиБ
		MaxFiles:      100_000,
		MaxFileBytes:  1 << 30, // 1 ГиБ
	}
}

// ErrUnsafeEntry возвращается при обнаружении опасной записи в архиве.
var ErrUnsafeEntry = errors.New("небезопасная запись в архиве")

// safeJoin проверяет имя записи и возвращает путь внутри dst.
//
// Важно именно отвергать опасные имена, а не «обезвреживать» их через Clean.
// Clean превратил бы ../evil.txt в evil.txt и файл лёг бы внутрь каталога —
// формально безопасно, но архив с такими именами либо повреждён, либо
// сконструирован злонамеренно, и устанавливать его не следует.
//
// filepath.IsLocal берёт на себя разбор "..", абсолютных путей, а на Windows
// ещё и зарезервированных имён устройств вроде CON или NUL и путей с двоеточием.
func safeJoin(dst, name string) (string, error) {
	// Внутри архивов разделителем считается "/". Обратный слеш встречается в
	// архивах, собранных на Windows, и тоже трактуется как разделитель:
	// иначе имя ..\evil прошло бы проверку как обычный файл.
	slashed := strings.ReplaceAll(name, `\`, "/")
	slashed = strings.TrimRight(slashed, "/")
	if slashed == "" || slashed == "." {
		return "", fmt.Errorf("%w: пустое имя записи", ErrUnsafeEntry)
	}

	local := filepath.FromSlash(slashed)
	if !filepath.IsLocal(local) {
		return "", fmt.Errorf("%w: %q выходит за пределы целевого каталога", ErrUnsafeEntry, name)
	}
	return filepath.Join(dst, local), nil
}

// counter отслеживает лимиты в процессе распаковки.
type counter struct {
	lim   Limits
	files int
	bytes int64
}

func (c *counter) addFile() error {
	c.files++
	if c.lim.MaxFiles > 0 && c.files > c.lim.MaxFiles {
		return fmt.Errorf("%w: превышено число файлов (%d)", ErrUnsafeEntry, c.lim.MaxFiles)
	}
	return nil
}

func (c *counter) addBytes(n int64) error {
	c.bytes += n
	if c.lim.MaxTotalBytes > 0 && c.bytes > c.lim.MaxTotalBytes {
		return fmt.Errorf("%w: превышен суммарный размер распакованного (%d байт)",
			ErrUnsafeEntry, c.lim.MaxTotalBytes)
	}
	return nil
}

// copyLimited копирует не более MaxFileBytes и учитывает общий объём.
//
// Ограничение применяется к фактически прочитанным байтам, а не к заявленному
// в заголовке размеру: заголовку архива доверять нельзя.
func copyLimited(dst io.Writer, src io.Reader, c *counter) error {
	limit := c.lim.MaxFileBytes
	if limit <= 0 {
		limit = c.lim.MaxTotalBytes
	}
	// +1 байт, чтобы отличить «ровно предел» от «предел превышен».
	n, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("%w: файл превышает допустимый размер (%d байт)", ErrUnsafeEntry, limit)
	}
	return c.addBytes(n)
}

func writeFile(target string, mode os.FileMode, src io.Reader, c *counter) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	// Бит исполнения сохраняется (он нужен для bin/java), остальные права
	// нормализуются: доверять маске из архива незачем.
	perm := os.FileMode(0o644)
	if mode&0o111 != 0 {
		perm = 0o755
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_EXCL, perm)
	if err != nil {
		// O_EXCL защищает от повторной записи в один и тот же путь: это
		// признак либо дубликата в архиве, либо попытки подмены.
		return fmt.Errorf("не удалось создать %s: %w", target, err)
	}
	defer f.Close()

	if err := copyLimited(f, src, c); err != nil {
		return err
	}
	return f.Close()
}

// ExtractZip распаковывает zip в dst.
func ExtractZip(src, dst string, lim Limits) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("не удалось открыть zip %s: %w", src, err)
	}
	defer r.Close()

	c := &counter{lim: lim}
	for _, f := range r.File {
		target, err := safeJoin(dst, f.Name)
		if err != nil {
			return err
		}
		info := f.FileInfo()

		switch {
		case info.IsDir():
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("%w: символические ссылки запрещены (%s)", ErrUnsafeEntry, f.Name)
		case !info.Mode().IsRegular():
			return fmt.Errorf("%w: нерегулярный файл %s", ErrUnsafeEntry, f.Name)
		default:
			if err := c.addFile(); err != nil {
				return err
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			err = writeFile(target, info.Mode(), rc, c)
			_ = rc.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// ExtractTarGz распаковывает tar.gz в dst.
func ExtractTarGz(src, dst string, lim Limits) error {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("не удалось открыть архив %s: %w", src, err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("не удалось открыть gzip-поток %s: %w", src, err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	c := &counter{lim: lim}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("ошибка чтения tar %s: %w", src, err)
		}

		target, err := safeJoin(dst, hdr.Name)
		if err != nil {
			return err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := c.addFile(); err != nil {
				return err
			}
			if err := writeFile(target, os.FileMode(hdr.Mode), tr, c); err != nil {
				return err
			}
		case tar.TypeSymlink, tar.TypeLink:
			// Архивы Temurin содержат несколько символических ссылок внутри
			// legal/. Они не нужны для работы Java, а разрешать ссылки ради
			// них означало бы открыть целый класс атак.
			return fmt.Errorf("%w: ссылки запрещены (%s -> %s)", ErrUnsafeEntry, hdr.Name, hdr.Linkname)
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			// Служебные записи pax, содержимого не несут.
			continue
		default:
			return fmt.Errorf("%w: неподдерживаемый тип записи %q в %s", ErrUnsafeEntry, hdr.Typeflag, hdr.Name)
		}
	}
}

// Extract выбирает распаковщик по расширению.
func Extract(src, dst string, lim Limits) error {
	lower := strings.ToLower(src)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return ExtractZip(src, dst, lim)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return ExtractTarGz(src, dst, lim)
	default:
		return fmt.Errorf("неизвестный формат архива: %s", src)
	}
}

// SingleRootDir возвращает единственный подкаталог dir.
//
// Архивы Temurin разворачиваются в каталог вида jdk-21.0.5+11-jre. Имя заранее
// неизвестно, поэтому после распаковки содержимое этого каталога поднимается
// на уровень выше.
func SingleRootDir(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) != 1 || len(entries) != 1 {
		return "", fmt.Errorf("ожидался ровно один корневой каталог в %s, найдено %d записей", dir, len(entries))
	}
	return filepath.Join(dir, dirs[0]), nil
}
