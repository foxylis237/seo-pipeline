package google

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// profileLockName — файл flock persistent-профиля: два процесса на одном профиле портят его LevelDB.
const profileLockName = ".seo-pipeline.lock"

func profileLockPath(profileDir string) string {
	return filepath.Join(profileDir, profileLockName)
}

// browserSession держит запущенный браузер вместе с блокировкой профиля.
type browserSession struct {
	pw      *playwright.Playwright
	context playwright.BrowserContext
	page    playwright.Page
	profile *os.File
}

// automationArgs снимают `navigator.webdriver`: с ним Google отказывает во входе
// («Возможно, этот браузер или приложение небезопасны»). Вход по-прежнему проходит человек.
var automationArgs = []string{
	"--disable-blink-features=AutomationControlled",
}

// launchBrowser поднимает браузер на persistent-профиле под flock. Сначала пробуется
// установленный Chrome (связанный Chromium Google отклоняет чаще), при отказе — Chromium.
func launchBrowser(profileDir string, headless bool, channel string) (*browserSession, error) {
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return nil, fmt.Errorf("создать каталог профиля Google: %w", err)
	}
	profile, err := os.OpenFile(profileLockPath(profileDir), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("открыть блокировку профиля Google: %w", err)
	}
	if err := syscall.Flock(int(profile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = profile.Close()
		return nil, fmt.Errorf("%w: %w", ErrProfileBusy, err)
	}

	pw, err := playwright.Run()
	if err != nil {
		_ = releaseProfile(profile)
		return nil, fmt.Errorf("запустить Playwright для Google: %w", err)
	}
	options := playwright.BrowserTypeLaunchPersistentContextOptions{
		Headless: playwright.Bool(headless),
		Args:     automationArgs,
		// Промпт вставляется через буфер обмена: набор десятков тысяч символов слишком медленный.
		Permissions: []string{"clipboard-read", "clipboard-write"},
	}
	if strings.TrimSpace(channel) != "" {
		options.Channel = playwright.String(channel)
	}
	browserContext, err := pw.Chromium.LaunchPersistentContext(profileDir, options)
	if err != nil && options.Channel != nil {
		options.Channel = nil
		browserContext, err = pw.Chromium.LaunchPersistentContext(profileDir, options)
	}
	if err != nil {
		_ = pw.Stop()
		_ = releaseProfile(profile)
		return nil, fmt.Errorf("открыть профиль браузера Google: %w", err)
	}
	page, err := firstPage(browserContext)
	if err != nil {
		_ = browserContext.Close()
		_ = pw.Stop()
		_ = releaseProfile(profile)
		return nil, err
	}
	return &browserSession{pw: pw, context: browserContext, page: page, profile: profile}, nil
}

func firstPage(browserContext playwright.BrowserContext) (playwright.Page, error) {
	if pages := browserContext.Pages(); len(pages) > 0 {
		return pages[0], nil
	}
	page, err := browserContext.NewPage()
	if err != nil {
		return nil, fmt.Errorf("открыть вкладку браузера Google: %w", err)
	}
	return page, nil
}

// closeTimeout ограничивает ожидание закрытия: установленный Chrome с открытым окном может не
// отпустить профиль никогда, а профиль к этому моменту Chrome уже записал на диск.
const closeTimeout = 10 * time.Second

// close закрывает браузер и отпускает профиль; блокировка снимается даже при зависшем закрытии.
func (s *browserSession) close() error {
	if s == nil {
		return nil
	}
	var result error
	if s.context != nil {
		if err := withTimeout(closeTimeout, func() error { return s.context.Close() }); err != nil {
			result = errors.Join(result, fmt.Errorf("закрыть контекст браузера Google: %w", err))
		}
	}
	if s.pw != nil {
		if err := withTimeout(closeTimeout, s.pw.Stop); err != nil {
			result = errors.Join(result, fmt.Errorf("остановить Playwright для Google: %w", err))
		}
	}
	return errors.Join(result, releaseProfile(s.profile))
}

// errCloseTimedOut отличает зависшее закрытие от настоящей ошибки.
var errCloseTimedOut = errors.New("браузер не закрылся за отведённое время, процесс мог остаться")

// withTimeout выполняет action в отдельной goroutine и не ждёт её дольше срока.
// Повисшая goroutine остаётся: прервать Close у Playwright нечем.
func withTimeout(limit time.Duration, action func() error) error {
	done := make(chan error, 1)
	go func() { done <- action() }()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return errCloseTimedOut
	}
}

func releaseProfile(profile *os.File) error {
	if profile == nil {
		return nil
	}
	var result error
	if err := syscall.Flock(int(profile.Fd()), syscall.LOCK_UN); err != nil {
		result = errors.Join(result, fmt.Errorf("снять блокировку профиля Google: %w", err))
	}
	if err := profile.Close(); err != nil {
		result = errors.Join(result, fmt.Errorf("закрыть блокировку профиля Google: %w", err))
	}
	return result
}

// ensureProfileIsFree проверяет, что профиль не занят, и сразу отпускает блокировку: два flock
// на один файл конфликтуют даже внутри одного процесса, а её возьмёт launchBrowser.
func ensureProfileIsFree(profileDir string) error {
	if _, err := os.Stat(profileDir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	lock, err := os.OpenFile(profileLockPath(profileDir), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("открыть блокировку профиля Google: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("%w: %w", ErrProfileBusy, err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		return fmt.Errorf("снять блокировку профиля Google: %w", err)
	}
	return nil
}

// ProfileExists отвечает, есть ли в каталоге профиля что-то кроме блокировки, то есть был ли вход.
func ProfileExists(profileDir string) bool {
	entries, err := os.ReadDir(profileDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.Name() != profileLockName {
			return true
		}
	}
	return false
}

// operationTimeout переводит дедлайн контекста в миллисекунды Playwright; без дедлайна — fallback.
func operationTimeout(ctx context.Context, fallback time.Duration) float64 {
	deadline, ok := ctx.Deadline()
	if !ok {
		return float64(fallback.Milliseconds())
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 1
	}
	return float64(remaining.Milliseconds())
}

// pause выдерживает небольшую случайную задержку между действиями и уважает отмену.
func pause(ctx context.Context) error {
	delay := minActionInterval + time.Duration(rand.Int63n(int64(actionJitter)))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// classifyPage опознаёт тупиковые состояния по адресу страницы, а не по локализованному тексту.
func classifyPage(currentURL string) error {
	lowered := strings.ToLower(currentURL)
	for _, marker := range challengeURLMarkers {
		if strings.Contains(lowered, marker) {
			return ErrManualVerification
		}
	}
	if strings.Contains(lowered, signInURLMarker) {
		return ErrSessionExpired
	}
	return nil
}
