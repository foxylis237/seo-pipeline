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

	case "prepare":
		err = runPrepare(ctx, articleRepository, cfg, taskLogger, writer, logRouter, command.ExternalID)

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
		mode, closeLLM, buildErr := buildLLM(ctx, stages, availability, generationDeps{
			repository: articleRepository, writer: writer, result: resultService, logger: taskLogger,
		})
		if buildErr != nil {
			err = buildErr
			break
		}
		// DEMO собирается отдельной операцией: статус, current_step и error_message статьи
		// её не выбирают и ею не меняются, а маршрутизация берётся та же, что у боевого
		// прогона этой статьи.
		buildDemo := func(ctx context.Context, externalID string) error {
			preparer := demo.PrepareFunc(func(ctx context.Context, externalID string) error {
				return runDemoPrepare(ctx, articleRepository, cfg, taskLogger, writer, logRouter, externalID)
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
					return runPrepare(ctx, articleRepository, cfg, taskLogger, writer, logRouter, externalID)
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
func newHandlerFactory(levelValue, formatValue string) (func(io.Writer) slog.Handler, error) {
	var level slog.Level
	switch strings.ToLower(strings.TrimSpace(levelValue)) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return nil, fmt.Errorf("неподдерживаемый LOG_LEVEL %q", levelValue)
	}
	options := &slog.HandlerOptions{Level: level}
	switch strings.ToLower(strings.TrimSpace(formatValue)) {
	case "text":
		return func(destination io.Writer) slog.Handler { return slog.NewTextHandler(destination, options) }, nil
	case "json":
		return func(destination io.Writer) slog.Handler { return slog.NewJSONHandler(destination, options) }, nil
	default:
		return nil, fmt.Errorf("неподдерживаемый LOG_FORMAT %q", formatValue)
	}
}

func newLogger(levelValue, formatValue string) (*slog.Logger, error) {
	newHandler, err := newHandlerFactory(levelValue, formatValue)
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
}

func parseCommand(args []string) (taskCommand, error) {
	dryRun := false
	plan := false
	filtered := make([]string, 0, len(args))
	for index, arg := range args {
		if index > 0 && arg == "--dry-run" {
			if dryRun {
				return taskCommand{}, fmt.Errorf("--dry-run may be specified only once")
			}
			dryRun = true
			continue
		}
		if index > 0 && arg == "--plan" {
			if plan {
				return taskCommand{}, fmt.Errorf("--plan may be specified only once")
			}
			plan = true
			continue
		}
		filtered = append(filtered, arg)
	}
	command, err := parseTaskCommand(filtered)
	if err != nil {
		return taskCommand{}, err
	}
	if dryRun && (command.Name != "run" || command.ExternalID != "") {
		return taskCommand{}, fmt.Errorf("--dry-run is supported only for task-1 run without external_id")
	}
	if plan && command.Name != "run" {
		return taskCommand{}, fmt.Errorf("--plan is supported only for task-1 run")
	}
	if plan && dryRun {
		return taskCommand{}, fmt.Errorf("--plan and --dry-run cannot be combined")
	}
	command.DryRun = dryRun
	command.Plan = plan
	return command, nil
}

func parseTaskCommand(args []string) (taskCommand, error) {
	const available = "available task-1 operations: import, import-check, errors, retry, run, regenerate, demo-generate, prepare, generate, article, info, review, fix, html, result, deepseek-login"
	if len(args) < 3 || (args[1] != "task-1" && args[1] != "task_1") {
		return taskCommand{}, fmt.Errorf("usage: seo-pipeline task-1 <operation> [arguments]; %s", available)
	}
	task := args[2]
	switch task {
	case "deepseek-login":
		if len(args) != 3 {
			return taskCommand{}, fmt.Errorf("usage: seo-pipeline task-1 deepseek-login")
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
	case "errors":
		return cfg.ValidateReset()
	default:
		return fmt.Errorf("unknown task %q", command)
	}
}
