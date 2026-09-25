package netx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testClient создаёт клиент, которому разрешён указанный httptest-сервер.
func testClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient([]string{u.Host})
	c.AllowHost(u.Hostname())
	c.AllowInsecureForTest()
	return c
}

func TestRejectsDisallowedHost(t *testing.T) {
	c := NewClient([]string{"example.com"})
	err := c.GetJSON(context.Background(), "https://evil.example.org/x.json", &struct{}{})
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("ожидался отказ по allowlist, получено: %v", err)
	}
}

func TestRejectsPlainHTTP(t *testing.T) {
	c := NewClient([]string{"example.com"})
	err := c.GetJSON(context.Background(), "http://example.com/x.json", &struct{}{})
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("ожидался отказ от http, получено: %v", err)
	}
}

// Редирект на посторонний хост — основной способ обойти наивную проверку
// URL, выполненную только один раз перед запросом.
func TestRejectsRedirectToDisallowedHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example.org/payload", http.StatusFound)
	}))
	defer srv.Close()

	c := testClient(t, srv)
	err := c.GetJSON(context.Background(), srv.URL+"/start", &struct{}{})
	if err == nil {
		t.Fatal("редирект на запрещённый хост должен приводить к ошибке")
	}
	if !strings.Contains(err.Error(), "evil.example.org") {
		t.Fatalf("сообщение об ошибке не называет хост: %v", err)
	}
}

func TestDownloadVerifiedRejectsWrongHash(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("подменённое содержимое"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "artifact.jar")
	c := testClient(t, srv)

	want := Digest{Algo: "sha256", Hex: strings.Repeat("00", 32)}
	err := c.DownloadVerified(context.Background(), srv.URL+"/a.jar", want, dest, nil)
	if err == nil {
		t.Fatal("несовпадение хеша должно приводить к ошибке")
	}

	if _, statErr := os.Stat(dest); statErr == nil {
		t.Fatal("файл с неверным хешем не должен оставаться на диске")
	}
	// Временные файлы тоже не должны оставаться.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".download-") {
			t.Fatalf("остался временный файл %s", e.Name())
		}
	}
}

func TestDownloadVerifiedHappyPath(t *testing.T) {
	const body = "содержимое ядра"
	// sha256 от строки выше посчитан тем же кодом, что и проверяет загрузку,
	// поэтому сначала пишем файл и берём его хеш.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	dir := t.TempDir()
	ref := filepath.Join(dir, "ref")
	if err := os.WriteFile(ref, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := hashFile(t, ref)

	dest := filepath.Join(dir, "core.jar")
	c := testClient(t, srv)
	if err := c.DownloadVerified(context.Background(), srv.URL+"/core.jar",
		Digest{Algo: "sha256", Hex: sum}, dest, nil); err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("содержимое не совпало: %q", got)
	}

	if err := VerifyFile(dest, Digest{Algo: "sha256", Hex: sum}); err != nil {
		t.Fatalf("VerifyFile на корректном файле вернул ошибку: %v", err)
	}
}

func TestDownloadRefusesWithoutDigest(t *testing.T) {
	c := NewClient([]string{"example.com"})
	err := c.DownloadVerified(context.Background(), "https://example.com/a.jar",
		Digest{}, filepath.Join(t.TempDir(), "a.jar"), nil)
	if err == nil || !strings.Contains(err.Error(), "контрольной суммы") {
		t.Fatalf("загрузка без хеша должна отвергаться, получено: %v", err)
	}
}

func TestUserAgentFormat(t *testing.T) {
	// PaperMC отклоняет обобщённые User-Agent и требует контакт в скобках.
	ua := UserAgent()
	if !strings.HasPrefix(ua, "izba/") || !strings.Contains(ua, "(") {
		t.Fatalf("User-Agent не соответствует требованиям PaperMC: %q", ua)
	}
}

func hashFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := Digest{Algo: "sha256"}.newHash()
	if err != nil {
		t.Fatal(err)
	}
	h.Write(data)
	const hexdigits = "0123456789abcdef"
	sum := h.Sum(nil)
	out := make([]byte, 0, len(sum)*2)
	for _, b := range sum {
		out = append(out, hexdigits[b>>4], hexdigits[b&0x0f])
	}
	return string(out)
}
