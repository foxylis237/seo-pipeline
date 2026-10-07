package wordpress

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// StatusError — отказ WordPress REST API с кодом HTTP.
type StatusError struct {
	StatusCode int
	// Endpoint — путь запроса без хоста.
	Endpoint string
	// Code — машинный код WordPress (rest_not_logged_in); пуст, если тело не JSON.
	Code string
	// Message — сообщение WordPress, обрезанное до messageLimit; тело не-JSON сюда не попадает.
	Message   string
	Retryable bool
	// RetryAfter — пауза из заголовка Retry-After.
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("WordPress %s: HTTP %d (%s): %s", e.Endpoint, e.StatusCode, e.Code, e.Message)
	}
	if e.Code != "" {
		return fmt.Sprintf("WordPress %s: HTTP %d (%s)", e.Endpoint, e.StatusCode, e.Code)
	}
	return fmt.Sprintf("WordPress %s: HTTP %d", e.Endpoint, e.StatusCode)
}

// transportError — отказ до ответа (DNS, соединение, разрыв, таймаут попытки); всегда повторяется.
type transportError struct {
	Endpoint string
	Err      error
}

func (e *transportError) Error() string {
	return fmt.Sprintf("WordPress %s: %v", e.Endpoint, e.Err)
}

func (e *transportError) Unwrap() error { return e.Err }

// ResponseError — площадка ответила, но ответ непригоден; считается отказом площадки (IsSystemFailure).
type ResponseError struct {
	// Endpoint — путь REST или имя метода XML-RPC.
	Endpoint string
	Message  string
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("WordPress %s: %s", e.Endpoint, e.Message)
}

// IsSystemFailure отличает отказ площадки (повторится на следующей статье) от негодных данных статьи.
func IsSystemFailure(err error) bool {
	if err == nil {
		return false
	}
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		return true
	}
	var transportErr *transportError
	if errors.As(err, &transportErr) {
		return true
	}
	var faultErr *FaultError
	if errors.As(err, &faultErr) {
		return true
	}
	var responseErr *ResponseError
	if errors.As(err, &responseErr) {
		return true
	}
	// Отмена сюда не относится: её причина в нас.
	return errors.Is(err, context.DeadlineExceeded)
}

// isRetryable решает, имеет ли смысл повтор. Отмену и общий дедлайн не проверять через
// errors.Is: таймаут одной попытки http.Client тоже даёт context.DeadlineExceeded.
func isRetryable(err error) bool {
	var transportErr *transportError
	if errors.As(err, &transportErr) {
		return true
	}
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		return statusErr.Retryable
	}
	return false
}

// retryableStatus перечисляет коды, за которыми стоит состояние сервера, а не наш запрос.
func retryableStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// NeedsCredentialsCheck отличает отказы, которые чинит человек в .env, от временных.
func NeedsCredentialsCheck(err error) bool {
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	return statusErr.StatusCode == http.StatusUnauthorized || statusErr.StatusCode == http.StatusForbidden
}

// parseRetryAfter читает Retry-After в форме delta-seconds; HTTP-date не разбирается —
// WordPress и rate-limit плагины отдают секунды.
func parseRetryAfter(header string) time.Duration {
	seconds, err := strconv.Atoi(header)
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// RetryPolicy ограничивает повторы временных отказов.
type RetryPolicy struct {
	MaxAttempts int
	// BaseDelay — пауза перед второй попыткой; дальше удваивается.
	BaseDelay time.Duration
	MaxDelay  time.Duration
}

// DefaultRetryPolicy — три попытки с паузами 1 с и 2 с.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 3, BaseDelay: time.Second, MaxDelay: 4 * time.Second}
}

// Backoff возвращает паузу перед попыткой, следующей за номером attempt.
func (p RetryPolicy) Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := p.BaseDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
		if p.MaxDelay > 0 && delay >= p.MaxDelay {
			return p.MaxDelay
		}
	}
	if p.MaxDelay > 0 && delay > p.MaxDelay {
		return p.MaxDelay
	}
	return delay
}
