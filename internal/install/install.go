// Package install выполняет этапы fetch и layout: скачивает артефакты,
// проверяет их и раскладывает по каталогам проекта.
//
// Все операции идемпотентны. Если в lock-файле записано то же самое, что
// требуется установить, и файлы на месте и проходят проверку хеша, работа не
// выполняется повторно.
package install

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"izba/internal/archive"
	"izba/internal/netx"
	"izba/internal/paths"
	"izba/internal/platform"
	"izba/internal/resolve"
)

// Logf — функция вывода прогресса.
type Logf func(format string, args ...any)

// Installer связывает сеть, кеш и раскладку каталогов.
type Installer struct {
	Client *netx.Client
	Layout paths.Layout
	Log    Logf
}

func (i Installer) logf(format string, args ...any) {
	if i.Log != nil {
		i.Log(format, args...)
	}
}

// cachePath формирует имя файла в кеше на основе хеша.
//
// Хеш в имени означает, что разные сборки не перетирают друг друга, а
// повторная установка той же версии обходится без скачивания.
func (i Installer) cachePath(a resolve.Artifact) string {
	name := fmt.Sprintf("%s-%s-%s", a.Digest.Algo, shortHex(a.Digest.Hex), safeName(a.Filename))
	return filepath.Join(i.Layout.Cache(), name)
}

func shortHex(h string) string {
	if len(h) > 16 {
		return h[:16]
	}
	return h
}

// safeName убирает из имени всё, что может увести файл за пределы кеша.
func safeName(n string) string {
	n = filepath.Base(n)
	n = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '.', r == '-', r == '_', r == '+':
			return r
		default:
			return '-'
		}
	}, n)
	if n == "" || n == "." || n == ".." {
		return "artifact"
	}
	return n
}

// fetch возвращает путь к проверенному файлу в кеше, скачивая его при
// необходимости.
func (i Installer) fetch(ctx context.Context, a resolve.Artifact, label string) (string, error) {
	dest := i.cachePath(a)

	if _, err := os.Stat(dest); err == nil {
		// Файл из кеша всё равно проверяется: за время между запусками с ним
		// могло случиться что угодно.
		if err := netx.VerifyFile(dest, a.Digest); err == nil {
			i.logf("  %s: уже в кеше", label)
			return dest, nil
		}
		i.logf("  %s: файл в кеше повреждён, скачиваю заново", label)
		_ = os.Remove(dest)
	}

	i.logf("  %s: загрузка %s", label, humanSize(a.Size))
	err := i.Client.DownloadVerified(ctx, a.URL, a.Digest, dest, nil)
	if err != nil {
		return "", err
	}
	return dest, nil
}

// InstallCore кладёт jar ядра в server/server.jar.
func (i Installer) InstallCore(ctx context.Context, res resolve.CoreResolution) error {
	src, err := i.fetch(ctx, res.Jar, "ядро")
	if err != nil {
		return err
	}

	dst := i.Layout.ServerJar()
	if err := copyFile(src, dst, 0o600); err != nil {
		return fmt.Errorf("не удалось разместить ядро: %w", err)
	}
	return nil
}

// CoreUpToDate сообщает, что установленный jar соответствует резолву.
func (i Installer) CoreUpToDate(res resolve.CoreResolution) bool {
	return netx.VerifyFile(i.Layout.ServerJar(), res.Jar.Digest) == nil
}

// InstallJava распаковывает среду выполнения и возвращает путь к JAVA_HOME.
//
// Распаковка идёт во временный каталог внутри проекта и переносится на место
// целиком: прерывание на середине не оставит частично установленную Java.
func (i Installer) InstallJava(ctx context.Context, res resolve.JavaResolution) (string, error) {
	src, err := i.fetch(ctx, res.Archive, "java")
	if err != nil {
		return "", err
	}

	target := filepath.Join(i.Layout.Java(), safeName(res.Release))
	if isUsableJavaHome(target) {
		i.logf("  java: уже установлена (%s)", res.Release)
		return target, nil
	}

	staging, err := os.MkdirTemp(i.Layout.Tmp(), "java-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)

	i.logf("  java: распаковка %s", res.Release)
	if err := archive.Extract(src, staging, archive.DefaultLimits()); err != nil {
		return "", fmt.Errorf("не удалось распаковать Java: %w", err)
	}

	// Архивы Temurin содержат один корневой каталог с именем релиза.
	root, err := archive.SingleRootDir(staging)
	if err != nil {
		return "", fmt.Errorf("неожиданная структура архива Java: %w", err)
	}
	// На macOS содержимое лежит глубже, в Contents/Home.
	if inner := filepath.Join(root, "Contents", "Home"); dirExists(inner) {
		root = inner
	}

	_ = os.RemoveAll(target)
	if err := os.Rename(root, target); err != nil {
		return "", fmt.Errorf("не удалось переместить Java в %s: %w", target, err)
	}

	if !isUsableJavaHome(target) {
		return "", fmt.Errorf("в распакованной Java не найден исполняемый файл %s",
			platform.JavaBinary(target))
	}
	return target, nil
}

func isUsableJavaHome(dir string) bool {
	bin := platform.JavaBinary(dir)
	info, err := os.Stat(bin)
	return err == nil && !info.IsDir()
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// copyFile копирует файл через временный с последующим переименованием.
func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".copy-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	if _, err := io.Copy(tmp, in); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	_ = os.Remove(dst)
	return os.Rename(tmpName, dst)
}

func humanSize(b int64) string {
	const mib = 1 << 20
	if b <= 0 {
		return "неизвестного размера"
	}
	return fmt.Sprintf("%.1f МиБ", float64(b)/float64(mib))
}
