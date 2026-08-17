package google

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultProfileDir — persistent-профиль Chromium. Лежит рядом с профилями Arsenkin и
	// DeepSeek и так же исключён из Git: внутри живые cookies аккаунта.
	DefaultProfileDir = "data/browser/google"
	// DefaultFolderURL — папка «Статьи ДПО ПРОФ» на Google Drive.
	DefaultFolderURL = "https://drive.google.com/drive/folders/1N-NRlswacwqKWUOEiA1OS3tKT_V_yLiS"
	// DefaultChannel — установленный Chrome. Вход в Google из связанного с Playwright
	// Chromium отклоняется заметно чаще; если Chrome не установлен, запуск откатывается
	// на Chromium сам.
	DefaultChannel = "chrome"

	// defaultOperationTimeout ограничивает одно браузерное ожидание.
	defaultOperationTimeout = 45 * time.Second
	// defaultPublishTimeout — бюджет одной попытки публикации целиком.
	defaultPublishTimeout = 3 * time.Minute
	// defaultLoginTimeout — сколько ждём человека за браузером. Столько же, сколько у
	// DeepSeek: вход с 2FA и подтверждением почты занимает минуты, а не секунды.
	defaultLoginTimeout = 30 * time.Minute

	// minActionInterval и actionJitter задают паузу между браузерными действиями. Случайность
	// здесь не маскирует автоматизацию, а убирает ровный машинный ритм — тот же приём, что в
	// deepseekweb.
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
	// Channel выбирает установленный браузер вместо связанного с Playwright Chromium.
	//
	// Google отказывает во входе автоматизированному браузеру («Возможно, этот браузер или
	// приложение небезопасны»), и связанный Chromium он отклоняет заметно охотнее настоящего
	// Chrome. Пустое значение означает связанный Chromium.
	Channel string
	// OperationTimeout ограничивает одно ожидание, PublishTimeout — попытку целиком.
	OperationTimeout time.Duration
	PublishTimeout   time.Duration
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

// FolderID вытаскивает идентификатор папки из её адреса. Он нужен отдельно: создание
// документа сразу в нужной папке идёт через ?folder=<id>, а не через переход по ссылке.
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
