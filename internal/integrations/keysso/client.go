package keysso

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
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
	cleanupInputSelector             = `textarea.p-inputtextarea`
	resultFieldSelector              = `div.field`
	operationTimeoutMilliseconds     = 30_000
	longOperationTimeoutMilliseconds = 60_000
)

// Config содержит настройки интеграции с Keys.so.
type Config struct {
	ArticleID int64
	Email     string
	Password  string
	Headless  bool
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
}

// New создаёт интеграцию с Keys.so.
func New(cfg Config, logger *slog.Logger) *Service {
	return &Service{cfg: cfg, logger: logger.With("article_id", cfg.ArticleID, "integration", "keysso")}
}

// CollectCleanKeywords получает запросы конкурента и очищает неявные дубли.
func (s *Service) CollectCleanKeywords(ctx context.Context, referenceURL string) (CollectResult, error) {
	s.startedAt = time.Now()
	s.collectedCount = 0
	s.cleanedCount = 0
	if strings.TrimSpace(referenceURL) == "" {
		return CollectResult{}, s.stageError("validate_reference_url", fmt.Errorf("reference URL is empty"))
	}
	if err := checkContext(ctx, "before starting browser"); err != nil {
		return CollectResult{}, s.stageError("start_browser", err)
	}
	if err := s.start(); err != nil {
		return CollectResult{}, s.stageError("start_browser", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.ensureAuthenticated(ctx); err != nil {
		return CollectResult{}, s.stageError("check_authorization", err)
	}
	queries, err := s.collectCompetitorQueries(ctx, referenceURL)
	if err != nil {
		return CollectResult{}, s.stageError("collect_competitor_queries", err)
	}
	s.collectedCount = len(queries)

	cleaned, err := s.cleanDuplicates(ctx, queries)
	if err != nil {
		return CollectResult{}, s.stageError("clean_duplicates", err)
	}
	s.cleanedCount = len(cleaned)
	return CollectResult{
		CollectedCount:  len(queries),
		CleanedKeywords: cleaned,
	}, nil
}

func (s *Service) start() error {
	if err := s.Close(); err != nil {
		return fmt.Errorf("close previous Keys.so session: %w", err)
	}

	s.log(slog.LevelDebug, "запуск Playwright", "start_browser", "profile_path", profilePath)
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
	s.log(slog.LevelDebug, "Playwright запущен с постоянным профилем", "start_browser")
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
	if err := s.open(ctx, homeURL, "open Keys.so search page"); err != nil {
		return nil, err
	}
	if err := s.ensureBusinessSession("open_search_page"); err != nil {
		return nil, err
	}
	searchInput := s.page.Locator(searchSelector)
	searchButton := s.page.GetByRole("button", playwright.PageGetByRoleOptions{
		Name:  "Поиск",
		Exact: playwright.Bool(true),
	})
	if err := s.requireUnique(searchInput, searchSelector, "поле поиска конкурента"); err != nil {
		return nil, fmt.Errorf("search field not found: %w", err)
	}
	if err := s.requireUnique(searchButton, `getByRole(button, name="Поиск")`, "кнопка поиска конкурента"); err != nil {
		return nil, err
	}
	if err := searchInput.Fill(referenceURL); err != nil {
		return nil, fmt.Errorf("fill competitor URL: %w", err)
	}
	if err := searchButton.Click(); err != nil {
		return nil, fmt.Errorf("start competitor search: %w", err)
	}
	s.log(slog.LevelDebug, "поиск конкурента отправлен, ожидание результатов", "collect_competitor_queries")
	if err := s.page.WaitForURL("**/ru/keysbypage**", playwright.PageWaitForURLOptions{
		WaitUntil: playwright.WaitUntilStateCommit,
		Timeout:   playwright.Float(longOperationTimeoutMilliseconds),
	}); err != nil {
		if restrictionErr := s.detectAccessRestriction("wait_search_results_url"); restrictionErr != nil {
			return nil, restrictionErr
		}
		return nil, fmt.Errorf("wait for competitor results URL: current URL %q: %w", s.currentURL(), err)
	}

	pageSize := s.page.Locator(pageSizeSelector)
	if _, err := s.page.WaitForFunction(
		`selector => document.querySelectorAll(selector).length > 0`,
		pageSizeSelector,
		playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(longOperationTimeoutMilliseconds)},
	); err != nil {
		s.logLocatorDiagnostic(pageSize, pageSizeSelector, "видимый select количества строк таблицы")
		if restrictionErr := s.detectAccessRestriction("wait_search_results"); restrictionErr != nil {
			return nil, restrictionErr
		}
		return nil, fmt.Errorf("keywords table did not load: %w", err)
	}
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
		return nil, fmt.Errorf("competitor queries list is empty")
	}
	s.collectedCount = len(queries)
	s.log(slog.LevelInfo, "запросы конкурента получены", "collect_competitor_queries")
	return queries, nil
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
			return fmt.Errorf("%s: %w", stage, err)
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
	s.log(slog.LevelDebug, "страница Keys.so открыта", stage)
	return nil
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

func (s *Service) log(level slog.Level, message, stage string, attributes ...any) {
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
	s.logger.Log(context.Background(), level, message, fields...)
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
