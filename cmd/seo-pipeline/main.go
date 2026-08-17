package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/integrations/arsenkin"
	"github.com/foxylis237/seo-pipeline/internal/integrations/google"
	"github.com/foxylis237/seo-pipeline/internal/storage"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/demo"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/diagnostics"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/generation"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/tasks/task1/output"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/repository"
	resultassembly "github.com/foxylis237/seo-pipeline/internal/tasks/task1/result"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	command, err := parseCommand(os.Args)
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
	if command.Name == "google-login" {
		// Ручной вход не трогает ни базу, ни конфигурацию задачи: человеку нужен только
		// браузер, поэтому команда обрабатывается до подключения к PostgreSQL.
		if err := google.Login(ctx, googleConfig(false), logger); err != nil {
			logger.Error("вход в Google не выполнен", "error", err)
			os.Exit(1)
		}
		return
	}
	if command.Name == "deepseek-login" {
		if err := runDeepSeekLogin(ctx, logger); err != nil {
			logger.Error("DeepSeek login failed", "error", err)
			os.Exit(1)
		}
		logger.Info("DeepSeek login completed")
		return
	}

	var cfg config.Config
	if command.DryRun {
		cfg, err = config.LoadDryRun()
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		logger.Error("не удалось загрузить конфигурацию", "error", err)
		os.Exit(1)
	}
	logger, err = newLogger(cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
		logger.Error("не удалось настроить логирование", "error", err)
		os.Exit(1)
	}
	if command.DryRun {
		err = cfg.ValidateDryRun()
	} else {
		err = validateConfig(command.Name, cfg)
	}
	if err != nil {
		logger.Error("не удалось загрузить конфигурацию", "error", err)
		os.Exit(1)
	}

	pool, err := storage.NewPostgres(ctx, cfg.DatabaseURL)
	if err != nil {
		if isGracefulCancellation(ctx, err) {
			logger.Info("завершение приложения по сигналу", "stage", "shutdown")
			return
		}
		logger.Error("не удалось подключиться к PostgreSQL", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	logger.Info("подключение к PostgreSQL успешно установлено")
	if err := repository.ValidateSchema(ctx, pool); err != nil {
		if isGracefulCancellation(ctx, err) {
			logger.Info("завершение приложения по сигналу", "stage", "shutdown")
			return
		}
		logger.Error("схема PostgreSQL не согласована с кодом", "error", err)
		os.Exit(1)
	}

	writer := articleoutput.NewWriter(cfg.OutputDir)
	// Логи этапа пишутся не только в stdout, но и в каталог самой статьи:
	// <external_id>-<slug>/logs/<operation>.log. Пайпинг через tee больше не нужен.
	newHandler, err := newHandlerFactory(cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		logger.Error("не удалось настроить логирование", "error", err)
		os.Exit(1)
	}
	var logRouter *diagnostics.ArticleLogRouter
	if isArticleOperation(command.Name) {
		logRouter = diagnostics.NewArticleLogRouter(writer, command.Name, newHandler)
	}
	defer func() {
		if closeErr := logRouter.Close(); closeErr != nil {
			logger.Warn("не удалось закрыть логи статей", "stage", "shutdown", "error", closeErr)
		}
	}()
	taskLogger := slog.New(logRouter.Handler(logger.Handler())).With("task", "task_1", "operation", command.Name)
	articleRepository := repository.NewArticleRepository(pool, taskLogger)
	resultService := resultassembly.NewService(articleRepository, writer, taskLogger)

	taskStarted := time.Now()
	taskLogger.Info("task started", "stage", "start")
	switch command.Name {
	case "errors":
		err = runErrors(ctx, articleRepository, command.ExternalID, os.Stdout)

	case "import":
		err = runImport(ctx, articleRepository, cfg.InputFilePath, "output/task1/import-reports", command.ImportLimit, taskLogger)

	case "import-check":
		err = runImportCheck(ctx, articleRepository, cfg.InputFilePath, os.Stdout, command.ExternalID)

	case "reset":
		err = runReset(ctx, articleRepository, resetOptions{
			OutputDir:   cfg.OutputDir,
			DatabaseURL: cfg.DatabaseURL,
			AssumeYes:   command.AssumeYes,
			Interactive: isCharDevice(os.Stdin),
			In:          os.Stdin,
			Out:         os.Stdout,
		}, taskLogger)

	case "google-publish":
		// Ни LLM, ни Keys.so, ни Arsenkin: команда берёт уже сохранённый промпт с диска.
		publisher := google.NewPublisher(
			google.NewSessionFactory(googleConfig(true), taskLogger, 0),
			google.DefaultRetryPolicy(),
		)
		err = runGooglePublish(ctx, articleRepository, writer, publisher.Publish, taskLogger, os.Stdout, command.ExternalID)

	case "clear":
		err = runClear(ctx, articleRepository, writer, clearOptions{
			AssumeYes:   command.AssumeYes,
			Interactive: isCharDevice(os.Stdin),
			In:          os.Stdin,
			Out:         os.Stdout,
		}, taskLogger, command.ExternalID)

	case "prepare":
		// Резервный источник запросов строится заранее, но браузер провайдера не
		// открывается: клиент DeepSeek создаёт сессию только на первом запросе, а его
		// не будет, пока Keys.so отдаёт запросы сам.
		newFallback, closeLLM := newPrepareKeywordsFallback(ctx, generationDeps{
			repository: articleRepository, writer: writer, result: resultService, logger: taskLogger,
		})
		err = runPrepare(ctx, articleRepository, cfg, taskLogger, writer, logRouter, newFallback, command.ExternalID)
		err = errors.Join(err, closeLLM())

	case "result":
		runResult := func(ctx context.Context, externalID string) error {
			if _, buildErr := resultService.Build(ctx, externalID); buildErr != nil {
				return buildErr
			}
			resultInput, loadErr := articleRepository.GetResultInput(ctx, externalID)
			if loadErr != nil {
				return loadErr
			}
			return articleRepository.CompleteGeneration(ctx, resultInput.Article.ID)
		}
		if command.ExternalID == "" {
			err = runBatchOperation(ctx, articleRepository, "result", func(ctx context.Context, externalID string) error {
				runErr := runResult(ctx, externalID)
				if runErr == nil || isGracefulCancellation(ctx, runErr) {
					return runErr
				}
				resultInput, loadErr := articleRepository.GetResultInput(ctx, externalID)
				if loadErr != nil {
					return errors.Join(runErr, loadErr)
				}
				if saveErr := articleRepository.SaveError(ctx, resultInput.Article.ID, runErr); saveErr != nil {
					return errors.Join(runErr, saveErr)
				}
				return runErr
			}, taskLogger)
		} else {
			err = runResult(ctx, command.ExternalID)
		}

	case "run", "regenerate", "generate", "demo-generate", "retry", "article", "info", "review", "fix", "html":
		if command.DryRun {
			err = runDryRun(ctx, articleRepository, cfg, taskLogger, writer, resultService, os.Stdout)
			break
		}
		stages, configErr := loadStageConfigs(taskLogger, true)
		if configErr != nil {
			err = configErr
			break
		}
		availability := newGeminiAvailability()
		// Истёкший маркер снимается один раз при старте, а не при выборе схемы: выбор обязан
		// быть чистым, иначе на batch-прогоне он повторяет одну и ту же запись в лог.
		if until, reason, removed, expireErr := availability.expire(); expireErr != nil {
			taskLogger.Warn("Gemini state file was not removed", "error", expireErr)
		} else if removed {
			taskLogger.Info("Gemini is available again, its disable period has expired",
				"was_disabled_until", until.UTC().Format(time.RFC3339), "previous_reason", reason)
		}
		// Публикация промпта идёт рядом с генерацией и не задерживает её. Wait обязателен:
		// без него команда завершилась бы с живым Chromium и недописанным документом.
		promptPublisher := newGooglePublisher(ctx, googleConfig(true), articleRepository, taskLogger)
		defer promptPublisher.Wait()
		mode, closeLLM, buildErr := buildLLM(ctx, stages, availability, generationDeps{
			repository: articleRepository, writer: writer, result: resultService, logger: taskLogger,
			promptPublisher: promptPublisher,
		})
		if buildErr != nil {
			err = buildErr
			break
		}
		// Резервный источник исходных запросов для prepare: та же схема маршрутизации, что
		// у генерации этой статьи.
		newKeywordsFallback := newKeywordsFallbackFactory(mode, taskLogger)
		// DEMO собирается отдельной операцией: статус, current_step и error_message статьи
		// её не выбирают и ею не меняются, а маршрутизация берётся та же, что у боевого
		// прогона этой статьи.
		buildDemo := func(ctx context.Context, externalID string) error {
			preparer := demo.PrepareFunc(func(ctx context.Context, externalID string) error {
				return runDemoPrepare(ctx, articleRepository, cfg, taskLogger, writer, logRouter, newKeywordsFallback, externalID)
			})
			builder := demo.NewBuilder(cfg.OutputDir, articleRepository, writer, resultService,
				mode.routerFor(externalID), preparer, taskLogger)
			return builder.Build(ctx, externalID)
		}
		// Одна форма для всех одностадийных команд: схема выбирается на статью, режим не
		// переключается. Раньше эти восемь веток отличались только именем операции.
		stageOperation := func(name string, run func(context.Context, *generation.Pipeline, string) error) error {
			if command.ExternalID != "" {
				return mode.stage(ctx, command.ExternalID, run)
			}
			return runBatchOperation(ctx, articleRepository, name, func(ctx context.Context, externalID string) error {
				return mode.stage(ctx, externalID, run)
			}, taskLogger)
		}
		// Полный пайплайн с возобновлением: этапы выполняются существующими сервисами,
		// раннер лишь выбирает, с какого продолжить.
		execute := func(pipeline *generation.Pipeline) func(context.Context, pipelineStage, string) error {
			return func(ctx context.Context, stage pipelineStage, externalID string) error {
				switch stage {
				case stagePrepare:
					if validateErr := cfg.ValidatePrepare(); validateErr != nil {
						return validateErr
					}
					return runPrepare(ctx, articleRepository, cfg, taskLogger, writer, logRouter, newKeywordsFallback, externalID)
				case stageStructure:
					_, stageErr := pipeline.RunStructureByExternalID(ctx, externalID)
					return stageErr
				case stageArticle:
					return runArticle(ctx, pipeline, externalID)
				case stageReview:
					return runReview(ctx, pipeline, externalID)
				case stageFix:
					return runFix(ctx, pipeline, externalID)
				case stageHTML:
					return runHTML(ctx, pipeline, externalID)
				case stageResult:
					if _, buildErr := resultService.Build(ctx, externalID); buildErr != nil {
						return buildErr
					}
					resultInput, loadErr := articleRepository.GetResultInput(ctx, externalID)
					if loadErr != nil {
						return loadErr
					}
					return articleRepository.CompleteGeneration(ctx, resultInput.Article.ID)
				default:
					return fmt.Errorf("неизвестный этап пайплайна %q", stage)
				}
			}
		}
		// Единственный полный прогон статьи: его делят run, regenerate и retry. У retry был
		// собственный demo-путь, который завершал статью после article/info, минуя review,
		// fix и html, — поэтому раннер объявлен здесь, а не внутри ветки run.
		//
		// Схема выбирается на статью: раньше run был единственной генерационной командой
		// вообще без переключения режима — исчерпание квоты Gemini не выключало провайдера
		// и не переводило статью на DeepSeek.
		runOne := func(ctx context.Context, externalID string) error {
			scheme, pipeline := mode.pipelineFor(externalID)
			runErr := runFullPipeline(ctx, articleRepository, execute(pipeline), taskLogger, externalID)
			return mode.guard(ctx, externalID, scheme, runErr)
		}
		switch command.Name {
		case "generate":
			if command.ExternalID == "" {
				err = runBatchOperation(ctx, articleRepository, "generate", func(ctx context.Context, externalID string) error {
					return mode.run(ctx, externalID, runGenerate)
				}, taskLogger)
			} else {
				err = mode.run(ctx, command.ExternalID, runGenerate)
			}
		case "run", "regenerate":
			if command.Plan {
				err = runPipelinePlan(ctx, articleRepository, os.Stdout, command.ExternalID)
				break
			}
			if command.Name == "regenerate" {
				err = runRegenerate(ctx, articleRepository, writer, runOne, taskLogger, command.ExternalID)
				break
			}
			if command.ExternalID == "" {
				var pending []article.Article
				if pending, err = incompleteArticles(ctx, articleRepository); err != nil {
					break
				}
				err = runSelectedArticles(ctx, pending, "run", runOne, taskLogger)
			} else {
				err = runOne(ctx, command.ExternalID)
			}
		case "demo-generate":
			if command.ExternalID == "" {
				// Все статьи, а не подмножество по состоянию: DEMO пересобирается и для
				// completed, и для failed. Ошибка одной статьи не прекращает обход, но
				// возвращается наружу — процесс завершится ненулевым кодом.
				var all []article.Article
				if all, err = articleRepository.GetAll(ctx); err != nil {
					break
				}
				err = runSelectedArticles(ctx, all, "demo-generate", buildDemo, taskLogger)
			} else {
				err = buildDemo(ctx, command.ExternalID)
			}
		case "retry":
			// Тот же раннер, что у run: retry снимает сохранённую ошибку и доводит статью
			// до конца через review, fix и html, а не завершает её сразу после article/info.
			err = runRetry(ctx, articleRepository, command.ExternalID, runOne, taskLogger)
		case "article", "info":
			err = stageOperation(command.Name, runArticle)
		case "review":
			err = stageOperation("review", runReview)
		case "fix":
			err = stageOperation("fix", runFix)
		case "html":
			err = stageOperation("html", runHTML)
		}
		err = errors.Join(err, closeLLM())

	}

	if err != nil {
		if isGracefulCancellation(ctx, err) {
			taskLogger.Info("завершение приложения по сигналу", "stage", "shutdown")
			return
		}
		var arsenkinErr *arsenkin.StageError
		if errors.As(err, &arsenkinErr) {
			taskLogger.Error(
				"этап Arsenkin завершён с ошибкой",
				"article_id", arsenkinErr.ArticleID,
				"integration", "arsenkin",
				"stage", arsenkinErr.Stage,
				"duration_ms", arsenkinErr.Duration.Milliseconds(),
				"current_url", arsenkinErr.CurrentURL,
				"error", arsenkinErr.Err,
			)
			os.Exit(1)
		}
		var stageErr *keyssoRunError
		if errors.As(err, &stageErr) {
			taskLogger.Error(
				"этап Keys.so завершён с ошибкой",
				"article_id", stageErr.articleID,
				"integration", "keysso",
				"stage", stageErr.stage,
				"duration_ms", stageErr.duration.Milliseconds(),
				"current_url", stageErr.currentURL,
				"collected_count", stageErr.collectedCount,
				"cleaned_count", stageErr.cleanedCount,
				"error", stageErr.err,
			)
		} else {
			taskLogger.Error("task failed", "stage", "failed", "duration_ms", time.Since(taskStarted).Milliseconds(), "error", err)
		}
		os.Exit(1)
	}
	taskLogger.Info("task completed", "stage", "complete", "duration_ms", time.Since(taskStarted).Milliseconds())
}

// isArticleOperation reports an operation that works on one article at a time and therefore
// deserves its own log in the article directory. Импорт сюда не входит: он работает со всей
// таблицей сразу и уже пишет отчёт в output/task1/import-reports.
func isArticleOperation(name string) bool {
	switch name {
	case "prepare", "run", "regenerate", "generate", "demo-generate", "retry",
		"article", "info", "review", "fix", "html", "result":
		return true
	default:
		return false
	}
}

func isGracefulCancellation(ctx context.Context, err error) bool {
	return errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled)
}

// newHandlerFactory builds log handlers of the configured level and format. The same factory
// serves stdout and the per-article stage logs, so both keep one format.
// parseLogLevel разбирает LOG_LEVEL. Вынесен из фабрики, потому что консольный pretty-формат
// собирается мимо неё, а уровень у них общий.
func parseLogLevel(levelValue string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(levelValue)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("неподдерживаемый LOG_LEVEL %q", levelValue)
	}
}

func newHandlerFactory(levelValue, formatValue string) (func(io.Writer) slog.Handler, error) {
	level, err := parseLogLevel(levelValue)
	if err != nil {
		return nil, err
	}
	options := &slog.HandlerOptions{Level: level}
	// Фабрика обслуживает логи статей в logs/<операция>.log, поэтому pretty здесь нет и быть
	// не должно: файл читают инструментами, а не глазами. auto и pretty сводятся к text.
	switch strings.ToLower(strings.TrimSpace(formatValue)) {
	case "text", "auto", "pretty":
		return func(destination io.Writer) slog.Handler { return slog.NewTextHandler(destination, options) }, nil
	case "json":
		return func(destination io.Writer) slog.Handler { return slog.NewJSONHandler(destination, options) }, nil
	default:
		return nil, fmt.Errorf("неподдерживаемый LOG_FORMAT %q", formatValue)
	}
}

// resolveConsoleFormat решает, каким будет формат консоли. auto означает «по месту»: в
// терминале человекочитаемый вывод, при перенаправлении в файл или пайп прежний text, чтобы
// `make task-1 run | grep` и запуск из CI работали как раньше.
func resolveConsoleFormat(formatValue string, interactive bool) string {
	format := strings.ToLower(strings.TrimSpace(formatValue))
	if format != "auto" {
		return format
	}
	if interactive {
		return "pretty"
	}
	return "text"
}

func newLogger(levelValue, formatValue string) (*slog.Logger, error) {
	format := resolveConsoleFormat(formatValue, isCharDevice(os.Stdout))
	if format == "pretty" {
		level, err := parseLogLevel(levelValue)
		if err != nil {
			return nil, err
		}
		// NO_COLOR — общепринятое соглашение: раз пользователь его выставил, цвет не наш.
		_, noColor := os.LookupEnv("NO_COLOR")
		return slog.New(newPrettyHandler(os.Stdout, level, terminalWidth(), !noColor)), nil
	}
	newHandler, err := newHandlerFactory(levelValue, format)
	if err != nil {
		return nil, err
	}
	return slog.New(newHandler(os.Stdout)), nil
}

type taskCommand struct {
	Name        string
	ExternalID  string
	ImportLimit int
	DryRun      bool
	// Plan печатает план возобновления, ничего не выполняя. Не путать с DryRun: тот
	// прогоняет офлайн-пайплайн на отдельной базе.
	Plan bool
	// AssumeYes подтверждает reset без вопроса. Нужен там, где stdin не терминал.
	AssumeYes bool
}

// isCharDevice reports stdin attached to a terminal. Reset спрашивает подтверждение только
// тогда, когда его есть кому прочитать.
func isCharDevice(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// taskFlags — служебные флаги, снятые с командной строки до разбора операции.
type taskFlags struct {
	dryRun    bool
	plan      bool
	assumeYes bool
}

// stripTaskFlags снимает служебные флаги и возвращает остальные аргументы. Повтор флага —
// ошибка, а не молчаливое подтверждение.
func stripTaskFlags(args []string) ([]string, taskFlags, error) {
	var flags taskFlags
	known := map[string]*bool{
		"--dry-run": &flags.dryRun,
		"--plan":    &flags.plan,
		"--yes":     &flags.assumeYes,
	}
	filtered := make([]string, 0, len(args))
	for index, arg := range args {
		seen, isFlag := known[arg]
		if index == 0 || !isFlag {
			filtered = append(filtered, arg)
			continue
		}
		if *seen {
			return nil, taskFlags{}, fmt.Errorf("%s may be specified only once", arg)
		}
		*seen = true
	}
	return filtered, flags, nil
}

func parseCommand(args []string) (taskCommand, error) {
	filtered, flags, err := stripTaskFlags(args)
	if err != nil {
		return taskCommand{}, err
	}
	command, err := parseTaskCommand(filtered)
	if err != nil {
		return taskCommand{}, err
	}
	if flags.dryRun && (command.Name != "run" || command.ExternalID != "") {
		return taskCommand{}, fmt.Errorf("--dry-run is supported only for task-1 run without external_id")
	}
	if flags.plan && command.Name != "run" {
		return taskCommand{}, fmt.Errorf("--plan is supported only for task-1 run")
	}
	if flags.plan && flags.dryRun {
		return taskCommand{}, fmt.Errorf("--plan and --dry-run cannot be combined")
	}
	if flags.assumeYes && command.Name != "reset" && command.Name != "clear" {
		return taskCommand{}, fmt.Errorf("--yes is supported only for task-1 reset and task-1 clear")
	}
	command.DryRun = flags.dryRun
	command.Plan = flags.plan
	command.AssumeYes = flags.assumeYes
	return command, nil
}

func parseTaskCommand(args []string) (taskCommand, error) {
	const available = "available task-1 operations: import, import-check, errors, retry, run, regenerate, demo-generate, prepare, generate, article, info, review, fix, html, result, clear, reset, google-login, google-publish, deepseek-login"
	if len(args) < 3 || (args[1] != "task-1" && args[1] != "task_1") {
		return taskCommand{}, fmt.Errorf("usage: seo-pipeline task-1 <operation> [arguments]; %s", available)
	}
	task := args[2]
	switch task {
	case "deepseek-login", "google-login", "reset":
		// Позиционных аргументов ни у одной нет: сброс одной статьи — это regenerate,
		// а публикация одной статьи — google-publish.
		if len(args) != 3 {
			return taskCommand{}, fmt.Errorf("usage: seo-pipeline task-1 %s", task)
		}
		return taskCommand{Name: task}, nil
	case "import":
		if len(args) > 4 {
			return taskCommand{}, fmt.Errorf("usage: seo-pipeline task-1 import [limit]")
		}
		command := taskCommand{Name: task}
		if len(args) == 4 {
			value := strings.TrimSpace(args[3])
			limit, err := strconv.Atoi(value)
			if err != nil || limit <= 0 {
				return taskCommand{}, fmt.Errorf("import limit must be a positive integer: %q", args[3])
			}
			command.ImportLimit = limit
		}
		return command, nil
	case "run":
		if len(args) == 3 {
			return taskCommand{Name: task}, nil
		}
		if len(args) != 4 {
			return taskCommand{}, fmt.Errorf("usage: seo-pipeline task-1 run [external_id]")
		}
		return parseExternalIDCommand(task, args[3])
	case "regenerate":
		if len(args) != 4 {
			return taskCommand{}, fmt.Errorf("usage: seo-pipeline task-1 regenerate <external_id>")
		}
		return parseExternalIDCommand(task, args[3])
	case "clear":
		// ID обязателен: очистка без него означала бы «стереть все статьи», а это reset.
		if len(args) != 4 {
			return taskCommand{}, fmt.Errorf("usage: seo-pipeline task-1 clear <external_id>")
		}
		return parseExternalIDCommand(task, args[3])
	case "google-publish":
		// ID обязателен: массовая публикация в чужой Google Drive не должна запускаться
		// одним словом без аргумента.
		if len(args) != 4 {
			return taskCommand{}, fmt.Errorf("usage: seo-pipeline task-1 google-publish <external_id>")
		}
		return parseExternalIDCommand(task, args[3])
	case "errors", "retry", "prepare", "generate", "demo-generate", "review", "fix", "info", "html", "result", "article", "import-check":
		if len(args) == 3 {
			return taskCommand{Name: task}, nil
		}
		if len(args) != 4 {
			return taskCommand{}, fmt.Errorf("usage: seo-pipeline task-1 %s [external_id]", task)
		}
		return parseExternalIDCommand(task, args[3])
	default:
		return taskCommand{}, fmt.Errorf("unknown task-1 operation %q; %s", task, available)
	}
}

func parseExternalIDCommand(name, value string) (taskCommand, error) {
	externalID, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || externalID <= 0 {
		return taskCommand{}, fmt.Errorf("external_id must be a positive integer: %q", value)
	}
	return taskCommand{Name: name, ExternalID: strconv.FormatInt(externalID, 10)}, nil
}

func validateConfig(command string, cfg config.Config) error {
	switch command {
	case "import", "import-check":
		return cfg.ValidateImport()
	case "prepare":
		return cfg.ValidatePrepare()
	case "run", "regenerate", "generate", "demo-generate", "retry", "article", "info", "review", "fix", "html":
		return cfg.ValidateGenerate()
	case "result":
		return cfg.ValidateReset()
	case "errors", "reset", "clear", "google-publish":
		return cfg.ValidateReset()
	default:
		return fmt.Errorf("unknown task %q", command)
	}
}
