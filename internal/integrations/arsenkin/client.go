// Package arsenkin автоматизирует получение частотностей в инструменте Wordstat.
package arsenkin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mxschmitt/playwright-go"
	"github.com/xuri/excelize/v2"
)

const (
	loginURL       = "https://arsenkin.ru/tools/login/"
	wordstatURL    = "https://arsenkin.ru/tools/wordstat/"
	copywritersURL = "https://arsenkin.ru/tools/copyrighters/"
	profilePath    = "data/arsenkin-browser-profile"
	lockFilePath   = profilePath + "/.seo-pipeline.lock"

	emailSelector            = `input[name="email"]`
	passwordSelector         = `input[name="password"]`
	logoutSelector           = `a[href="/tools/auth/logout"]`
	keysSelector             = `textarea#keystr[name="keys"]`
	startSelector            = `button#ok`
	copywritersInputSelector = `textarea[name="keys"][placeholder*="Каждый запрос с новой строки"]`
	structureLinkSelector    = `a[data-target="#structure"][data-toggle="modal"]`

	operationTimeout   = 30_000
	wordstatTimeout    = 300_000
	copywritersTimeout = 600_000
	resultLimit        = 50
)

// KeywordFrequency contains one Wordstat query and its frequency.
type KeywordFrequency struct {
	Query     string `json:"query"`
	Frequency int    `json:"frequency"`
}

type rawKeywordFrequency struct {
	Query     string `json:"query"`
	Frequency string `json:"frequency"`
}

// Result содержит структурированные результаты последовательных этапов Arsenkin.
type Result struct {
	WordstatKeywords    []KeywordFrequency
	CopywriterQueries   []string
	LSIWords            []string
	CompetitorStructure string
}

type Config struct {
	ArticleID int64
	Email     string
	Password  string
	Headless  bool
}

// StageError содержит обязательный контекст ошибки этапа.
type StageError struct {
	ArticleID  int64
	Stage      string
	CurrentURL string
	Duration   time.Duration
	Err        error
}

func (e *StageError) Error() string {
	return fmt.Sprintf("Arsenkin article_id=%d stage=%s current_url=%q: %v", e.ArticleID, e.Stage, e.CurrentURL, e.Err)
}
func (e *StageError) Unwrap() error { return e.Err }

type Service struct {
	cfg       Config
	logger    *slog.Logger
	pw        *playwright.Playwright
	context   playwright.BrowserContext
	page      playwright.Page
	profile   *os.File
	startedAt time.Time
}

func New(cfg Config, logger *slog.Logger) *Service {
	return &Service{cfg: cfg, logger: logger.With("article_id", cfg.ArticleID, "integration", "arsenkin")}
}

// CollectResearch выполняет Wordstat и Copywriters в одном browser context.
func (s *Service) CollectResearch(ctx context.Context, queries []string) (Result, error) {
	s.startedAt = time.Now()
	normalized := normalizeInputQueries(queries)
	if len(normalized) == 0 {
		return Result{}, s.stageError("validate_queries", fmt.Errorf("cleaned Keys.so queries are empty"))
	}
	s.log(slog.LevelInfo, "запуск Arsenkin", "start")
	if err := s.start(); err != nil {
		return Result{}, s.stageError("start_browser", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.ensureAuthenticated(ctx); err != nil {
		return Result{}, s.stageError("authorize", err)
	}
	wordstat, err := s.runWordstat(ctx, normalized)
	if err != nil {
		return Result{}, s.stageError("wordstat", err)
	}
	copywriters, err := s.runCopywriters(ctx, wordstat)
	if err != nil {
		return Result{}, s.stageError("copywriters", err)
	}
	copywriters.WordstatKeywords = wordstat
	return copywriters, nil
}

func (s *Service) start() error {
	if err := os.MkdirAll(profilePath, 0o700); err != nil {
		return fmt.Errorf("create persistent browser profile: %w", err)
	}
	if err := os.Chmod(profilePath, 0o700); err != nil {
		return fmt.Errorf("protect persistent browser profile: %w", err)
	}
	profile, err := os.OpenFile(lockFilePath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open browser profile lock: %w", err)
	}
	if err := syscall.Flock(int(profile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = profile.Close()
		return fmt.Errorf("persistent browser profile is already used by another process: %w", err)
	}
	s.profile = profile

	pw, err := playwright.Run()
	if err != nil {
		_ = s.releaseProfile()
		return fmt.Errorf("start Playwright: %w", err)
	}
	browserContext, err := pw.Chromium.LaunchPersistentContext(profilePath, playwright.BrowserTypeLaunchPersistentContextOptions{
		Headless: playwright.Bool(s.cfg.Headless),
	})
	if err != nil {
		_ = pw.Stop()
		_ = s.releaseProfile()
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
			_ = s.releaseProfile()
			return fmt.Errorf("create Arsenkin page: %w", err)
		}
	}
	page.SetDefaultTimeout(operationTimeout)
	page.SetDefaultNavigationTimeout(operationTimeout)
	s.pw, s.context, s.page = pw, browserContext, page
	return nil
}

func (s *Service) ensureAuthenticated(ctx context.Context) error {
	if err := s.open(ctx, wordstatURL, "check_session"); err != nil {
		return err
	}
	authenticated, err := s.isAuthenticated()
	if err != nil {
		return err
	}
	if authenticated {
		if err := s.waitWordstatForm(); err != nil {
			return fmt.Errorf("verify active Arsenkin session: %w", err)
		}
		s.log(slog.LevelInfo, "сессия Arsenkin подтверждена", "check_session")
		return nil
	}

	s.log(slog.LevelInfo, "авторизация Arsenkin", "authorize")
	if err := s.open(ctx, loginURL, "open_login"); err != nil {
		return err
	}
	authenticated, err = s.isAuthenticated()
	if err != nil {
		return err
	}
	if authenticated {
		if err := s.open(ctx, wordstatURL, "verify_authorization"); err != nil {
			return err
		}
		s.log(slog.LevelInfo, "сессия Arsenkin подтверждена", "check_session")
		return nil
	}
	email := s.page.Locator(emailSelector)
	password := s.page.Locator(passwordSelector)
	loginForm := s.page.Locator(`form:has(input[name="email"]):has(input[name="password"])`)
	if err := s.waitUniqueVisible(email, emailSelector, "поле email"); err != nil {
		return err
	}
	if err := s.waitUniqueVisible(password, passwordSelector, "поле password"); err != nil {
		return err
	}
	if err := s.waitUniqueVisible(loginForm, "verified login form", "форма входа"); err != nil {
		return err
	}
	submit, err := s.firstVisible(loginForm.Locator(`button[type="submit"], input[type="submit"], button`), "login form submit", "кнопка входа")
	if err != nil {
		return err
	}
	if err := email.Fill(s.cfg.Email); err != nil {
		return fmt.Errorf("fill Arsenkin email: %w", err)
	}
	if err := password.Fill(s.cfg.Password); err != nil {
		return fmt.Errorf("fill Arsenkin password: %w", err)
	}
	if err := submit.Click(); err != nil {
		return fmt.Errorf("submit Arsenkin login: %w", err)
	}
	if err := s.page.WaitForURL(func(url string) bool { return !isLoginURL(url) }, playwright.PageWaitForURLOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(operationTimeout),
	}); err != nil {
		return fmt.Errorf("wait for working page after Arsenkin login: %w", err)
	}
	if isLoginURL(s.currentURL()) {
		return fmt.Errorf("Arsenkin authorization ended on login page")
	}
	if err := s.open(ctx, wordstatURL, "verify_authorization"); err != nil {
		return err
	}
	authenticated, err = s.isAuthenticated()
	if err != nil {
		return err
	}
	if !authenticated {
		return fmt.Errorf("Arsenkin authorization did not create an authenticated session")
	}
	if err := s.waitWordstatForm(); err != nil {
		return fmt.Errorf("verify Arsenkin authorization: %w", err)
	}
	s.log(slog.LevelInfo, "сессия Arsenkin подтверждена", "check_session")
	return nil
}

func (s *Service) isAuthenticated() (bool, error) {
	logout := s.page.Locator(logoutSelector)
	count, err := logout.Count()
	if err != nil {
		return false, fmt.Errorf("check Arsenkin logout control: %w", err)
	}
	s.log(slog.LevelDebug, "проверка признака авторизации", "check_session", "locator", logoutSelector, "matches_count", count)
	return count > 0, nil
}

func (s *Service) runWordstat(ctx context.Context, queries []string) ([]KeywordFrequency, error) {
	if err := s.open(ctx, wordstatURL, "open_wordstat"); err != nil {
		return nil, err
	}
	if isLoginURL(s.currentURL()) {
		return nil, fmt.Errorf("Arsenkin session expired before Wordstat start")
	}
	input, err := s.wordstatInput()
	if err != nil {
		return nil, err
	}
	button := s.page.Locator(startSelector)
	if err := s.waitUniqueVisible(button, startSelector, "кнопка запуска Wordstat"); err != nil {
		diagnostic, diagnosticErr := s.page.Locator("body").Evaluate(`body => Array.from(body.querySelectorAll('button, input[type="submit"], a.btn')).slice(0, 40).map(element => {
			const style = getComputedStyle(element);
			const rect = element.getBoundingClientRect();
			return {
				tag: element.tagName.toLowerCase(),
				id: element.id,
				className: String(element.className || ''),
				type: element.getAttribute('type'),
				text: (element.textContent || element.value || '').trim(),
				display: style.display,
				visibility: style.visibility,
				opacity: style.opacity,
				width: rect.width,
				height: rect.height,
				disabled: Boolean(element.disabled)
			};
		})`, nil)
		if diagnosticErr != nil {
			return nil, errors.Join(err, fmt.Errorf("inspect Wordstat start controls: %w", diagnosticErr))
		}
		return nil, fmt.Errorf("%w; start controls: %v", err, diagnostic)
	}
	knownTaskIDs, err := s.wordstatTaskIDs()
	if err != nil {
		return nil, err
	}
	if err := input.Fill(strings.Join(queries, "\n")); err != nil {
		return nil, fmt.Errorf("fill Wordstat queries: %w", err)
	}
	if err := button.Click(); err != nil {
		return nil, fmt.Errorf("start Wordstat: %w", err)
	}
	s.log(slog.LevelInfo, "Wordstat стартовал", "wordstat_start", "queries_count", len(queries))

	for _, threshold := range []int{25, 50, 75} {
		if err := s.waitWordstatProgressOrDownload(ctx, threshold, knownTaskIDs); err != nil {
			return nil, err
		}
		progress, err := s.currentProgress()
		if err != nil {
			return nil, err
		}
		if progress >= threshold {
			s.log(slog.LevelInfo, fmt.Sprintf("progress %d", threshold), "wordstat_progress")
		}
	}
	taskID, err := s.waitWordstatDownload(ctx, knownTaskIDs)
	if err != nil {
		return nil, err
	}
	s.log(slog.LevelInfo, "progress 100", "wordstat_progress")
	result, err := s.downloadWordstatResult(taskID)
	if err != nil {
		return nil, err
	}
	s.log(slog.LevelInfo, "таблица получена", "parse_download", "matches_count", len(result))
	return result, nil
}

func (s *Service) wordstatTaskIDs() ([]string, error) {
	raw, err := s.page.Locator("body").Evaluate(`body => Array.from(new Set(
		Array.from(body.querySelectorAll('[data-task-id]')).map(element => element.getAttribute('data-task-id')).filter(Boolean)
	))`, nil)
	if err != nil {
		return nil, fmt.Errorf("read existing Wordstat task IDs: %w", err)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode existing Wordstat task IDs: %w", err)
	}
	var taskIDs []string
	if err := json.Unmarshal(encoded, &taskIDs); err != nil {
		return nil, fmt.Errorf("decode existing Wordstat task IDs: %w", err)
	}
	return taskIDs, nil
}

func (s *Service) waitWordstatDownload(ctx context.Context, knownTaskIDs []string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.log(slog.LevelDebug, "ожидание файла результата Wordstat", "wait_download")
	_, err := s.page.WaitForFunction(`knownTaskIDs => {
		const known = new Set(knownTaskIDs);
		return Array.from(document.querySelectorAll('.arshis__row--body[data-task-id]')).some(row => {
			const taskID = row.getAttribute('data-task-id');
			const done = row.querySelector('.arshis__status--done');
			const download = row.querySelector('a[href*="/tools/download/23/csv/"][href*="encode=xls"]');
			return taskID && !known.has(taskID) && done && download;
		});
	}`, knownTaskIDs, playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(wordstatTimeout)})
	if err != nil {
		return "", fmt.Errorf("wait for completed Wordstat download: %w", err)
	}
	raw, err := s.page.Locator("body").Evaluate(`(body, knownTaskIDs) => {
		const known = new Set(knownTaskIDs);
		const row = Array.from(body.querySelectorAll('.arshis__row--body[data-task-id]')).find(candidate => {
			const taskID = candidate.getAttribute('data-task-id');
			return taskID && !known.has(taskID) && candidate.querySelector('.arshis__status--done') &&
				candidate.querySelector('a[href*="/tools/download/23/csv/"][href*="encode=xls"]');
		});
		return row?.getAttribute('data-task-id') || '';
	}`, knownTaskIDs)
	if err != nil {
		return "", fmt.Errorf("read completed Wordstat task ID: %w", err)
	}
	taskID := strings.TrimSpace(fmt.Sprint(raw))
	if taskID == "" {
		return "", fmt.Errorf("completed Wordstat task ID is empty")
	}
	return taskID, nil
}

func (s *Service) downloadWordstatResult(taskID string) ([]KeywordFrequency, error) {
	selector := fmt.Sprintf(`.arshis__row--body[data-task-id="%s"] a[href*="/tools/download/23/csv/"][href*="encode=xls"]`, taskID)
	link := s.page.Locator(selector)
	if err := s.waitUniqueVisible(link, selector, "XLSX результата Wordstat"); err != nil {
		return nil, err
	}
	download, err := s.page.ExpectDownload(func() error { return link.Click() }, playwright.PageExpectDownloadOptions{
		Timeout: playwright.Float(operationTimeout),
	})
	if err != nil {
		return nil, fmt.Errorf("download Wordstat XLSX: %w", err)
	}
	path, err := download.Path()
	if err != nil {
		return nil, fmt.Errorf("get Wordstat XLSX path: %w", err)
	}
	workbook, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("open Wordstat XLSX: %w", err)
	}
	defer func() { _ = workbook.Close() }()
	sheets := workbook.GetSheetList()
	if len(sheets) == 0 {
		return nil, fmt.Errorf("Wordstat XLSX contains no worksheets")
	}
	rows, err := workbook.GetRows(sheets[0])
	if err != nil {
		return nil, fmt.Errorf("read Wordstat XLSX rows: %w", err)
	}
	return parseWordstatRows(rows)
}

func parseWordstatRows(rows [][]string) ([]KeywordFrequency, error) {
	if len(rows) < 2 {
		return nil, fmt.Errorf("Wordstat XLSX contains no data rows")
	}
	queryIndex, frequencyIndex := -1, -1
	headerRow := -1
	for rowIndex, row := range rows {
		for columnIndex, value := range row {
			normalized := strings.ToLower(strings.TrimSpace(value))
			if normalized == "фраза" || normalized == "запрос" || strings.Contains(normalized, "ключевая фраза") {
				queryIndex = columnIndex
			}
			if normalized == "ws" || normalized == "частотность" || strings.Contains(normalized, "(ws)") || strings.Contains(normalized, "базовая частот") || strings.Contains(normalized, "wordstat") {
				frequencyIndex = columnIndex
			}
		}
		if queryIndex >= 0 && frequencyIndex >= 0 {
			headerRow = rowIndex
			break
		}
	}
	if headerRow < 0 {
		return nil, fmt.Errorf("Wordstat XLSX headers Фраза/WS not found")
	}
	raw := make([]rawKeywordFrequency, 0, len(rows)-headerRow-1)
	for _, row := range rows[headerRow+1:] {
		if queryIndex >= len(row) || frequencyIndex >= len(row) {
			continue
		}
		raw = append(raw, rawKeywordFrequency{Query: row[queryIndex], Frequency: row[frequencyIndex]})
	}
	return normalizeResults(raw)
}

func (s *Service) runCopywriters(ctx context.Context, keywords []KeywordFrequency) (Result, error) {
	if err := s.open(ctx, copywritersURL, "open_copywriters"); err != nil {
		return Result{}, err
	}
	if isLoginURL(s.currentURL()) {
		return Result{}, fmt.Errorf("Arsenkin session expired before Copywriters start")
	}

	copywriterQueries := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		if query := strings.TrimSpace(keyword.Query); query != "" {
			copywriterQueries = append(copywriterQueries, query)
		}
	}
	if len(copywriterQueries) > resultLimit {
		copywriterQueries = copywriterQueries[:resultLimit]
	}
	if len(copywriterQueries) == 0 {
		return Result{}, fmt.Errorf("Wordstat Top-50 is empty")
	}

	input := s.page.Locator(copywritersInputSelector)
	button := s.page.Locator(`button#ok`)
	if err := s.waitUniqueVisible(input, copywritersInputSelector, "поле «Список запросов (до 50 шт.)» Copywriters"); err != nil {
		return Result{}, err
	}
	if err := s.waitUniqueVisible(button, `button#ok`, "кнопка запуска Copywriters"); err != nil {
		return Result{}, err
	}
	if err := input.Fill(strings.Join(copywriterQueries, "\n")); err != nil {
		return Result{}, fmt.Errorf("fill Copywriters Top-50: %w", err)
	}
	if err := button.Click(); err != nil {
		return Result{}, fmt.Errorf("start Copywriters: %w", err)
	}
	s.log(slog.LevelInfo, "Copywriters стартовал", "copywriters_start", "queries_count", len(copywriterQueries))

	for _, threshold := range []int{25, 50, 75} {
		if err := s.waitCopywritersProgressOrResult(ctx, threshold); err != nil {
			return Result{}, err
		}
		progress, err := s.copywritersProgress()
		if err != nil {
			return Result{}, err
		}
		if progress >= threshold {
			s.log(slog.LevelInfo, fmt.Sprintf("Copywriters progress %d", threshold), "copywriters_progress")
		}
	}
	if err := s.waitCopywritersResult(ctx); err != nil {
		return Result{}, err
	}
	s.log(slog.LevelInfo, "Copywriters progress 100", "copywriters_progress")

	result, err := s.parseCopywritersResult()
	if err != nil {
		return Result{}, err
	}
	result.CopywriterQueries = copywriterQueries
	s.log(slog.LevelInfo, "результат Copywriters получен", "copywriters_result",
		"lsi_count", len(result.LSIWords),
		"competitor_structure_length", len(result.CompetitorStructure),
	)
	return result, nil
}

func (s *Service) waitCopywritersProgressOrResult(ctx context.Context, threshold int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.log(slog.LevelDebug, "ожидание прогресса Copywriters", "wait_copywriters_progress", "threshold", threshold)
	_, err := s.page.WaitForFunction(`threshold => {
		const match = (document.body?.innerText || '').match(/Прогресс\s*:\s*(\d+)\s*%/i);
		const progress = match ? Number(match[1]) : 0;
		const theme = (document.querySelector('textarea[name="theme"]')?.value || '').trim();
		const structureLink = document.querySelector('a[data-target="#structure"][data-toggle="modal"]');
		const structure = (document.querySelector('#structure .modal-body')?.innerText || '').trim();
		const complete = theme.length > 0 && structureLink && structure.length > 0 && !match;
		return progress >= threshold || complete;
	}`, threshold, playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(copywritersTimeout)})
	if err != nil {
		return fmt.Errorf("wait for Copywriters progress %d%%: %w", threshold, err)
	}
	return nil
}

func (s *Service) copywritersProgress() (int, error) {
	value, err := s.page.Locator("body").Evaluate(`body => {
		const match = (body.innerText || '').match(/Прогресс\s*:\s*(\d+)\s*%/i);
		if (match) return match[1];
		const ready = (body.querySelector('textarea[name="theme"]')?.value || '').trim() &&
			body.querySelector('a[data-target="#structure"][data-toggle="modal"]') &&
			(body.querySelector('#structure .modal-body')?.innerText || '').trim();
		return ready ? '100' : '0';
	}`, nil)
	if err != nil {
		return 0, fmt.Errorf("read Copywriters progress: %w", err)
	}
	progress, err := strconv.Atoi(fmt.Sprint(value))
	if err != nil {
		return 0, fmt.Errorf("parse Copywriters progress %q: %w", value, err)
	}
	return progress, nil
}

func (s *Service) waitCopywritersResult(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.log(slog.LevelDebug, "ожидание результата Copywriters", "wait_copywriters_result")
	_, err := s.page.WaitForFunction(`() => {
		const taskID = (document.querySelector('input[name="task_id"]')?.value || '').trim();
		const theme = (document.querySelector('textarea[name="theme"]')?.value || '').trim();
		const structureLink = document.querySelector('a[data-target="#structure"][data-toggle="modal"]');
		const structure = (document.querySelector('#structure .modal-body')?.innerText || '').trim();
		const match = (document.body?.innerText || '').match(/Прогресс\s*:\s*(\d+)\s*%/i);
		return taskID.length > 0 && theme.length > 0 && structureLink && structure.length > 0 &&
			(!match || Number(match[1]) >= 100);
	}`, nil, playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(copywritersTimeout)})
	if err != nil {
		return fmt.Errorf("wait for final Copywriters result: %w", err)
	}
	return nil
}

func (s *Service) parseCopywritersResult() (Result, error) {
	theme := s.page.Locator(`textarea[name="theme"][placeholder="Список тематических слов"]`)
	if err := s.waitUniqueVisible(theme, `textarea[name="theme"][placeholder="Список тематических слов"]`, "тематические слова Copywriters"); err != nil {
		return Result{}, err
	}
	value, err := theme.InputValue()
	if err != nil {
		return Result{}, fmt.Errorf("read Copywriters LSI value: %w", err)
	}
	lsi := uniqueNonEmpty(strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n"))
	if len(lsi) == 0 {
		return Result{}, fmt.Errorf("Copywriters LSI result is empty")
	}

	structureLink := s.page.Locator(structureLinkSelector)
	if err := s.waitUniqueVisible(structureLink, structureLinkSelector, "ссылка структуры конкурентов"); err != nil {
		return Result{}, err
	}
	if err := structureLink.Click(); err != nil {
		return Result{}, fmt.Errorf("open Copywriters competitor structure: %w", err)
	}
	modal := s.page.Locator(`#structure`)
	if err := modal.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateVisible, Timeout: playwright.Float(operationTimeout)}); err != nil {
		return Result{}, fmt.Errorf("wait for visible Copywriters structure modal: %w", err)
	}
	modalBody := modal.Locator(`.modal-body`)
	if err := s.waitUniqueVisible(modalBody, `#structure .modal-body`, "содержимое структуры конкурентов"); err != nil {
		return Result{}, err
	}
	structureText, err := modalBody.InnerText()
	if err != nil {
		return Result{}, fmt.Errorf("read Copywriters competitor structure: %w", err)
	}
	structure := normalizeCompetitorStructure(structureText)
	if structure == "" {
		return Result{}, fmt.Errorf("Copywriters competitor structure is empty")
	}
	return Result{LSIWords: lsi, CompetitorStructure: structure}, nil
}

func uniqueNonEmpty(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func normalizeCompetitorStructure(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	lines := strings.Split(text, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "+")
		line = strings.TrimLeft(line, " ")
		line = strings.TrimLeft(line, "|")
		line = strings.TrimSpace(line)

		if line == "" {
			if len(result) > 0 && result[len(result)-1] != "" {
				result = append(result, "")
			}
			continue
		}
		result = append(result, line)
	}
	if len(result) > 0 && result[len(result)-1] == "" {
		result = result[:len(result)-1]
	}
	return strings.Join(result, "\n")
}

func (s *Service) wordstatInput() (playwright.Locator, error) {
	locator := s.page.Locator(keysSelector)
	if err := locator.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateVisible, Timeout: playwright.Float(operationTimeout)}); err != nil {
		return nil, fmt.Errorf("wait for Wordstat textarea using %s: %w", keysSelector, err)
	}
	matches, err := locator.All()
	if err != nil {
		return nil, fmt.Errorf("find Wordstat textareas: %w", err)
	}
	s.log(slog.LevelDebug, "найдены элементы", "select_locator", "locator", keysSelector, "matches_count", len(matches))
	for _, match := range matches {
		visible, err := match.IsVisible()
		if err != nil {
			return nil, fmt.Errorf("check Wordstat textarea visibility: %w", err)
		}
		if visible {
			return match, nil
		}
	}
	return nil, fmt.Errorf("visible verified Wordstat textarea not found")
}

func (s *Service) waitWordstatProgressOrDownload(ctx context.Context, threshold int, knownTaskIDs []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.log(slog.LevelDebug, "ожидание прогресса Wordstat", "wait_progress", "threshold", threshold)
	_, err := s.page.WaitForFunction(`args => {
		const text = document.body?.innerText || '';
		const match = text.match(/Прогресс\s*:\s*(\d+)\s*%/i);
		const progress = match ? Number(match[1]) : 0;
		const known = new Set(args.knownTaskIDs);
		const done = Array.from(document.querySelectorAll('.arshis__row--body[data-task-id]')).some(row => {
			const taskID = row.getAttribute('data-task-id');
			return taskID && !known.has(taskID) && row.querySelector('.arshis__status--done') &&
				row.querySelector('a[href*="/tools/download/23/csv/"][href*="encode=xls"]');
		});
		return progress >= args.threshold || done;
	}`, map[string]any{"threshold": threshold, "knownTaskIDs": knownTaskIDs}, playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(wordstatTimeout)})
	if err != nil {
		return fmt.Errorf("wait for Wordstat progress %d%%: %w", threshold, err)
	}
	return nil
}

func (s *Service) currentProgress() (int, error) {
	text, err := s.page.Locator("body").InnerText()
	if err != nil {
		return 0, fmt.Errorf("read Wordstat progress: %w", err)
	}
	match := regexp.MustCompile(`(?i)Прогресс\s*:\s*(\d+)\s*%`).FindStringSubmatch(text)
	if len(match) != 2 {
		return 0, nil
	}
	return strconv.Atoi(match[1])
}

func normalizeResults(rows []rawKeywordFrequency) ([]KeywordFrequency, error) {
	result := make([]KeywordFrequency, 0, len(rows))
	indexes := make(map[string]int, len(rows))
	for _, row := range rows {
		query := strings.TrimSpace(row.Query)
		if query == "" {
			continue
		}
		frequencyText := strings.TrimSpace(row.Frequency)
		frequencyText = strings.ReplaceAll(frequencyText, " ", "")
		frequencyText = strings.ReplaceAll(frequencyText, "\u00a0", "")
		frequencyText = strings.ReplaceAll(frequencyText, "\u202f", "")
		frequency, err := strconv.Atoi(frequencyText)
		if err != nil {
			return nil, fmt.Errorf("parse Wordstat frequency %q for query %q: %w", row.Frequency, query, err)
		}
		if index, found := indexes[query]; found {
			if frequency > result[index].Frequency {
				result[index].Frequency = frequency
			}
			continue
		}
		indexes[query] = len(result)
		result = append(result, KeywordFrequency{Query: query, Frequency: frequency})
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Frequency > result[j].Frequency
	})
	if len(result) > resultLimit {
		result = result[:resultLimit]
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("Wordstat table contains no non-empty rows")
	}
	return result, nil
}

func normalizeInputQueries(queries []string) []string {
	result := make([]string, 0, len(queries))
	seen := make(map[string]struct{}, len(queries))
	for _, query := range queries {
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		if _, found := seen[query]; found {
			continue
		}
		seen[query] = struct{}{}
		result = append(result, query)
	}
	return result
}

func (s *Service) waitWordstatForm() error {
	_, err := s.wordstatInput()
	return err
}

func (s *Service) waitUniqueVisible(locator playwright.Locator, selector, reason string) error {
	s.log(slog.LevelDebug, "ожидание locator", "wait_locator", "locator", selector, "selection_reason", reason)
	if err := locator.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateVisible, Timeout: playwright.Float(operationTimeout)}); err != nil {
		return fmt.Errorf("wait for %s using %s: %w", reason, selector, err)
	}
	count, err := locator.Count()
	if err != nil {
		return fmt.Errorf("count %s: %w", reason, err)
	}
	s.log(slog.LevelDebug, "найдены элементы", "select_locator", "locator", selector, "matches_count", count)
	if count != 1 {
		return fmt.Errorf("expected one %s using %s, found %d", reason, selector, count)
	}
	return nil
}

func (s *Service) firstVisible(locator playwright.Locator, selector, reason string) (playwright.Locator, error) {
	s.log(slog.LevelDebug, "ожидание locator", "wait_locator", "locator", selector, "selection_reason", reason)
	if err := locator.First().WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateVisible, Timeout: playwright.Float(operationTimeout)}); err != nil {
		return nil, fmt.Errorf("wait for %s using %s: %w", reason, selector, err)
	}
	matches, err := locator.All()
	if err != nil {
		return nil, fmt.Errorf("find %s: %w", reason, err)
	}
	s.log(slog.LevelDebug, "найдены элементы", "select_locator", "locator", selector, "matches_count", len(matches))
	for _, match := range matches {
		visible, err := match.IsVisible()
		if err != nil {
			return nil, fmt.Errorf("check %s visibility: %w", reason, err)
		}
		if visible {
			return match, nil
		}
	}
	return nil, fmt.Errorf("visible %s not found using %s", reason, selector)
}

func (s *Service) open(ctx context.Context, targetURL, stage string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.log(slog.LevelDebug, "открытие URL", stage, "target_url", targetURL)
	if _, err := s.page.Goto(targetURL, playwright.PageGotoOptions{WaitUntil: playwright.WaitUntilStateDomcontentloaded}); err != nil {
		return fmt.Errorf("open %s: %w", targetURL, err)
	}
	s.log(slog.LevelDebug, "URL открыт", stage)
	return ctx.Err()
}

func isLoginURL(value string) bool { return strings.Contains(value, "/tools/login") }

func (s *Service) stageError(stage string, err error) error {
	return &StageError{ArticleID: s.cfg.ArticleID, Stage: stage, CurrentURL: s.currentURL(), Duration: time.Since(s.startedAt), Err: err}
}

func (s *Service) log(level slog.Level, message, stage string, fields ...any) {
	attributes := []any{"stage", stage, "current_url", s.currentURL(), "duration_ms", time.Since(s.startedAt).Milliseconds()}
	s.logger.Log(context.Background(), level, message, append(attributes, fields...)...)
}

func (s *Service) currentURL() string {
	if s.page == nil {
		return "<browser not started>"
	}
	return s.page.URL()
}

func (s *Service) releaseProfile() error {
	if s.profile == nil {
		return nil
	}
	err := syscall.Flock(int(s.profile.Fd()), syscall.LOCK_UN)
	closeErr := s.profile.Close()
	s.profile = nil
	if err != nil {
		return err
	}
	return closeErr
}

func (s *Service) Close() error {
	var result error
	if s.context != nil {
		result = s.context.Close()
	}
	if s.pw != nil {
		if err := s.pw.Stop(); result == nil {
			result = err
		}
	}
	if err := s.releaseProfile(); result == nil {
		result = err
	}
	s.page, s.context, s.pw = nil, nil, nil
	return result
}
