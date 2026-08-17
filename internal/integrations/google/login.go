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

// Login открывает видимый Chromium для ручного входа в Google и сохраняет persistent-профиль.
//
// Логин, пароль, CAPTCHA и 2FA проходит человек: автоматизировать их нельзя, и обходить
// защиты Google эта команда не пытается. Как и у DeepSeek, вход всегда начинается с чистого
// состояния — переиспользование прежних cookies маскирует ровно ту проблему, ради которой
// команду и запускают.
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
	// Ошибка закрытия не превращает удавшийся вход в неудачу: профиль к этому моменту уже
	// записан Chrome на диск, и отказ команды сбил бы с толку — вход-то состоялся.
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
		"timeout", defaultLoginTimeout, "folder_url", cfg.FolderURL)

	// Признак успеха — та самая папка, в которую потом публикуются промпты: вход, после
	// которого до неё нет доступа, для этой команды бесполезен.
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

// resetProfile удаляет сохранённое состояние браузера перед ручным входом.
//
// Каталог сначала проверяется на занятость: launchBrowser держит flock на файле внутри
// профиля, и удаление каталога под работающим процессом сняло бы эту защиту — блокировка
// осталась бы на удалённом inode, а профиль испортился бы у обоих.
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
