package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"text/template"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/config"
)

type Request struct {
	Prompt      string
	Model       string
	Temperature float64
	MaxTokens   int
	// ArticleID позволяет провайдеру держать один диалог на статью вместо нового на
	// каждую стадию. Провайдеры без такого режима поле игнорируют.
	ArticleID int64
	// SingleChat включает режим одного диалога на статью. Свойство запроса, а не клиента:
	// один и тот же браузерный клиент обслуживает обе схемы стадий, и в схеме Gemini этот
	// режим не нужен.
	SingleChat bool
	// NewChat требует начать новую беседу даже в режиме одного диалога. Так поток с
	// несколькими чатами на статью отделяет их друг от друга.
	NewChat bool
	// Attachments — документы, которые уходят вместе с промптом. Пути приходят из
	// конфигурации стадии и уже разрешены: провайдер их не ищет, а только прикрепляет.
	Attachments []string
	// Mode — подпись режима ответа в интерфейсе провайдера. Пустое значение означает
	// «не переключать»; провайдеры без выбора режима поле игнорируют.
	Mode string
	// Search просит включить поиск в интернете, если провайдер это умеет. Пустое значение —
	// прежнее поведение: модель отвечает из своих знаний.
	Search bool
	// Stage — имя стадии, ради которой сделан запрос. Боевые провайдеры его игнорируют:
	// им достаточно модели. Значимо оно там, где запрос надо отличить по стадии, а модель
	// этого больше не даёт — в чате второе и последующие сообщения идут к target, который
	// ответил на первое, поэтому у всех стадий одного чата модель одна.
	Stage string
}

// AttachmentClient — провайдер, умеющий отправить документ вместе с промптом.
//
// Стадия с документами не имеет права молча уйти без него: без регламента модель ответит
// не тем, а по тексту ответа это уже не отличить. Поэтому провайдер без такой поддержки
// получает стадию с вложениями как ошибку маршрутизации, а не как обычный запрос.
type AttachmentClient interface {
	SupportsAttachments() bool
}

type Response struct {
	Text         string
	Model        string
	InputTokens  int
	OutputTokens int
}

type Client interface {
	Generate(ctx context.Context, request Request) (Response, error)
}

// Chat represents one provider conversation without exposing its SDK.
type Chat interface {
	Generate(ctx context.Context, prompt string) (Response, error)
	Close() error
}

type ErrorType string

const (
	ErrorTypeRateLimit        ErrorType = "rate_limit"
	ErrorTypeQuotaExhausted   ErrorType = "quota_exhausted"
	ErrorTypeCreditsExhausted ErrorType = "credits_exhausted"
	ErrorTypeUnauthorized     ErrorType = "unauthorized"
	ErrorTypeProvider         ErrorType = "provider_error"
	// ErrorTypeOverloaded — провайдер жив и авторизация цела, но обслуживать запрос он
	// сейчас отказывается («Server is busy» у DeepSeek). От остальных временных отказов
	// отличается только паузой: повтор через несколько секунд упирается в ту же перегрузку.
	ErrorTypeOverloaded ErrorType = "overloaded"
)

type StatusError struct {
	Code    int
	Type    ErrorType
	Message string
	Err     error
}

func (e *StatusError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("provider request failed with HTTP %d", e.Code)
}
func (e *StatusError) Unwrap() error { return e.Err }

func NewStatusError(code int, providerMessage string) *StatusError {
	errorType := classifyStatusError(code, providerMessage)
	var message string
	switch errorType {
	case ErrorTypeRateLimit:
		message = "rate limit exceeded"
	case ErrorTypeQuotaExhausted:
		message = "quota exceeded"
	case ErrorTypeCreditsExhausted:
		message = "credits exhausted"
	case ErrorTypeUnauthorized:
		message = "authorization failed"
	default:
		message = fmt.Sprintf("provider request failed with HTTP %d", code)
	}
	return &StatusError{Code: code, Type: errorType, Message: message}
}

func classifyStatusError(code int, message string) ErrorType {
	normalized := strings.ToLower(message)
	switch {
	case code == 401 || code == 403:
		return ErrorTypeUnauthorized
	case code == 402:
		return ErrorTypeCreditsExhausted
	case strings.Contains(normalized, "insufficient_quota"),
		strings.Contains(normalized, "resource exhausted"),
		strings.Contains(normalized, "quota exceeded"),
		strings.Contains(normalized, "quota has been exceeded"),
		strings.Contains(normalized, "free requests exhausted"),
		strings.Contains(normalized, "free tier exhausted"):
		return ErrorTypeQuotaExhausted
	case strings.Contains(normalized, "credits exhausted"),
		strings.Contains(normalized, "insufficient credits"):
		return ErrorTypeCreditsExhausted
	case code == 429 || strings.Contains(normalized, "rate limit exceeded"):
		return ErrorTypeRateLimit
	default:
		return ErrorTypeProvider
	}
}

func safeProviderMessage(message string) string {
	message = strings.Join(strings.Fields(message), " ")
	if message == "" {
		return ""
	}
	lower := strings.ToLower(message)
	for _, sensitive := range []string{"authorization", "bearer ", "api_key", "api key"} {
		if strings.Contains(lower, sensitive) {
			return "provider message redacted"
		}
	}
	const maxLength = 160
	runes := []rune(message)
	if len(runes) > maxLength {
		message = string(runes[:maxLength]) + "…"
	}
	return message
}

type Call struct {
	Stage     string
	ArticleID int64
	Data      any
	// NewChat просит провайдера начать новую беседу вместо продолжения текущей. Нужен
	// потокам, у которых чатов на статью несколько: без него провайдер с режимом одного
	// диалога склеил бы их в один. Стадии task_1 флаг не выставляют, и для них ничего
	// не меняется.
	NewChat bool
}

type RoutedResponse struct {
	Response
	Prompt   string
	Provider string
	Model    string
}

// PreparedCall contains a rendered stage prompt. Маршрутные поля отсюда убраны: их никто не
// читал, а держать копию маршрутизации рядом с промптом значит заводить второй её источник.
type PreparedCall struct {
	Prompt string
}

type Router struct {
	config            config.LLMConfig
	clients           map[string]Client
	logger            *slog.Logger
	sleep             func(context.Context, time.Duration) error
	baseDelay         time.Duration
	heartbeatInterval time.Duration
}

func NewRouter(cfg config.LLMConfig, clients map[string]Client, logger *slog.Logger) *Router {
	return &Router{
		config: cfg, clients: clients, logger: logger, sleep: sleepContext,
		baseDelay: 200 * time.Millisecond, heartbeatInterval: 30 * time.Second,
	}
}

// HasStage отвечает, есть ли стадия в схеме этой задачи.
//
// Нужен тем, кто обязан обойтись без стадии, которой у задачи может не быть. Схемы стадий у
// задач разные — набор объявляет профиль, — и спрашивать несуществующую стадию значит упасть
// на «LLM stage ... is not configured» уже после того, как соседние стадии оплачены.
func (r *Router) HasStage(name string) bool {
	_, found := r.config.Stages[name]
	return found
}

func (r *Router) Generate(ctx context.Context, call Call) (RoutedResponse, error) {
	prepared, err := r.Prepare(call)
	if err != nil {
		return RoutedResponse{}, err
	}
	return r.generatePrompt(ctx, call, prepared.Prompt)
}

func (r *Router) generatePrompt(ctx context.Context, call Call, prompt string) (RoutedResponse, error) {
	stage, found := r.config.Stages[call.Stage]
	if !found {
		return RoutedResponse{}, fmt.Errorf("LLM stage %q is not configured", call.Stage)
	}
	if len(stage.Targets) == 0 {
		return RoutedResponse{}, fmt.Errorf("LLM stage %q has no configured targets", call.Stage)
	}
	var failures []error
	for targetIndex, target := range stage.Targets {
		response, targetErr := r.generateTarget(ctx, call, prompt, stage, target, targetIndex)
		if targetErr == nil {
			return response, nil
		}
		failures = append(failures, fmt.Errorf("provider=%s model=%s: %w", target.Provider, target.Model, targetErr))
		if !isFallbackEligible(targetErr) || targetIndex == len(stage.Targets)-1 {
			break
		}
		// Логируем провайдера, на которого переходим: до правки в строке стоял тот, что
		// уже отказал, и по логу было не понять, кто выполнил стадию.
		next := stage.Targets[targetIndex+1]
		r.logger.Warn("LLM fallback selected",
			"article_id", call.ArticleID, "stage", call.Stage,
			"provider", next.Provider, "model", next.Model, "target_index", targetIndex+1,
			"reason", "provider_fallback", "failed_provider", target.Provider,
			"failed_model", target.Model, "error_type", errorTypeOf(targetErr))
	}
	return RoutedResponse{}, fmt.Errorf("LLM stage %q failed for all attempted targets: %w", call.Stage, errors.Join(failures...))
}

// NewStageChatFactory creates chats that retain explicit message history across provider fallback.
func (r *Router) NewStageChatFactory(stages ...string) *StageChatFactory {
	return &StageChatFactory{router: r, stages: stages}
}

// NewIsolatedChatFactory — то же самое, но первое сообщение чата просит провайдера начать
// новую беседу. Нужно там, где чатов на статью несколько и граница между ними значима.
func (r *Router) NewIsolatedChatFactory(stages ...string) *StageChatFactory {
	return &StageChatFactory{router: r, stages: stages, isolated: true}
}

type StageChatFactory struct {
	router *Router
	stages []string
	// isolated включает NewChat на первом сообщении. По умолчанию выключен: чат task_1
	// продолжает беседу, начатую предыдущими стадиями статьи.
	isolated bool
}

func (f *StageChatFactory) NewChat(_ context.Context, articleID int64) (Chat, error) {
	return &stageChat{factory: f, articleID: articleID}, nil
}

// NewChatWithHistory creates a chat that already contains the previous messages of the same
// article. Возобновлённая стадия видит тот же контекст, что и продолжение живого чата, и при
// этом не требует повторного обращения к модели за уже полученным ответом.
func (f *StageChatFactory) NewChatWithHistory(_ context.Context, articleID int64, history ...Message) (Chat, error) {
	return &stageChat{
		factory: f, articleID: articleID,
		history: append([]Message(nil), history...),
		next:    len(history) / 2,
	}, nil
}

type stageChat struct {
	factory   *StageChatFactory
	articleID int64
	next      int
	history   []Message
	closed    bool
	// bound — target, ответивший на первое сообщение чата. Последующие стадии идут к нему же:
	// продолжение диалога не должно попадать к другой модели, чем его начало.
	bound *config.LLMTargetConfig
}
type Message struct{ Role, Content string }

func (c *stageChat) Generate(ctx context.Context, prompt string) (Response, error) {
	if c.closed {
		return Response{}, fmt.Errorf("LLM chat is closed")
	}
	if c.next >= len(c.factory.stages) {
		return Response{}, fmt.Errorf("LLM chat has no configured stage for message %d", c.next+1)
	}
	var transcript strings.Builder
	switch {
	case len(c.history) == 0 || c.keepsContext():
		// Провайдер, который держит один диалог на статью, уже видит предыдущие стадии как
		// историю. Повторная склейка транскрипта отправила бы тот же текст второй раз.
		transcript.WriteString(prompt)
	default:
		for _, message := range c.history {
			fmt.Fprintf(&transcript, "%s:\n%s\n\n", message.Role, message.Content)
		}
		fmt.Fprintf(&transcript, "user:\n%s", prompt)
	}
	stage := c.factory.stages[c.next]
	call := Call{Stage: stage, ArticleID: c.articleID, NewChat: c.factory.isolated && c.next == 0}
	var result RoutedResponse
	var err error
	if c.bound != nil {
		result, err = c.factory.router.generateOnTarget(ctx, call, transcript.String(), *c.bound)
	} else {
		result, err = c.factory.router.generatePrompt(ctx, call, transcript.String())
	}
	if err != nil {
		return Response{}, err
	}
	if c.bound == nil {
		c.bound = &config.LLMTargetConfig{Provider: result.Provider, Model: result.Model}
		c.factory.router.logger.Info("LLM chat bound to provider",
			"article_id", c.articleID, "stage", stage, "provider", result.Provider, "model", result.Model)
	}
	c.history = append(c.history, Message{Role: "user", Content: prompt}, Message{Role: "assistant", Content: result.Text})
	c.next++
	return result.Response, nil
}

func (c *stageChat) Close() error { c.closed = true; c.history = nil; return nil }

// keepsContext сообщает, хранит ли выбранный провайдер историю диалога на своей стороне.
func (c *stageChat) keepsContext() bool {
	if c.bound == nil {
		return false
	}
	return c.factory.router.config.Providers[c.bound.Provider].SingleChatPerArticle
}

// generateOnTarget выполняет стадию на заранее выбранном target, без перебора остальных.
// Повторы внутри самого target сохраняются, fallback на другого провайдера — нет.
func (r *Router) generateOnTarget(ctx context.Context, call Call, prompt string, target config.LLMTargetConfig) (RoutedResponse, error) {
	stage, found := r.config.Stages[call.Stage]
	if !found {
		return RoutedResponse{}, fmt.Errorf("LLM stage %q is not configured", call.Stage)
	}
	return r.generateTarget(ctx, call, prompt, stage, target, 0)
}

func (r *Router) generateTarget(ctx context.Context, call Call, prompt string, stage config.LLMStageConfig, target config.LLMTargetConfig, targetIndex int) (RoutedResponse, error) {
	client, found := r.clients[target.Provider]
	if !found {
		return RoutedResponse{}, fmt.Errorf("LLM provider %q is not registered", target.Provider)
	}
	// Документ стадии разрешается здесь, а не при загрузке конфигурации: файл на диске
	// живёт своей жизнью, и путь к нему обязан быть свежим на момент запроса. Отказ здесь
	// окончательный — обычная ошибка, а не StatusError, поэтому ни повтора, ни перехода к
	// следующему провайдеру не будет: другой провайдер тот же файл тоже не найдёт.
	attachments, err := config.ResolveStageAttachments(call.Stage, stage.AttachmentsDir)
	if err != nil {
		return RoutedResponse{}, err
	}
	if len(attachments) > 0 {
		support, ok := client.(AttachmentClient)
		if !ok || !support.SupportsAttachments() {
			return RoutedResponse{}, fmt.Errorf(
				"LLM stage %q requires document attachments, provider %q does not support them",
				call.Stage, target.Provider)
		}
	}
	request := Request{
		Prompt: prompt, Model: target.Model, Temperature: *stage.Temperature, MaxTokens: stage.MaxTokens,
		ArticleID: call.ArticleID, SingleChat: r.config.Providers[target.Provider].SingleChatPerArticle,
		NewChat: call.NewChat, Attachments: attachments, Mode: stage.Mode, Search: stage.Search,
		Stage: call.Stage,
	}
	// Самоограничение провайдера выдерживается до наложения таймаута стадии: пауза между
	// запросами — не работа модели, и вычитать её из бюджета генерации нельзя.
	if pacer, ok := client.(interface {
		WaitBeforeRequest(context.Context) error
	}); ok {
		if err := pacer.WaitBeforeRequest(ctx); err != nil {
			return RoutedResponse{}, fmt.Errorf("LLM stage %q pacing: %w", call.Stage, err)
		}
	}
	stageCtx, cancel := context.WithTimeout(ctx, stage.Timeout)
	defer cancel()
	attemptTimeout := stage.AttemptTimeout
	if attemptTimeout <= 0 {
		attemptTimeout = stage.Timeout
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if err := stageCtx.Err(); err != nil {
			return RoutedResponse{}, fmt.Errorf("deadline before attempt %d: %w", attempt, err)
		}
		remaining := time.Duration(-1)
		if deadline, found := stageCtx.Deadline(); found {
			remaining = time.Until(deadline)
			if remaining < 0 {
				remaining = 0
			}
		}
		r.logger.Info("LLM request attempt started",
			"article_id", call.ArticleID, "stage", call.Stage, "provider", target.Provider,
			"model", target.Model, "target_index", targetIndex, "attempt", attempt,
			"remaining_ms", remaining.Milliseconds(), "attempt_timeout_ms", attemptTimeout.Milliseconds(),
		)
		started := time.Now()
		// Попытка ограничена отдельно от стадии: без этого первая же зависшая попытка
		// забирала весь бюджет, и до повтора дело не доходило. Родителем остаётся stageCtx,
		// поэтому попытка не может пережить стадию, а без attempt_timeout срок у неё
		// прежний — весь остаток бюджета.
		attemptCtx, cancelAttempt := context.WithTimeout(stageCtx, attemptTimeout)
		stopHeartbeat := r.startHeartbeat(attemptCtx, call, target.Provider, target.Model, attempt, started)
		response, requestErr := client.Generate(attemptCtx, request)
		stopHeartbeat()
		cancelAttempt()
		fields := []any{"article_id", call.ArticleID, "stage", call.Stage, "provider", target.Provider, "model", target.Model, "target_index", targetIndex, "attempt", attempt, "duration_ms", time.Since(started).Milliseconds(), "success", requestErr == nil}
		if requestErr == nil {
			fields = append(fields, "input_tokens", response.InputTokens, "output_tokens", response.OutputTokens)
			r.logger.Info("LLM request completed", fields...)
			return RoutedResponse{Response: response, Prompt: prompt, Provider: target.Provider, Model: target.Model}, nil
		}
		retryable := isTemporary(requestErr)
		statusCode, errorType, providerMessage := errorLogFields(requestErr)
		fields = append(fields, "status_code", statusCode, "error_type", errorType, "provider_message", providerMessage, "retryable", retryable)
		r.logger.Warn("LLM request failed", fields...)
		if attempt == 3 || !retryable {
			return RoutedResponse{}, routedError(call.Stage, target.Provider, target.Model, requestErr)
		}
		if err := r.sleep(stageCtx, r.retryDelay(target.Provider, attempt, requestErr)); err != nil {
			return RoutedResponse{}, fmt.Errorf("LLM stage %q deadline exceeded before retry: %w", call.Stage, err)
		}
	}
	return RoutedResponse{}, fmt.Errorf("LLM target failed")
}

// browserProviderType — тип провайдера, работающего через реальный браузер. Его повторы
// разносятся во времени сильнее остальных: за частоту обращений блокируют аккаунт.
const browserProviderType = "deepseek_web"

// browserRetryDelays задаёт паузы перед повторами для браузерных провайдеров.
var browserRetryDelays = []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second}

// overloadedRetryDelays задаёт паузы для перегруженного провайдера. Секунды здесь бесполезны:
// отказ держится минутами, и быстрый повтор только тратит одну из трёх попыток впустую.
var overloadedRetryDelays = []time.Duration{time.Minute, 3 * time.Minute, 5 * time.Minute}

// retryDelay возвращает паузу перед следующей попыткой того же target. Вид отказа важнее
// провайдера: перегрузку пережидают дольше, чем сбой браузера.
func (r *Router) retryDelay(provider string, attempt int, err error) time.Duration {
	if err != nil && errorTypeOf(err) == ErrorTypeOverloaded {
		return delayFromTable(overloadedRetryDelays, attempt)
	}
	if r.config.Providers[provider].Type == browserProviderType {
		return delayFromTable(browserRetryDelays, attempt)
	}
	return r.baseDelay * time.Duration(1<<(attempt-1))
}

// delayFromTable выбирает паузу по номеру попытки, удерживая номер в границах таблицы.
func delayFromTable(delays []time.Duration, attempt int) time.Duration {
	index := attempt - 1
	if index >= len(delays) {
		index = len(delays) - 1
	}
	if index < 0 {
		index = 0
	}
	return delays[index]
}

func isFallbackEligible(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		switch statusErr.Type {
		// Отказ авторизации и исчерпанная оплата не лечатся повтором у того же провайдера,
		// но резервный провайдер от них не страдает — переключаемся, если он настроен.
		case ErrorTypeQuotaExhausted, ErrorTypeRateLimit, ErrorTypeUnauthorized, ErrorTypeCreditsExhausted:
			return true
		}
		return statusErr.Code == 502 || statusErr.Code == 503 || statusErr.Code == 504
	}
	var netErr net.Error
	return errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary())
}

func errorTypeOf(err error) ErrorType { _, kind, _ := errorLogFields(err); return kind }

// Prepare renders a configured stage without sending an LLM request.
func (r *Router) Prepare(call Call) (PreparedCall, error) {
	stage, found := r.config.Stages[call.Stage]
	if !found {
		return PreparedCall{}, fmt.Errorf("LLM stage %q is not configured", call.Stage)
	}
	tmpl, err := template.New(call.Stage).Parse(stage.PromptTemplate)
	if err != nil {
		return PreparedCall{}, fmt.Errorf("parse prompt for stage %q: %w", call.Stage, err)
	}
	var prompt bytes.Buffer
	if err := tmpl.Execute(&prompt, call.Data); err != nil {
		return PreparedCall{}, fmt.Errorf("render prompt for stage %q: %w", call.Stage, err)
	}
	if strings.TrimSpace(prompt.String()) == "" {
		return PreparedCall{}, fmt.Errorf("LLM stage %q rendered an empty prompt", call.Stage)
	}
	return PreparedCall{Prompt: prompt.String()}, nil
}

func (r *Router) startHeartbeat(ctx context.Context, call Call, provider, model string, attempt int, started time.Time) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(r.heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				remaining := time.Duration(-1)
				if deadline, found := ctx.Deadline(); found {
					remaining = time.Until(deadline)
					if remaining < 0 {
						remaining = 0
					}
				}
				r.logger.Info("LLM request still running",
					"article_id", call.ArticleID, "stage", call.Stage, "provider", provider,
					"model", model, "attempt", attempt, "elapsed_ms", time.Since(started).Milliseconds(),
					"remaining_ms", remaining.Milliseconds(),
				)
			case <-ctx.Done():
				return
			case <-stop:
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

func isTemporary(err error) bool {
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		switch statusErr.Type {
		case ErrorTypeQuotaExhausted, ErrorTypeCreditsExhausted, ErrorTypeUnauthorized:
			return false
		}
		switch statusErr.Code {
		case 429, 500, 502, 503, 504:
			return true
		default:
			return false
		}
	}
	var netErr net.Error
	return errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary())
}

func errorLogFields(err error) (int, ErrorType, string) {
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		errorType := statusErr.Type
		if errorType == "" {
			errorType = classifyStatusError(statusErr.Code, statusErr.Error())
		}
		return statusErr.Code, errorType, safeProviderMessage(statusErr.Error())
	}
	return 0, ErrorTypeProvider, safeProviderMessage(err.Error())
}

func routedError(stage, provider, model string, err error) error {
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		errorType := statusErr.Type
		if errorType == "" {
			errorType = classifyStatusError(statusErr.Code, statusErr.Error())
		}
		switch errorType {
		case ErrorTypeQuotaExhausted:
			return fmt.Errorf("LLM quota exhausted: provider=%s stage=%s model=%s: %w", provider, stage, model, err)
		case ErrorTypeCreditsExhausted:
			return fmt.Errorf("LLM credits exhausted: provider=%s stage=%s model=%s: %w", provider, stage, model, err)
		case ErrorTypeRateLimit:
			return fmt.Errorf("LLM rate limit reached: provider=%s stage=%s model=%s; retry later: %w", provider, stage, model, err)
		case ErrorTypeOverloaded:
			return fmt.Errorf("LLM provider is overloaded: provider=%s stage=%s model=%s; retry later: %w", provider, stage, model, err)
		}
	}
	return fmt.Errorf("LLM stage %q provider %q: %w", stage, provider, err)
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
