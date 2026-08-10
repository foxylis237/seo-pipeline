package keysso

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"
)

const (
	loginURL                         = "https://www.keys.so/ru/login"
	homeURL                          = "https://www.keys.so/ru/"
	cleanupURL                       = "https://www.keys.so/ru/tools/delete-double"
	profilePath                      = "data/keysso-browser-profile"
	emailSelector                    = `input[name="email"]`
	passwordSelector                 = `input[name="password"]`
	loginLinkSelector                = `a[href="/ru/login"]`
	searchSelector                   = `input[placeholder="Введите адрес сайта или запрос"]`
	pageSizeSelector                 = `select.per-page-dropdown`
	keywordsTableSelector            = `table:has(th#_word)`
	keywordsEmptySelector            = `table:has(th#_word) tbody tr.p-datatable-emptymessage`
	keywordsLoaderSelector           = `.p-datatable-loading-overlay, .p-datatable-loading-icon`
	cleanupInputSelector             = `textarea.p-inputtextarea`
	resultFieldSelector              = `div.field`
	operationTimeoutMilliseconds     = 30_000
	longOperationTimeoutMilliseconds = 60_000
	keywordsTableMaxAttempts         = 3
	debugArtifactsRoot               = "output/task1/debug/keysso"
)

// Config содержит настройки интеграции с Keys.so.
type Config struct {
	ArticleID  int64
	ExternalID string
	Email      string
	Password   string
	Headless   bool
}

// CollectResult содержит данные, полученные на этапе Keys.so.
type CollectResult struct {
	CollectedCount  int
	CleanedKeywords []string
}

// StageError передаёт orchestration-слою контекст ошибки без повторного логирования.
type StageError struct {
	Stage          string
	CurrentURL     string
	Duration       time.Duration
	CollectedCount int
	CleanedCount   int
	Err            error
}

func (e *StageError) Error() string {
	return fmt.Sprintf("Keys.so stage=%s current_url=%q: %v", e.Stage, e.CurrentURL, e.Err)
}

func (e *StageError) Unwrap() error { return e.Err }

// Service управляет браузерной автоматизацией Keys.so.
type Service struct {
	cfg    Config
	logger *slog.Logger

	pw             *playwright.Playwright
	browserContext playwright.BrowserContext
	page           playwright.Page
	startedAt      time.Time
	collectedCount int
	cleanedCount   int
	referenceURL   string
	requestedURL   string
	resultsURL     string

	waitKeywordsResultsHook    func(context.Context) error
	refreshKeywordsResultsHook func(context.Context) error
	saveDebugArtifactsHook     func(string, int, int, error)
}

type debugInfo struct {
	ArticleID    int64     `json:"article_id"`
	ExternalID   string    `json:"external_id"`
	Attempt      int       `json:"attempt"`
	MaxAttempts  int       `json:"max_attempts"`
	Stage        string    `json:"stage"`
	Operation    string    `json:"operation"`
	URL          string    `json:"url"`
	Title        string    `json:"title"`
	ReadyState   string    `json:"ready_state"`
	Timestamp    time.Time `json:"timestamp"`
	Error        string    `json:"error"`
	ReferenceURL string    `json:"reference_url,omitempty"`
	RequestedURL string    `json:"requested_url,omitempty"`
	Result       string    `json:"result,omitempty"`
	Retryable    bool      `json:"retryable"`
}

type resultKind string

const (
	resultSuccess         resultKind = "success"
	resultNoData          resultKind = "no_data"
	resultMaintenance     resultKind = "maintenance"
	resultNavigationError resultKind = "navigation_error"
	resultTimeout         resultKind = "timeout"
	resultUnexpectedPage  resultKind = "unexpected_page"
)

type resultError struct {
	Kind      resultKind
	Retryable bool
	Err       error
}

func (e *resultError) Error() string { return e.Err.Error() }
func (e *resultError) Unwrap() error { return e.Err }

type debugCapturedError struct{ err error }

func (e *debugCapturedError) Error() string { return e.err.Error() }
func (e *debugCapturedError) Unwrap() error { return e.err }

type keywordsTableWaitError struct {
	Attempts         int
	URL              string
	Selector         string
	AttemptDurations []time.Duration
	Err              error
}

func (e *keywordsTableWaitError) Error() string {
	durations := make([]string, len(e.AttemptDurations))
	for index, duration := range e.AttemptDurations {
		durations[index] = duration.String()
	}
	return fmt.Sprintf(
		"keywords table did not load after %d attempts: url=%q selector=%q attempt_durations=[%s]: %v",
		e.Attempts, e.URL, e.Selector, strings.Join(durations, ", "), e.Err,
	)
}

func (e *keywordsTableWaitError) Unwrap() error { return e.Err }

// New создаёт интеграцию с Keys.so.
func New(cfg Config, logger *slog.Logger) *Service {
	return &Service{cfg: cfg, logger: logger.With("article_id", cfg.ArticleID, "external_id", cfg.ExternalID, "integration", "keysso")}
}

// CollectCleanKeywords получает запросы конкурента и очищает неявные дубли.
func (s *Service) CollectCleanKeywords(ctx context.Context, referenceURL string) (CollectResult, error) {
	s.startedAt = time.Now()
	s.collectedCount = 0
	s.cleanedCount = 0
	s.referenceURL = strings.TrimSpace(referenceURL)
	s.requestedURL = ""
	s.resultsURL = ""
	if strings.TrimSpace(referenceURL) == "" {
		return CollectResult{}, s.stageError("validate_reference_url", fmt.Errorf("reference URL is empty"))
	}
	if err := checkContext(ctx, "before starting browser"); err != nil {
		return CollectResult{}, s.stageError("start_browser", err)
	}
	if err := s.start(ctx); err != nil {
		return CollectResult{}, s.stageError("start_browser", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.authenticateWithRetries(ctx); err != nil {
		return CollectResult{}, err
	}
	queries, err := s.collectCompetitorQueries(ctx, referenceURL)
	if err != nil {
		return CollectResult{}, s.stageError("collect_competitor_queries", s.captureError("collect_competitor_queries", 1, 1, err))
	}
	s.collectedCount = len(queries)

	cleaned, err := s.cleanDuplicates(ctx, queries)
	if err != nil {
		return CollectResult{}, s.stageError("clean_duplicates", s.captureError("clean_duplicates", 1, 1, err))
	}
	s.cleanedCount = len(cleaned)
	return CollectResult{
		CollectedCount:  len(queries),
		CleanedKeywords: cleaned,
	}, nil
}

// CleanKeywords прогоняет через очистку Keys.so запросы, полученные не от самого Keys.so.
//
// Нужен, когда исходных запросов у конкурента не оказалось и их подобрал резервный источник:
// правила очистки должны остаться ровно одни и те же, независимо от происхождения списка,
// поэтому здесь открывается та же форма delete-double, а не пишется вторая очистка.
func (s *Service) CleanKeywords(ctx context.Context, queries []string) (CollectResult, error) {
	s.startedAt = time.Now()
	s.collectedCount = len(queries)
	s.cleanedCount = 0
	s.requestedURL = ""
	s.resultsURL = ""
	if len(queries) == 0 {
		return CollectResult{}, s.stageError("validate_raw_keywords", fmt.Errorf("raw keywords list is empty"))
	}
	if err := checkContext(ctx, "before starting browser"); err != nil {
		return CollectResult{}, s.stageError("start_browser", err)
	}
	if err := s.start(ctx); err != nil {
		return CollectResult{}, s.stageError("start_browser", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.authenticateWithRetries(ctx); err != nil {
		return CollectResult{}, err
	}
	cleaned, err := s.cleanDuplicates(ctx, queries)
	if err != nil {
		return CollectResult{}, s.stageError("clean_duplicates", s.captureErrorContext(ctx, "clean_duplicates", 1, 1, err))
	}
	s.cleanedCount = len(cleaned)
	return CollectResult{
		CollectedCount:  len(queries),
		CleanedKeywords: cleaned,
	}, nil
}

// ErrNoRawKeywords — Keys.so дошёл до результата и не нашёл у конкурента ни одного запроса.
//
// Это окончательный ответ сервиса, а не техническая неудача: ни повтор, ни другой браузер
// его не изменят. Ошибка объявлена в публичном виде, потому что только по ней вызывающий
// может отличить «данных нет» от «интеграция сломалась» — и решить, искать ли запросы в
// другом источнике.
var ErrNoRawKeywords = errors.New("Keys.so вернул пустой список запросов конкурента")

// NoRawKeywords сообщает, что этап закончился отсутствием исходных запросов.
// Таймауты, отказ авторизации и сломанная навигация сюда не попадают.
func NoRawKeywords(err error) bool { return errors.Is(err, ErrNoRawKeywords) }

// authenticateWithRetries доводит сессию до авторизованного состояния, повторяя только
// временные отказы. Общая для сбора и для отдельно вызванной очистки: обе открывают
// страницы Keys.so и обе требуют живой сессии.
func (s *Service) authenticateWithRetries(ctx context.Context) error {
	var authenticationErr error
	for attempt := 1; attempt <= keywordsTableMaxAttempts; attempt++ {
		authenticationErr = s.ensureAuthenticated(ctx)
		if authenticationErr == nil {
			return nil
		}
		var classified *resultError
		if !errors.As(authenticationErr, &classified) || !classified.Retryable {
			return s.stageError("check_authorization", s.captureError("check_authorization", attempt, keywordsTableMaxAttempts, authenticationErr))
		}
		authenticationErr = s.captureError("check_authorization", attempt, keywordsTableMaxAttempts, authenticationErr)
	}
	return s.stageError("check_authorization", authenticationErr)
}

// start поднимает Playwright и открывает persistent-профиль. Контекст берётся параметром
// только ради логирования: сам запуск браузера отменяемых операций не содержит, а обе точки
// входа вызывают start уже после checkContext.
func (s *Service) start(ctx context.Context) error {
	if err := s.Close(); err != nil {
		return fmt.Errorf("close previous Keys.so session: %w", err)
	}

	s.logContext(ctx, slog.LevelDebug, "запуск Playwright", "start_browser", "profile_path", profilePath)
	if err := os.MkdirAll(profilePath, 0o700); err != nil {
		return fmt.Errorf("create persistent browser profile: %w", err)
	}
	if err := os.Chmod(profilePath, 0o700); err != nil {
		return fmt.Errorf("protect persistent browser profile: %w", err)
	}
	pw, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("start Playwright: %w", err)
	}
	browserContext, err := pw.Chromium.LaunchPersistentContext(
		profilePath,
		playwright.BrowserTypeLaunchPersistentContextOptions{
			Headless: playwright.Bool(s.cfg.Headless),
		},
	)
	if err != nil {
		_ = pw.Stop()
		return fmt.Errorf("launch Chromium with persistent profile: %w", err)
	}
	pages := browserContext.Pages()
	var page playwright.Page
	if len(pages) > 0 {
		page = pages[0]
	} else {
		page, err = browserContext.NewPage()
		if err != nil {
			_ = browserContext.Close()
			_ = pw.Stop()
			return fmt.Errorf("create Keys.so page: %w", err)
		}
	}
	page.SetDefaultTimeout(operationTimeoutMilliseconds)
	page.SetDefaultNavigationTimeout(longOperationTimeoutMilliseconds)

	s.pw = pw
	s.browserContext = browserContext
	s.page = page
	s.logContext(ctx, slog.LevelDebug, "Playwright запущен с постоянным профилем", "start_browser")
	return nil
}

func (s *Service) ensureAuthenticated(ctx context.Context) error {
	s.log(slog.LevelDebug, "проверка активной сессии Keys.so", "check_authorization")
	if err := s.open(ctx, homeURL, "check Keys.so session"); err != nil {
		return err
	}
	if err := s.detectAccessRestriction("check_authorization"); err != nil {
		return err
	}
	if err := s.detectMaintenancePage(); err != nil {
		return err
	}
	searchInput := s.page.Locator(searchSelector)
	if err := searchInput.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(longOperationTimeoutMilliseconds),
	}); err != nil {
		return fmt.Errorf("wait for Keys.so home page while checking session: %w", err)
	}
	loginLink := s.page.Locator(loginLinkSelector)
	count, err := loginLink.Count()
	if err != nil {
		return fmt.Errorf("determine Keys.so authorization state: %w", err)
	}
	if count == 0 {
		s.log(slog.LevelInfo, "сессия Keys.so подтверждена", "check_authorization")
		return nil
	}

	s.log(slog.LevelWarn, "сессия Keys.so истекла, требуется повторная авторизация", "authorize")
	visibleLoginLink, loginLinkIndex, err := s.firstVisible(loginLink, loginLinkSelector, "ссылка входа на главной странице")
	if err != nil {
		return err
	}
	s.log(slog.LevelDebug, "выбрана ссылка входа", "authorize", "locator_index", loginLinkIndex)
	if err := visibleLoginLink.Click(); err != nil {
		return fmt.Errorf("open Keys.so login page through navigation link: %w", err)
	}
	if _, err := s.page.WaitForFunction(
		`() => location.pathname.includes('/login')`,
		nil,
		playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(longOperationTimeoutMilliseconds)},
	); err != nil {
		return fmt.Errorf("wait for Keys.so login URL: %w", err)
	}
	s.log(slog.LevelDebug, "страница входа Keys.so открыта", "open_login_page")
	if err := s.detectAccessRestriction("open_login_page"); err != nil {
		return err
	}
	if !strings.Contains(s.page.URL(), "/login") {
		s.log(slog.LevelInfo, "сессия Keys.so подтверждена", "check_authorization")
		return nil
	}

	emailInput := s.page.Locator(emailSelector)
	passwordInput := s.page.Locator(passwordSelector)
	loginButton := s.page.GetByRole("button", playwright.PageGetByRoleOptions{
		Name:  "Войти",
		Exact: playwright.Bool(true),
	})
	if err := s.waitVisible(emailInput, emailSelector, "поле email формы входа", "wait_login_form"); err != nil {
		return err
	}
	if err := s.waitVisible(passwordInput, passwordSelector, "поле пароля формы входа", "wait_login_form"); err != nil {
		return err
	}
	if err := s.waitVisible(loginButton, `getByRole(button, name="Войти")`, "кнопка подтверждения входа", "wait_login_form"); err != nil {
		return err
	}
	if err := s.requireUnique(emailInput, emailSelector, "поле email формы входа"); err != nil {
		return err
	}
	if err := s.requireUnique(passwordInput, passwordSelector, "поле пароля формы входа"); err != nil {
		return err
	}
	if err := s.requireUnique(loginButton, `getByRole(button, name="Войти")`, "кнопка подтверждения входа"); err != nil {
		return err
	}
	if err := emailInput.Fill(s.cfg.Email); err != nil {
		return fmt.Errorf("fill Keys.so email: %w", err)
	}
	if err := passwordInput.Fill(s.cfg.Password); err != nil {
		return fmt.Errorf("fill Keys.so password: %w", err)
	}
	if err := loginButton.Click(); err != nil {
		return fmt.Errorf("submit Keys.so login: %w", err)
	}
	if _, err := s.page.WaitForFunction(
		`() => !location.pathname.includes('/login') ||
			document.body.innerText.includes('использование аккаунта на нескольких устройствах') ||
			document.body.innerText.includes('подозрительная активность')`,
		nil,
		playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(longOperationTimeoutMilliseconds)},
	); err != nil {
		return fmt.Errorf("wait for Keys.so authorization: %w", err)
	}
	if err := s.detectAccessRestriction("authorize"); err != nil {
		return err
	}
	if err := s.open(ctx, homeURL, "verify Keys.so authorization"); err != nil {
		return err
	}
	if _, err := s.page.WaitForFunction(
		`selector => Boolean(document.querySelector(selector))`,
		searchSelector,
		playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(longOperationTimeoutMilliseconds)},
	); err != nil {
		return fmt.Errorf("verify Keys.so authorization page: %w", err)
	}
	count, err = loginLink.Count()
	if err != nil {
		return fmt.Errorf("verify Keys.so authorization state: %w", err)
	}
	if count != 0 {
		return fmt.Errorf("Keys.so authorization did not succeed")
	}
	s.log(slog.LevelInfo, "авторизация Keys.so выполнена", "authorize")
	return nil
}

func (s *Service) collectCompetitorQueries(ctx context.Context, referenceURL string) ([]string, error) {
	s.log(slog.LevelDebug, "начало поиска конкурента", "collect_competitor_queries")
	var navigationErr error
	for attempt := 1; attempt <= keywordsTableMaxAttempts; attempt++ {
		navigationErr = s.submitCompetitorSearch(ctx, referenceURL)
		if navigationErr == nil {
			break
		}
		navigationErr = s.captureError("navigate_search_results", attempt, keywordsTableMaxAttempts, navigationErr)
		if !isRetryableResultError(navigationErr) || attempt == keywordsTableMaxAttempts {
			return nil, navigationErr
		}
		result, retryable := resultErrorFields(navigationErr)
		s.log(slog.LevelWarn, "Keys.so search navigation retry", "navigate_search_results", "attempt", attempt+1, "max_attempts", keywordsTableMaxAttempts, "reference_url", referenceURL, "result", result, "retryable", retryable, "error", navigationErr)
	}

	if err := s.waitKeywordsResults(ctx); err != nil {
		pageSize := s.page.Locator(pageSizeSelector)
		s.logLocatorDiagnostic(pageSize, pageSizeSelector, "видимый select количества строк таблицы")
		if restrictionErr := s.detectAccessRestriction("wait_search_results"); restrictionErr != nil {
			return nil, restrictionErr
		}
		return nil, err
	}
	pageSize := s.page.Locator(pageSizeSelector)
	pageSizeMatches, err := pageSize.All()
	if err != nil {
		return nil, fmt.Errorf("get result page size selectors: %w", err)
	}
	s.log(slog.LevelDebug, "найдены селекторы количества строк", "select_page_size", "matches_count", len(pageSizeMatches), "locator", pageSizeSelector)
	if len(pageSizeMatches) != 1 {
		s.log(slog.LevelWarn, "найдено нестандартное количество селекторов строк", "select_page_size", "matches_count", len(pageSizeMatches), "locator", pageSizeSelector)
	}

	selectedIndex := -1
	var selectedPageSize playwright.Locator
	for index, candidate := range pageSizeMatches {
		visible, err := candidate.IsVisible()
		if err != nil {
			return nil, fmt.Errorf("check visibility of result page size selector %d: %w", index, err)
		}
		if visible {
			selectedIndex = index
			selectedPageSize = candidate
			break
		}
	}
	if selectedPageSize == nil {
		return nil, fmt.Errorf("visible result page size selector not found")
	}
	currentPageSize, err := selectedPageSize.InputValue()
	if err != nil {
		return nil, fmt.Errorf("read current result page size: %w", err)
	}
	s.log(slog.LevelDebug,
		"выбран видимый селектор количества строк",
		"select_page_size",
		"selected_index", selectedIndex,
		"current_value", currentPageSize,
	)

	values := []string{"500"}
	if _, err := selectedPageSize.SelectOption(playwright.SelectOptionValues{Values: &values}); err != nil {
		return nil, fmt.Errorf("select 500 competitor queries: %w", err)
	}
	s.log(slog.LevelDebug, "успешно выбрано 500 строк", "select_page_size", "selected_index", selectedIndex, "value", "500")
	if err := s.page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State:   playwright.LoadStateNetworkidle,
		Timeout: playwright.Float(longOperationTimeoutMilliseconds),
	}); err != nil {
		return nil, fmt.Errorf("wait for 500 competitor queries: %w", err)
	}
	if _, err := s.page.WaitForFunction(
		`selector => {
			const table = document.querySelector(selector);
			return table && table.querySelectorAll('tbody tr').length > 0;
		}`,
		keywordsTableSelector,
		playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(longOperationTimeoutMilliseconds)},
	); err != nil {
		return nil, fmt.Errorf("keywords table did not update: %w", err)
	}
	if err := checkContext(ctx, "after loading competitor queries"); err != nil {
		return nil, err
	}

	table := s.page.Locator(keywordsTableSelector)
	if err := s.requireUnique(table, keywordsTableSelector, "таблица запросов конкурента"); err != nil {
		return nil, err
	}
	raw, err := table.Evaluate(`table => {
		const headers = Array.from(table.querySelectorAll('thead th'));
		const queryIndex = headers.findIndex(header => header.id === '_word');
		if (queryIndex < 0) return [];
		return Array.from(table.querySelectorAll('tbody tr')).map(row => {
			const cells = Array.from(row.querySelectorAll('td'));
			return (cells[queryIndex]?.innerText || '').trim();
		}).filter(Boolean);
	}`, nil)
	if err != nil {
		return nil, fmt.Errorf("get competitor queries from table: %w", err)
	}
	queries, err := decodeStringSlice(raw)
	if err != nil {
		return nil, fmt.Errorf("decode competitor queries: %w", err)
	}
	if len(queries) == 0 {
		// Пустая таблица — окончательный ответ Keys.so о конкуренте, а не техническая
		// неудача: повтор её не изменит. Классифицируем так же, как «Нет данных» на
		// странице результатов, чтобы вызывающий мог отличить отсутствие данных от отказа.
		return nil, &resultError{Kind: resultNoData, Retryable: false,
			Err: fmt.Errorf("%w: таблица запросов конкурента пуста", ErrNoRawKeywords)}
	}
	s.collectedCount = len(queries)
	s.log(slog.LevelInfo, "запросы конкурента получены", "collect_competitor_queries")
	return queries, nil
}

func (s *Service) submitCompetitorSearch(ctx context.Context, referenceURL string) error {
	if err := s.open(ctx, homeURL, "open Keys.so search page"); err != nil {
		return err
	}
	if err := s.ensureBusinessSession("open_search_page"); err != nil {
		return err
	}
	searchInput := s.page.Locator(searchSelector)
	searchButton := s.page.GetByRole("button", playwright.PageGetByRoleOptions{Name: "Поиск", Exact: playwright.Bool(true)})
	if err := s.requireUnique(searchInput, searchSelector, "поле поиска конкурента"); err != nil {
		return fmt.Errorf("search field not found: %w", err)
	}
	if err := s.requireUnique(searchButton, `getByRole(button, name="Поиск")`, "кнопка поиска конкурента"); err != nil {
		return err
	}
	if err := searchInput.Fill(referenceURL); err != nil {
		return fmt.Errorf("fill competitor URL: %w", err)
	}
	response, err := s.page.ExpectNavigation(func() error { return searchButton.Click() }, playwright.PageExpectNavigationOptions{
		URL: "**/ru/keysbypage**", WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout: playwright.Float(longOperationTimeoutMilliseconds),
	})
	if err != nil {
		if restrictionErr := s.detectAccessRestriction("wait_search_results_url"); restrictionErr != nil {
			return restrictionErr
		}
		return &resultError{Kind: resultNavigationError, Retryable: true, Err: fmt.Errorf("Keys.so navigation failed: %w", err)}
	}
	if response != nil {
		s.resultsURL = response.URL()
	}
	if s.resultsURL == "" {
		s.resultsURL = s.currentURL()
	}
	s.requestedURL = s.resultsURL
	s.log(slog.LevelDebug, "поиск конкурента отправлен, ожидание результатов", "collect_competitor_queries", "requested_url", s.requestedURL)
	if !resultsMatchReference(s.resultsURL, referenceURL) {
		s.log(slog.LevelWarn,
			"страница результатов Keys.so не упоминает reference_url статьи",
			"validate_results_url",
			"article_id", s.cfg.ArticleID, "external_id", s.cfg.ExternalID,
			"reference_url", referenceURL, "results_url", s.resultsURL,
		)
	}
	return nil
}

// resultsMatchReference reports the Keys.so results URL carrying the requested competitor
// site. A false result means the page shown belongs to some other search — most likely the
// previous article's one — and the collected queries must not be trusted.
func resultsMatchReference(resultsURL, referenceURL string) bool {
	reference, err := url.Parse(strings.TrimSpace(referenceURL))
	if err != nil {
		return true
	}
	host := strings.TrimPrefix(strings.ToLower(reference.Hostname()), "www.")
	if host == "" {
		// reference_url may be stored without a scheme; take the first path segment instead.
		host = strings.ToLower(strings.Split(strings.TrimPrefix(reference.Path, "/"), "/")[0])
	}
	if host == "" {
		return true
	}
	decoded, err := url.QueryUnescape(resultsURL)
	if err != nil {
		decoded = resultsURL
	}
	return strings.Contains(strings.ToLower(decoded), host)
}

func (s *Service) waitKeywordsResults(ctx context.Context) error {
	durations := make([]time.Duration, 0, keywordsTableMaxAttempts)
	var lastErr error
	for attempt := 1; attempt <= keywordsTableMaxAttempts; attempt++ {
		if err := checkContext(ctx, "wait keywords table"); err != nil {
			return err
		}
		if attempt > 1 {
			s.log(slog.LevelInfo, "Keys.so: refreshing results page", "wait_search_results", "attempt", attempt, "max_attempts", keywordsTableMaxAttempts)
			if err := s.refreshKeywordsResults(ctx); err != nil {
				return fmt.Errorf("refresh Keys.so results before attempt %d/%d: %w", attempt, keywordsTableMaxAttempts, errors.Join(lastErr, err))
			}
		}

		s.log(slog.LevelInfo, "Keys.so: waiting keywords table", "wait_search_results", "attempt", attempt, "max_attempts", keywordsTableMaxAttempts, "locator", keywordsTableSelector)
		started := time.Now()
		lastErr = s.waitKeywordsResultsOnce(ctx)
		duration := time.Since(started)
		durations = append(durations, duration)
		if lastErr == nil {
			s.log(slog.LevelInfo, "Keys.so: keywords table loaded", "wait_search_results", "attempt", attempt, "max_attempts", keywordsTableMaxAttempts, "attempt_duration_ms", duration.Milliseconds(), "locator", keywordsTableSelector)
			return nil
		}
		result, retryable := resultErrorFields(lastErr)
		s.log(slog.LevelWarn, "Keys.so: keywords table attempt failed", "wait_search_results", "attempt", attempt, "max_attempts", keywordsTableMaxAttempts, "attempt_duration_ms", duration.Milliseconds(), "locator", keywordsTableSelector, "reference_url", s.referenceURL, "requested_url", s.requestedURL, "result", result, "retryable", retryable, "error", lastErr)
		lastErr = s.captureError("wait_search_results", attempt, keywordsTableMaxAttempts, lastErr)
		if !isRetryableResultError(lastErr) {
			return lastErr
		}
		if errors.Is(lastErr, context.Canceled) || errors.Is(lastErr, context.DeadlineExceeded) {
			return lastErr
		}
		if s.waitKeywordsResultsHook == nil {
			if restrictionErr := s.detectAccessRestriction("wait_search_results"); restrictionErr != nil {
				return restrictionErr
			}
		}
	}
	result, retryable := resultErrorFields(lastErr)
	s.log(slog.LevelError, "Keys.so failed after 3 attempts", "wait_search_results", "attempts", keywordsTableMaxAttempts, "locator", keywordsTableSelector, "reference_url", s.referenceURL, "requested_url", s.requestedURL, "result", result, "retryable", retryable, "error", lastErr)
	return &keywordsTableWaitError{
		Attempts: keywordsTableMaxAttempts, URL: s.currentURL(), Selector: keywordsTableSelector,
		AttemptDurations: durations, Err: lastErr,
	}
}

func (s *Service) waitKeywordsResultsOnce(ctx context.Context) error {
	if s.waitKeywordsResultsHook != nil {
		return s.waitKeywordsResultsHook(ctx)
	}
	if err := checkContext(ctx, "wait keywords table"); err != nil {
		return err
	}
	if err := s.validateResultsPage(); err != nil {
		return err
	}
	handle, err := s.page.WaitForFunction(
		`selectors => {
			if (location.protocol === 'chrome-error:') return 'navigation_error';
			if ((document.title || '').trim() === selectors.maintenance ||
				(document.body?.innerText || '').trim() === selectors.maintenance) return 'maintenance';
			const loaders = Array.from(document.querySelectorAll(selectors.loader));
			const loading = loaders.some(element => {
				const style = window.getComputedStyle(element);
				return style.visibility !== 'hidden' && style.display !== 'none' && element.getClientRects().length > 0;
			});
			if (loading) return false;
			const table = document.querySelector(selectors.table);
			if (!table) return false;
			const empty = document.querySelector(selectors.empty);
			if (empty && (empty.textContent || '').trim() === selectors.noData) return 'no_data';
			const rows = table.querySelectorAll('tbody tr:not(.p-datatable-emptymessage)');
			if (rows.length > 0 && document.querySelector(selectors.pageSize)) return 'success';
			return false;
		}`,
		map[string]any{
			"table": keywordsTableSelector, "empty": keywordsEmptySelector,
			"pageSize": pageSizeSelector, "loader": keywordsLoaderSelector,
			"noData": "Нет данных", "maintenance": "Технические работы на сайте",
		},
		playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(longOperationTimeoutMilliseconds)},
	)
	if err != nil {
		return &resultError{Kind: resultTimeout, Retryable: true, Err: fmt.Errorf("Keys.so timed out waiting for keywords results: %w", err)}
	}
	value, err := handle.JSONValue()
	if err != nil {
		return fmt.Errorf("read Keys.so result state: %w", err)
	}
	state, _ := value.(string)
	switch resultKind(state) {
	case resultSuccess:
		return nil
	case resultNoData:
		return &resultError{Kind: resultNoData, Retryable: false, Err: fmt.Errorf("%w: страница результатов для %s пуста", ErrNoRawKeywords, s.referenceURL)}
	case resultMaintenance:
		return &resultError{Kind: resultMaintenance, Retryable: true, Err: fmt.Errorf("Keys.so is unavailable: maintenance page detected")}
	case resultNavigationError:
		return &resultError{Kind: resultNavigationError, Retryable: true, Err: fmt.Errorf("Keys.so navigation failed: current URL %q", s.currentURL())}
	default:
		return &resultError{Kind: resultUnexpectedPage, Retryable: false, Err: fmt.Errorf("unexpected Keys.so result state %q", state)}
	}
}

func (s *Service) refreshKeywordsResults(ctx context.Context) error {
	if s.refreshKeywordsResultsHook != nil {
		return s.refreshKeywordsResultsHook(ctx)
	}
	if err := checkContext(ctx, "before refreshing keywords results"); err != nil {
		return err
	}
	if !isExpectedKeysSOPage(s.currentURL(), "/ru/keysbypage") {
		if s.resultsURL == "" {
			return &resultError{Kind: resultNavigationError, Retryable: true, Err: fmt.Errorf("Keys.so navigation failed: results URL is unavailable; current URL %q", s.currentURL())}
		}
		if _, err := s.page.Goto(s.resultsURL, playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateLoad, Timeout: playwright.Float(longOperationTimeoutMilliseconds),
		}); err != nil {
			return &resultError{Kind: resultNavigationError, Retryable: true, Err: fmt.Errorf("reopen Keys.so results page: %w", err)}
		}
		return nil
	}
	if err := s.page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateLoad, Timeout: playwright.Float(longOperationTimeoutMilliseconds),
	}); err != nil {
		return fmt.Errorf("wait for current Keys.so navigation before refresh: %w", err)
	}
	if _, err := s.page.Reload(playwright.PageReloadOptions{
		WaitUntil: playwright.WaitUntilStateLoad, Timeout: playwright.Float(longOperationTimeoutMilliseconds),
	}); err != nil {
		return fmt.Errorf("refresh Keys.so keywords page: %w", err)
	}
	if err := s.page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateLoad, Timeout: playwright.Float(longOperationTimeoutMilliseconds),
	}); err != nil {
		return fmt.Errorf("wait for refreshed Keys.so page: %w", err)
	}
	return checkContext(ctx, "after refreshing keywords results")
}

func (s *Service) cleanDuplicates(ctx context.Context, queries []string) ([]string, error) {
	s.log(slog.LevelDebug, "открытие страницы удаления дублей", "open_cleanup_page", "target_url", cleanupURL)
	if err := s.open(ctx, cleanupURL, "open duplicate cleanup tool"); err != nil {
		return nil, err
	}
	if err := s.page.WaitForURL("**/ru/tools/delete-double", playwright.PageWaitForURLOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(longOperationTimeoutMilliseconds),
	}); err != nil {
		return nil, fmt.Errorf("duplicate cleanup navigation did not finish: current URL %q: %w", s.page.URL(), err)
	}
	currentURL := s.page.URL()
	if !strings.Contains(currentURL, "/ru/tools/delete-double") {
		return nil, fmt.Errorf("duplicate cleanup navigation ended on unexpected URL %q", currentURL)
	}
	s.log(slog.LevelDebug, "страница удаления дублей открыта", "open_cleanup_page")
	s.log(slog.LevelDebug, "ожидание формы удаления дублей", "find_cleanup_input")

	const inputLabel = "Список поисковых фраз"
	_, waitErr := s.page.WaitForFunction(
		`label => Array.from(document.querySelectorAll('div.required')).some(
			element => (element.textContent || '').trim() === label
		)`,
		inputLabel,
		playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(longOperationTimeoutMilliseconds)},
	)

	allTextareas := s.page.Locator("textarea")
	pageTextareaCount, countErr := allTextareas.Count()
	if countErr != nil {
		return nil, fmt.Errorf("count all textareas on duplicate cleanup page: %w", countErr)
	}

	labels := s.page.GetByText(inputLabel, playwright.PageGetByTextOptions{
		Exact: playwright.Bool(true),
	})
	labelCount, err := labels.Count()
	if err != nil {
		return nil, fmt.Errorf("count duplicate cleanup input labels: %w", err)
	}
	labelFound := labelCount > 0
	if labelFound {
		s.log(slog.LevelDebug, "найдена подпись поля удаления дублей", "find_cleanup_input", "label", inputLabel, "matches_count", labelCount)
	}
	if labelCount != 1 {
		s.log(slog.LevelWarn, "найдено нестандартное количество подписей поля удаления дублей", "find_cleanup_input", "matches_count", labelCount)
	}

	labelMatches, err := labels.All()
	if err != nil {
		return nil, fmt.Errorf("get duplicate cleanup input labels: %w", err)
	}
	var formContainer playwright.Locator
	for index, label := range labelMatches {
		visible, err := label.IsVisible()
		if err != nil {
			return nil, fmt.Errorf("check visibility of duplicate cleanup input label %d: %w", index, err)
		}
		if !visible {
			continue
		}

		container := label.Locator(`xpath=ancestor::div[contains(concat(' ', normalize-space(@class), ' '), ' field ')][1]`)
		containerCount, err := container.Count()
		if err != nil {
			return nil, fmt.Errorf("count duplicate cleanup form containers: %w", err)
		}
		if containerCount == 1 {
			formContainer = container
			s.log(slog.LevelDebug, "найден контейнер поля удаления дублей", "find_cleanup_input", "label_index", index)
			break
		}
	}

	containerTextareaCount := 0
	var input playwright.Locator
	selectedTextareaIndex := -1
	if formContainer != nil {
		containerTextareas := formContainer.Locator("textarea")
		containerTextareaCount, err = containerTextareas.Count()
		if err != nil {
			return nil, fmt.Errorf("count textareas in duplicate cleanup form container: %w", err)
		}
		s.log(slog.LevelDebug,
			"найдены textarea в контейнере удаления дублей",
			"find_cleanup_input",
			"matches_count", containerTextareaCount,
		)
		if containerTextareaCount != 1 {
			s.log(slog.LevelWarn, "найдено нестандартное количество textarea в контейнере", "find_cleanup_input", "matches_count", containerTextareaCount)
		}
		containerTextareaMatches, err := containerTextareas.All()
		if err != nil {
			return nil, fmt.Errorf("get textareas from duplicate cleanup form container: %w", err)
		}
		for index, candidate := range containerTextareaMatches {
			visible, err := candidate.IsVisible()
			if err != nil {
				return nil, fmt.Errorf("check visibility of container textarea %d: %w", index, err)
			}
			if visible {
				selectedTextareaIndex = index
				input = candidate
				break
			}
		}
	}
	if input == nil {
		s.log(slog.LevelDebug,
			"поле формы удаления дублей не найдено",
			"find_cleanup_input",
			"input_label_found", labelFound,
			"container_textarea_count", containerTextareaCount,
			"page_textarea_count", pageTextareaCount,
		)
		if waitErr != nil {
			return nil, fmt.Errorf(
				"duplicate cleanup input not found at %q (label found: %t, container textareas: %d, page textareas: %d): %w",
				s.page.URL(), labelFound, containerTextareaCount, pageTextareaCount, waitErr,
			)
		}
		return nil, fmt.Errorf(
			"visible duplicate cleanup input not found at %q (label found: %t, container textareas: %d, page textareas: %d)",
			s.page.URL(), labelFound, containerTextareaCount, pageTextareaCount,
		)
	}
	s.log(slog.LevelDebug, "выбрано видимое textarea в контейнере", "find_cleanup_input", "textarea_index", selectedTextareaIndex)

	processButton := s.page.Locator(`button[aria-label="Обработать"]`)
	if err := s.requireUnique(processButton, `button[aria-label="Обработать"]`, "кнопка обработки дублей"); err != nil {
		return nil, err
	}
	if err := input.Fill(strings.Join(queries, "\n")); err != nil {
		return nil, fmt.Errorf("fill duplicate cleanup input: %w", err)
	}
	s.log(slog.LevelDebug, "запросы вставлены в форму удаления дублей", "fill_cleanup_input", "queries_count", len(queries))
	if err := processButton.Click(); err != nil {
		return nil, fmt.Errorf("start duplicate cleanup: %w", err)
	}
	s.log(slog.LevelDebug, "обработка дублей запущена, ожидание результата", "wait_cleanup_result")

	resultField := s.page.Locator(resultFieldSelector).Filter(playwright.LocatorFilterOptions{
		HasText: "Результат",
	})
	result := resultField.Locator(cleanupInputSelector)
	if err := result.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(longOperationTimeoutMilliseconds),
	}); err != nil {
		s.logLocatorDiagnostic(
			result,
			`div.field hasText="Результат" >> textarea.p-inputtextarea`,
			"textarea результата удаления дублей",
		)
		if restrictionErr := s.detectAccessRestriction("wait_cleanup_result"); restrictionErr != nil {
			return nil, restrictionErr
		}
		return nil, fmt.Errorf("duplicate cleanup did not return result: %w", err)
	}
	if err := s.requireUnique(result, `div.field hasText="Результат" >> textarea.p-inputtextarea`, "результат удаления дублей"); err != nil {
		return nil, err
	}
	value, err := result.InputValue()
	if err != nil {
		return nil, fmt.Errorf("read duplicate cleanup result: %w", err)
	}
	cleaned := parseQueries(value)
	if len(cleaned) == 0 {
		return nil, fmt.Errorf("duplicate cleanup result is empty")
	}
	s.cleanedCount = len(cleaned)
	s.log(slog.LevelInfo, "запросы очищены", "clean_duplicates")
	return cleaned, nil
}

func (s *Service) open(ctx context.Context, url, stage string) error {
	if err := checkContext(ctx, stage); err != nil {
		return err
	}
	s.log(slog.LevelDebug, "открытие страницы Keys.so", stage, "target_url", url)
	if _, err := s.page.Goto(url, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
	}); err != nil {
		if !samePage(s.currentURL(), url) {
			return &resultError{Kind: resultNavigationError, Retryable: true, Err: fmt.Errorf("Keys.so navigation failed during %s: %w", stage, err)}
		}
		s.log(slog.LevelWarn,
			"навигационное событие Keys.so не завершилось, но целевой URL достигнут",
			stage,
			"error", err,
		)
	}
	if err := checkContext(ctx, stage); err != nil {
		return err
	}
	if err := s.detectAccessRestriction(stage); err != nil {
		return err
	}
	if err := s.detectMaintenancePage(); err != nil {
		return err
	}
	title, err := s.page.Title()
	if err != nil {
		s.log(slog.LevelWarn, "не удалось получить title страницы Keys.so", stage, "error", err)
	} else {
		s.log(slog.LevelInfo, "страница Keys.so загружена", stage, "page_title", title, "page_url", s.currentURL())
	}
	s.log(slog.LevelDebug, "страница Keys.so открыта", stage)
	return nil
}

func (s *Service) detectMaintenancePage() error {
	title, err := s.page.Title()
	if err != nil {
		return fmt.Errorf("read Keys.so page title: %w", err)
	}
	if strings.EqualFold(strings.TrimSpace(title), "Технические работы на сайте") {
		s.log(slog.LevelWarn, "Keys.so maintenance page detected", "maintenance", "page_title", title, "retryable", true, "reference_url", s.referenceURL)
		return &resultError{Kind: resultMaintenance, Retryable: true, Err: fmt.Errorf("Keys.so is unavailable: maintenance page detected")}
	}
	return nil
}

func (s *Service) validateResultsPage() error {
	current := s.currentURL()
	if strings.HasPrefix(current, "chrome-error://") {
		return &resultError{Kind: resultNavigationError, Retryable: true, Err: fmt.Errorf("Keys.so navigation failed: current URL %q", current)}
	}
	if isExpectedKeysSOPage(current, "/ru/keysbypage") {
		return nil
	}
	title, _ := s.page.Title()
	return &resultError{Kind: resultUnexpectedPage, Retryable: false, Err: fmt.Errorf("unexpected Keys.so page: url=%q title=%q", current, title)}
}

func isExpectedKeysSOPage(rawURL, pathPrefix string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return parsed.Scheme == "https" && strings.EqualFold(parsed.Hostname(), "www.keys.so") && strings.HasPrefix(parsed.Path, pathPrefix)
}

func isRetryableResultError(err error) bool {
	var classified *resultError
	if errors.As(err, &classified) {
		return classified.Retryable
	}
	return true
}

func resultErrorFields(err error) (string, bool) {
	var classified *resultError
	if errors.As(err, &classified) {
		return string(classified.Kind), classified.Retryable
	}
	return string(resultTimeout), true
}

// captureErrorContext сохраняет диагностику неудачной попытки в переданном контексте.
// Нужен там, где у вызывающего есть ctx; captureError остался обёрткой, поэтому остальные
// шесть точек вызова не тронуты и ведут себя как раньше.
func (s *Service) captureErrorContext(ctx context.Context, stage string, attempt, maxAttempts int, err error) error {
	if err == nil {
		return nil
	}
	var captured *debugCapturedError
	if errors.As(err, &captured) {
		return err
	}
	if s.saveDebugArtifactsHook != nil {
		s.saveDebugArtifactsHook(stage, attempt, maxAttempts, err)
	} else {
		s.saveDebugArtifacts(ctx, stage, attempt, maxAttempts, err)
	}
	return &debugCapturedError{err: err}
}

func (s *Service) captureError(stage string, attempt, maxAttempts int, err error) error {
	return s.captureErrorContext(context.Background(), stage, attempt, maxAttempts, err)
}

func (s *Service) saveDebugArtifacts(ctx context.Context, stage string, attempt, maxAttempts int, processingErr error) {
	if s.page == nil {
		s.logContext(ctx, slog.LevelWarn, "Keys.so debug artifacts were not saved: page is unavailable", stage, "attempt", attempt)
		return
	}
	timestamp := time.Now()
	directory := filepath.Join(
		debugArtifactsRoot,
		fmt.Sprintf("article-%d", s.cfg.ArticleID),
		fmt.Sprintf("%s-attempt-%d", timestamp.Format("20060102-150405.000000000"), attempt),
	)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		s.logContext(ctx, slog.LevelWarn, "не удалось создать каталог Keys.so debug", stage, "attempt", attempt, "debug_path", directory, "error", err)
		return
	}

	screenshotPath := filepath.Join(directory, "screenshot.png")
	sensitiveFields := s.page.Locator(`input[type="password"], input[name="email"], input[type="hidden"]`)
	if _, err := s.page.Screenshot(playwright.PageScreenshotOptions{
		Path: playwright.String(screenshotPath), FullPage: playwright.Bool(true), Mask: []playwright.Locator{sensitiveFields},
	}); err != nil {
		s.logContext(ctx, slog.LevelWarn, "не удалось сохранить screenshot Keys.so", stage, "attempt", attempt, "debug_path", screenshotPath, "error", err)
	}

	html, htmlErr := s.page.Content()
	if htmlErr != nil {
		s.logContext(ctx, slog.LevelWarn, "не удалось получить HTML Keys.so", stage, "attempt", attempt, "debug_path", directory, "error", htmlErr)
	} else if err := os.WriteFile(filepath.Join(directory, "page.html"), []byte(redactDiagnosticHTML(html, s.cfg.Email, s.cfg.Password)), 0o600); err != nil {
		s.logContext(ctx, slog.LevelWarn, "не удалось сохранить HTML Keys.so", stage, "attempt", attempt, "debug_path", directory, "error", err)
	}

	title, titleErr := s.page.Title()
	if titleErr != nil {
		s.logContext(ctx, slog.LevelWarn, "не удалось получить title для Keys.so debug", stage, "attempt", attempt, "error", titleErr)
	}
	readyState := "<unavailable>"
	if value, evaluateErr := s.page.Evaluate(`() => document.readyState`); evaluateErr != nil {
		s.logContext(ctx, slog.LevelWarn, "не удалось получить readyState для Keys.so debug", stage, "attempt", attempt, "error", evaluateErr)
	} else if state, ok := value.(string); ok {
		readyState = state
	}
	result, retryable := resultErrorFields(processingErr)
	info := debugInfo{
		ArticleID: s.cfg.ArticleID, ExternalID: s.cfg.ExternalID,
		Attempt: attempt, MaxAttempts: maxAttempts, Stage: stage, Operation: "prepare",
		URL: s.currentURL(), Title: title, ReadyState: readyState,
		Timestamp: timestamp, Error: safeDiagnosticError(processingErr),
		ReferenceURL: s.referenceURL, RequestedURL: s.requestedURL, Result: result, Retryable: retryable,
	}
	encoded, encodeErr := json.MarshalIndent(info, "", "  ")
	if encodeErr != nil {
		s.logContext(ctx, slog.LevelWarn, "не удалось сформировать Keys.so info.json", stage, "attempt", attempt, "error", encodeErr)
	} else if err := os.WriteFile(filepath.Join(directory, "info.json"), encoded, 0o600); err != nil {
		s.logContext(ctx, slog.LevelWarn, "не удалось сохранить Keys.so info.json", stage, "attempt", attempt, "debug_path", directory, "error", err)
	}
	s.logContext(ctx, slog.LevelInfo, "Keys.so debug artifacts saved", stage, "attempt", attempt, "debug_path", directory)
}

func safeDiagnosticError(err error) string {
	message := strings.TrimSpace(err.Error())
	lower := strings.ToLower(message)
	if strings.Contains(lower, "authorization:") || strings.Contains(lower, "bearer ") || strings.Contains(lower, "api_key=") {
		return "sensitive error details redacted"
	}
	const maxRunes = 2000
	runes := []rune(message)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	return message
}

func redactDiagnosticHTML(html string, secrets ...string) string {
	for _, secret := range secrets {
		if strings.TrimSpace(secret) != "" {
			html = strings.ReplaceAll(html, secret, "[REDACTED]")
		}
	}
	return html
}

func samePage(actualURL, expectedURL string) bool {
	actual, actualErr := url.Parse(actualURL)
	expected, expectedErr := url.Parse(expectedURL)
	if actualErr != nil || expectedErr != nil {
		return false
	}
	return actual.Host == expected.Host &&
		strings.TrimSuffix(actual.Path, "/") == strings.TrimSuffix(expected.Path, "/")
}

func decodeStringSlice(value any) ([]string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var values []string
	if err := json.Unmarshal(encoded, &values); err != nil {
		return nil, err
	}
	return normalizeQueries(values), nil
}

func parseQueries(value string) []string {
	return normalizeQueries(strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n"))
}

func normalizeQueries(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func (s *Service) requireUnique(locator playwright.Locator, selector, reason string) error {
	count, err := locator.Count()
	if err != nil {
		return fmt.Errorf("count locator %s: %w", selector, err)
	}
	if count != 1 {
		s.logLocatorDiagnostic(locator, selector, reason)
		return fmt.Errorf("expected one %s using %s, found %d", reason, selector, count)
	}
	s.log(slog.LevelDebug, "locator Keys.so выбран", "select_locator", "locator", selector, "selection_reason", reason)
	return nil
}

func (s *Service) waitVisible(
	locator playwright.Locator,
	selector string,
	reason string,
	stage string,
) error {
	s.log(slog.LevelDebug, "ожидание видимого locator Keys.so", stage, "locator", selector, "selection_reason", reason)
	if err := locator.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(longOperationTimeoutMilliseconds),
	}); err != nil {
		s.logLocatorDiagnostic(locator, selector, reason)
		if restrictionErr := s.detectAccessRestriction(stage); restrictionErr != nil {
			return restrictionErr
		}
		return fmt.Errorf("wait for %s using %s: %w", reason, selector, err)
	}
	return nil
}

func (s *Service) firstVisible(
	locator playwright.Locator,
	selector string,
	reason string,
) (playwright.Locator, int, error) {
	matches, err := locator.All()
	if err != nil {
		return nil, -1, fmt.Errorf("get locator matches for %s: %w", selector, err)
	}
	if len(matches) != 1 {
		s.log(slog.LevelWarn, "найдено нестандартное количество locator", "select_locator", "matches_count", len(matches), "locator", selector, "selection_reason", reason)
	}
	for index, candidate := range matches {
		visible, err := candidate.IsVisible()
		if err != nil {
			return nil, -1, fmt.Errorf("check visibility for %s at index %d: %w", selector, index, err)
		}
		if visible {
			return candidate, index, nil
		}
	}
	s.logLocatorDiagnostic(locator, selector, reason)
	return nil, -1, fmt.Errorf("visible %s not found using %s", reason, selector)
}

func (s *Service) logLocatorDiagnostic(locator playwright.Locator, selector, reason string) {
	count, countErr := locator.Count()
	if countErr != nil {
		count = -1
	}
	title, titleErr := s.page.Title()
	if titleErr != nil {
		title = "<title unavailable>"
	}
	s.log(slog.LevelDebug,
		"диагностика locator Keys.so",
		"locator_diagnostic",
		"page_title", title,
		"matches_count", count,
		"locator", selector,
		"selection_reason", reason,
	)
}

func (s *Service) ensureBusinessSession(stage string) error {
	if err := s.detectAccessRestriction(stage); err != nil {
		return err
	}
	if strings.Contains(s.currentURL(), "/login") {
		return fmt.Errorf("Keys.so requires repeated authorization; automatic retry is disabled")
	}
	count, err := s.page.Locator(loginLinkSelector).Count()
	if err != nil {
		return fmt.Errorf("check active Keys.so session: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("Keys.so session is no longer active; repeated authorization is required")
	}
	return nil
}

func (s *Service) detectAccessRestriction(stage string) error {
	restrictions := []struct {
		text        string
		explanation string
	}{
		{"использование аккаунта на нескольких устройствах", "Keys.so detected account use on multiple devices"},
		{"подозрительная активность", "Keys.so detected suspicious activity"},
		{"требуется повторная авторизация", "Keys.so requires repeated authorization"},
	}
	for _, restriction := range restrictions {
		matches := s.page.GetByText(restriction.text, playwright.PageGetByTextOptions{})
		items, err := matches.All()
		if err != nil {
			return fmt.Errorf("check Keys.so access restriction during %s: %w", stage, err)
		}
		for _, item := range items {
			visible, err := item.IsVisible()
			if err != nil {
				return fmt.Errorf("check Keys.so restriction visibility during %s: %w", stage, err)
			}
			if visible {
				return fmt.Errorf("%s; automatic bypass is disabled", restriction.explanation)
			}
		}
	}
	return nil
}

func (s *Service) stageError(stage string, err error) error {
	return &StageError{
		Stage:          stage,
		CurrentURL:     s.currentURL(),
		Duration:       time.Since(s.startedAt),
		CollectedCount: s.collectedCount,
		CleanedCount:   s.cleanedCount,
		Err:            err,
	}
}

// logContext пишет запись этапа в переданном контексте.
//
// Отдельная функция нужна там, где контекст есть на руках: без неё вызывающий с ctx не может
// залогировать ничего, не потеряв его. Общий log остался обёрткой — переписывать все четыре
// десятка его вызовов ради этого не требуется, и остальной пакет ведёт себя как раньше.
func (s *Service) logContext(ctx context.Context, level slog.Level, message, stage string, attributes ...any) {
	duration := time.Duration(0)
	if !s.startedAt.IsZero() {
		duration = time.Since(s.startedAt)
	}
	fields := []any{
		"stage", stage,
		"duration_ms", duration.Milliseconds(),
		"current_url", s.currentURL(),
		"collected_count", s.collectedCount,
		"cleaned_count", s.cleanedCount,
	}
	fields = append(fields, attributes...)
	s.logger.Log(ctx, level, message, fields...)
}

func (s *Service) log(level slog.Level, message, stage string, attributes ...any) {
	s.logContext(context.Background(), level, message, stage, attributes...)
}

func (s *Service) currentURL() string {
	if s.page == nil {
		return "<browser not started>"
	}
	return s.page.URL()
}

// Close безопасно закрывает все ресурсы Playwright.
func (s *Service) Close() error {
	var closeErr error
	if s.browserContext != nil {
		if err := s.browserContext.Close(); err != nil {
			closeErr = fmt.Errorf("close browser context: %w", err)
		}
	}
	if s.pw != nil {
		if err := s.pw.Stop(); err != nil && closeErr == nil {
			closeErr = fmt.Errorf("stop Playwright: %w", err)
		}
	}
	s.page = nil
	s.browserContext = nil
	s.pw = nil
	return closeErr
}

func checkContext(ctx context.Context, stage string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("Keys.so %s: %w", stage, err)
	}
	return nil
}
