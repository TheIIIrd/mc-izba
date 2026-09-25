package resolve

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"izba/internal/netx"
	"izba/internal/platform"
)

// AdoptiumBaseURL — API Eclipse Adoptium.
const AdoptiumBaseURL = "https://api.adoptium.net/v3"

type adoptiumAsset struct {
	ReleaseName string `json:"release_name"`
	Binary      struct {
		ImageType    string `json:"image_type"`
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Package      struct {
			Name     string `json:"name"`
			Link     string `json:"link"`
			Checksum string `json:"checksum"` // sha256
			Size     int64  `json:"size"`
		} `json:"package"`
	} `json:"binary"`
}

// Java резолвит среду выполнения Java.
type Java struct {
	Client *netx.Client
	// BaseURL переопределяет адрес API; пустое значение означает AdoptiumBaseURL.
	BaseURL string
	// OS и Arch позволяют зафиксировать платформу в тестах. Пустые значения
	// означают текущую машину.
	OS   string
	Arch string
}

func (j Java) baseURL() string {
	if j.BaseURL != "" {
		return j.BaseURL
	}
	return AdoptiumBaseURL
}

// Resolve подбирает сборку Temurin под текущую ОС и архитектуру.
//
// Сначала запрашивается JRE: он примерно вдвое меньше JDK, а серверу
// Minecraft компилятор не нужен. Для части версий Adoptium публикует только
// JDK, поэтому предусмотрен откат.
func (j Java) Resolve(ctx context.Context, major int) (JavaResolution, error) {
	osName, arch := j.OS, j.Arch
	if osName == "" {
		var err error
		if osName, err = platform.AdoptiumOS(); err != nil {
			return JavaResolution{}, err
		}
	}
	if arch == "" {
		var err error
		if arch, err = platform.AdoptiumArch(); err != nil {
			return JavaResolution{}, err
		}
	}

	var lastErr error
	for _, imageType := range []string{"jre", "jdk"} {
		res, err := j.resolveImage(ctx, major, osName, arch, imageType)
		if err == nil {
			return res, nil
		}
		lastErr = err
	}
	return JavaResolution{}, fmt.Errorf(
		"не удалось подобрать Java %d для %s/%s: %w", major, osName, arch, lastErr)
}

func (j Java) resolveImage(ctx context.Context, major int, osName, arch, imageType string) (JavaResolution, error) {
	q := url.Values{}
	q.Set("architecture", arch)
	q.Set("image_type", imageType)
	q.Set("os", osName)
	q.Set("vendor", "eclipse")
	q.Set("jvm_impl", "hotspot")
	q.Set("heap_size", "normal")
	q.Set("project", "jdk")

	endpoint := fmt.Sprintf("%s/assets/latest/%d/hotspot?%s", j.baseURL(), major, q.Encode())

	var assets []adoptiumAsset
	if err := j.Client.GetJSON(ctx, endpoint, &assets); err != nil {
		return JavaResolution{}, err
	}

	for _, a := range assets {
		b := a.Binary
		if b.ImageType != imageType || b.OS != osName || b.Architecture != arch {
			continue
		}
		if b.Package.Link == "" || b.Package.Checksum == "" {
			continue
		}
		if !supportedArchive(b.Package.Name) {
			continue
		}
		return JavaResolution{
			Vendor:    "temurin",
			Major:     major,
			Release:   a.ReleaseName,
			ImageType: imageType,
			Archive: Artifact{
				URL:      b.Package.Link,
				Digest:   netx.Digest{Algo: "sha256", Hex: b.Package.Checksum},
				Size:     b.Package.Size,
				Filename: b.Package.Name,
			},
		}, nil
	}
	return JavaResolution{}, fmt.Errorf("подходящий %s не найден среди %d вариантов", imageType, len(assets))
}

// supportedArchive отсекает форматы, которые izba не распаковывает
// (msi, pkg, deb и прочие установщики: они меняют систему, а нам нужен
// самодостаточный каталог).
func supportedArchive(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".zip") ||
		strings.HasSuffix(lower, ".tar.gz") ||
		strings.HasSuffix(lower, ".tgz")
}
