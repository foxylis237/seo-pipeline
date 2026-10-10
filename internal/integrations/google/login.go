package google

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/mxschmitt/playwright-go"
)

// Login открывает видимый браузер для ручного входа в Google и сохраняет persistent-профиль.
// Вход начинается с чистого профиля: прежние cookies маскировали бы проблему, ради которой его запускают.
func Login(ctx context.Context, cfg Config, logger *slog.Logger) error {
	if strings.TrimSpace(cfg.ProfileDir) == "" {
		return fmt.Errorf("каталог профиля Google пуст")
	}
	if logger == nil {
		return fmt.Errorf("логгер Google не задан")
	}
	loginCtx, cancel := context.WithTimeout(ctx, defaultLoginTimeout)
	defer cancel()

	if err := resetProfile(cfg.ProfileDir, logger); err != nil {
		return err
	}
	session, err := launchBrowser(cfg.ProfileDir, false, cfg.Channel)
	if err != nil {
		return err
	}
	// Ошибка закрытия только логируется: профиль к этому моменту уже записан на диск.
	defer func() {
		if closeErr := session.close(); closeErr != nil {
			logger.Warn("браузер закрылся не полностью, профиль при этом сохранён",
				"profile_dir", cfg.ProfileDir, "error", closeErr)
		}
	}()

	logger.Info("открывается страница входа Google в чистом профиле", "profile_dir", cfg.ProfileDir)
	if _, err := session.page.Goto(cfg.FolderURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(operationTimeout(loginCtx, defaultOperationTimeout)),
	}); err != nil {
		return fmt.Errorf("открыть Google Drive: %w", err)
	}
	logger.Info("войдите в Google вручную в открывшемся Chromium; CAPTCHA и 2FA не автоматизируются",
		"timeout", defaultLoginTimeout, "url", cfg.FolderURL)

	// Успех — открылась папка публикации, а не просто состоялся вход.
	if err := waitForDriveFolder(loginCtx, session.page); err != nil {
		return err
	}
	logger.Info("вход в Google выполнен, persistent-профиль сохранён", "profile_dir", cfg.ProfileDir)
	return nil
}

// waitForDriveFolder ждёт, пока человек войдёт и Drive покажет содержимое папки.
func waitForDriveFolder(ctx context.Context, page playwright.Page) error {
	_, err := page.WaitForFunction(`() => {
		if (location.hostname.indexOf('drive.google.com') === -1) { return false; }
		return document.querySelectorAll('[role="row"], [role="listitem"], [aria-label]').length > 0;
	}`, nil, playwright.PageWaitForFunctionOptions{
		Polling: "raf",
		Timeout: playwright.Float(operationTimeout(ctx, defaultLoginTimeout)),
	})
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("ожидание ручного входа в Google: %w", ctx.Err())
		}
		return fmt.Errorf("ожидание ручного входа в Google: %w", err)
	}
	return nil
}

// resetProfile удаляет профиль перед ручным входом. Сначала проверяется flock: удаление под
// работающим процессом оставило бы блокировку на удалённом inode.
func resetProfile(profileDir string, logger *slog.Logger) error {
	if _, err := os.Stat(profileDir); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("проверить профиль Google: %w", err)
	}
	if err := ensureProfileIsFree(profileDir); err != nil {
		return err
	}
	if err := os.RemoveAll(profileDir); err != nil {
		return fmt.Errorf("удалить профиль Google: %w", err)
	}
	logger.Info("профиль Google удалён перед ручным входом", "profile_dir", profileDir)
	return nil
}
