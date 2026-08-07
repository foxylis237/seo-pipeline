package deepseekweb

import (
	"context"
	"fmt"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/mxschmitt/playwright-go"
)

const (
	// reasonChallenge — единственная причина недоступности, которая лечится руками и не
	// означает проблем с аккаунтом.
	reasonChallenge = "challenge_or_captcha"

	// captchaWaitTimeout ограничивает ожидание человека. Окно браузера остаётся открытым всё
	// это время; если его закрыть, ожидание прекращается сразу.
	captchaWaitTimeout  = 5 * time.Minute
	captchaPollInterval = 2 * time.Second
)

// captchaError — понятный отказ, когда проверку так и не прошли.
//
// Тип Unauthorized выбран, чтобы isTemporary вернул false: автоматических повторов против
// капчи быть не должно.
func captchaError() error {
	return &llm.StatusError{
		Code:    403,
		Type:    llm.ErrorTypeUnauthorized,
		Message: "DeepSeek requires manual captcha verification",
	}
}

// handleUnavailable разделяет два разных случая, которые до этого лечились одинаково.
//
// Блокировка аккаунта — состояние надолго: пишется cooldown, браузер к аккаунту больше не
// ходит. Проверка Cloudflare или капча — наоборот, разовая помеха: аккаунт исправен,
// авторизация цела, нужен только человек. Профиль в этом случае не трогаем и cooldown не
// включаем.
func (c *Client) handleUnavailable(ctx context.Context, reason string) error {
	if reason != reasonChallenge {
		return c.blockAccount(reason)
	}
	return c.resolveChallenge(ctx)
}

// resolveChallenge открывает видимое окно с тем же профилем и ждёт, пока человек пройдёт
// проверку. Профиль не удаляется, сессия не сбрасывается — меняется только режим окна.
func (c *Client) resolveChallenge(ctx context.Context) error {
	c.logger.Warn("DeepSeek requires manual captcha verification",
		"profile_dir", c.cfg.ProfileDir, "wait", captchaWaitTimeout.String())

	// Headless-сессию приходится закрыть: профиль защищён flock, и видимое окно иначе его
	// не получит. Это закрытие браузера, а не сброс авторизации — cookies остаются в профиле.
	if err := c.resetSession(); err != nil {
		c.logger.Warn("DeepSeek headless session was not closed cleanly", "error", err)
	}

	if err := c.waitForManualVerification(ctx); err != nil {
		c.logger.Error("DeepSeek captcha was not passed", "error", err)
		return captchaError()
	}
	c.logger.Info("DeepSeek captcha passed; continuing with the same profile and session",
		"profile_dir", c.cfg.ProfileDir)
	// Временная ошибка, а не отказ: роутер повторит стадию, и она пойдёт уже по проверенному
	// профилю. Так автоматизация продолжается сама, без перезапуска команды.
	return temporaryError("DeepSeek captcha passed, retrying the stage", nil)
}

// waitForManualVerification держит видимое окно, пока страница не станет рабочей.
//
// Ожидание намеренно не подчиняется таймауту стадии: человек не обязан укладываться в
// бюджет генерации. Прервать его можно, закрыв окно браузера — тогда опрос страницы вернёт
// ошибку и ожидание закончится.
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
	c.logger.Warn("pass the DeepSeek verification in the opened Chromium window; the run continues by itself afterwards")

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
