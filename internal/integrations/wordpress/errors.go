package wordpress

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// StatusError — отказ WordPress REST API, у которого есть код HTTP.
//
// Классифицируется через errors.As, а не по подстрокам сообщения: текст WordPress зависит от
// локали сайта и установленных плагинов, а код — нет.
type StatusError struct {
	// StatusCode — код ответа.
	StatusCode int
	// Endpoint — путь запроса без хоста. Хост в ошибку не попадает намеренно: он не добавляет
	// ничего к диагностике, зато делает сообщения разными на разных площадках.
	Endpoint string
	// Code — машинный код WordPress (rest_not_logged_in, rest_forbidden_context). Пустой,
	// если тело ответа не было JSON — так отвечает не сам WordPress, а веб-сервер или плагин.
	Code string
	// Message — сообщение WordPress, обрезанное до messageLimit. Тело, не похожее на JSON,
	// сюда не попадает: страница логина или заглушка хостера в логе бесполезны.
	Message string
	// Retryable — отказ временный, и повтор имеет смысл.
	Retryable bool
	// RetryAfter — пауза из заголовка Retry-After, если сервер её назвал.
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

// transportError — отказ до того, как появился ответ: DNS, отказ в соединении, разрыв,
// таймаут одной попытки.
//
// Тип нужен ровно для одного решения — повторять или нет. Разбирать net.Error по подвидам
// ради одного GET не окупается: неизвестный сетевой отказ чаще лечится повтором, чем нет.
type transportError struct {
	Endpoint string
	Err      error
}

func (e *transportError) Error() string {
	return fmt.Sprintf("WordPress %s: %v", e.Endpoint, e.Err)
}

func (e *transportError) Unwrap() error { return e.Err }

// ResponseError — площадка ответила, но ответ непригоден: нет идентификатора созданного
// объекта, поле не сохранилось, структура не та.
//
// Отдельный тип, а не fmt.Errorf, потому что по нему принимается решение: такой отказ
// относится к площадке, а не к данным статьи, и следующая статья с большой вероятностью
// упрётся в то же самое. Пропускать её по-тихому нельзя.
type ResponseError struct {
	// Endpoint — вызов, который так ответил (путь REST или имя метода XML-RPC).
	Endpoint string
	Message  string
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("WordPress %s: %s", e.Endpoint, e.Message)
}

// IsSystemFailure отличает отказ площадки от негодных данных статьи.
//
// Вопрос не риторический: на этом различии держится поведение полного прогона. Отказ
// площадки — 401, 5xx, таймаут, оборванное соединение — повторится и на следующей статье,
// и после трёх подряд публикация в прогоне выключается. Негодные данные одной статьи
// (нет метки, нет картинки, пустое поле) касаются только её, и останавливать из-за них
// весь прогон незачем.
//
// Классификация по типам, а не по тексту: сообщения WordPress зависят от локали площадки и
// набора плагинов, а типы — нет.
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
	// Истёкший бюджет — это состояние площадки или канала, а не данных статьи. Отмену сюда
	// не относим: её причина в нас, и выключать из-за неё публикацию бессмысленно — прогон
	// и так заканчивается.
	return errors.Is(err, context.DeadlineExceeded)
}

// isRetryable решает, имеет ли смысл повтор.
//
// Отмена и общий дедлайн сюда не попадают: они возвращаются наружу голыми, без обёртки в
// transportError, и потому честно считаются неповторяемыми. Проверять их через errors.Is
// нельзя — таймаут одной попытки у http.Client тоже разворачивается в
// context.DeadlineExceeded, а его-то повторять как раз и нужно.
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
// Всё остальное — 400, 401, 403, 404 — вторая попытка воспроизведёт слово в слово.
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
// Вызывающему это нужно, чтобы подсказать, что править, вместо «попробуйте позже».
func NeedsCredentialsCheck(err error) bool {
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	return statusErr.StatusCode == http.StatusUnauthorized || statusErr.StatusCode == http.StatusForbidden
}

// parseRetryAfter читает Retry-After в форме delta-seconds.
//
// Форма HTTP-date не поддержана намеренно: WordPress и типовые rate-limit плагины отдают
// секунды, а разбор даты потребовал бы доверять часам чужого сервера. Не разобрали — просто
// уходим на обычный backoff.
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

// DefaultRetryPolicy — три попытки с паузами 1 с и 2 с. Проверка подключения должна либо
// ответить быстро, либо честно сказать, что площадка недоступна: держать человека у
// молчащего терминала дольше нескольких секунд она права не имеет.
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
