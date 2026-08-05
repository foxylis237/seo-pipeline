package deepseekweb

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/mxschmitt/playwright-go"
)

func Login(ctx context.Context, cfg Config, logger *slog.Logger) error {
	if err := validateConfig(cfg, logger); err != nil {
		return err
	}
	loginCtx, cancel := context.WithTimeout(ctx, defaultLoginTimeout)
	defer cancel()
	session, err := launchBrowser(cfg.ProfileDir, false)
	if err != nil {
		return err
	}
	defer session.close()

	logger.Info("opening DeepSeek Chat for manual login", "profile_dir", cfg.ProfileDir)
	if _, err := session.page.Goto(cfg.ChatURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(operationTimeout(loginCtx, defaultOperationTimeout)),
	}); err != nil {
		return fmt.Errorf("open DeepSeek Chat: %w", err)
	}
	state, err := waitForChatReady(session.page, operationTimeout(loginCtx, defaultOperationTimeout))
	if err != nil {
		return fmt.Errorf("check DeepSeek session: %w", err)
	}
	if state == "ready" {
		logger.Info("DeepSeek session is already active", "profile_dir", cfg.ProfileDir)
		return nil
	}
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
