package keysso

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// manualLoginTimeout — how long the visible browser waits for a person to finish signing in.
const manualLoginTimeout = 10 * time.Minute

// signedInJS reports the Keys.so page of a signed-in account: the login form is gone, there is
// no login link, and the site search is on the page.
const signedInJS = `selectors => !location.pathname.startsWith('/ru/login') &&
	!document.querySelector(selectors.login) && !!document.querySelector(selectors.search)`

// Login opens the Keys.so login page in a visible browser on the pipeline's persistent profile
// and waits until a person signs in. Keys.so shows Yandex SmartCaptcha to an unrecognized
// account; the form is pre-filled from .env, so the person only solves it. The profile is kept.
func Login(ctx context.Context, email, password string, logger *slog.Logger) error {
	loginCtx, cancel := context.WithTimeout(ctx, manualLoginTimeout)
	defer cancel()

	service := New(Config{Headless: false}, logger)
	if err := service.start(loginCtx); err != nil {
		return err
	}
	defer func() { _ = service.Close() }()

	if _, err := service.page.Goto(loginURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(longOperationTimeoutMilliseconds),
	}); err != nil {
		return fmt.Errorf("open Keys.so login page: %w", err)
	}
	service.prefill(loginCtx, emailSelector, email, "email")
	service.prefill(loginCtx, passwordSelector, password, "password")

	logger.Info("Keys.so: войдите в открытом окне браузера — введите капчу и нажмите «Войти»",
		"timeout", manualLoginTimeout, "profile_dir", profilePath)
	if _, err := service.page.WaitForFunction(signedInJS,
		map[string]any{"login": loginLinkSelector, "search": searchSelector},
		playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(float64(manualLoginTimeout.Milliseconds()))},
	); err != nil {
		if loginCtx.Err() != nil {
			return fmt.Errorf("wait for manual Keys.so login: %w", loginCtx.Err())
		}
		return fmt.Errorf("wait for manual Keys.so login: %w", err)
	}
	logger.Info("Keys.so: вход выполнен, сессия сохранена в профиле", "profile_dir", profilePath)
	return nil
}

// prefill types a credential into the login form; a missing field is not an error.
func (s *Service) prefill(ctx context.Context, selector, value, field string) {
	if value == "" {
		return
	}
	if err := s.page.Locator(selector).Fill(value, playwright.LocatorFillOptions{
		Timeout: playwright.Float(operationTimeoutMilliseconds),
	}); err != nil {
		s.log(ctx, slog.LevelWarn, "Keys.so: поле формы входа не заполнено, введите вручную", "manual_login", "field", field)
	}
}
