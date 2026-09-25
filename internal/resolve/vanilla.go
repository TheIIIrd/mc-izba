package resolve

import (
	"context"
	"fmt"

	"izba/internal/netx"
)

// MojangManifestURL — манифест версий версии 2. В отличие от v1 он содержит
// sha1 для каждого файла описания версии.
const MojangManifestURL = "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json"

type mojangManifest struct {
	Latest struct {
		Release  string `json:"release"`
		Snapshot string `json:"snapshot"`
	} `json:"latest"`
	Versions []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		URL  string `json:"url"`
		SHA1 string `json:"sha1"`
	} `json:"versions"`
}

type mojangVersion struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Downloads struct {
		Server struct {
			SHA1 string `json:"sha1"`
			Size int64  `json:"size"`
			URL  string `json:"url"`
		} `json:"server"`
	} `json:"downloads"`
	JavaVersion struct {
		Component    string `json:"component"`
		MajorVersion int    `json:"majorVersion"`
	} `json:"javaVersion"`
}

// Vanilla резолвит ванильное ядро.
type Vanilla struct {
	Client *netx.Client
	// ManifestURL переопределяет адрес манифеста. Пустое значение означает
	// MojangManifestURL; поле существует ради тестов на httptest.
	ManifestURL string
}

func (v Vanilla) manifestURL() string {
	if v.ManifestURL != "" {
		return v.ManifestURL
	}
	return MojangManifestURL
}

// versionMeta возвращает описание конкретной версии Minecraft.
//
// Оно нужно и ванильному резолверу (ради ссылки на server.jar), и Paper
// (ради требуемой мажорной версии Java).
func (v Vanilla) versionMeta(ctx context.Context, version string) (mojangVersion, string, error) {
	var manifest mojangManifest
	if err := v.Client.GetJSON(ctx, v.manifestURL(), &manifest); err != nil {
		return mojangVersion{}, "", fmt.Errorf("не удалось получить манифест версий Mojang: %w", err)
	}

	wanted := version
	if wanted == "" || wanted == LatestVersion {
		wanted = manifest.Latest.Release
		if wanted == "" {
			return mojangVersion{}, "", fmt.Errorf("манифест Mojang не содержит последнего релиза")
		}
	}

	var url string
	for _, e := range manifest.Versions {
		if e.ID == wanted {
			url = e.URL
			break
		}
	}
	if url == "" {
		return mojangVersion{}, "", fmt.Errorf(
			"версия %q не найдена в манифесте Mojang; проверьте написание "+
				"(последний релиз: %s)", wanted, manifest.Latest.Release)
	}

	var meta mojangVersion
	if err := v.Client.GetJSON(ctx, url, &meta); err != nil {
		return mojangVersion{}, "", fmt.Errorf("не удалось получить описание версии %s: %w", wanted, err)
	}
	return meta, wanted, nil
}

// JavaMajorFor возвращает мажорную версию Java, требуемую данной версией игры.
//
// Поле javaVersion появилось в описаниях версий не сразу; для старых версий
// возвращается 8, что соответствует реальности тех лет.
func (v Vanilla) JavaMajorFor(ctx context.Context, version string) (int, error) {
	meta, _, err := v.versionMeta(ctx, version)
	if err != nil {
		return 0, err
	}
	if meta.JavaVersion.MajorVersion == 0 {
		return 8, nil
	}
	return meta.JavaVersion.MajorVersion, nil
}

// Resolve возвращает артефакт ванильного server.jar.
func (v Vanilla) Resolve(ctx context.Context, version string) (CoreResolution, error) {
	meta, resolved, err := v.versionMeta(ctx, version)
	if err != nil {
		return CoreResolution{}, err
	}

	d := meta.Downloads.Server
	if d.URL == "" || d.SHA1 == "" {
		return CoreResolution{}, fmt.Errorf(
			"для версии %s Mojang не публикует серверный jar (такое бывает у очень старых версий)", resolved)
	}

	javaMajor := meta.JavaVersion.MajorVersion
	if javaMajor == 0 {
		javaMajor = 8
	}

	return CoreResolution{
		Core:             CoreVanilla,
		MinecraftVersion: resolved,
		JavaMajor:        javaMajor,
		Jar: Artifact{
			URL:      d.URL,
			Digest:   netx.Digest{Algo: "sha1", Hex: d.SHA1},
			Size:     d.Size,
			Filename: fmt.Sprintf("minecraft-server-%s.jar", resolved),
		},
	}, nil
}
