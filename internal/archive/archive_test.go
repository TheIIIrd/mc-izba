package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeZip собирает zip в памяти из списка записей.
func writeZip(t *testing.T, dir string, entries []zipEntry) string {
	t.Helper()
	path := filepath.Join(dir, "test.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		hdr.SetMode(e.mode)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type zipEntry struct {
	name string
	body string
	mode os.FileMode
}

func TestExtractZipRejectsPathTraversal(t *testing.T) {
	// Классический zip slip: запись пытается уйти выше целевого каталога.
	for _, name := range []string{
		"../escaped.txt",
		"a/../../escaped.txt",
		`..\escaped.txt`,
		"/absolute.txt",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			src := writeZip(t, dir, []zipEntry{{name: name, body: "x", mode: 0o644}})
			dst := filepath.Join(dir, "out")

			err := ExtractZip(src, dst, DefaultLimits())
			if err == nil {
				t.Fatalf("распаковка %q должна была завершиться ошибкой", name)
			}
			if !errors.Is(err, ErrUnsafeEntry) {
				t.Fatalf("ожидалась ErrUnsafeEntry, получено: %v", err)
			}
			if _, statErr := os.Stat(filepath.Join(dir, "escaped.txt")); statErr == nil {
				t.Fatal("файл оказался записан за пределами целевого каталога")
			}
		})
	}
}

func TestExtractZipRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	src := writeZip(t, dir, []zipEntry{
		{name: "link", body: "/etc/passwd", mode: os.ModeSymlink | 0o777},
	})

	err := ExtractZip(src, filepath.Join(dir, "out"), DefaultLimits())
	if !errors.Is(err, ErrUnsafeEntry) {
		t.Fatalf("символическая ссылка должна отвергаться, получено: %v", err)
	}
}

func TestExtractZipEnforcesLimits(t *testing.T) {
	dir := t.TempDir()
	src := writeZip(t, dir, []zipEntry{
		{name: "a.txt", body: strings.Repeat("a", 4096), mode: 0o644},
	})

	lim := Limits{MaxTotalBytes: 1024, MaxFiles: 10, MaxFileBytes: 1024}
	err := ExtractZip(src, filepath.Join(dir, "out"), lim)
	if !errors.Is(err, ErrUnsafeEntry) {
		t.Fatalf("превышение лимита размера должно отвергаться, получено: %v", err)
	}
}

func TestExtractZipRejectsTooManyFiles(t *testing.T) {
	dir := t.TempDir()
	var entries []zipEntry
	for i := 0; i < 5; i++ {
		entries = append(entries, zipEntry{name: string(rune('a'+i)) + ".txt", body: "x", mode: 0o644})
	}
	src := writeZip(t, dir, entries)

	lim := Limits{MaxTotalBytes: 1 << 20, MaxFiles: 3, MaxFileBytes: 1 << 20}
	if err := ExtractZip(src, filepath.Join(dir, "out"), lim); !errors.Is(err, ErrUnsafeEntry) {
		t.Fatalf("превышение числа файлов должно отвергаться, получено: %v", err)
	}
}

func TestExtractZipHappyPath(t *testing.T) {
	dir := t.TempDir()
	src := writeZip(t, dir, []zipEntry{
		{name: "jdk-21/bin/java", body: "#!/bin/sh\n", mode: 0o755},
		{name: "jdk-21/release", body: "JAVA_VERSION=21\n", mode: 0o644},
	})
	dst := filepath.Join(dir, "out")

	if err := ExtractZip(src, dst, DefaultLimits()); err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dst, "jdk-21", "release"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "JAVA_VERSION=21\n" {
		t.Fatalf("содержимое не совпало: %q", data)
	}

	info, err := os.Stat(filepath.Join(dst, "jdk-21", "bin", "java"))
	if err != nil {
		t.Fatal(err)
	}
	// Бит исполнения обязан сохраниться, иначе java не запустится.
	//
	// На Windows проверять нечего: у NTFS нет бита исполнения, os.Chmod
	// управляет там только атрибутом «только для чтения», а Perm() всегда
	// возвращает 0666 либо 0444. Запуск java.exe определяется расширением,
	// а не правами доступа.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("бит исполнения потерян: %v", info.Mode())
	}
}

func writeTarGz(t *testing.T, dir string, build func(*tar.Writer)) string {
	t.Helper()
	path := filepath.Join(dir, "test.tar.gz")
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	build(tw)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractTarGzRejectsTraversalAndLinks(t *testing.T) {
	t.Run("traversal", func(t *testing.T) {
		dir := t.TempDir()
		src := writeTarGz(t, dir, func(tw *tar.Writer) {
			body := []byte("x")
			_ = tw.WriteHeader(&tar.Header{
				Name: "../escaped.txt", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
			})
			_, _ = tw.Write(body)
		})
		if err := ExtractTarGz(src, filepath.Join(dir, "out"), DefaultLimits()); !errors.Is(err, ErrUnsafeEntry) {
			t.Fatalf("ожидалась ErrUnsafeEntry, получено: %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		src := writeTarGz(t, dir, func(tw *tar.Writer) {
			_ = tw.WriteHeader(&tar.Header{
				Name: "link", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink, Mode: 0o777,
			})
		})
		if err := ExtractTarGz(src, filepath.Join(dir, "out"), DefaultLimits()); !errors.Is(err, ErrUnsafeEntry) {
			t.Fatalf("ожидалась ErrUnsafeEntry, получено: %v", err)
		}
	})
}

func TestExtractTarGzHappyPath(t *testing.T) {
	dir := t.TempDir()
	src := writeTarGz(t, dir, func(tw *tar.Writer) {
		_ = tw.WriteHeader(&tar.Header{Name: "jdk-21/", Typeflag: tar.TypeDir, Mode: 0o755})
		body := []byte("ok")
		_ = tw.WriteHeader(&tar.Header{
			Name: "jdk-21/bin/java", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body)),
		})
		_, _ = tw.Write(body)
	})
	dst := filepath.Join(dir, "out")

	if err := ExtractTarGz(src, dst, DefaultLimits()); err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	root, err := SingleRootDir(dst)
	if err != nil {
		t.Fatalf("SingleRootDir: %v", err)
	}
	if filepath.Base(root) != "jdk-21" {
		t.Fatalf("неверный корневой каталог: %s", root)
	}
}

func TestSingleRootDirRejectsAmbiguous(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := SingleRootDir(dir); err == nil {
		t.Fatal("два каталога в корне должны приводить к ошибке")
	}
}
