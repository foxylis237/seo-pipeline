package deepseekweb

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mxschmitt/playwright-go"

	"github.com/foxylis237/seo-pipeline/internal/llm"
)

type Client struct {
	cfg    Config
	logger *slog.Logger
	// reqLog несёт статью и стадию текущего запроса; запросы сериализует access.
	reqLog  *slog.Logger
	access  chan struct{}
	mu      sync.Mutex
	session *browserSession

	pace          pacing
	lastRequestAt time.Time
	// requestsSinceBreak считает запросы после последнего длинного перерыва.
	requestsSinceBreak int

	// openArticleID — статья открытой беседы; ноль — беседы нет.
	openArticleID int64

	// sent — тексты, отправленные в открытую беседу: свои слова не принимаются за плашку (см. noticeTextJS).
	sent []string
}

func NewClient(cfg Config, logger *slog.Logger) (*Client, error) {
	if err := validateConfig(cfg, logger); err != nil {
		return nil, err
	}
	return &Client{
		cfg: cfg, logger: logger, access: make(chan struct{}, 1),
		pace: defaultPacing(),
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
	c.reqLog = c.logger.With("article_id", request.ArticleID, "stage", request.Stage)
	defer func() { c.reqLog = nil }()
	started := time.Now()
	response, err := c.generate(ctx, request)
	c.markRequestFinished()
	if err != nil {
		c.log().Warn("генерация DeepSeek не удалась", "model", request.Model, "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return llm.Response{}, err
	}
	return response, nil
}

func (c *Client) generate(ctx context.Context, request llm.Request) (llm.Response, error) {
	// Проверка до ensureSession: пока действует cooldown, Chromium не запускается вовсе.
	if until, reason, blocked := readBlockedUntil(c.cfg.ProfileDir, c.pace.now()); blocked {
		c.log().Warn("запрос к DeepSeek отклонён: аккаунт на паузе",
			"blocked_until", until.UTC().Format(time.RFC3339), "reason", reason)
		return llm.Response{}, accountUnavailableError(fmt.Sprintf(
			"cooldown until %s (%s)", until.UTC().Format(time.RFC3339), reason))
	}
	session, err := c.ensureSession()
	if err != nil {
		c.log().Warn("браузер DeepSeek не запустился", "error", err)
		return llm.Response{}, temporaryError("start DeepSeek browser", err)
	}
	page := session.page
	timeout := operationTimeout(ctx, defaultOperationTimeout)
	// Переход на chat_url всегда открывает новую беседу.
	newChat := c.shouldOpenNewChat(request)
	if newChat {
		if _, err := page.Goto(c.cfg.ChatURL, playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateDomcontentloaded,
			Timeout:   playwright.Float(timeout),
		}); err != nil {
			return llm.Response{}, c.browserError(ctx, "open DeepSeek Chat", err)
		}
		c.markChatOpened(request.ArticleID)
		c.step("open_page", "url", c.cfg.ChatURL)
	} else {
		c.step("continue_chat")
	}
	if reason, blocked := c.detectBlocked(page); blocked {
		return llm.Response{}, c.handleUnavailable(ctx, reason)
	}
	state, err := waitForChatReady(page, timeout)
	if err != nil {
		// Страница без поля ввода и формы входа — чаще всего блокировка.
		if reason, blocked := c.detectBlocked(page); blocked {
			return llm.Response{}, c.handleUnavailable(ctx, reason)
		}
		return llm.Response{}, c.browserError(ctx, "wait for DeepSeek Chat", err)
	}
	if state == "expired" {
		return llm.Response{}, sessionExpiredError()
	}
	composer := page.Locator(composerSelector).First()
	if err := composer.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible, Timeout: playwright.Float(timeout),
	}); err != nil {
		return llm.Response{}, c.browserError(ctx, "find DeepSeek prompt input", err)
	}
	c.step("input_found")

	// Режим, поиск и документы — до снимка треда: они меняют страницу, но сообщением не являются.
	c.applyMode(page, request, newChat)
	c.applySearch(page, request, newChat)
	if err := c.attachDocuments(ctx, page, request); err != nil {
		return llm.Response{}, err
	}

	// Снимок треда до отправки: по нему отличается новый ответ от уже имеющихся.
	mark, err := takeAnswerMark(page)
	if err != nil {
		return llm.Response{}, c.browserError(ctx, "read DeepSeek thread state", err)
	}
	if err := c.sendPrompt(ctx, page, composer, request, timeout); err != nil {
		return llm.Response{}, err
	}

	if err := c.waitForAnswer(ctx, page, mark, request.ArticleID); err != nil {
		return llm.Response{}, err
	}
	c.waitForAnswerActions(ctx, page)
	sources, err := c.readAnswerSources(page)
	if err != nil {
		return llm.Response{}, c.browserError(ctx, "read DeepSeek answer", err)
	}
	text, source := selectAnswer(sources)
	if text == "" {
		return llm.Response{}, temporaryError("DeepSeek returned an empty response", nil)
	}
	c.step("answer_received",
		"source", source,
		"response_chars", len([]rune(text)),
		"has_markdown_headings", containsMarkdownHeading(text),
		"has_markdown_table", containsMarkdownTable(text),
		"has_html_tags", containsHTMLTags(text),
	)
	if lost := detectFormatLoss(sources, text, source); len(lost) > 0 {
		return llm.Response{}, formatLostError(request.Model, source, lost)
	}
	return llm.Response{Text: text, Model: request.Model}, nil
}

// waitForAnswer waits for the generation to finish, reporting progress every heartbeat.
// Короткие интервалы — чтобы писать состояние в лог без второй горутины возле страницы.
func (c *Client) waitForAnswer(ctx context.Context, page playwright.Page, mark answerMark, articleID int64) error {
	started := time.Now()
	deadline := started.Add(time.Duration(operationTimeout(ctx, defaultResponseTimeout)) * time.Millisecond)
	state := "waiting"
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if c.detectServerBusy(page, mark) {
				return serverBusyError()
			}
			if expired, checkErr := isSessionExpired(page); checkErr == nil && expired {
				return sessionExpiredError()
			}
			// Снимок и проверка на челлендж — до browserError: он закрывает страницу.
			// Проверка Cloudflare приходит и вместо ответа на уже отправленное сообщение.
			c.saveDiagnostics(page, "wait_answer", articleID)
			if reason, blocked := c.detectBlocked(page); blocked {
				return c.handleUnavailable(ctx, reason)
			}
			return c.browserError(ctx, "wait for complete DeepSeek answer", fmt.Errorf(
				"ответ не завершился за %s, последнее состояние %s", time.Since(started).Round(time.Second), state,
			))
		}
		wait := min(responseHeartbeat, remaining)
		_, err := page.WaitForFunction(completedAnswerJS, completedAnswerOptions(mark),
			playwright.PageWaitForFunctionOptions{Polling: "raf", Timeout: playwright.Float(float64(wait.Milliseconds()))})
		if err == nil {
			return nil
		}
		// Отказ сервера сам не исчезнет: без проверки ожидание съело бы весь бюджет стадии.
		if c.detectServerBusy(page, mark) {
			return serverBusyError()
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("wait for complete DeepSeek answer: %w", ctxErr)
		}
		state = responseState(page, mark)
		c.step("waiting_response", "duration_ms", time.Since(started).Milliseconds(), "state", state)
	}
}

// waitForAnswerActions waits for the action panel under the last answer before it is read.
// Без панели нет кнопки «Копировать»; не дождавшись её, чтение идёт дальше с предупреждением.
func (c *Client) waitForAnswerActions(ctx context.Context, page playwright.Page) {
	ready, err := page.Evaluate(answerActionsReadyJS, map[string]any{"answerSelector": answerSelector})
	if err == nil {
		if ok, _ := ready.(bool); ok {
			return
		}
	}
	wait := answerActionsGrace
	if deadline, ok := ctx.Deadline(); ok {
		wait = min(wait, time.Until(deadline))
	}
	if wait <= 0 {
		c.step("answer_actions_missing", "duration_ms", 0)
		return
	}
	started := time.Now()
	if _, err := page.WaitForFunction(answerActionsReadyJS, map[string]any{"answerSelector": answerSelector},
		playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(float64(wait.Milliseconds()))}); err != nil {
		c.log().Warn("панель действий под ответом DeepSeek не появилась, ответ может быть оборван",
			"duration_ms", time.Since(started).Milliseconds(), "error", err.Error())
		return
	}
	c.step("answer_actions_ready", "duration_ms", time.Since(started).Milliseconds())
}

// readAnswerSources собирает все доступные представления последнего ответа.
// innerText теряет Markdown (заголовки, таблицы), поэтому нужен и буфер обмена.
func (c *Client) readAnswerSources(page playwright.Page) (answerSources, error) {
	sources := answerSources{}
	raw, err := page.Evaluate(answerSourcesJS, map[string]any{"answerSelector": answerSelector})
	if err != nil {
		return sources, err
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return sources, fmt.Errorf("unexpected DeepSeek answer payload %T", raw)
	}
	sources.Rendered, _ = fields["rendered"].(string)
	sources.CodeBlock, _ = fields["codeBlock"].(string)
	sources.DOMHasTable, _ = fields["hasTable"].(bool)
	sources.DOMHasHeadings, _ = fields["hasHeadings"].(bool)

	clipboard, err := c.copyAnswerToClipboard(page)
	if err != nil {
		c.step("clipboard_unavailable", "error", err.Error())
		return sources, nil
	}
	sources.Clipboard = clipboard
	return sources, nil
}

// copyAnswerToClipboard нажимает кнопку копирования и ждёт нового значения буфера.
func (c *Client) copyAnswerToClipboard(page playwright.Page) (string, error) {
	marker := clipboardMarker
	if _, err := page.Evaluate(`(marker) => navigator.clipboard.writeText(marker)`, marker); err != nil {
		return "", fmt.Errorf("подготовить буфер обмена: %w", err)
	}
	clicked, err := page.Evaluate(copyLastAnswerJS, map[string]any{"answerSelector": answerSelector})
	if err != nil {
		return "", fmt.Errorf("нажать кнопку копирования: %w", err)
	}
	if state, _ := clicked.(string); state != "clicked" {
		return "", fmt.Errorf("кнопка копирования не найдена: %s", state)
	}
	deadline := time.Now().Add(clipboardTimeout)
	for {
		raw, err := page.Evaluate(`() => navigator.clipboard.readText()`)
		if err != nil {
			return "", fmt.Errorf("прочитать буфер обмена: %w", err)
		}
		value, _ := raw.(string)
		if text, ok := acceptClipboardValue(marker, value); ok {
			return text, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("буфер обмена не обновился за %s", clipboardTimeout)
		}
		if _, err := page.WaitForFunction(
			`marker => navigator.clipboard.readText().then(value => value.trim() !== "" && value !== marker)`,
			marker,
			playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(float64(clipboardPollInterval.Milliseconds()))},
		); err != nil {
			continue
		}
	}
}

// formatLostError сообщает, что видимый текст потерял разметку, показанную на странице.
func formatLostError(model, source string, lost []string) error {
	return &llm.StatusError{
		Code: 503, Type: llm.ErrorTypeProvider,
		Message: fmt.Sprintf("deepseek_response_format_lost: model=%s source=%s lost=%s",
			model, source, strings.Join(lost, ",")),
	}
}

// answerMark фиксирует состояние треда до отправки промпта.
// Ключ — основной ориентир: тред виртуализирован, и число смонтированных ответов не растёт.
type answerMark struct {
	count int
	key   int
}

// takeAnswerMark снимает состояние треда перед отправкой промпта.
func takeAnswerMark(page playwright.Page) (answerMark, error) {
	count, err := page.Locator(answerSelector).Count()
	if err != nil {
		return answerMark{}, err
	}
	// Ключ -1 — скрипты ожидания сравнивают по количеству ответов.
	mark := answerMark{count: count, key: -1}
	if value, evaluateErr := page.Evaluate(lastItemKeyJS, map[string]any{
		"itemSelector": itemSelector, "itemKeyAttribute": itemKeyAttribute,
	}); evaluateErr == nil {
		switch key := value.(type) {
		case int:
			mark.key = key
		case float64:
			mark.key = int(key)
		}
	}
	return mark, nil
}

// completedAnswerOptions: ключи совпадают с options.* в скриптах ожидания.
func completedAnswerOptions(mark answerMark) map[string]any {
	options := responseStateOptions(mark)
	options["settledForMs"] = responseSettledFor.Milliseconds()
	options["stableForMs"] = responseStableFor.Milliseconds()
	return options
}

func responseStateOptions(mark answerMark) map[string]any {
	return map[string]any{
		"answerSelector":   answerSelector,
		"stopSelector":     stopSelector,
		"itemSelector":     itemSelector,
		"itemKeyAttribute": itemKeyAttribute,
		"previousCount":    mark.count,
		"previousKey":      mark.key,
	}
}

func responseState(page playwright.Page, mark answerMark) string {
	value, err := page.Evaluate(responseStateJS, responseStateOptions(mark))
	if err != nil {
		return "unknown"
	}
	if state, ok := value.(string); ok {
		return state
	}
	return "unknown"
}

// step writes one step of the DeepSeek run.
func (c *Client) step(name string, fields ...any) {
	c.log().Info("шаг DeepSeek", append([]any{"step", name}, fields...)...)
}

// log returns the logger of the current request, outside a request the client's own.
func (c *Client) log() *slog.Logger {
	if c.reqLog != nil {
		return c.reqLog
	}
	return c.logger
}

// shouldOpenNewChat решает, начинать ли новую беседу: в режиме одного диалога — только при
// смене статьи, по требованию запроса или после потери сессии.
func (c *Client) shouldOpenNewChat(request llm.Request) bool {
	if request.NewChat {
		return true
	}
	if !request.SingleChat || request.ArticleID == 0 {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.openArticleID != request.ArticleID
}

func (c *Client) markChatOpened(articleID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.openArticleID = articleID
	c.sent = nil
}

func (c *Client) rememberSentText(prompt string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, prompt)
}

// sentTexts отдаёт копию: срез уходит в браузер.
func (c *Client) sentTexts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sent...)
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
	c.log().Warn("операция в браузере DeepSeek не удалась", "step", operation, "error", err)
	_ = c.resetSession()
	return temporaryError(operation, err)
}

func (c *Client) resetSession() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.session.close()
	c.session = nil
	c.openArticleID = 0
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
	defer func() { _ = handle.Dispose() }()
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
