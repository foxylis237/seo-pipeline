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

	pace          pacing
	lastRequestAt time.Time

	// openArticleID — статья, диалог которой сейчас открыт в браузере. Ноль означает, что
	// открытой беседы нет и следующий запрос должен начать новую.
	openArticleID int64
}

func NewClient(cfg Config, logger *slog.Logger) (*Client, error) {
	if err := validateConfig(cfg, logger); err != nil {
		return nil, err
	}
	return &Client{
		cfg: cfg, logger: logger.With("provider_type", "deepseek_web"), access: make(chan struct{}, 1),
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
	started := time.Now()
	c.logger.Info("DeepSeek Web generation started", "model", request.Model)
	response, err := c.generate(ctx, request)
	c.markRequestFinished()
	if err != nil {
		c.logger.Warn("DeepSeek Web generation failed", "model", request.Model, "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return llm.Response{}, err
	}
	c.logger.Info("DeepSeek Web generation completed", "model", request.Model, "duration_ms", time.Since(started).Milliseconds(), "response_chars", len([]rune(response.Text)))
	return response, nil
}

func (c *Client) generate(ctx context.Context, request llm.Request) (llm.Response, error) {
	// Проверка до ensureSession: пока действует cooldown, Chromium не запускается вовсе.
	if until, reason, blocked := readBlockedUntil(c.cfg.ProfileDir, c.pace.now()); blocked {
		c.logger.Warn("DeepSeek request rejected by cooldown",
			"blocked_until", until.UTC().Format(time.RFC3339), "reason", reason)
		return llm.Response{}, accountUnavailableError(fmt.Sprintf(
			"cooldown until %s (%s)", until.UTC().Format(time.RFC3339), reason))
	}
	session, err := c.ensureSession()
	if err != nil {
		c.logger.Warn("DeepSeek browser start failed", "error", err)
		return llm.Response{}, temporaryError("start DeepSeek browser", err)
	}
	page := session.page
	timeout := operationTimeout(ctx, defaultOperationTimeout)
	// Переход на chat_url всегда открывает новую беседу. В режиме одного диалога на статью
	// он выполняется только для первой стадии: дальше промпт уходит в уже открытый чат,
	// и модель видит все предыдущие стадии как историю.
	newChat := c.shouldOpenNewChat(request)
	if newChat {
		if _, err := page.Goto(c.cfg.ChatURL, playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateDomcontentloaded,
			Timeout:   playwright.Float(timeout),
		}); err != nil {
			return llm.Response{}, c.browserError(ctx, "open DeepSeek Chat", err)
		}
		c.markChatOpened(request.ArticleID)
		c.stage("open_page", "url", c.cfg.ChatURL, "article_id", request.ArticleID)
	} else {
		c.stage("continue_chat", "article_id", request.ArticleID)
	}
	if reason, blocked := c.detectBlocked(page); blocked {
		return llm.Response{}, c.handleUnavailable(ctx, reason)
	}
	state, err := waitForChatReady(page, timeout)
	if err != nil {
		// Неизвестная страница — самый частый вид блокировки: ни поля ввода, ни формы входа.
		// Проверяем ещё раз, прежде чем считать ошибку временной и уходить в повтор.
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
	c.stage("input_found")

	// Режим и документы задаются до снимка треда: и то, и другое меняет страницу, но
	// сообщением не является, а сообщение должно уйти уже в готовый чат.
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
	sources, err := c.readAnswerSources(page)
	if err != nil {
		return llm.Response{}, c.browserError(ctx, "read DeepSeek answer", err)
	}
	text, source := selectAnswer(sources)
	if text == "" {
		return llm.Response{}, temporaryError("DeepSeek returned an empty response", nil)
	}
	c.stage("answer_received",
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
// Ожидание разбито на короткие интервалы вместо одного длинного, чтобы состояние попадало
// в лог без второй горутины возле страницы Playwright.
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
			// Диагностика и проверка на челлендж — здесь, пока страница ещё жива.
			//
			// Раньше обе стояли у вызывающего, после waitForAnswer, и обе были слепы:
			// browserError ниже сбрасывает сессию вместе со страницей, поэтому скриншот и
			// разметка возвращались с "target closed", а detectBlocked на закрытой странице
			// молча отвечал «не заблокировано». То есть ровно в тот момент, когда надо
			// отличить занятый сервер от антибота, посмотреть было нечем — проверено на
			// пачке pprof_fix_2, где три попытки подряд не оставили ни одного снимка.
			c.saveDiagnostics(page, "wait_answer", articleID)
			// Проверка Cloudflare приходит и в ответ на уже отправленное сообщение: оно
			// уходит, а вместо ответа появляется заглушка. Статья 47 стояла так по пять минут.
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
		// Отказ сервера приходит вместо ответа и сам не исчезнет: ждать дальше нечего, а
		// ждать было чем — до этой проверки ожидание висело до конца бюджета стадии и
		// забирало его целиком, не оставляя времени ни на один повтор.
		if c.detectServerBusy(page, mark) {
			return serverBusyError()
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("wait for complete DeepSeek answer: %w", ctxErr)
		}
		state = responseState(page, mark)
		c.stage("waiting_response", "elapsed", time.Since(started).Round(time.Second).String(), "state", state)
	}
}

// readAnswerSources собирает все доступные представления последнего ответа.
//
// Разметку модели сохраняет только кнопка «Копировать»: innerText отдаёт отрисованный текст,
// в котором «## Заголовок» превращается в «Заголовок», а Markdown-таблица — в строки с
// табуляцией. Поэтому сначала пробуем буфер обмена и лишь потом читаем DOM.
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
		// Буфер — предпочтительный, но не обязательный источник: разметку страницы могли
		// поменять. Остальные источники ниже по точности, но рабочие.
		c.stage("clipboard_unavailable", "error", err.Error())
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

// responseState reports whether the answer has started arriving.
// answerMark фиксирует состояние треда до отправки промпта.
//
// Ключ — основной ориентир: тред виртуализирован, и число смонтированных ответов не растёт.
// Количество остаётся запасным признаком на случай, если разметка списка изменится.
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
	// Ключ читается «по возможности»: если список не виртуализирован или разметка изменилась,
	// ключ остаётся -1 и скрипты ожидания уходят на запасной путь по количеству ответов.
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

// completedAnswerOptions собирает параметры скриптов ожидания в одном месте: имена ключей
// должны совпадать с options.* внутри скрипта, иначе сравнение молча пойдёт с undefined.
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

// stage writes one step of the DeepSeek run in the format the operator greps for.
func (c *Client) stage(name string, fields ...any) {
	c.logger.Info("[deepseek]", append([]any{"stage", name}, fields...)...)
}

// shouldOpenNewChat решает, начинать ли новую беседу.
//
// Без режима одного диалога поведение прежнее: новая беседа на каждый запрос. В режиме
// одного диалога беседа переоткрывается только при смене статьи, по явному требованию
// запроса или после потери сессии.
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
	// Вместе с сессией теряется и открытая беседа: следующий запрос начнёт новую.
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
