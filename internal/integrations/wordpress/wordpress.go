// Package wordpress обращается к WordPress REST API обычным net/http.
//
// Пакет намеренно не похож на остальные интеграции проекта: ни Playwright, ни
// persistent-профиля, ни flock, ни человекоподобных пауз здесь нет и быть не должно. REST API
// — это документированный интерфейс с авторизацией по Application Password, а не веб-интерфейс,
// который приходится изображать браузером.
//
// Контракт узкий по замыслу: наружу выставлена операция, а не произвольный доступ к API.
// Универсального Do(method, path) в пакете нет — новая операция добавляется своим методом,
// который видно в ревью, а не появляется у любого вызывающего сама собой.
//
// О задачах пакет не знает. Какой сайт проверять и чьими credentials — решает Config, который
// собирает composition root из настроек конкретной задачи. Две задачи, публикующиеся на разные
// площадки, — это два экземпляра Client, а не два пакета и не флаг внутри.
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

// currentUserPath — единственный путь, к которому ходит пакет на сегодня.
//
// context=edit обязателен: без него WordPress отдаёт публичное представление пользователя, где
// нет ни username, ни capabilities, — то есть ровно тех полей, ради которых запрос и делается.
const currentUserPath = "/wp-json/wp/v2/users/me?context=edit"

// publishPostsCapability — право, по которому видно, сможет ли этот пользователь публиковать.
const publishPostsCapability = "publish_posts"

const (
	// defaultTimeout — бюджет одной попытки, а не всей операции. Общий потолок ставит
	// вызывающий контекстом: иначе таймауты множатся молча (ловушка H6 в CLAUDE.md).
	defaultTimeout = 10 * time.Second

	// maxResponseBytes ограничивает то, что мы вообще готовы прочитать. Ответ users/me —
	// единицы килобайт; страница логина, заглушка хостера или HTML-ошибка плагина могут быть
	// какими угодно, и тянуть их в память незачем.
	maxResponseBytes = 1 << 20

	// messageLimit обрезает сообщение сервера, попадающее в ошибку и лог.
	messageLimit = 200
)

// Config — доступ к одной площадке WordPress.
//
// Значения приходят готовыми строками из composition root. Пакет не читает окружение сам:
// иначе имя переменной (а оно у каждой задачи своё) стало бы частью его контракта.
type Config struct {
	// BaseURL — корень сайта, https. Подкаталог допустим: WordPress часто живёт в /blog.
	BaseURL string
	// Username — логин пользователя WordPress.
	Username string
	// AppPassword — Application Password. Обычный пароль администратора сюда класть нельзя:
	// он не отзывается отдельно и открывает вход в админку.
	AppPassword string
	// Timeout — бюджет одной попытки. Ноль означает defaultTimeout.
	Timeout time.Duration
	// Retry — политика повторов. Нулевая означает DefaultRetryPolicy.
	Retry RetryPolicy
}

// User — пользователь, которым авторизован клиент.
type User struct {
	ID    int64
	Login string
	Name  string
	// CanPublishPosts — есть ли у пользователя право publish_posts.
	//
	// Успехом проверки не является: цель проверки — подтвердить, что credentials рабочие, а
	// не что всё готово к публикации. Но ответ на «а этому пользователю вообще можно?» виден
	// в том же ответе бесплатно, и знать его лучше до первого POST, чем во время него.
	CanPublishPosts bool
}

// Connection — то, чем закончилась проверка подключения.
type Connection struct {
	// StatusCode — код ответа, которым подтверждено подключение.
	StatusCode int
	User       User
}

// Client работает с одной площадкой. Экземпляр фиксирует сайт и credentials: сменить их
// на лету нельзя, и перепутать площадки в рамках одного клиента поэтому невозможно.
type Client struct {
	cfg        Config
	httpClient *http.Client
	// xmlrpcClient обслуживает единственный записывающий вызов и живёт отдельно от httpClient
	// намеренно: у чтения справочника бюджет в секунды, а тело статьи — десятки килобайт,
	// и общий таймаут пришлось бы задирать обоим. Разные бюджеты — разные клиенты.
	xmlrpcClient *http.Client
	// mediaClient обслуживает загрузку обложки. Третий клиент здесь не роскошь: файл весит
	// мегабайты, и его время определяется каналом, а не работой WordPress. Общий с записью
	// бюджет пришлось бы задирать до размера самого медленного аплинка.
	mediaClient *http.Client
	// sleep подменяется в тестах, чтобы повторы проверялись без реального ожидания.
	sleep func(ctx context.Context, d time.Duration) error
}

// NewClient проверяет настройки и собирает клиента. Непригодный адрес отбивается здесь, до
// первого запроса: смысла идти в сеть с заведомо неверным конфигом нет.
func NewClient(cfg Config) (*Client, error) {
	normalized, err := cfg.normalized()
	if err != nil {
		return nil, err
	}
	return &Client{
		cfg:          normalized,
		httpClient:   &http.Client{Timeout: normalized.Timeout},
		xmlrpcClient: &http.Client{Timeout: xmlrpcTimeout},
		mediaClient:  &http.Client{Timeout: mediaUploadTimeout},
		sleep:        sleepContext,
	}, nil
}

// CheckConnection подтверждает, что credentials рабочие и REST API доступен.
//
// Запрос read-only: ничего не создаёт и не меняет. В этом её смысл — выяснять состояние
// доступа публикацией боевой статьи нельзя, а других способов у нас нет.
func (c *Client) CheckConnection(ctx context.Context) (Connection, error) {
	var payload currentUserPayload
	if err := c.get(ctx, currentUserPath, &payload); err != nil {
		return Connection{}, err
	}
	user := payload.user()
	// Успехом считается только 200 с разобранными id и username. Двухсотка с пустым телом —
	// это не WordPress, а кэширующий прокси или заглушка перед ним, и принимать её за
	// работающие credentials нельзя.
	if user.ID <= 0 || user.Login == "" {
		return Connection{}, fmt.Errorf(
			"WordPress %s: ответ 200, но в нём нет id и username — похоже, отвечает не REST API",
			currentUserPath)
	}
	return Connection{StatusCode: http.StatusOK, User: user}, nil
}

// currentUserPayload — та часть ответа users/me, которая нам нужна.
//
// capabilities разбирается как map[string]any, а не map[string]bool: значения кладёт не только
// ядро WordPress, но и плагины, и один нелогический ключ уронил бы разбор всего ответа —
// то есть превратил бы рабочие credentials в красный фейл.
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

// capabilityEnabled приводит значение права к булеву. WordPress отдаёт true, но через
// сериализацию из wp_options то же значение приходит и как 1, и как "1".
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

// get выполняет GET с повторами. Единственная точка, где вообще делается запрос: любая
// будущая операция обязана идти через неё, а не поднимать свой http.Client.
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
			// Общий дедлайн истёк в паузе. Наружу уходят обе причины: без исходной ошибки
			// человек увидит только «context deadline exceeded» и не узнает, чем ответил сайт.
			return errors.Join(lastErr, sleepErr)
		}
	}
	return lastErr
}

// retryDelay выбирает паузу перед следующей попыткой.
//
// Названный сервером Retry-After имеет приоритет над своим backoff: сервер знает про свой
// rate limit больше нас. Сверху его ограничивает не константа, а общий дедлайн операции —
// пауза идёт через ctx-aware sleep и прерывается вместе с ним.
func (c *Client) retryDelay(err error, attempt int) time.Duration {
	var statusErr *StatusError
	if errors.As(err, &statusErr) && statusErr.RetryAfter > 0 {
		return statusErr.RetryAfter
	}
	return c.cfg.Retry.Backoff(attempt)
}

// getOnce — одна попытка целиком.
func (c *Client) getOnce(ctx context.Context, endpoint, path string, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("собрать запрос %s: %w", path, err)
	}
	request.Header.Set("Accept", "application/json")
	// Единственное место во всём пакете, где используется пароль. Он уходит в заголовок и
	// больше никуда: ни в URL, ни в ошибку, ни в лог.
	request.SetBasicAuth(c.cfg.Username, c.cfg.AppPassword)

	response, err := c.httpClient.Do(request)
	if err != nil {
		// Отмена и общий дедлайн возвращаются голыми: повторять их нельзя, а transportError
		// объявлен повторяемым целиком.
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

// newStatusError собирает ошибку из ответа, не пропуская в неё лишнего.
//
// secret вычищается из сообщения сервера. Выглядит паранойей ровно до первого плагина
// безопасности, который отвечает «доступ запрещён для <то, что вы прислали>»: текст чужого
// сервера попадает в наши логи, и считать его безопасным нельзя.
func newStatusError(path string, response *http.Response, body []byte, secret string) *StatusError {
	statusErr := &StatusError{
		StatusCode: response.StatusCode,
		Endpoint:   path,
		Retryable:  retryableStatus(response.StatusCode),
		RetryAfter: parseRetryAfter(response.Header.Get("Retry-After")),
	}
	// Тело разбирается только как ошибка WordPress. Всё прочее — страница логина, заглушка
	// хостера, HTML плагина — в сообщение не попадает: в логе от него нет пользы, а объём есть.
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

// redactSecret вычищает секрет из чужого текста до того, как тот попадёт в ошибку и лог.
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

// normalized проверяет настройки и приводит их к рабочему виду.
func (c Config) normalized() (Config, error) {
	c.Username = strings.TrimSpace(c.Username)
	if c.Username == "" {
		return Config{}, errors.New("WordPress username is empty")
	}
	// Пароль не триммится: пробел может быть его частью. WordPress показывает Application
	// Password группами через пробел, но принимает и с ними, и без них.
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

// validateBaseURL — единственное место, где решается, какой адрес допустим.
//
// Сегодня допустим только https. Application Password уходит заголовком Basic, то есть
// обычным base64: по http он утекает открытым текстом любому на пути, и сам WordPress по той
// же причине отказывается выдавать Application Passwords не-https площадкам.
//
// Дверь для локального стенда не заколочена: явный dev-режим — это одно условие здесь, а не
// послабление, расползающееся по пакету. Пока такого стенда нет, разрешающей настройки в
// конфиге тоже нет: мёртвый флаг опаснее отсутствующего, потому что его однажды включат.
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
	// Форма https://user:pass@host запрещена: оттуда пароль попадает и в логи, и в тексты
	// ошибок, и в заголовок Referer. Credentials идут только отдельными переменными.
	if parsed.User != nil {
		return "", errors.New("WordPress base URL must not contain credentials, use WORDPRESS_USERNAME and WORDPRESS_APP_PASSWORD")
	}
	// Хвостовой слэш срезается, подкаталог сохраняется: путь запроса клеится конкатенацией,
	// и двойной слэш в середине часть серверов отдаёт редиректом, теряя заголовок Basic.
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

// sleepContext ждёт паузу, но не дольше, чем живёт контекст.
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
