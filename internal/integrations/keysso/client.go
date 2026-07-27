package keysso

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"
)

const (
	loginURL                         = "https://keys.so/login"
	operationTimeoutMilliseconds     = 30_000
	longOperationTimeoutMilliseconds = 60_000
)

// Config содержит настройки интеграции с Keys.so.
type Config struct {
	Email    string
	Password string
	Headless bool
}

// Service управляет браузерной автоматизацией Keys.so.
type Service struct {
	cfg Config

	pw      *playwright.Playwright
	browser playwright.Browser
	page    playwright.Page
}

// New создаёт интеграцию с Keys.so.
func New(cfg Config) *Service {
	return &Service{
		cfg: cfg,
	}
}

// CollectCleanKeywords выполняет полный сценарий:
//
//  1. авторизуется;
//  2. ищет данные по URL конкурента;
//  3. получает таблицу;
//  4. отправляет её в очистку неявных дублей;
//  5. возвращает первый очищенный результат.
func (s *Service) CollectCleanKeywords(
	ctx context.Context,
	referenceURL string,
) (string, error) {
	if strings.TrimSpace(referenceURL) == "" {
		return "", fmt.Errorf("reference URL is empty")
	}
	if err := checkContext(ctx, "before starting browser"); err != nil {
		return "", err
	}

	if err := s.start(); err != nil {
		return "", err
	}
	defer func() {
		_ = s.Close()
	}()

	if err := s.login(ctx); err != nil {
		return "", err
	}

	rawTable, err := s.collectCompetitorTable(ctx, referenceURL)
	if err != nil {
		return "", err
	}

	result, err := s.cleanDuplicates(ctx, rawTable)
	if err != nil {
		return "", err
	}

	return result, nil
}

// start запускает Playwright и Chromium.
func (s *Service) start() error {
	if err := s.Close(); err != nil {
		return fmt.Errorf("close previous Keys.so session: %w", err)
	}

	pw, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("start Playwright: %w", err)
	}

	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(s.cfg.Headless),
		SlowMo:   playwright.Float(150),
	})
	if err != nil {
		_ = pw.Stop()
		return fmt.Errorf("launch Chromium: %w", err)
	}

	page, err := browser.NewPage()
	if err != nil {
		_ = browser.Close()
		_ = pw.Stop()
		return fmt.Errorf("create browser page: %w", err)
	}

	page.SetDefaultTimeout(operationTimeoutMilliseconds)
	page.SetDefaultNavigationTimeout(operationTimeoutMilliseconds)

	s.pw = pw
	s.browser = browser
	s.page = page

	return nil
}

// login авторизуется в Keys.so.
func (s *Service) login(ctx context.Context) error {
	if err := checkContext(ctx, "before authorization"); err != nil {
		return err
	}

	if _, err := s.page.Goto(
		loginURL,
		playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		},
	); err != nil {
		return fmt.Errorf("open Keys.so login page: %w", err)
	}
	if err := checkContext(ctx, "after opening Keys.so login page"); err != nil {
		return err
	}

	/*
		ВНИМАНИЕ: селекторы ниже не подтверждены на реальном Keys.so.
		Их нужно проверить вручную в авторизованной сессии перед подключением интеграции.

		Пример:
		emailInput := s.page.Locator(`input[type="email"]`)
		passwordInput := s.page.Locator(`input[type="password"]`)
		loginButton := s.page.GetByRole("button", playwright.PageGetByRoleOptions{
			Name: "Войти",
		})
	*/

	emailInput := s.page.Locator(`[data-testid="login-email"]`)
	passwordInput := s.page.Locator(`[data-testid="login-password"]`)
	loginButton := s.page.Locator(`[data-testid="login-submit"]`)

	if err := emailInput.Fill(s.cfg.Email); err != nil {
		return fmt.Errorf("fill Keys.so email: %w", err)
	}

	if err := passwordInput.Fill(s.cfg.Password); err != nil {
		return fmt.Errorf("fill Keys.so password: %w", err)
	}

	if err := loginButton.Click(); err != nil {
		return fmt.Errorf("click Keys.so login button: %w", err)
	}

	if err := s.page.WaitForLoadState(
		playwright.PageWaitForLoadStateOptions{
			State: playwright.LoadStateNetworkidle,
		},
	); err != nil {
		return fmt.Errorf("wait for Keys.so authorization: %w", err)
	}
	if err := checkContext(ctx, "after Keys.so authorization"); err != nil {
		return err
	}

	return nil
}

// collectCompetitorTable ищет конкурента и возвращает таблицу как текст.
func (s *Service) collectCompetitorTable(
	ctx context.Context,
	referenceURL string,
) (string, error) {
	if err := checkContext(ctx, "before competitor search"); err != nil {
		return "", err
	}

	searchInput := s.page.Locator(`[data-testid="main-search-input"]`)
	searchButton := s.page.Locator(`[data-testid="main-search-submit"]`)

	if err := searchInput.Fill(referenceURL); err != nil {
		return "", fmt.Errorf("fill competitor URL: %w", err)
	}

	if err := searchButton.Click(); err != nil {
		return "", fmt.Errorf("start competitor search: %w", err)
	}
	if err := checkContext(ctx, "after starting competitor search"); err != nil {
		return "", err
	}

	table := s.page.Locator(`[data-testid="competitor-keywords-table"]`)

	if err := table.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(longOperationTimeoutMilliseconds),
	}); err != nil {
		return "", fmt.Errorf("wait for competitor table: %w", err)
	}
	if err := checkContext(ctx, "after waiting for competitor table"); err != nil {
		return "", err
	}

	tableText, err := table.InnerText()
	if err != nil {
		return "", fmt.Errorf("read competitor table: %w", err)
	}

	tableText = strings.TrimSpace(tableText)
	if tableText == "" {
		return "", fmt.Errorf("competitor table is empty")
	}

	return tableText, nil
}

// cleanDuplicates отправляет таблицу в инструмент очистки неявных дублей.
func (s *Service) cleanDuplicates(
	ctx context.Context,
	rawTable string,
) (string, error) {
	if err := checkContext(ctx, "before duplicate cleanup"); err != nil {
		return "", err
	}

	queriesMenu := s.page.GetByText(
		"Запросы",
		playwright.PageGetByTextOptions{
			Exact: playwright.Bool(true),
		},
	)

	if err := queriesMenu.Click(); err != nil {
		return "", fmt.Errorf("open queries menu: %w", err)
	}

	cleanupLink := s.page.GetByText(
		"Чистка неявных дублей",
		playwright.PageGetByTextOptions{
			Exact: playwright.Bool(true),
		},
	)

	if err := cleanupLink.Click(); err != nil {
		return "", fmt.Errorf("open implicit duplicates cleanup: %w", err)
	}
	if err := checkContext(ctx, "after opening duplicate cleanup"); err != nil {
		return "", err
	}

	input := s.page.Locator(`[data-testid="duplicates-input"]`)
	runButton := s.page.Locator(`[data-testid="duplicates-submit"]`)

	if err := input.Fill(rawTable); err != nil {
		return "", fmt.Errorf("fill duplicates input: %w", err)
	}

	if err := runButton.Click(); err != nil {
		return "", fmt.Errorf("start duplicate cleanup: %w", err)
	}
	if err := checkContext(ctx, "after starting duplicate cleanup"); err != nil {
		return "", err
	}

	result := s.page.Locator(`[data-testid="duplicates-first-result"]`)

	if err := result.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(longOperationTimeoutMilliseconds),
	}); err != nil {
		return "", fmt.Errorf("wait for duplicate cleanup result: %w", err)
	}
	if err := checkContext(ctx, "after waiting for duplicate cleanup result"); err != nil {
		return "", err
	}

	resultText, err := result.InnerText()
	if err != nil {
		return "", fmt.Errorf("read duplicate cleanup result: %w", err)
	}

	resultText = strings.TrimSpace(resultText)
	if resultText == "" {
		return "", fmt.Errorf("duplicate cleanup result is empty")
	}

	return resultText, nil
}

// Close закрывает браузер и Playwright.
func (s *Service) Close() error {
	var closeErr error

	if s.browser != nil {
		if err := s.browser.Close(); err != nil {
			closeErr = fmt.Errorf("close browser: %w", err)
		}
		s.browser = nil
		s.page = nil
	}

	if s.pw != nil {
		if err := s.pw.Stop(); err != nil && closeErr == nil {
			closeErr = fmt.Errorf("stop Playwright: %w", err)
		}
		s.pw = nil
	}

	return closeErr
}

func checkContext(ctx context.Context, stage string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("Keys.so %s: %w", stage, err)
	}
	return nil
}

// Wait оставляет окно открытым на время отладки.
func (s *Service) Wait() {
	time.Sleep(5 * time.Second)
}
