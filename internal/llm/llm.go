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
}

type Response struct {
	Text         string
	InputTokens  int
	OutputTokens int
}

type Client interface {
	Generate(ctx context.Context, request Request) (Response, error)
}

type ErrorType string

const (
	ErrorTypeRateLimit        ErrorType = "rate_limit"
	ErrorTypeQuotaExhausted   ErrorType = "quota_exhausted"
	ErrorTypeCreditsExhausted ErrorType = "credits_exhausted"
	ErrorTypeUnauthorized     ErrorType = "unauthorized"
	ErrorTypeProvider         ErrorType = "provider_error"
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
}

type RoutedResponse struct {
	Response
	Prompt   string
	Provider string
	Model    string
}

// PreparedCall contains a rendered stage prompt and its configured routing data.
type PreparedCall struct {
	Prompt   string
	Provider string
	Model    string
	Timeout  time.Duration
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

func (r *Router) Generate(ctx context.Context, call Call) (RoutedResponse, error) {
	prepared, err := r.Prepare(call)
	if err != nil {
		return RoutedResponse{}, err
	}
	stage, found := r.config.Stages[call.Stage]
	if !found {
		return RoutedResponse{}, fmt.Errorf("LLM stage %q is not configured", call.Stage)
	}
	client, found := r.clients[stage.Provider]
	if !found {
		return RoutedResponse{}, fmt.Errorf("LLM provider %q for stage %q is not registered", stage.Provider, call.Stage)
	}
	request := Request{Prompt: prepared.Prompt, Model: stage.Model, Temperature: *stage.Temperature, MaxTokens: stage.MaxTokens}
	stageCtx, cancel := context.WithTimeout(ctx, stage.Timeout)
	defer cancel()
	for attempt := 1; attempt <= 3; attempt++ {
		if err := stageCtx.Err(); err != nil {
			return RoutedResponse{}, fmt.Errorf("LLM stage %q deadline exceeded before attempt %d: %w", call.Stage, attempt, err)
		}
		remaining := time.Duration(-1)
		if deadline, found := stageCtx.Deadline(); found {
			remaining = time.Until(deadline)
			if remaining < 0 {
				remaining = 0
			}
		}
		r.logger.Info("LLM request attempt started",
			"article_id", call.ArticleID, "stage", call.Stage, "provider", stage.Provider,
			"model", stage.Model, "attempt", attempt, "remaining_ms", remaining.Milliseconds(),
		)
		started := time.Now()
		stopHeartbeat := r.startHeartbeat(stageCtx, call, stage.Provider, stage.Model, attempt, started)
		response, requestErr := client.Generate(stageCtx, request)
		stopHeartbeat()
		fields := []any{"article_id", call.ArticleID, "stage", call.Stage, "provider", stage.Provider, "model", stage.Model, "attempt", attempt, "duration_ms", time.Since(started).Milliseconds(), "success", requestErr == nil}
		if requestErr == nil {
			fields = append(fields, "input_tokens", response.InputTokens, "output_tokens", response.OutputTokens)
			r.logger.Info("LLM request completed", fields...)
			return RoutedResponse{Response: response, Prompt: prepared.Prompt, Provider: stage.Provider, Model: stage.Model}, nil
		}
		retryable := isTemporary(requestErr)
		statusCode, errorType, providerMessage := errorLogFields(requestErr)
		fields = append(fields, "status_code", statusCode, "error_type", errorType, "provider_message", providerMessage, "retryable", retryable)
		r.logger.Warn("LLM request failed", fields...)
		if attempt == 3 || !retryable {
			return RoutedResponse{}, routedError(call.Stage, stage.Provider, stage.Model, requestErr)
		}
		if err := r.sleep(stageCtx, r.baseDelay*time.Duration(1<<(attempt-1))); err != nil {
			return RoutedResponse{}, fmt.Errorf("LLM stage %q deadline exceeded before retry: %w", call.Stage, err)
		}
	}
	return RoutedResponse{}, fmt.Errorf("LLM stage %q failed", call.Stage)
}

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
	return PreparedCall{Prompt: prompt.String(), Provider: stage.Provider, Model: stage.Model, Timeout: stage.Timeout}, nil
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
