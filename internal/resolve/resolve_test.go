package resolve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"izba/internal/netx"
)

func testClient(t *testing.T, srv *httptest.Server) *netx.Client {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c := netx.NewClient([]string{u.Host})
	c.AllowHost(u.Hostname())
	c.AllowInsecureForTest()
	return c
}

// --- Mojang ---------------------------------------------------------------

const mojangManifestJSON = `{
  "latest": {"release": "1.21.4", "snapshot": "25w01a"},
  "versions": [
    {"id": "1.21.4", "type": "release", "url": "%s/versions/1.21.4.json", "sha1": "aaa"},
    {"id": "1.16.5", "type": "release", "url": "%s/versions/1.16.5.json", "sha1": "bbb"}
  ]
}`

const mojangVersion121 = `{
  "id": "1.21.4",
  "type": "release",
  "downloads": {
    "server": {
      "sha1": "0123456789abcdef0123456789abcdef01234567",
      "size": 54185955,
      "url": "https://piston-data.mojang.com/v1/objects/abc/server.jar"
    }
  },
  "javaVersion": {"component": "java-runtime-delta", "majorVersion": 21}
}`

// В описаниях старых версий поле javaVersion отсутствует.
const mojangVersion116NoJava = `{
  "id": "1.16.5",
  "type": "release",
  "downloads": {
    "server": {
      "sha1": "1111111111111111111111111111111111111111",
      "size": 100,
      "url": "https://piston-data.mojang.com/v1/objects/def/server.jar"
    }
  }
}`

func mojangServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)

	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.ReplaceAll(mojangManifestJSON, "%s", srv.URL)))
	})
	mux.HandleFunc("/versions/1.21.4.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(mojangVersion121))
	})
	mux.HandleFunc("/versions/1.16.5.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(mojangVersion116NoJava))
	})
	return srv
}

func TestVanillaResolveLatest(t *testing.T) {
	srv := mojangServer(t)
	defer srv.Close()

	v := Vanilla{Client: testClient(t, srv), ManifestURL: srv.URL + "/manifest.json"}
	res, err := v.Resolve(context.Background(), LatestVersion)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}

	if res.MinecraftVersion != "1.21.4" {
		t.Errorf("версия: получено %q, ожидалось 1.21.4", res.MinecraftVersion)
	}
	if res.JavaMajor != 21 {
		t.Errorf("Java: получено %d, ожидалось 21", res.JavaMajor)
	}
	if res.Jar.Digest.Algo != "sha1" || res.Jar.Digest.Hex == "" {
		t.Errorf("контрольная сумма не заполнена: %+v", res.Jar.Digest)
	}
}

// Отсутствие javaVersion в манифесте должно давать Java 8, а не ноль:
// иначе резолвер Adoptium ушёл бы запрашивать несуществующую версию.
func TestVanillaDefaultsToJava8(t *testing.T) {
	srv := mojangServer(t)
	defer srv.Close()

	v := Vanilla{Client: testClient(t, srv), ManifestURL: srv.URL + "/manifest.json"}
	res, err := v.Resolve(context.Background(), "1.16.5")
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if res.JavaMajor != 8 {
		t.Errorf("Java: получено %d, ожидалось 8", res.JavaMajor)
	}
}

func TestVanillaUnknownVersion(t *testing.T) {
	srv := mojangServer(t)
	defer srv.Close()

	v := Vanilla{Client: testClient(t, srv), ManifestURL: srv.URL + "/manifest.json"}
	_, err := v.Resolve(context.Background(), "1.99.9")
	if err == nil || !strings.Contains(err.Error(), "не найдена") {
		t.Fatalf("ожидалось понятное сообщение о ненайденной версии, получено: %v", err)
	}
}

// --- Paper ----------------------------------------------------------------

// Порядок ключей здесь значим: Fill отдаёт свежую группу версий первой,
// и обычный разбор в map этот порядок теряет.
const paperProject = `{
  "project": {"id": "paper", "name": "Paper"},
  "versions": {
    "26.2": ["26.2", "26.2-rc1"],
    "1.21": ["1.21.11", "1.21.10"]
  }
}`

const paperBuilds = `[
  {"id": 50, "channel": "ALPHA", "downloads": {"server:default": {
    "name": "paper-26.2-50.jar", "url": "https://fill-data.papermc.io/v1/objects/x/paper-26.2-50.jar",
    "size": 1, "checksums": {"sha256": "ffff"}}}},
  {"id": 48, "channel": "STABLE", "downloads": {"server:default": {
    "name": "paper-26.2-48.jar", "url": "https://fill-data.papermc.io/v1/objects/y/paper-26.2-48.jar",
    "size": 54185955, "checksums": {"sha256": "bfca155b4a6b45644bfc1766f4e02a83c736e45fcc060e8788c71d6e7b3d56f6"}}}}
]`

func TestPaperPicksLatestStableBuild(t *testing.T) {
	mojang := mojangServer(t)
	defer mojang.Close()

	mux := http.NewServeMux()
	paper := httptest.NewServer(mux)
	defer paper.Close()

	mux.HandleFunc("/v3/projects/paper", func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "izba/") {
			t.Errorf("Paper требует осмысленный User-Agent, получено %q", ua)
		}
		_, _ = w.Write([]byte(paperProject))
	})
	mux.HandleFunc("/v3/projects/paper/versions/26.2/builds", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(paperBuilds))
	})

	client := netx.NewClient([]string{})
	for _, s := range []*httptest.Server{mojang, paper} {
		u, _ := url.Parse(s.URL)
		client.AllowHost(u.Hostname())
	}
	client.AllowInsecureForTest()

	// Резолвер Paper спрашивает версию Java у манифеста Mojang, но версии
	// 26.2 там нет: проверяем, что ошибка внятная.
	p := Paper{
		Client:  client,
		BaseURL: paper.URL + "/v3",
		Vanilla: Vanilla{Client: client, ManifestURL: mojang.URL + "/manifest.json"},
	}
	_, err := p.Resolve(context.Background(), LatestVersion)
	if err == nil || !strings.Contains(err.Error(), "версию Java") {
		t.Fatalf("ожидалась ошибка определения версии Java, получено: %v", err)
	}
}

// Самая свежая группа версий определяется по порядку ключей в JSON.
func TestPaperLatestVersionRespectsKeyOrder(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/v3/projects/paper", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(paperProject))
	})

	p := Paper{Client: testClient(t, srv), BaseURL: srv.URL + "/v3"}
	got, err := p.latestVersion(context.Background())
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if got != "26.2" {
		t.Fatalf("получено %q, ожидалось 26.2 (первый ключ объекта versions)", got)
	}
}

func TestPaperRejectsMissingStableBuild(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/v3/projects/paper/versions/26.2/builds", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":50,"channel":"ALPHA","downloads":{}}]`))
	})

	p := Paper{Client: testClient(t, srv), BaseURL: srv.URL + "/v3"}
	_, err := p.stableBuild(context.Background(), "26.2")
	if err == nil || !strings.Contains(err.Error(), "стабильных сборок") {
		t.Fatalf("экспериментальные сборки не должны выбираться, получено: %v", err)
	}
}

// --- Adoptium -------------------------------------------------------------

const adoptiumJRE = `[{
  "release_name": "jdk-21.0.5+11",
  "binary": {
    "image_type": "jre",
    "os": "linux",
    "architecture": "x64",
    "package": {
      "name": "OpenJDK21U-jre_x64_linux_hotspot_21.0.5_11.tar.gz",
      "link": "https://github.com/adoptium/temurin21-binaries/releases/download/x/OpenJDK21U-jre.tar.gz",
      "checksum": "3fdb1b0e0e8b1a1a0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e",
      "size": 45000000
    }
  }
}]`

// Adoptium иногда отдаёт установщик вместо архива; такой вариант должен
// отбрасываться, потому что msi меняет систему, а нам нужен каталог.
const adoptiumInstallerOnly = `[{
  "release_name": "jdk-21.0.5+11",
  "binary": {
    "image_type": "jre", "os": "windows", "architecture": "x64",
    "package": {"name": "OpenJDK21U-jre_x64_windows_hotspot.msi",
      "link": "https://github.com/adoptium/x/OpenJDK21U-jre.msi",
      "checksum": "aaaa", "size": 1}
  }
}]`

func TestJavaResolvePrefersJRE(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var gotImageType string
	mux.HandleFunc("/v3/assets/latest/21/hotspot", func(w http.ResponseWriter, r *http.Request) {
		gotImageType = r.URL.Query().Get("image_type")
		_, _ = w.Write([]byte(adoptiumJRE))
	})

	j := Java{Client: testClient(t, srv), BaseURL: srv.URL + "/v3", OS: "linux", Arch: "x64"}
	res, err := j.Resolve(context.Background(), 21)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if gotImageType != "jre" {
		t.Errorf("первым должен запрашиваться jre, запрошен %q", gotImageType)
	}
	if res.Release != "jdk-21.0.5+11" {
		t.Errorf("release: получено %q", res.Release)
	}
	if res.Archive.Digest.Algo != "sha256" {
		t.Errorf("Adoptium публикует sha256, получено %q", res.Archive.Digest.Algo)
	}
}

func TestJavaRejectsInstallerPackages(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/v3/assets/latest/21/hotspot", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(adoptiumInstallerOnly))
	})

	j := Java{Client: testClient(t, srv), BaseURL: srv.URL + "/v3", OS: "windows", Arch: "x64"}
	if _, err := j.Resolve(context.Background(), 21); err == nil {
		t.Fatal("msi-установщик не должен приниматься как архив Java")
	}
}

func TestParseCore(t *testing.T) {
	for _, in := range []string{"paper", "PAPER", " vanilla "} {
		if _, err := ParseCore(in); err != nil {
			t.Errorf("ParseCore(%q): %v", in, err)
		}
	}
	if _, err := ParseCore("forge"); err == nil {
		t.Error("неподдерживаемое ядро должно отвергаться")
	}
}
