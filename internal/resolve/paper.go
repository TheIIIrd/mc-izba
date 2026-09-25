package resolve

import (
	"context"
	"encoding/json"
	"fmt"

	"izba/internal/netx"
)

// PaperBaseURL — сервис Fill v3.
//
// Прежний api.papermc.io/v2 перестал получать новые сборки 31 декабря 2025
// года и был отключён 1 июля 2026 года, поэтому поддержки v2 здесь нет.
const PaperBaseURL = "https://fill.papermc.io/v3"

// paperChannelStable — канал, который PaperMC рекомендует для рабочих серверов.
// Экспериментальные сборки не поддерживаются и в izba не выбираются.
const paperChannelStable = "STABLE"

type paperDownload struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Size      int64  `json:"size"`
	Checksums struct {
		SHA256 string `json:"sha256"`
	} `json:"checksums"`
}

type paperBuild struct {
	ID        int                      `json:"id"`
	Channel   string                   `json:"channel"`
	Downloads map[string]paperDownload `json:"downloads"`
}

// Paper резолвит ядро Paper.
//
// Требуемая версия Java берётся из манифеста Mojang: Paper собирается под ту
// же версию игры и не публикует это требование отдельно.
type Paper struct {
	Client  *netx.Client
	Vanilla Vanilla
	// BaseURL переопределяет адрес Fill; пустое значение означает PaperBaseURL.
	BaseURL string
}

func (p Paper) baseURL() string {
	if p.BaseURL != "" {
		return p.BaseURL
	}
	return PaperBaseURL
}

// latestVersion возвращает самую свежую версию Minecraft, для которой Paper
// публикует сборки.
//
// Ответ Fill группирует версии по веткам, и порядок ключей значим: первая
// группа — самая новая. Поэтому объект разбирается с сохранением порядка.
func (p Paper) latestVersion(ctx context.Context) (string, error) {
	raw, err := p.Client.GetRaw(ctx, p.baseURL()+"/projects/paper")
	if err != nil {
		return "", fmt.Errorf("не удалось получить список версий Paper: %w", err)
	}

	keys, values, err := orderedKeys(raw, "versions")
	if err != nil {
		return "", fmt.Errorf("неожиданный формат ответа Paper: %w", err)
	}
	if len(keys) == 0 {
		return "", fmt.Errorf("Paper не вернул ни одной версии")
	}

	var group []string
	if err := json.Unmarshal(values[keys[0]], &group); err != nil {
		return "", fmt.Errorf("не удалось разобрать группу версий %q: %w", keys[0], err)
	}
	if len(group) == 0 {
		return "", fmt.Errorf("группа версий %q пуста", keys[0])
	}
	return group[0], nil
}

// stableBuild возвращает свежайшую стабильную сборку для версии.
func (p Paper) stableBuild(ctx context.Context, version string) (paperBuild, error) {
	url := fmt.Sprintf("%s/projects/paper/versions/%s/builds", p.baseURL(), version)

	var builds []paperBuild
	if err := p.Client.GetJSON(ctx, url, &builds); err != nil {
		return paperBuild{}, fmt.Errorf("не удалось получить сборки Paper для %s: %w", version, err)
	}
	// Fill отдаёт сборки от новых к старым.
	for _, b := range builds {
		if b.Channel == paperChannelStable {
			return b, nil
		}
	}
	return paperBuild{}, fmt.Errorf(
		"для версии %s нет стабильных сборок Paper; "+
			"либо версия слишком свежая, либо её стоит указать явно", version)
}

// Resolve возвращает артефакт jar-файла Paper.
func (p Paper) Resolve(ctx context.Context, version string) (CoreResolution, error) {
	resolved := version
	if resolved == "" || resolved == LatestVersion {
		v, err := p.latestVersion(ctx)
		if err != nil {
			return CoreResolution{}, err
		}
		resolved = v
	}

	build, err := p.stableBuild(ctx, resolved)
	if err != nil {
		return CoreResolution{}, err
	}

	// Ключ "server:default" — основной серверный артефакт сборки.
	dl, ok := build.Downloads["server:default"]
	if !ok || dl.URL == "" {
		return CoreResolution{}, fmt.Errorf(
			"сборка Paper %d для %s не содержит серверного артефакта", build.ID, resolved)
	}
	if dl.Checksums.SHA256 == "" {
		return CoreResolution{}, fmt.Errorf(
			"Paper не вернул sha256 для сборки %d; загрузка без проверки суммы не выполняется", build.ID)
	}

	javaMajor, err := p.Vanilla.JavaMajorFor(ctx, resolved)
	if err != nil {
		return CoreResolution{}, fmt.Errorf(
			"не удалось определить требуемую версию Java для Minecraft %s: %w", resolved, err)
	}

	name := dl.Name
	if name == "" {
		name = fmt.Sprintf("paper-%s-%d.jar", resolved, build.ID)
	}

	return CoreResolution{
		Core:             CorePaper,
		MinecraftVersion: resolved,
		Build:            fmt.Sprintf("%d", build.ID),
		JavaMajor:        javaMajor,
		Jar: Artifact{
			URL:      dl.URL,
			Digest:   netx.Digest{Algo: "sha256", Hex: dl.Checksums.SHA256},
			Size:     dl.Size,
			Filename: name,
		},
	}, nil
}
