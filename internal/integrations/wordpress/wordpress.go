// Package wordpress обращается к WordPress по REST API и XML-RPC обычным net/http.
//
// Наружу выставлены операции, а не произвольный доступ к API: универсального Do(method, path)
// нет. О задачах пакет не знает — площадку и доступ задаёт Config, один Client на площадку.
package wordpress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// currentUserPath — проверка подключения; без context=edit WordPress не отдаёт username и capabilities.
const currentUserPath = "/wp-json/wp/v2/users/me?context=edit"

const publishPostsCapability = "publish_posts"

const (
	// defaultTimeout — бюджет одной попытки; общий потолок ставит вызывающий контекстом.
	// Площадка с плагинами отвечает на /wp/v2/categories и /wp/v2/tags дольше десяти секунд.
	defaultTimeout = 45 * time.Second

	// maxResponseBytes ограничивает читаемый ответ.
	maxResponseBytes = 1 << 20

	// messageLimit обрезает сообщение сервера, попадающее в ошибку и лог.
	messageLimit = 200
)

// Config — доступ к одной площадке WordPress; окружение пакет не читает.
type Config struct {
	// BaseURL — корень сайта, https; подкаталог допустим.
	BaseURL  string
	Username string
	// AppPassword — Application Password, не пароль администратора.
	AppPassword string
	// Timeout — бюджет одной попытки. Ноль означает defaultTimeout.
	Timeout time.Duration
	// Retry — политика повторов. Нулевая означает DefaultRetryPolicy.
	Retry RetryPolicy
	// Transport подменяет транспорт всех трёх клиентов (обход VPN, internal/integrations/netbind);
	// nil — транспорт по умолчанию.
	Transport http.RoundTripper
}

// User — пользователь, которым авторизован клиент.
type User struct {
	ID    int64
	Login string
	Name  string
	// CanPublishPosts — есть ли у пользователя право publish_posts; на успех проверки не влияет.
	CanPublishPosts bool
}

// Connection — то, чем закончилась проверка подключения.
type Connection struct {
	StatusCode int
	User       User
}

// Client работает с одной площадкой; сайт и credentials фиксируются при создании.
type Client struct {
	cfg        Config
	httpClient *http.Client
	// xmlrpcClient и mediaClient — отдельные клиенты ради своих таймаутов: тело статьи и файл
	// обложки передаются дольше, чем читается справочник.
	xmlrpcClient *http.Client
	mediaClient  *http.Client
	// sleep подменяется в тестах.
	sleep func(ctx context.Context, d time.Duration) error
}

// NewClient проверяет настройки и собирает клиента.
func NewClient(cfg Config) (*Client, error) {
	normalized, err := cfg.normalized()
	if err != nil {
		return nil, err
	}
	return &Client{
		cfg:          normalized,
		httpClient:   &http.Client{Timeout: normalized.Timeout, Transport: cfg.Transport},
		xmlrpcClient: &http.Client{Timeout: xmlrpcTimeout, Transport: cfg.Transport},
		mediaClient:  &http.Client{Timeout: mediaUploadTimeout, Transport: cfg.Transport},
		sleep:        sleepContext,
	}, nil
}

// CheckConnection подтверждает read-only запросом, что credentials рабочие и REST API доступен.
func (c *Client) CheckConnection(ctx context.Context) (Connection, error) {
	var payload currentUserPayload
	if err := c.get(ctx, currentUserPath, &payload); err != nil {
		return Connection{}, err
	}
	user := payload.user()
	// 200 без id и username отдаёт кэширующий прокси или заглушка, а не WordPress.
	if user.ID <= 0 || user.Login == "" {
		return Connection{}, fmt.Errorf(
			"WordPress %s: ответ 200, но в нём нет id и username — похоже, отвечает не REST API",
			currentUserPath)
	}
	return Connection{StatusCode: http.StatusOK, User: user}, nil
}

// currentUserPayload — часть ответа users/me. capabilities — map[string]any: плагины кладут
// туда и нелогические значения.
type currentUserPayload struct {
	ID           int64          `json:"id"`
	Username     string         `json:"username"`
	Name         string         `json:"name"`
	Capabilities map[string]any `json:"capabilities"`
}

func (p currentUserPayload) user() User {
	return User{
		ID:              p.ID,
		Login:           p.Username,
		Name:            p.Name,
		CanPublishPosts: capabilityEnabled(p.Capabilities[publishPostsCapability]),
	}
}

// capabilityEnabled приводит значение права к булеву: оно приходит как true, 1 или "1".
func capabilityEnabled(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case float64:
		return typed != 0
	case string:
		return typed == "1" || strings.EqualFold(typed, "true")
	default:
		return false
	}
}

// get выполняет GET с повторами.
func (c *Client) get(ctx context.Context, path string, out any) error {
	endpoint := c.cfg.BaseURL + path
	attempts := c.cfg.Retry.MaxAttempts
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		err := c.getOnce(ctx, endpoint, path, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isRetryable(err) || attempt == attempts {
			break
		}
		if sleepErr := c.sleep(ctx, c.retryDelay(err, attempt)); sleepErr != nil {
			// Обе причины: иначе виден только «context deadline exceeded», а не ответ сайта.
			return errors.Join(lastErr, sleepErr)
		}
	}
	return lastErr
}

// retryDelay выбирает паузу перед следующей попыткой: Retry-After сервера важнее своего backoff,
// а сверху паузу ограничивает дедлайн контекста.
func (c *Client) retryDelay(err error, attempt int) time.Duration {
	var statusErr *StatusError
	if errors.As(err, &statusErr) && statusErr.RetryAfter > 0 {
		return statusErr.RetryAfter
	}
	return c.cfg.Retry.Backoff(attempt)
}

func (c *Client) getOnce(ctx context.Context, endpoint, path string, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("собрать запрос %s: %w", path, err)
	}
	request.Header.Set("Accept", "application/json")
	request.SetBasicAuth(c.cfg.Username, c.cfg.AppPassword)

	response, err := c.httpClient.Do(request)
	if err != nil {
		// Отмена и дедлайн не оборачиваются в transportError: тот повторяется всегда.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("запрос %s прерван: %w", path, ctxErr)
		}
		return &transportError{Endpoint: path, Err: err}
	}
	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("прочитать ответ %s: %w", path, ctxErr)
		}
		return &transportError{Endpoint: path, Err: fmt.Errorf("прочитать ответ: %w", err)}
	}
	if response.StatusCode != http.StatusOK {
		return newStatusError(path, response, body, c.cfg.AppPassword)
	}
	if err := json.Unmarshal(body, out); err != nil {
		// Тело в ошибку не кладём: при неверном пути это HTML целой страницы.
		return fmt.Errorf("разобрать ответ %s: %w", path, err)
	}
	return nil
}

// newStatusError собирает ошибку из ответа; secret вычищается: плагины безопасности
// повторяют присланное в тексте отказа.
func newStatusError(path string, response *http.Response, body []byte, secret string) *StatusError {
	statusErr := &StatusError{
		StatusCode: response.StatusCode,
		Endpoint:   path,
		Retryable:  retryableStatus(response.StatusCode),
		RetryAfter: parseRetryAfter(response.Header.Get("Retry-After")),
	}
	// Тело, не похожее на JSON-ошибку WordPress, в сообщение не попадает.
	var payload struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		statusErr.Code = redactSecret(payload.Code, secret)
		statusErr.Message = truncate(redactSecret(payload.Message, secret), messageLimit)
	}
	return statusErr
}

func redactSecret(value, secret string) string {
	if secret == "" {
		return value
	}
	return strings.ReplaceAll(value, secret, "***")
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

func (c Config) normalized() (Config, error) {
	c.Username = strings.TrimSpace(c.Username)
	if c.Username == "" {
		return Config{}, errors.New("WordPress username is empty")
	}
	// Пароль не триммится: WordPress показывает Application Password группами через пробел.
	if c.AppPassword == "" {
		return Config{}, errors.New("WordPress application password is empty")
	}
	baseURL, err := validateBaseURL(c.BaseURL)
	if err != nil {
		return Config{}, err
	}
	c.BaseURL = baseURL
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	if c.Retry.MaxAttempts <= 0 {
		c.Retry = DefaultRetryPolicy()
	}
	return c, nil
}

// validateBaseURL допускает только https: Application Password уходит заголовком Basic, и
// WordPress сам не выдаёт их не-https площадкам.
func validateBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("WordPress base URL is empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse WordPress base URL: %w", err)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("WordPress base URL must use https, got %q", raw)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("WordPress base URL has no host: %q", raw)
	}
	// Из https://user:pass@host пароль попал бы в логи, ошибки и Referer.
	if parsed.User != nil {
		return "", errors.New("WordPress base URL must not contain credentials, use WORDPRESS_USERNAME and WORDPRESS_APP_PASSWORD")
	}
	// Двойной слэш после конкатенации часть серверов отдаёт редиректом, теряя заголовок Basic.
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
