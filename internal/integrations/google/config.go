package google

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultProfileDir — persistent-профиль Chromium с живыми cookies аккаунта.
	DefaultProfileDir = "data/browser/google"
	// DefaultFolderURL — папка «Статьи ДПО ПРОФ» на Google Drive.
	DefaultFolderURL = "https://drive.google.com/drive/folders/1N-NRlswacwqKWUOEiA1OS3tKT_V_yLiS"
	// DefaultChannel — установленный Chrome: из связанного Chromium вход отклоняется чаще.
	DefaultChannel = "chrome"

	// defaultOperationTimeout ограничивает одно браузерное ожидание.
	defaultOperationTimeout = 45 * time.Second
	// searchResultsWait — сколько ждать первую строку результатов поиска в Drive.
	searchResultsWait = 10 * time.Second
	// defaultPublishTimeout — бюджет одной попытки публикации целиком.
	defaultPublishTimeout = 3 * time.Minute
	// defaultLoginTimeout — сколько ждём человека за браузером: вход с 2FA занимает минуты.
	defaultLoginTimeout = 30 * time.Minute

	// minActionInterval и actionJitter задают случайную паузу между браузерными действиями.
	minActionInterval = 400 * time.Millisecond
	actionJitter      = 700 * time.Millisecond
)

// Config описывает, куда публиковать и каким профилем.
type Config struct {
	// ProfileDir — persistent-профиль Chromium с сессией Google.
	ProfileDir string
	// FolderURL — папка Drive, в которой живут документы промптов.
	FolderURL string
	// Headless выключается для google-login: вход проходит человек.
	Headless bool
	// Channel выбирает установленный браузер вместо связанного Chromium; пустое — Chromium.
	Channel string
	// OperationTimeout ограничивает одно ожидание, PublishTimeout — попытку целиком.
	OperationTimeout time.Duration
	PublishTimeout   time.Duration
	// DiagnosticsDir — корень диагностики; пустое — общий каталог по умолчанию.
	DiagnosticsDir string
}

// DefaultConfig возвращает конфигурацию обычного прогона.
func DefaultConfig() Config {
	return Config{
		ProfileDir:       DefaultProfileDir,
		FolderURL:        DefaultFolderURL,
		Headless:         true,
		Channel:          DefaultChannel,
		OperationTimeout: defaultOperationTimeout,
		PublishTimeout:   defaultPublishTimeout,
	}
}

// Validate проверяет конфигурацию до запуска браузера.
func (c Config) Validate() error {
	if strings.TrimSpace(c.ProfileDir) == "" {
		return fmt.Errorf("каталог профиля Google пуст")
	}
	if _, err := FolderID(c.FolderURL); err != nil {
		return err
	}
	return nil
}

// FolderID вытаскивает идентификатор папки из её адреса (документ создаётся через ?folder=<id>).
func FolderID(folderURL string) (string, error) {
	trimmed := strings.TrimSpace(folderURL)
	if trimmed == "" {
		return "", fmt.Errorf("адрес папки Google Drive пуст")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("разобрать адрес папки Google Drive %q: %w", folderURL, err)
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for index, segment := range segments {
		if segment == "folders" && index+1 < len(segments) {
			id := strings.TrimSpace(segments[index+1])
			if id == "" {
				break
			}
			return id, nil
		}
	}
	return "", fmt.Errorf("в адресе папки Google Drive нет идентификатора: %q", folderURL)
}
