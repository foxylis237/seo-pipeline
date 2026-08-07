package deepseekweb

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/mxschmitt/playwright-go"
)

const (
	// promptSentTimeout — сколько ждать подтверждения отправки. DeepSeek очищает поле ввода,
	// когда принимает сообщение; если текст остался, ответа не будет никогда.
	promptSentTimeout  = 15 * time.Second
	promptPollInterval = 500 * time.Millisecond
)

// sendPrompt вводит промпт и убеждается, что он действительно ушёл.
//
// Fill и Press не сообщают, прижился ли текст и принято ли сообщение. Прогон статьи 47
// упирался именно в это: стадия info молча ждала ответ 300 s, а на снимке страницы поле
// ввода оказалось пустым и третьего ответа не было — сообщение не отправлялось. Теперь
// каждый шаг проверяется, и неудача видна сразу, а не через таймаут стадии.
func (c *Client) sendPrompt(ctx context.Context, page playwright.Page, composer playwright.Locator, request llm.Request, timeout float64) error {
	if err := c.typePrompt(composer, request.Prompt, timeout); err != nil {
		c.saveDiagnostics(page, "type_prompt", request.ArticleID)
		return c.browserError(ctx, "type DeepSeek prompt", err)
	}
	if err := composer.Press("Enter", playwright.LocatorPressOptions{Timeout: playwright.Float(timeout)}); err != nil {
		return c.browserError(ctx, "send DeepSeek prompt", err)
	}
	if c.promptAccepted(composer) {
		c.stage("send_prompt", "length", len([]rune(request.Prompt)), "model", request.Model, "sent_by", "enter")
		return nil
	}
	// Enter не всегда отправляет: если поле осталось заполненным, пробуем кнопку отправки.
	c.stage("send_button_fallback", "article_id", request.ArticleID)
	if err := c.clickSendButton(page); err != nil {
		c.saveDiagnostics(page, "send_button", request.ArticleID)
		return c.browserError(ctx, "click DeepSeek send button", err)
	}
	if !c.promptAccepted(composer) {
		c.saveDiagnostics(page, "confirm_sent", request.ArticleID)
		return c.browserError(ctx, "confirm DeepSeek prompt was sent",
			fmt.Errorf("промпт остался в поле ввода спустя %s после отправки", promptSentTimeout))
	}
	c.stage("send_prompt", "length", len([]rune(request.Prompt)), "model", request.Model, "sent_by", "button")
	return nil
}

// typePrompt пишет промпт в поле и читает его обратно.
//
// Значение, выставленное через Fill, не всегда доходит до состояния компонента: тогда
// Enter отправляет пустоту, а мы ждём ответ, которого не будет. Посимвольный ввод здесь не
// используется намеренно — промпты доходят до 10 000 символов.
func (c *Client) typePrompt(composer playwright.Locator, prompt string, timeout float64) error {
	if err := composer.Fill(prompt, playwright.LocatorFillOptions{Timeout: playwright.Float(timeout)}); err != nil {
		return fmt.Errorf("заполнить поле ввода: %w", err)
	}
	typed, err := composer.InputValue(playwright.LocatorInputValueOptions{Timeout: playwright.Float(timeout)})
	if err != nil {
		return fmt.Errorf("прочитать поле ввода: %w", err)
	}
	if typed != prompt {
		return fmt.Errorf("в поле ввода оказалось %d символов из %d", len([]rune(typed)), len([]rune(prompt)))
	}
	return nil
}

// promptAccepted ждёт, пока поле ввода опустеет: DeepSeek очищает его, принимая сообщение.
func (c *Client) promptAccepted(composer playwright.Locator) bool {
	deadline := time.Now().Add(promptSentTimeout)
	for {
		value, err := composer.InputValue(playwright.LocatorInputValueOptions{
			Timeout: playwright.Float(float64(promptPollInterval.Milliseconds())),
		})
		if err == nil && strings.TrimSpace(value) == "" {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(promptPollInterval)
	}
}

// clickSendButton нажимает кнопку отправки рядом с полем ввода.
//
// Кнопка опознаётся положением, как и «Копировать» под ответом: она самая правая в блоке
// поля ввода. Ни aria-label, ни title у неё нет.
func (c *Client) clickSendButton(page playwright.Page) error {
	value, err := page.Evaluate(clickSendButtonJS, map[string]any{"composerSelector": composerSelector})
	if err != nil {
		return err
	}
	result, _ := value.(string)
	if result != "clicked" {
		return fmt.Errorf("кнопка отправки не найдена: %s", result)
	}
	return nil
}
