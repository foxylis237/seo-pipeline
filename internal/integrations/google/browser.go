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

// profileLockName защищает persistent-профиль от двух одновременных процессов — тот же приём,
// что у Arsenkin и DeepSeek. Без него параллельные публикации портят LevelDB профиля.
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

// automationArgs снимают метку автоматизации у браузера.
//
// Google отказывает во входе, когда видит `navigator.webdriver`: страница показывает
// «Возможно, этот браузер или приложение небезопасны». Флаг убирает именно этот признак —
// это стандартная настройка Playwright для работы со своим же аккаунтом, а не обход защиты:
// логин, пароль, CAPTCHA и 2FA по-прежнему проходит человек, и ничего за него здесь не
// решается.
var automationArgs = []string{
	"--disable-blink-features=AutomationControlled",
}

// launchBrowser поднимает браузер на persistent-профиле под flock.
//
// Сначала пробуется установленный Chrome: связанный с Playwright Chromium Google отклоняет на
// входе заметно чаще. Если Chrome не установлен, запуск молча откатывается на Chromium —
// команда не должна падать из-за отсутствия необязательного браузера.
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
		// Разрешение на буфер обмена выдаётся только этому профилю: промпт вставляется
		// через clipboard, набор текста на десятки тысяч символов слишком медленный.
		Permissions: []string{"clipboard-read", "clipboard-write"},
	}
	if strings.TrimSpace(channel) != "" {
		options.Channel = playwright.String(channel)
	}
	browserContext, err := pw.Chromium.LaunchPersistentContext(profileDir, options)
	if err != nil && options.Channel != nil {
		// Chrome не установлен или не запустился — идём на связанном Chromium. Вход в Google
		// из него проходит хуже, но это лучше, чем отказ команды целиком.
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

// closeTimeout ограничивает ожидание закрытия браузера.
//
// Close персистентного контекста ждёт, пока браузер отпустит профиль, а установленный Chrome
// с открытым окном может не отпустить его никогда: команда ручного входа из-за этого висела
// после успешного логина. Профиль к моменту закрытия уже записан на диск самим Chrome, так
// что ограниченное ожидание ничего не теряет.
const closeTimeout = 10 * time.Second

// close закрывает браузер и отпускает профиль.
//
// Ошибки собираются вместе: незакрытый Playwright оставляет процесс после завершения команды.
// Зависшее закрытие не должно останавливать команду — оно превращается в предупреждение, а
// блокировка профиля снимается в любом случае, иначе следующий запуск упрётся в занятый
// профиль из-за уже мёртвого процесса.
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

// errCloseTimedOut отличает зависшее закрытие от настоящей ошибки: первое — повод предупредить
// и идти дальше, второе — повод показать причину.
var errCloseTimedOut = errors.New("браузер не закрылся за отведённое время, процесс мог остаться")

// withTimeout выполняет закрытие в отдельной goroutine и не ждёт его дольше срока.
//
// Повисшая goroutine намеренно не убивается: у Playwright нет способа прервать Close, а
// удерживать команду до конца рабочего дня хуже, чем оставить её дожидаться в фоне до выхода
// процесса.
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

// ensureProfileIsFree проверяет, что профилем не пользуется другой процесс, и сразу отпускает
// блокировку: её возьмёт launchBrowser, а два flock на один файл конфликтуют между собой даже
// внутри одного процесса.
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

// ProfileExists отвечает, был ли вход хоть раз. Пустой каталог профиля означает, что
// google-login ещё не запускали, и это отдельная причина отказа: повторять её бессмысленно.
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

// operationTimeout переводит дедлайн контекста в миллисекунды Playwright. Своего дедлайна у
// контекста может не быть — тогда берётся переданный запас.
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

// classifyPage опознаёт состояния, из которых автоматика выйти не может, по адресу страницы.
// Адрес, а не текст: интерфейс Google локализован, и подстроки в нём меняются.
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
