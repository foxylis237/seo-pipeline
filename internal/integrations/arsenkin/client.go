// Package arsenkin автоматизирует получение частотностей в инструменте Wordstat.
package arsenkin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

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

	wordstatTaskRowSelector = `.arshis__row--body[data-task-id]`

	operationTimeout = 30_000
	// wordstatHistoryTimeout ограничивает ожидание отрисовки списка задач: пустая история
	// тоже валидна, поэтому ждать её долго незачем.
	wordstatHistoryTimeout = 15_000
	wordstatForeignSample  = 5
	// wordstatSanitizeSample ограничивает список примеров вычищенных фраз в логе.
	wordstatSanitizeSample = 5
	// wordstatPollInterval — шаг ожидания между перезагрузками списка задач.
	wordstatPollInterval = 20_000
	// wordstatStartTimeout — бюджет подтверждения того, что Arsenkin принял запросы.
	// Задача заводится сразу после submit, поэтому её отсутствие через полторы минуты —
	// это отказ приёма, а не медленный расчёт: ждать такое весь бюджет результата незачем.
	wordstatStartTimeout = 90_000
	// submitObservationWindow — окно наблюдения за отправкой: сколько ждём POST-запрос
	// после клика и состояние кнопки. Обработчик страницы блокирует кнопку в beforeSend,
	// то есть до самого запроса, поэтому длинного окна тут не нужно.
	submitObservationWindow = 10_000
	// containerSampleRunes ограничивает выдержку из #container: туда попадает либо прогресс,
	// либо текст ошибки сервера, и первых строк для разбора достаточно.
	containerSampleRunes  = 500
	diagnosticSampleRunes = 300
	// wordstatTimeout — бюджет ожидания своей задачи. Раньше хватало и меньшего, потому что
	// подходила любая готовая строка, в том числе чужая; теперь ждём именно свою.
	wordstatTimeout    = 600_000
	copywritersTimeout = 600_000
	resultLimit        = 50
	// maxWordstatQueries ограничивает размер отправляемого в форму списка. Лишние запросы
	// отбрасываются с конца, порядок оставшихся сохраняется.
	maxWordstatQueries = 49

	// defaultDebugArtifactsRoot используется, когда каталог не задан вызывающим. Имени задачи
	// здесь нет намеренно: интеграция не знает, что задач больше одной, — корень ей передают.
	defaultDebugArtifactsRoot = "output/debug/arsenkin"
)

// errWordstatTaskNotCreated marks the state in which the start button was clicked but the
// account still shows only the tasks that were there before.
var errWordstatTaskNotCreated = errors.New("Wordstat не создал новую задачу")

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
	// DebugDir — корень диагностики этой интеграции. Задаётся вызывающим, чтобы дампы разных
	// пайплайнов не смешивались; пустое значение включает общий каталог по умолчанию.
	DebugDir string
}

// debugArtifactsRoot возвращает каталог диагностики этого прогона.
func (s *Service) debugArtifactsRoot() string {
	if root := strings.TrimSpace(s.cfg.DebugDir); root != "" {
		return root
	}
	return defaultDebugArtifactsRoot
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
	// lastSubmit — что страница сделала в ответ на последний клик запуска. Нужен, чтобы
	// итоговая ошибка этапа несла доказательства отправки, а не только свой вердикт.
	lastSubmit *submitOutcome
}

func New(cfg Config, logger *slog.Logger) *Service {
	return &Service{cfg: cfg, logger: logger.With("article_id", cfg.ArticleID, "integration", "arsenkin")}
}

// CollectResearch выполняет Wordstat и Copywriters в одном browser context.
func (s *Service) CollectResearch(ctx context.Context, queries []string) (Result, error) {
	s.startedAt = time.Now()
	if sanitized, samples := sanitizedQueries(queries); sanitized > 0 {
		s.logCtx(ctx, slog.LevelInfo, "во фразах Wordstat вычищены лишние символы", "wordstat_sanitize",
			"sanitized_count", sanitized, "samples", samples)
	}
	normalized := normalizeInputQueries(queries)
	if len(normalized) == 0 {
		return Result{}, s.stageError("validate_queries", fmt.Errorf("cleaned Keys.so queries are empty"))
	}
	submitted := limitWordstatQueries(normalized)
	if len(submitted) != len(normalized) {
		s.log(slog.LevelInfo, "список запросов Wordstat обрезан до лимита формы", "wordstat_limit",
			"original_count", len(normalized), "submitted_count", len(submitted))
	}
	s.log(slog.LevelInfo, "запуск Arsenkin", "start")
	if err := s.start(); err != nil {
		return Result{}, s.stageError("start_browser", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.ensureAuthenticated(ctx); err != nil {
		return Result{}, s.stageError("authorize", err)
	}
	wordstat, err := s.runWordstat(ctx, submitted)
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
	knownTaskIDs, err := s.snapshotWordstatTasks(ctx)
	if err != nil {
		return nil, err
	}
	s.log(slog.LevelInfo, "состояние Wordstat до запуска", "wordstat_start",
		"known_task_count", len(knownTaskIDs), "known_task_ids", knownTaskIDs)
	expected := strings.Join(queries, "\n")
	if err := input.Fill(expected); err != nil {
		return nil, fmt.Errorf("fill Wordstat queries: %w", err)
	}
	filled, err := s.inspectWordstatInput(input, queries, expected)
	if err != nil {
		return nil, err
	}
	s.logCtx(ctx, slog.LevelInfo, "поле Wordstat заполнено", "wordstat_fill", filled.fields()...)
	s.saveStageSnapshot(ctx, "after_fill", filled, nil)
	if err := filled.accept(); err != nil {
		s.saveDebugArtifacts(ctx, "wordstat_fill", err, debugState{KnownTaskIDs: knownTaskIDs, SubmittedCount: len(queries)})
		return nil, err
	}

	submit := s.submitWordstat(ctx, button)
	s.lastSubmit = &submit
	s.saveStageSnapshot(ctx, "after_submit", submit, nil)
	if submit.ClickErr != nil {
		return nil, fmt.Errorf("start Wordstat: %w", submit.ClickErr)
	}
	s.logCtx(ctx, slog.LevelInfo, "кнопка запуска Wordstat нажата", "wordstat_start",
		append([]any{"queries_count", len(queries)}, submit.fields()...)...)

	// Клик сам по себе ничего не доказывает: форма отправляется фоновым запросом, адрес
	// страницы не меняется, и отказ приёма выглядит ровно как принятый запрос. Признак
	// приёма один — в списке задач аккаунта появился идентификатор, которого до запуска
	// не было.
	taskID, err := s.confirmWordstatTaskCreated(ctx, knownTaskIDs, len(queries))
	if err != nil {
		return nil, err
	}
	s.log(slog.LevelInfo, "Arsenkin принял запросы", "wordstat_start",
		"known_task_count", len(knownTaskIDs), "task_id", taskID, "queries_count", len(queries))

	if err := s.waitWordstatTaskCompleted(ctx, taskID); err != nil {
		return nil, err
	}
	s.log(slog.LevelInfo, "progress 100", "wordstat_progress")
	s.log(slog.LevelInfo, "состояние Wordstat после ожидания", "wordstat_result",
		"known_task_count", len(knownTaskIDs), "task_id", taskID)
	result, err := s.downloadWordstatResult(taskID)
	if err != nil {
		return nil, err
	}
	if err := acceptWordstatResult(queries, result); err != nil {
		return nil, err
	}
	s.log(slog.LevelInfo, "таблица получена", "parse_download", "matches_count", len(result))
	return result, nil
}

// snapshotWordstatTasks records the tasks already on the page before a new one is started.
//
// Список задач подгружается отдельным запросом, поэтому снимать его сразу после появления
// формы нельзя: пустой снимок делает чужую завершённую задачу «новой», и её результат
// уезжает в текущую статью. Ждём отрисовки списка; пустая история — законный исход
// (чистый профиль), и тогда снимок пуст по существу, а не из-за гонки.
func (s *Service) snapshotWordstatTasks(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.waitWordstatHistoryRendered(wordstatHistoryTimeout * time.Millisecond); err != nil {
		s.log(slog.LevelWarn, "история задач Wordstat пуста или не отрисовалась", "wordstat_start",
			"locator", wordstatTaskRowSelector, "timeout_ms", wordstatHistoryTimeout, "error", err)
	}
	return s.wordstatTaskIDs()
}

// waitWordstatHistoryRendered waits until the account task list is drawn in the document.
// Список приходит отдельным XHR уже после DOMContentLoaded, поэтому это единственный
// надёжный признак того, что читать идентификаторы задач уже имеет смысл.
func (s *Service) waitWordstatHistoryRendered(timeout time.Duration) error {
	_, err := s.page.WaitForFunction(
		`selector => document.querySelectorAll(selector).length > 0`,
		wordstatTaskRowSelector,
		playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(float64(timeout.Milliseconds()))},
	)
	return err
}

// acceptWordstatResult verifies the downloaded table answers the submitted queries.
//
// Wordstat отвечает теми же фразами, которые ему отправили, поэтому расхождение означает,
// что скачан файл чужой задачи. Это последняя проверка, не зависящая от вёрстки: она ловит
// подмену даже тогда, когда разметка страницы поменялась и защита по task_id промахнулась.
func acceptWordstatResult(submitted []string, returned []KeywordFrequency) error {
	if len(returned) == 0 {
		return nil
	}
	known := make(map[string]struct{}, len(submitted))
	for _, query := range submitted {
		if normalized := normalizeWordstatPhrase(query); normalized != "" {
			known[normalized] = struct{}{}
		}
	}
	matched := 0
	foreign := make([]string, 0, wordstatForeignSample)
	for _, row := range returned {
		if _, found := known[normalizeWordstatPhrase(row.Query)]; found {
			matched++
			continue
		}
		if len(foreign) < wordstatForeignSample {
			foreign = append(foreign, row.Query)
		}
	}
	if matched*2 >= len(returned) {
		return nil
	}
	return fmt.Errorf(
		"Wordstat вернул результат другой задачи: из %d фраз отправлялись только %d (например: %v)",
		len(returned), matched, foreign,
	)
}

func normalizeWordstatPhrase(value string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "ё", "е")), " ")
}

// selectNewWordstatTask returns the task created by this run: the single task on the page
// that was not there before the start. Its errWordstatTaskNotCreated result is a state to
// wait through while the list is still being refreshed, not yet a verdict.
func selectNewWordstatTask(known, visible []string) (string, error) {
	seen := make(map[string]struct{}, len(known))
	for _, taskID := range known {
		if trimmed := strings.TrimSpace(taskID); trimmed != "" {
			seen[trimmed] = struct{}{}
		}
	}
	fresh := make([]string, 0, 1)
	for _, taskID := range visible {
		trimmed := strings.TrimSpace(taskID)
		if trimmed == "" {
			continue
		}
		if _, found := seen[trimmed]; found {
			continue
		}
		if !slices.Contains(fresh, trimmed) {
			fresh = append(fresh, trimmed)
		}
	}
	switch len(fresh) {
	case 1:
		return fresh[0], nil
	case 0:
		return "", fmt.Errorf(
			"%w: на странице только прежние результаты (задач до запуска: %d, сейчас на странице: %d)",
			errWordstatTaskNotCreated, len(known), len(visible),
		)
	default:
		return "", fmt.Errorf("Wordstat показал несколько новых задач %v: определить свою невозможно", fresh)
	}
}

func (s *Service) wordstatTaskIDs() ([]string, error) {
	raw, err := s.page.Locator("body").Evaluate(`body => Array.from(new Set(
		Array.from(body.querySelectorAll('[data-task-id]')).map(element => element.getAttribute('data-task-id')).filter(Boolean)
	))`, nil)
	if err != nil {
		return nil, fmt.Errorf("read existing Wordstat task IDs: %w", err)
	}
	return decodeTaskIDs(raw, "existing Wordstat task IDs")
}

func decodeTaskIDs(raw any, reason string) ([]string, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", reason, err)
	}
	var taskIDs []string
	if err := json.Unmarshal(encoded, &taskIDs); err != nil {
		return nil, fmt.Errorf("decode %s: %w", reason, err)
	}
	return taskIDs, nil
}

// wordstatInputState is what the textarea really holds after Fill. Сами запросы сюда не
// попадают: длина, число строк и укороченный SHA-256 отвечают на вопрос «то ли лежит в поле»
// не хуже, а в журнал и артефакты ключевые слова не утекают.
type wordstatInputState struct {
	Selector            string `json:"selector"`
	QueriesCount        int    `json:"queries_count"`
	ExpectedLength      int    `json:"expected_length"`
	ExpectedLines       int    `json:"expected_lines"`
	ExpectedFingerprint string `json:"expected_fingerprint"`
	DOMLength           int    `json:"dom_length"`
	DOMLines            int    `json:"dom_lines"`
	DOMFingerprint      string `json:"dom_fingerprint"`
	Visible             bool   `json:"visible"`
	Enabled             bool   `json:"enabled"`
	ReadOnly            bool   `json:"read_only"`
	MaxLength           int    `json:"max_length"`
}

// Match отвечает на главный вопрос диагностики: в поле лежит ровно то, что отправляем.
func (s wordstatInputState) Match() bool {
	return s.DOMFingerprint == s.ExpectedFingerprint
}

func (s wordstatInputState) fields() []any {
	return []any{
		"locator", s.Selector, "queries_count", s.QueriesCount,
		"expected_length", s.ExpectedLength, "expected_lines", s.ExpectedLines,
		"expected_fingerprint", s.ExpectedFingerprint,
		"dom_length", s.DOMLength, "dom_lines", s.DOMLines, "dom_fingerprint", s.DOMFingerprint,
		"fingerprint_match", s.Match(),
		"visible", s.Visible, "enabled", s.Enabled, "read_only", s.ReadOnly, "max_length", s.MaxLength,
	}
}

// accept blocks the click when the field cannot carry the queries at all. Расхождение
// отпечатков при совпавшем числе строк само по себе не отказ: страница нормализует перевод
// строк на событии change, и такой прогон должен дойти до сервера и быть разобран по
// артефактам, а не остановиться здесь.
func (s wordstatInputState) accept() error {
	switch {
	case !s.Visible || !s.Enabled || s.ReadOnly:
		return fmt.Errorf(
			"поле запросов Wordstat недоступно для ввода: visible=%t enabled=%t read_only=%t",
			s.Visible, s.Enabled, s.ReadOnly)
	case s.DOMLength == 0:
		return fmt.Errorf(
			"после заполнения поле запросов Wordstat пустое: отправлять нечего (ожидалось строк: %d, символов: %d)",
			s.ExpectedLines, s.ExpectedLength)
	case s.DOMLines != s.ExpectedLines:
		return fmt.Errorf(
			"поле запросов Wordstat содержит %d строк вместо %d (символов %d вместо %d, отпечаток %s вместо %s)",
			s.DOMLines, s.ExpectedLines, s.DOMLength, s.ExpectedLength, s.DOMFingerprint, s.ExpectedFingerprint)
	default:
		return nil
	}
}

// inspectWordstatInput reads back the textarea instead of trusting a successful Fill.
func (s *Service) inspectWordstatInput(input playwright.Locator, queries []string, expected string) (wordstatInputState, error) {
	raw, err := input.Evaluate(`element => ({
		value: element.value || '',
		readOnly: Boolean(element.readOnly),
		disabled: Boolean(element.disabled),
		maxLength: Number(element.maxLength),
		visible: element.getClientRects().length > 0
	})`, nil)
	if err != nil {
		return wordstatInputState{}, fmt.Errorf("read Wordstat textarea state: %w", err)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return wordstatInputState{}, fmt.Errorf("encode Wordstat textarea state: %w", err)
	}
	var dom struct {
		Value     string `json:"value"`
		ReadOnly  bool   `json:"readOnly"`
		Disabled  bool   `json:"disabled"`
		MaxLength int    `json:"maxLength"`
		Visible   bool   `json:"visible"`
	}
	if err := json.Unmarshal(encoded, &dom); err != nil {
		return wordstatInputState{}, fmt.Errorf("decode Wordstat textarea state: %w", err)
	}
	return wordstatInputState{
		Selector: keysSelector, QueriesCount: len(queries),
		ExpectedLength: len([]rune(expected)), ExpectedLines: countLines(expected),
		ExpectedFingerprint: fingerprint(expected),
		DOMLength:           len([]rune(dom.Value)), DOMLines: countLines(dom.Value),
		DOMFingerprint: fingerprint(dom.Value),
		Visible:        dom.Visible, Enabled: !dom.Disabled, ReadOnly: dom.ReadOnly, MaxLength: dom.MaxLength,
	}, nil
}

// submitOutcome is the observable proof of what the click actually did.
type submitOutcome struct {
	RequestStarted bool `json:"post_request_started"`
	// Acknowledged — страница сама подтвердила отправку: beforeSend заблокировал кнопку
	// или очистил #container. Признак из DOM, независимый от сетевого наблюдения.
	Acknowledged   bool     `json:"submit_acknowledged"`
	RequestURL     string   `json:"post_request_url,omitempty"`
	ResponseStatus int      `json:"post_response_status,omitempty"`
	ResponseBytes  int      `json:"post_response_bytes,omitempty"`
	ButtonDisabled bool     `json:"button_disabled_after_click"`
	ButtonText     string   `json:"button_text_after_click"`
	ContainerText  string   `json:"container_sample"`
	PageErrors     []string `json:"page_errors,omitempty"`
	ConsoleErrors  []string `json:"console_errors,omitempty"`
	Dialogs        []string `json:"dialogs,omitempty"`
	Verdict        string   `json:"verdict"`
	ClickErr       error    `json:"-"`
}

func (o submitOutcome) fields() []any {
	return []any{
		"verdict", o.Verdict, "post_request_started", o.RequestStarted, "submit_acknowledged", o.Acknowledged,
		"post_response_status", o.ResponseStatus, "post_response_bytes", o.ResponseBytes,
		"button_disabled", o.ButtonDisabled, "button_text", o.ButtonText,
		"page_error_count", len(o.PageErrors), "console_error_count", len(o.ConsoleErrors),
		"dialog_count", len(o.Dialogs),
	}
}

const (
	submitVerdictNotStarted = "post_not_started"
	submitVerdictRejected   = "post_rejected"
	submitVerdictAccepted   = "post_accepted"
)

// classifySubmit turns the observations into the three outcomes worth telling apart.
func classifySubmit(outcome submitOutcome) string {
	// Сеть и DOM подтверждают отправку независимо; хватает любого из двух признаков.
	if !outcome.RequestStarted && !outcome.Acknowledged {
		return submitVerdictNotStarted
	}
	if outcome.ResponseStatus != 0 && (outcome.ResponseStatus < 200 || outcome.ResponseStatus >= 300) {
		return submitVerdictRejected
	}
	return submitVerdictAccepted
}

// submitWordstat clicks the start button and records what the page did in response.
//
// Формы на странице нет: кнопку обслуживает обработчик, который сам собирает поля и шлёт
// POST. Поэтому успешный клик ничего не доказывает, а доказывает — сам запрос, блокировка
// кнопки в beforeSend и содержимое #container, куда страница кладёт ответ.
func (s *Service) submitWordstat(ctx context.Context, button playwright.Locator) submitOutcome {
	var (
		mutex   sync.Mutex
		outcome submitOutcome
	)
	s.page.OnPageError(func(err error) {
		mutex.Lock()
		defer mutex.Unlock()
		outcome.PageErrors = append(outcome.PageErrors, safeDiagnosticError(err))
	})
	s.page.OnConsole(func(message playwright.ConsoleMessage) {
		if message.Type() != "error" {
			return
		}
		mutex.Lock()
		defer mutex.Unlock()
		outcome.ConsoleErrors = append(outcome.ConsoleErrors, truncateRunes(message.Text(), diagnosticSampleRunes))
	})
	s.page.OnDialog(func(dialog playwright.Dialog) {
		mutex.Lock()
		outcome.Dialogs = append(outcome.Dialogs,
			truncateRunes(dialog.Type()+": "+dialog.Message(), diagnosticSampleRunes))
		mutex.Unlock()
		// С зарегистрированным обработчиком Playwright больше не закрывает диалоги сам.
		_ = dialog.Dismiss()
	})
	s.page.OnRequest(func(request playwright.Request) {
		if !isWordstatSubmitRequest(request.Method(), request.URL()) {
			return
		}
		mutex.Lock()
		defer mutex.Unlock()
		outcome.RequestStarted = true
		outcome.RequestURL = request.URL()
	})
	s.page.OnResponse(func(response playwright.Response) {
		if !isWordstatSubmitRequest(response.Request().Method(), response.URL()) {
			return
		}
		mutex.Lock()
		defer mutex.Unlock()
		outcome.ResponseStatus = response.Status()
		outcome.ResponseBytes = responseSize(response)
	})

	if outcome.ClickErr = button.Click(); outcome.ClickErr != nil {
		return outcome
	}
	// Наблюдаемый признак того, что обработчик страницы дошёл до отправки: beforeSend
	// блокирует кнопку и очищает #container перед самим запросом. Неудача этого ожидания —
	// такое же свидетельство, как и удача, поэтому ошибку только записываем.
	outcome.Acknowledged = s.waitSubmitAcknowledged() == nil

	if state, err := s.readSubmitPageState(); err != nil {
		s.logCtx(ctx, slog.LevelWarn, "не удалось прочитать состояние страницы после клика", "wordstat_start", "error", err)
	} else {
		mutex.Lock()
		outcome.ButtonDisabled, outcome.ButtonText, outcome.ContainerText = state.disabled, state.text, state.container
		mutex.Unlock()
	}

	mutex.Lock()
	defer mutex.Unlock()
	outcome.Verdict = classifySubmit(outcome)
	return outcome
}

// waitSubmitAcknowledged waits for the page to admit it is sending the request.
func (s *Service) waitSubmitAcknowledged() error {
	_, err := s.page.WaitForFunction(`() => {
		const button = document.querySelector('button#ok');
		const container = document.querySelector('#container');
		return Boolean(button && button.disabled) ||
			Boolean(container && (container.innerText || '').trim().length > 0);
	}`, nil, playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(submitObservationWindow)})
	return err
}

type submitPageState struct {
	disabled  bool
	text      string
	container string
}

func (s *Service) readSubmitPageState() (submitPageState, error) {
	raw, err := s.page.Evaluate(`() => {
		const button = document.querySelector('button#ok');
		const container = document.querySelector('#container');
		return {
			disabled: button ? Boolean(button.disabled) : false,
			text: button ? (button.textContent || '').trim() : '',
			container: container ? (container.innerText || '').trim() : ''
		};
	}`)
	if err != nil {
		return submitPageState{}, fmt.Errorf("read Wordstat submit state: %w", err)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return submitPageState{}, fmt.Errorf("encode Wordstat submit state: %w", err)
	}
	var state struct {
		Disabled  bool   `json:"disabled"`
		Text      string `json:"text"`
		Container string `json:"container"`
	}
	if err := json.Unmarshal(encoded, &state); err != nil {
		return submitPageState{}, fmt.Errorf("decode Wordstat submit state: %w", err)
	}
	return submitPageState{
		disabled:  state.Disabled,
		text:      truncateRunes(state.Text, diagnosticSampleRunes),
		container: truncateRunes(state.Container, containerSampleRunes),
	}, nil
}

// isWordstatSubmitRequest recognizes the POST the page sends instead of a form submit.
func isWordstatSubmitRequest(method, requestURL string) bool {
	return strings.EqualFold(method, "POST") && strings.Contains(requestURL, "/tools/wordstat/")
}

func responseSize(response playwright.Response) int {
	value, found := response.Headers()["content-length"]
	if !found {
		return -1
	}
	size, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return -1
	}
	return size
}

// fingerprint identifies a text without disclosing it.
func fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}

func countLines(value string) int {
	if value == "" {
		return 0
	}
	return strings.Count(value, "\n") + 1
}

func truncateRunes(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}

// wordstatTaskList observes the Arsenkin task list. The browser sits behind function values,
// so the rules of waiting can be checked without a page.
type wordstatTaskList struct {
	// waitRendered blocks until the task list of the account is drawn in the document.
	waitRendered func(timeout time.Duration) error
	// taskIDs returns every task identifier currently rendered.
	taskIDs func() ([]string, error)
	// reload re-renders the list: the page fills it on load only.
	reload func() error
	now    func() time.Time
}

// confirmWordstatTaskCreated proves that Arsenkin accepted the queries and opened a task.
//
// Без этой проверки отказ приёма неотличим от медленного расчёта: страница остаётся на том
// же адресе, прогресс не появляется, и прогон молча выбирает весь бюджет ожидания
// результата. Теперь несозданная задача — быстрая ошибка с диагностикой страницы.
func (s *Service) confirmWordstatTaskCreated(ctx context.Context, knownTaskIDs []string, submitted int) (string, error) {
	taskID, err := waitWordstatTaskCreated(ctx, knownTaskIDs, wordstatTaskList{
		waitRendered: s.waitWordstatHistoryRendered,
		taskIDs:      s.wordstatTaskIDs,
		reload:       func() error { return s.reloadWordstatHistory(ctx) },
		now:          time.Now,
	}, func(attempt int, remaining time.Duration) {
		s.logCtx(ctx, slog.LevelDebug, "подтверждение приёма запросов Wordstat", "wordstat_start",
			"attempt", attempt, "known_task_count", len(knownTaskIDs), "remaining_ms", remaining.Milliseconds())
	})
	if err != nil {
		s.saveDebugArtifacts(ctx, "wordstat_start", err, debugState{KnownTaskIDs: knownTaskIDs, SubmittedCount: submitted})
		return "", err
	}
	return taskID, nil
}

// waitWordstatTaskCreated polls the task list until exactly one identifier outside known
// appears. Перезагрузка между попытками обязательна: список отрисовывается при загрузке
// страницы, поэтому в уже открытом документе новая задача не появится никогда.
//
// Порядок внутри попытки — «дождаться отрисовки, потом читать, и только потом
// перезагружать». Список приходит отдельным XHR, и перезагрузка раньше срока обрывала его:
// каждая попытка видела пустую страницу, а этап заканчивался выводом «задача не создана»,
// хотя список ни разу не был прочитан отрисованным.
func waitWordstatTaskCreated(ctx context.Context, known []string, list wordstatTaskList, onAttempt func(int, time.Duration)) (string, error) {
	deadline := list.now().Add(wordstatStartTimeout * time.Millisecond)
	lastVisible, renderedAtLeastOnce := 0, false
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		remaining := deadline.Sub(list.now())
		if remaining <= 0 {
			if !renderedAtLeastOnce {
				return "", fmt.Errorf(
					"список задач Wordstat ни разу не отрисовался за %s (задач до запуска: %d, попыток: %d): "+
						"подтвердить или опровергнуть приём запросов невозможно",
					wordstatStartTimeout*time.Millisecond, len(known), attempt-1,
				)
			}
			return "", fmt.Errorf(
				"%w за %s: Arsenkin не принял запросы, отрисованный список задач не изменился "+
					"(задач до запуска: %d, в последнем списке: %d, попыток: %d)",
				errWordstatTaskNotCreated, wordstatStartTimeout*time.Millisecond, len(known), lastVisible, attempt-1,
			)
		}
		if onAttempt != nil {
			onAttempt(attempt, remaining)
		}
		// Бюджет отрисовки — тот же, что и у снимка до запуска: там он уже доказал, что
		// списка достаточно дождаться, а не угадывать шаг опроса.
		rendered := list.waitRendered(min(wordstatHistoryTimeout*time.Millisecond, remaining)) == nil
		visible, readErr := list.taskIDs()
		if readErr != nil {
			return "", readErr
		}
		if rendered || len(visible) > 0 {
			renderedAtLeastOnce = true
			lastVisible = len(visible)
			taskID, selectErr := selectNewWordstatTask(known, visible)
			// «Пока не создана» — повод перезагрузить список, а не ответ. Всё остальное,
			// включая несколько новых задач сразу, повтором не лечится.
			if selectErr == nil || !errors.Is(selectErr, errWordstatTaskNotCreated) {
				return taskID, selectErr
			}
		}
		if err := list.reload(); err != nil {
			return "", err
		}
	}
}

// waitWordstatTaskCompleted waits for the task created by this run to become downloadable.
//
// Список задач Arsenkin отрисовывается при загрузке страницы: завершение задачи в уже
// открытом документе не появляется само. Поэтому ждём короткими интервалами и между ними
// перезагружаем страницу. Раньше этого было не видно, потому что подходила любая
// завершённая строка — в том числе задача предыдущей статьи.
func (s *Service) waitWordstatTaskCompleted(ctx context.Context, taskID string) error {
	deadline := time.Now().Add(wordstatTimeout * time.Millisecond)
	var progress wordstatProgressReporter
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			err := fmt.Errorf(
				"задача Wordstat task_id=%s не завершилась за %s: результат предыдущей задачи не принимается (перезагрузок страницы: %d)",
				taskID, wordstatTimeout*time.Millisecond, attempt-1,
			)
			s.saveDebugArtifacts(ctx, "wait_download", err, debugState{AwaitedTaskID: taskID})
			return err
		}
		wait := min(wordstatPollInterval*time.Millisecond, remaining)
		s.log(slog.LevelDebug, "ожидание файла результата Wordstat", "wait_download",
			"attempt", attempt, "task_id", taskID, "remaining_ms", remaining.Milliseconds())
		// Идентификатор сравнивается как значение атрибута, а не подставляется в селектор:
		// склейка строк сломалась бы на любом неожиданном символе в task_id.
		_, err := s.page.WaitForFunction(`taskID => Array.from(
			document.querySelectorAll('.arshis__row--body[data-task-id]')
		).some(row => row.getAttribute('data-task-id') === taskID &&
			row.querySelector('.arshis__status--done') &&
			row.querySelector('a[href*="/tools/download/23/csv/"][href*="encode=xls"]'))`,
			taskID, playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(float64(wait.Milliseconds()))})
		if err == nil {
			return nil
		}
		if current, progressErr := s.currentProgress(); progressErr == nil && current > 0 {
			for _, threshold := range progress.crossed(current) {
				s.log(slog.LevelInfo, fmt.Sprintf("progress %d", threshold), "wordstat_progress", "task_id", taskID)
			}
		}
		if reloadErr := s.reloadWordstatHistory(ctx); reloadErr != nil {
			return reloadErr
		}
	}
}

// reloadWordstatHistory re-renders the task list, the only way to see a task finish.
func (s *Service) reloadWordstatHistory(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.log(slog.LevelDebug, "перезагрузка списка задач Wordstat", "wait_download")
	if _, err := s.page.Reload(playwright.PageReloadOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(operationTimeout),
	}); err != nil {
		return fmt.Errorf("перезагрузить список задач Wordstat: %w", err)
	}
	if isLoginURL(s.currentURL()) {
		return fmt.Errorf("Arsenkin session expired while waiting for the Wordstat result")
	}
	return nil
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
	// Copywriters, в отличие от Wordstat, восстанавливает на странице последнюю
	// завершённую задачу аккаунта. Запоминаем её до запуска и дальше ждём строго
	// другой task_id — так же, как Wordstat ждёт task_id вне knownTaskIDs.
	previousTask, err := s.copywritersTask()
	if err != nil {
		return Result{}, err
	}
	s.log(slog.LevelInfo, "состояние Copywriters до запуска", "copywriters_start",
		"previous_task_id", previousTask.ID, "previous_structure_length", len([]rune(previousTask.Structure)))
	if err := input.Fill(strings.Join(copywriterQueries, "\n")); err != nil {
		return Result{}, fmt.Errorf("fill Copywriters Top-50: %w", err)
	}
	if err := button.Click(); err != nil {
		return Result{}, fmt.Errorf("start Copywriters: %w", err)
	}
	s.log(slog.LevelInfo, "Copywriters стартовал", "copywriters_start", "queries_count", len(copywriterQueries))

	for _, threshold := range []int{25, 50, 75} {
		if err := s.waitCopywritersProgressOrResult(ctx, threshold, previousTask.ID); err != nil {
			return Result{}, err
		}
		progress, err := s.copywritersProgress(previousTask.ID)
		if err != nil {
			return Result{}, err
		}
		if progress >= threshold {
			s.log(slog.LevelInfo, fmt.Sprintf("Copywriters progress %d", threshold), "copywriters_progress")
		}
	}
	if err := s.waitCopywritersResult(ctx, previousTask.ID); err != nil {
		return Result{}, err
	}
	s.log(slog.LevelInfo, "Copywriters progress 100", "copywriters_progress")

	currentTask, err := s.copywritersTask()
	if err != nil {
		return Result{}, err
	}
	s.log(slog.LevelInfo, "состояние Copywriters после ожидания", "copywriters_result",
		"previous_task_id", previousTask.ID, "task_id", currentTask.ID,
		"structure_length", len([]rune(currentTask.Structure)))
	if err := acceptCopywritersResult(previousTask, currentTask); err != nil {
		return Result{}, err
	}

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

// copywritersTask identifies the task currently shown on the Copywriters page.
type copywritersTask struct {
	ID        string
	Theme     string
	Structure string
}

// acceptCopywritersResult verifies the page shows the task started for this very article.
// The result is rejected when the task identifier did not change, when it is missing, and —
// as a fallback for a page without an identifier — when the payload equals the previous one.
func acceptCopywritersResult(previous, current copywritersTask) error {
	if strings.TrimSpace(current.ID) == "" {
		return fmt.Errorf(
			"Copywriters не выдал task_id: подтвердить, что результат принадлежит этой статье, невозможно",
		)
	}
	if current.ID == previous.ID {
		return fmt.Errorf(
			"Copywriters вернул результат предыдущей задачи: task_id=%q не изменился после запуска; данные принадлежат другой статье",
			current.ID,
		)
	}
	if previous.ID == "" && previous.Structure != "" &&
		previous.Theme == current.Theme && previous.Structure == current.Structure {
		return fmt.Errorf(
			"Copywriters вернул неизменённый результат предыдущей задачи: тематические слова и структура совпадают с состоянием до запуска",
		)
	}
	return nil
}

func (s *Service) copywritersTask() (copywritersTask, error) {
	raw, err := s.page.Locator("body").Evaluate(`body => ({
		id: (body.querySelector('input[name="task_id"]')?.value || '').trim(),
		theme: (body.querySelector('textarea[name="theme"]')?.value || '').trim(),
		structure: (body.querySelector('#structure .modal-body')?.innerText || '').trim()
	})`, nil)
	if err != nil {
		return copywritersTask{}, fmt.Errorf("read Copywriters task state: %w", err)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return copywritersTask{}, fmt.Errorf("encode Copywriters task state: %w", err)
	}
	var state struct {
		ID        string `json:"id"`
		Theme     string `json:"theme"`
		Structure string `json:"structure"`
	}
	if err := json.Unmarshal(encoded, &state); err != nil {
		return copywritersTask{}, fmt.Errorf("decode Copywriters task state: %w", err)
	}
	return copywritersTask{ID: state.ID, Theme: state.Theme, Structure: state.Structure}, nil
}

func (s *Service) waitCopywritersProgressOrResult(ctx context.Context, threshold int, previousTaskID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.log(slog.LevelDebug, "ожидание прогресса Copywriters", "wait_copywriters_progress", "threshold", threshold, "previous_task_id", previousTaskID)
	_, err := s.page.WaitForFunction(`args => {
		const match = (document.body?.innerText || '').match(/Прогресс\s*:\s*(\d+)\s*%/i);
		const progress = match ? Number(match[1]) : 0;
		const taskID = (document.querySelector('input[name="task_id"]')?.value || '').trim();
		const theme = (document.querySelector('textarea[name="theme"]')?.value || '').trim();
		const structureLink = document.querySelector('a[data-target="#structure"][data-toggle="modal"]');
		const structure = (document.querySelector('#structure .modal-body')?.innerText || '').trim();
		const fresh = taskID.length > 0 && taskID !== args.previousTaskID;
		const complete = fresh && theme.length > 0 && structureLink && structure.length > 0 && !match;
		return progress >= args.threshold || complete;
	}`, map[string]any{"threshold": threshold, "previousTaskID": previousTaskID},
		playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(copywritersTimeout)})
	if err != nil {
		return fmt.Errorf("wait for Copywriters progress %d%%: %w", threshold, err)
	}
	return nil
}

func (s *Service) copywritersProgress(previousTaskID string) (int, error) {
	value, err := s.page.Locator("body").Evaluate(`(body, previousTaskID) => {
		const match = (body.innerText || '').match(/Прогресс\s*:\s*(\d+)\s*%/i);
		if (match) return match[1];
		const taskID = (body.querySelector('input[name="task_id"]')?.value || '').trim();
		const ready = taskID.length > 0 && taskID !== previousTaskID &&
			(body.querySelector('textarea[name="theme"]')?.value || '').trim() &&
			body.querySelector('a[data-target="#structure"][data-toggle="modal"]') &&
			(body.querySelector('#structure .modal-body')?.innerText || '').trim();
		return ready ? '100' : '0';
	}`, previousTaskID)
	if err != nil {
		return 0, fmt.Errorf("read Copywriters progress: %w", err)
	}
	progress, err := strconv.Atoi(fmt.Sprint(value))
	if err != nil {
		return 0, fmt.Errorf("parse Copywriters progress %q: %w", value, err)
	}
	return progress, nil
}

func (s *Service) waitCopywritersResult(ctx context.Context, previousTaskID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.log(slog.LevelDebug, "ожидание результата Copywriters", "wait_copywriters_result", "previous_task_id", previousTaskID)
	_, err := s.page.WaitForFunction(`previousTaskID => {
		const taskID = (document.querySelector('input[name="task_id"]')?.value || '').trim();
		const theme = (document.querySelector('textarea[name="theme"]')?.value || '').trim();
		const structureLink = document.querySelector('a[data-target="#structure"][data-toggle="modal"]');
		const structure = (document.querySelector('#structure .modal-body')?.innerText || '').trim();
		const match = (document.body?.innerText || '').match(/Прогресс\s*:\s*(\d+)\s*%/i);
		return taskID.length > 0 && taskID !== previousTaskID && theme.length > 0 &&
			structureLink && structure.length > 0 && (!match || Number(match[1]) >= 100);
	}`, previousTaskID, playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(copywritersTimeout)})
	if err != nil {
		return fmt.Errorf(
			"не удалось дождаться новой задачи Copywriters (task_id до запуска %q): результат предыдущей задачи не принимается: %w",
			previousTaskID, err,
		)
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

// wordstatProgressThresholds — ступени, о которых сообщается по ходу ожидания Wordstat.
var wordstatProgressThresholds = []int{25, 50, 75}

// wordstatProgressReporter reports each progress threshold once, as it is crossed.
//
// Ждать по-прежнему нечего, кроме готовой задачи в списке: именно доверие к прогресс-бару
// давало зависание, когда задачи не существовало вовсе. Проценты остаются отчётностью —
// по ним видно, что задача жива, но исход этапа они не решают. Поэтому пропущенная ступень
// не откатывается: прогресс, перескочивший с 20 сразу на 80, сообщает 25, 50 и 75 подряд.
type wordstatProgressReporter struct {
	reported int
}

func (r *wordstatProgressReporter) crossed(progress int) []int {
	var crossed []int
	for _, threshold := range wordstatProgressThresholds {
		if threshold > r.reported && progress >= threshold {
			crossed = append(crossed, threshold)
			r.reported = threshold
		}
	}
	return crossed
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

// SubmittedQueries returns exactly the phrases CollectResearch will type into the Wordstat
// form: the normalized list cut to the form limit.
//
// Диагностике нужен именно этот набор. Считая «отправленным» весь cleaned_keywords, отчёт
// называл отправленным и то, что осталось за лимитом, а проверка происхождения сверяла
// ответ Wordstat с фразами, которых он не получал.
func SubmittedQueries(queries []string) []string {
	return limitWordstatQueries(normalizeInputQueries(queries))
}

// limitWordstatQueries keeps the first maxWordstatQueries phrases in their original order.
// Обрезка идёт после нормализации и до сборки текста для textarea, поэтому отпечаток
// поля считается уже по отправляемому списку.
func limitWordstatQueries(queries []string) []string {
	if len(queries) <= maxWordstatQueries {
		return queries
	}
	return queries[:maxWordstatQueries]
}

// normalizeInputQueries приводит список к тому виду, который принимает форма Wordstat: без
// лишних символов, без пустых строк и без повторов.
func normalizeInputQueries(queries []string) []string {
	result := make([]string, 0, len(queries))
	seen := make(map[string]struct{}, len(queries))
	for _, query := range queries {
		query = sanitizeWordstatQuery(query)
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

// sanitizeWordstatQuery оставляет во фразе только буквы, цифры и одиночные пробелы.
//
// Чистка живёт здесь, а не у источника запросов: требование ставит сама форма Wordstat, а
// источников у списка три — сбор у конкурента, ручная вставка и резервный подбор моделью.
// Ловушка у всех одна и молчаливая: операторные символы («-», «/», «"», «!») форма
// принимает, а задачу по ним не создаёт вовсе, и прогон встаёт без объяснения. Очистка
// Keys.so её не снимает: форма delete-double убирает дубли, а символы не трогает.
//
// Лишний символ заменяется пробелом, а не выбрасывается: «seo-продвижение» обязано остаться
// двумя словами, иначе чистка сама превращает фразу в мусор. Дубли, которые она создаёт
// («курсы/охрана» рядом с «курсы охрана»), снимает отбор повторов выше — он идёт после неё.
func sanitizeWordstatQuery(query string) string {
	var cleaned strings.Builder
	cleaned.Grow(len(query))
	for _, symbol := range query {
		if unicode.IsLetter(symbol) || unicode.IsDigit(symbol) {
			cleaned.WriteRune(symbol)
			continue
		}
		cleaned.WriteRune(' ')
	}
	return strings.Join(strings.Fields(cleaned.String()), " ")
}

// sanitizedQueries сообщает, сколько фраз пришлось чистить, и показывает несколько примеров.
//
// Молчаливая чистка опасна тем же, чем молчаливый отказ формы: отправленное расходится с
// сохранёнными cleaned_keywords, и по логу прогона это должно быть видно. Схлопывание
// повторных пробелов не считается: оно ничего не меняет по смыслу.
func sanitizedQueries(queries []string) (int, []string) {
	count := 0
	samples := make([]string, 0, wordstatSanitizeSample)
	for _, query := range queries {
		if sanitizeWordstatQuery(query) == strings.Join(strings.Fields(query), " ") {
			continue
		}
		count++
		if len(samples) < wordstatSanitizeSample {
			samples = append(samples, query)
		}
	}
	return count, samples
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

// debugInfo describes the page state at the moment an Arsenkin stage failed.
// Ни cookies, ни заголовки авторизации, ни сами запросы сюда не попадают.
type debugInfo struct {
	ArticleID      int64     `json:"article_id"`
	Stage          string    `json:"stage"`
	Operation      string    `json:"operation"`
	URL            string    `json:"url"`
	Title          string    `json:"title"`
	ReadyState     string    `json:"ready_state"`
	Timestamp      time.Time `json:"timestamp"`
	Error          string    `json:"error"`
	KnownTaskIDs   []string  `json:"known_task_ids,omitempty"`
	PageTaskIDs    []string  `json:"page_task_ids,omitempty"`
	AwaitedTaskID  string    `json:"awaited_task_id,omitempty"`
	SubmittedCount int       `json:"submitted_count,omitempty"`
	// Submit отвечает на вопрос, который иначе остаётся открытым при любом отказе ниже:
	// ушёл ли POST вообще и что на него ответили.
	Submit *submitOutcome `json:"submit,omitempty"`
}

// debugState — то, что известно о запуске на момент отказа. Сами запросы сюда не попадают:
// для разбора хватает их количества.
type debugState struct {
	KnownTaskIDs   []string
	AwaitedTaskID  string
	SubmittedCount int
}

// formFragmentSelectors ограничивает выгружаемый DOM формой запросов и контейнером ответа:
// весь документ для разбора отправки не нужен, а лишние 150 КБ на попытку — нужны ещё меньше.
var formFragmentSelectors = []string{"#div-queries", "#container"}

// saveStageSnapshot записывает лёгкий снимок стадии: скриншот, состояние и фрагмент формы.
// В отличие от saveDebugArtifacts, вызывается и на успешном пути, поэтому не тянет за собой
// полный HTML страницы.
func (s *Service) saveStageSnapshot(ctx context.Context, stage string, payload any, failure error) {
	if s.page == nil {
		s.logCtx(ctx, slog.LevelWarn, "снимок стадии Arsenkin не сохранён: страница недоступна", stage)
		return
	}
	directory, err := s.prepareDebugDirectory(ctx, stage)
	if err != nil {
		return
	}
	s.writeScreenshot(ctx, directory, stage)
	s.writeFormFragment(ctx, directory, stage)
	snapshot := struct {
		ArticleID int64     `json:"article_id"`
		Stage     string    `json:"stage"`
		Operation string    `json:"operation"`
		URL       string    `json:"url"`
		Timestamp time.Time `json:"timestamp"`
		Error     string    `json:"error,omitempty"`
		State     any       `json:"state"`
	}{
		ArticleID: s.cfg.ArticleID, Stage: stage, Operation: "prepare", URL: s.currentURL(),
		Timestamp: time.Now(), Error: safeDiagnosticError(failure), State: payload,
	}
	s.writeJSON(ctx, directory, "info.json", stage, snapshot)
	s.logCtx(ctx, slog.LevelInfo, "снимок стадии Arsenkin сохранён", stage, "debug_path", directory)
}

func (s *Service) prepareDebugDirectory(ctx context.Context, stage string) (string, error) {
	directory := filepath.Join(
		s.debugArtifactsRoot(),
		fmt.Sprintf("article-%d", s.cfg.ArticleID),
		fmt.Sprintf("%s-%s", time.Now().Format("20060102-150405.000000000"), stage),
	)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		s.logCtx(ctx, slog.LevelWarn, "не удалось создать каталог диагностики Arsenkin", stage,
			"debug_path", directory, "error", err)
		return "", err
	}
	return directory, nil
}

func (s *Service) writeScreenshot(ctx context.Context, directory, stage string) {
	path := filepath.Join(directory, "screenshot.png")
	sensitiveFields := s.page.Locator(`input[type="password"], input[name="email"], input[type="hidden"]`)
	if _, err := s.page.Screenshot(playwright.PageScreenshotOptions{
		Path: playwright.String(path), FullPage: playwright.Bool(true), Mask: []playwright.Locator{sensitiveFields},
	}); err != nil {
		s.logCtx(ctx, slog.LevelWarn, "не удалось сохранить screenshot Arsenkin", stage, "debug_path", path, "error", err)
	}
}

func (s *Service) writeFormFragment(ctx context.Context, directory, stage string) {
	raw, err := s.page.Evaluate(`selectors => selectors
		.map(selector => {
			const element = document.querySelector(selector);
			return element ? '<!-- ' + selector + ' -->\n' + element.outerHTML : '<!-- ' + selector + ': not found -->';
		})
		.join('\n\n')`, formFragmentSelectors)
	if err != nil {
		s.logCtx(ctx, slog.LevelWarn, "не удалось получить фрагмент формы Arsenkin", stage, "error", err)
		return
	}
	fragment := redactDiagnosticHTML(fmt.Sprint(raw), s.cfg.Email, s.cfg.Password)
	if err := os.WriteFile(filepath.Join(directory, "form.html"), []byte(fragment), 0o600); err != nil {
		s.logCtx(ctx, slog.LevelWarn, "не удалось сохранить фрагмент формы Arsenkin", stage,
			"debug_path", directory, "error", err)
	}
}

func (s *Service) writeJSON(ctx context.Context, directory, name, stage string, payload any) {
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		s.logCtx(ctx, slog.LevelWarn, "не удалось сформировать "+name+" Arsenkin", stage, "error", err)
		return
	}
	if err := os.WriteFile(filepath.Join(directory, name), encoded, 0o600); err != nil {
		s.logCtx(ctx, slog.LevelWarn, "не удалось сохранить "+name+" Arsenkin", stage,
			"debug_path", directory, "error", err)
	}
}

// saveDebugArtifacts stores the screenshot, HTML and page state of a failed Arsenkin stage,
// the same way Keys.so does. Без этого отказ приёма запросов остаётся одной строкой лога.
func (s *Service) saveDebugArtifacts(ctx context.Context, stage string, failure error, state debugState) {
	if s.page == nil {
		s.logCtx(ctx, slog.LevelWarn, "диагностика Arsenkin не сохранена: страница недоступна", stage)
		return
	}
	timestamp := time.Now()
	directory := filepath.Join(
		s.debugArtifactsRoot(),
		fmt.Sprintf("article-%d", s.cfg.ArticleID),
		fmt.Sprintf("%s-%s", timestamp.Format("20060102-150405.000000000"), stage),
	)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		s.logCtx(ctx, slog.LevelWarn, "не удалось создать каталог диагностики Arsenkin", stage, "debug_path", directory, "error", err)
		return
	}

	screenshotPath := filepath.Join(directory, "screenshot.png")
	sensitiveFields := s.page.Locator(`input[type="password"], input[name="email"], input[type="hidden"]`)
	if _, err := s.page.Screenshot(playwright.PageScreenshotOptions{
		Path: playwright.String(screenshotPath), FullPage: playwright.Bool(true), Mask: []playwright.Locator{sensitiveFields},
	}); err != nil {
		s.logCtx(ctx, slog.LevelWarn, "не удалось сохранить screenshot Arsenkin", stage, "debug_path", screenshotPath, "error", err)
	}

	html, htmlErr := s.page.Content()
	if htmlErr != nil {
		s.logCtx(ctx, slog.LevelWarn, "не удалось получить HTML Arsenkin", stage, "debug_path", directory, "error", htmlErr)
	} else if err := os.WriteFile(filepath.Join(directory, "page.html"),
		[]byte(redactDiagnosticHTML(html, s.cfg.Email, s.cfg.Password)), 0o600); err != nil {
		s.logCtx(ctx, slog.LevelWarn, "не удалось сохранить HTML Arsenkin", stage, "debug_path", directory, "error", err)
	}

	title, titleErr := s.page.Title()
	if titleErr != nil {
		s.logCtx(ctx, slog.LevelWarn, "не удалось получить title для диагностики Arsenkin", stage, "error", titleErr)
	}
	readyState := "<unavailable>"
	if value, evaluateErr := s.page.Evaluate(`() => document.readyState`); evaluateErr != nil {
		s.logCtx(ctx, slog.LevelWarn, "не удалось получить readyState для диагностики Arsenkin", stage, "error", evaluateErr)
	} else if state, ok := value.(string); ok {
		readyState = state
	}
	pageTaskIDs, taskIDsErr := s.wordstatTaskIDs()
	if taskIDsErr != nil {
		s.logCtx(ctx, slog.LevelWarn, "не удалось прочитать задачи Wordstat для диагностики", stage, "error", taskIDsErr)
	}
	info := debugInfo{
		ArticleID: s.cfg.ArticleID, Stage: stage, Operation: "prepare",
		URL: s.currentURL(), Title: title, ReadyState: readyState,
		Timestamp: timestamp, Error: safeDiagnosticError(failure),
		KnownTaskIDs: state.KnownTaskIDs, PageTaskIDs: pageTaskIDs,
		AwaitedTaskID: state.AwaitedTaskID, SubmittedCount: state.SubmittedCount,
		Submit: s.lastSubmit,
	}
	encoded, encodeErr := json.MarshalIndent(info, "", "  ")
	if encodeErr != nil {
		s.logCtx(ctx, slog.LevelWarn, "не удалось сформировать info.json Arsenkin", stage, "error", encodeErr)
	} else if err := os.WriteFile(filepath.Join(directory, "info.json"), encoded, 0o600); err != nil {
		s.logCtx(ctx, slog.LevelWarn, "не удалось сохранить info.json Arsenkin", stage, "debug_path", directory, "error", err)
	}
	s.logCtx(ctx, slog.LevelInfo, "диагностика Arsenkin сохранена", stage, "debug_path", directory)
}

func redactDiagnosticHTML(html string, secrets ...string) string {
	for _, secret := range secrets {
		if strings.TrimSpace(secret) != "" {
			html = strings.ReplaceAll(html, secret, "[REDACTED]")
		}
	}
	return html
}

func safeDiagnosticError(err error) string {
	if err == nil {
		return ""
	}
	const maxRunes = 2000
	runes := []rune(strings.TrimSpace(err.Error()))
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	return string(runes)
}

func (s *Service) stageError(stage string, err error) error {
	return &StageError{ArticleID: s.cfg.ArticleID, Stage: stage, CurrentURL: s.currentURL(), Duration: time.Since(s.startedAt), Err: err}
}

func (s *Service) log(level slog.Level, message, stage string, fields ...any) {
	s.logCtx(context.Background(), level, message, stage, fields...)
}

// logCtx пишет журнал с живым контекстом. Общий log() до сих пор подставляет
// context.Background() — это отдельный пункт бэклога аудита, и тянуть его целиком сюда
// незачем; новый код передаёт ctx, как требуют правила интеграций.
func (s *Service) logCtx(ctx context.Context, level slog.Level, message, stage string, fields ...any) {
	attributes := []any{"stage", stage, "current_url", s.currentURL(), "duration_ms", time.Since(s.startedAt).Milliseconds()}
	s.logger.Log(ctx, level, message, append(attributes, fields...)...)
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
