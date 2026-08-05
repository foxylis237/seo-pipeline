package deepseekweb

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/mxschmitt/playwright-go"
)

type Client struct {
	cfg     Config
	logger  *slog.Logger
	access  chan struct{}
	mu      sync.Mutex
	session *browserSession
}

func NewClient(cfg Config, logger *slog.Logger) (*Client, error) {
	if err := validateConfig(cfg, logger); err != nil {
		return nil, err
	}
	return &Client{
		cfg: cfg, logger: logger.With("provider_type", "deepseek_web"), access: make(chan struct{}, 1),
	}, nil
}

func (c *Client) Generate(ctx context.Context, request llm.Request) (llm.Response, error) {
	if strings.TrimSpace(request.Prompt) == "" {
		return llm.Response{}, fmt.Errorf("prompt is empty")
	}
	select {
	case c.access <- struct{}{}:
		defer func() { <-c.access }()
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	started := time.Now()
	c.logger.Info("DeepSeek Web generation started", "model", request.Model)
	response, err := c.generate(ctx, request)
	if err != nil {
		c.logger.Warn("DeepSeek Web generation failed", "model", request.Model, "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return llm.Response{}, err
	}
	c.logger.Info("DeepSeek Web generation completed", "model", request.Model, "duration_ms", time.Since(started).Milliseconds(), "response_chars", len([]rune(response.Text)))
	return response, nil
}

func (c *Client) generate(ctx context.Context, request llm.Request) (llm.Response, error) {
	session, err := c.ensureSession()
	if err != nil {
		c.logger.Warn("DeepSeek browser start failed", "error", err)
		return llm.Response{}, temporaryError("start DeepSeek browser", err)
	}
	page := session.page
	timeout := operationTimeout(ctx, defaultOperationTimeout)
	if _, err := page.Goto(c.cfg.ChatURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(timeout),
	}); err != nil {
		return llm.Response{}, c.browserError(ctx, "open DeepSeek Chat", err)
	}
	state, err := waitForChatReady(page, timeout)
	if err != nil {
		return llm.Response{}, c.browserError(ctx, "wait for DeepSeek Chat", err)
	}
	if state == "expired" {
		return llm.Response{}, sessionExpiredError()
	}
	c.stage("open_page", "url", c.cfg.ChatURL)

	composer := page.Locator(composerSelector).First()
	if err := composer.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible, Timeout: playwright.Float(timeout),
	}); err != nil {
		return llm.Response{}, c.browserError(ctx, "find DeepSeek prompt input", err)
	}
	c.stage("input_found")

	answers := page.Locator(answerSelector)
	previousCount, err := answers.Count()
	if err != nil {
		return llm.Response{}, c.browserError(ctx, "count DeepSeek answers", err)
	}
	if err := composer.Fill(request.Prompt, playwright.LocatorFillOptions{Timeout: playwright.Float(timeout)}); err != nil {
		return llm.Response{}, c.browserError(ctx, "fill DeepSeek prompt", err)
	}
	if err := composer.Press("Enter", playwright.LocatorPressOptions{Timeout: playwright.Float(timeout)}); err != nil {
		return llm.Response{}, c.browserError(ctx, "send DeepSeek prompt", err)
	}
	c.stage("send_prompt", "length", len([]rune(request.Prompt)), "model", request.Model)

	if err := c.waitForAnswer(ctx, page, previousCount); err != nil {
		return llm.Response{}, err
	}
	text, source, err := answerText(answers.Last())
	if err != nil {
		return llm.Response{}, c.browserError(ctx, "read DeepSeek answer", err)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return llm.Response{}, temporaryError("DeepSeek returned an empty response", nil)
	}
	c.stage("answer_received", "length", len([]rune(text)), "source", source)
	return llm.Response{Text: text, Model: request.Model}, nil
}

// waitForAnswer waits for the generation to finish, reporting progress every heartbeat.
// Ожидание разбито на короткие интервалы вместо одного длинного, чтобы состояние попадало
// в лог без второй горутины возле страницы Playwright.
func (c *Client) waitForAnswer(ctx context.Context, page playwright.Page, previousCount int) error {
	started := time.Now()
	deadline := started.Add(time.Duration(operationTimeout(ctx, defaultResponseTimeout)) * time.Millisecond)
	state := "waiting"
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if expired, checkErr := isSessionExpired(page); checkErr == nil && expired {
				return sessionExpiredError()
			}
			return c.browserError(ctx, "wait for complete DeepSeek answer", fmt.Errorf(
				"ответ не завершился за %s, последнее состояние %s", time.Since(started).Round(time.Second), state,
			))
		}
		wait := responseHeartbeat
		if remaining < wait {
			wait = remaining
		}
		_, err := page.WaitForFunction(completedAnswerJS, map[string]any{
			"answerSelector": answerSelector,
			"stopSelector":   stopSelector,
			"previousCount":  previousCount,
			"stableForMs":    responseStableFor.Milliseconds(),
		}, playwright.PageWaitForFunctionOptions{Polling: "raf", Timeout: playwright.Float(float64(wait.Milliseconds()))})
		if err == nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("wait for complete DeepSeek answer: %w", ctxErr)
		}
		state = responseState(page, previousCount)
		c.stage("waiting_response", "elapsed", time.Since(started).Round(time.Second).String(), "state", state)
	}
}

// answerText returns the text the model wrote and where it was taken from.
//
// Разметку чат показывает блоком кода: внутри него лежит исходный текст модели, а innerText
// всего ответа отдал бы отрендеренный DOM вместе с классами интерфейса. Для обычных
// текстовых стадий блока кода нет, и поведение остаётся прежним.
func answerText(answer playwright.Locator) (string, string, error) {
	value, err := answer.Evaluate(answerTextJS, nil)
	if err != nil {
		return "", "", err
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return "", "", fmt.Errorf("unexpected DeepSeek answer payload %T", value)
	}
	text, _ := fields["text"].(string)
	source, _ := fields["source"].(string)
	return text, source, nil
}

// responseState reports whether the answer has started arriving.
func responseState(page playwright.Page, previousCount int) string {
	value, err := page.Evaluate(responseStateJS, map[string]any{
		"answerSelector": answerSelector,
		"stopSelector":   stopSelector,
		"previousCount":  previousCount,
	})
	if err != nil {
		return "unknown"
	}
	if state, ok := value.(string); ok {
		return state
	}
	return "unknown"
}

// stage writes one step of the DeepSeek run in the format the operator greps for.
func (c *Client) stage(name string, fields ...any) {
	c.logger.Info("[deepseek]", append([]any{"stage", name}, fields...)...)
}

func (c *Client) ensureSession() (*browserSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil {
		return c.session, nil
	}
	session, err := launchBrowser(c.cfg.ProfileDir, c.cfg.Headless)
	if err != nil {
		return nil, err
	}
	c.session = session
	return session, nil
}

func (c *Client) browserError(ctx context.Context, operation string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%s: %w", operation, ctxErr)
	}
	c.logger.Warn("DeepSeek browser operation failed", "operation", operation, "error", err)
	_ = c.resetSession()
	return temporaryError(operation, err)
}

func (c *Client) resetSession() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.session.close()
	c.session = nil
	return err
}

func (c *Client) Close() error {
	select {
	case c.access <- struct{}{}:
		defer func() { <-c.access }()
	default:
		return fmt.Errorf("close DeepSeek Web client while generation is running")
	}
	return c.resetSession()
}

func operationTimeout(ctx context.Context, limit time.Duration) float64 {
	remaining := limit
	if deadline, ok := ctx.Deadline(); ok {
		untilDeadline := time.Until(deadline)
		if remaining == 0 || untilDeadline < remaining {
			remaining = untilDeadline
		}
	}
	if remaining <= 0 {
		remaining = time.Millisecond
	}
	return float64(remaining.Milliseconds())
}

func waitForChatReady(page playwright.Page, timeout float64) (string, error) {
	handle, err := page.WaitForFunction(chatReadyJS, map[string]any{
		"composerSelector": composerSelector,
		"loginSelector":    loginSelector,
	}, playwright.PageWaitForFunctionOptions{Polling: "raf", Timeout: playwright.Float(timeout)})
	if err != nil {
		return "", err
	}
	defer handle.Dispose()
	value, err := handle.JSONValue()
	if err != nil {
		return "", err
	}
	state, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("unexpected DeepSeek chat state %T", value)
	}
	return state, nil
}

func isSessionExpired(page playwright.Page) (bool, error) {
	if isLoginURL(page.URL()) {
		return true, nil
	}
	return visible(page, loginSelector)
}

func sessionExpiredError() error {
	return &llm.StatusError{Code: 401, Type: llm.ErrorTypeUnauthorized, Message: "DeepSeek session expired. Run deepseek-login."}
}

func temporaryError(message string, err error) error {
	return &llm.StatusError{Code: 503, Type: llm.ErrorTypeProvider, Message: message, Err: err}
}
