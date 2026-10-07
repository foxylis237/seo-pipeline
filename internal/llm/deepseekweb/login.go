package deepseekweb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// Login выполняет ручной вход в DeepSeek, предварительно удалив сохранённый профиль.
func Login(ctx context.Context, cfg Config, logger *slog.Logger) error {
	if err := validateConfig(cfg, logger); err != nil {
		return err
	}
	loginCtx, cancel := context.WithTimeout(ctx, defaultLoginTimeout)
	defer cancel()
	if err := resetProfile(cfg.ProfileDir, logger); err != nil {
		return err
	}
	session, err := launchBrowser(cfg.ProfileDir, false)
	if err != nil {
		return err
	}
	defer session.close()

	logger.Info("opening DeepSeek login page in a clean browser profile", "profile_dir", cfg.ProfileDir)
	if _, err := session.page.Goto(cfg.LoginURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(operationTimeout(loginCtx, defaultOperationTimeout)),
	}); err != nil {
		return fmt.Errorf("open DeepSeek login page: %w", err)
	}
	logger.Info("complete DeepSeek login manually in Chromium; CAPTCHA and confirmations are not automated", "timeout", defaultLoginTimeout)
	if _, err := session.page.WaitForFunction(visibleElementJS, composerSelector, playwright.PageWaitForFunctionOptions{
		Polling: "raf", Timeout: playwright.Float(operationTimeout(loginCtx, 0)),
	}); err != nil {
		if loginCtx.Err() != nil {
			return fmt.Errorf("wait for manual DeepSeek login: %w", loginCtx.Err())
		}
		return fmt.Errorf("wait for manual DeepSeek login: %w", err)
	}
	logger.Info("DeepSeek login completed; persistent browser profile saved", "profile_dir", cfg.ProfileDir)
	return nil
}

// resetProfile удаляет сохранённое состояние браузера перед ручным входом. Сначала проверка
// занятости: удалённый под flock каталог оставил бы блокировку на мёртвом inode.
func resetProfile(profileDir string, logger *slog.Logger) error {
	if _, err := os.Stat(profileDir); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("check DeepSeek browser profile: %w", err)
	}
	if err := ensureProfileIsFree(profileDir); err != nil {
		return err
	}
	_, _, cooldown := readBlockedUntil(profileDir, time.Now())
	if err := os.RemoveAll(profileDir); err != nil {
		return fmt.Errorf("remove DeepSeek browser profile: %w", err)
	}
	logger.Info("DeepSeek browser profile removed before manual login", "profile_dir", profileDir)
	if cooldown {
		logger.Warn("DeepSeek block cooldown was cleared together with the profile", "profile_dir", profileDir)
	}
	return nil
}

// ensureProfileIsFree проверяет, что профилем не пользуется другой процесс. Блокировка сразу
// отпускается: два flock на один файл конфликтуют даже внутри процесса.
func ensureProfileIsFree(profileDir string) error {
	lock, err := os.OpenFile(profileLockPath(profileDir), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open DeepSeek browser profile lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("DeepSeek browser profile is already used by another process: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		return fmt.Errorf("unlock DeepSeek browser profile: %w", err)
	}
	return nil
}

func visible(page playwright.Page, selector string) (bool, error) {
	value, err := page.Evaluate(visibleElementJS, selector)
	if err != nil {
		return false, err
	}
	visible, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("unexpected visibility result %T", value)
	}
	return visible, nil
}
