package deepseekweb

import (
	"context"
	"fmt"
	"time"

	"github.com/mxschmitt/playwright-go"

	"github.com/foxylis237/seo-pipeline/internal/llm"
)

const (
	// reasonChallenge — причина, которая лечится руками и не означает проблем с аккаунтом.
	reasonChallenge = "challenge_or_captcha"

	// captchaWaitTimeout ограничивает ожидание человека; закрытое окно прекращает его сразу.
	captchaWaitTimeout  = 5 * time.Minute
	captchaPollInterval = 2 * time.Second
)

// captchaError — отказ, когда проверку не прошли; Unauthorized не повторяется.
func captchaError() error {
	return &llm.StatusError{
		Code:    403,
		Type:    llm.ErrorTypeUnauthorized,
		Message: "DeepSeek requires manual captcha verification",
	}
}

// handleUnavailable: блокировка аккаунта пишет cooldown, а проверка Cloudflare или капча —
// разовая помеха, её проходит человек без cooldown и без сброса профиля.
func (c *Client) handleUnavailable(ctx context.Context, reason string) error {
	if reason != reasonChallenge {
		return c.blockAccount(reason)
	}
	return c.resolveChallenge(ctx)
}

// resolveChallenge открывает видимое окно с тем же профилем и ждёт, пока человек пройдёт проверку.
func (c *Client) resolveChallenge(ctx context.Context) error {
	c.log().Warn("DeepSeek требует ручной проверки",
		"profile_dir", c.cfg.ProfileDir, "wait", captchaWaitTimeout.String())

	// Профиль под flock: headless-сессию закрываем, cookies остаются.
	if err := c.resetSession(); err != nil {
		c.log().Warn("фоновая сессия DeepSeek закрылась с ошибкой", "error", err)
	}

	if err := c.waitForManualVerification(ctx); err != nil {
		c.log().Error("проверка DeepSeek не пройдена", "error", err)
		return captchaError()
	}
	c.log().Info("проверка DeepSeek пройдена, продолжаем в том же профиле",
		"profile_dir", c.cfg.ProfileDir)
	// Временная ошибка: роутер повторит стадию по проверенному профилю.
	return temporaryError("DeepSeek captcha passed, retrying the stage", nil)
}

// waitForManualVerification держит видимое окно, пока страница не станет рабочей.
// Таймаут стадии на ожидание не действует; прервать — закрыть окно.
func (c *Client) waitForManualVerification(ctx context.Context) error {
	session, err := launchBrowser(c.cfg.ProfileDir, false)
	if err != nil {
		return fmt.Errorf("открыть видимое окно DeepSeek: %w", err)
	}
	defer func() { _ = session.close() }()

	waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), captchaWaitTimeout)
	defer cancel()

	if _, err := session.page.Goto(c.cfg.ChatURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(operationTimeout(waitCtx, defaultOperationTimeout)),
	}); err != nil {
		return fmt.Errorf("открыть DeepSeek Chat для ручной проверки: %w", err)
	}
	c.log().Warn("пройдите проверку DeepSeek в открытом окне Chromium — прогон продолжится сам")

	for {
		if _, blocked := c.detectBlocked(session.page); !blocked {
			if state, err := waitForChatReady(session.page, float64(captchaPollInterval.Milliseconds())); err == nil && state == "ready" {
				return nil
			}
		}
		if _, err := session.page.Title(); err != nil {
			return fmt.Errorf("окно проверки закрыто: %w", err)
		}
		if err := waitCtx.Err(); err != nil {
			return fmt.Errorf("проверка не пройдена за %s: %w", captchaWaitTimeout, err)
		}
		if err := sleepContext(waitCtx, captchaPollInterval); err != nil {
			return fmt.Errorf("проверка не пройдена за %s: %w", captchaWaitTimeout, err)
		}
	}
}
