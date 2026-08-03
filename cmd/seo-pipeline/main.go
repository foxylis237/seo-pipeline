package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/integrations/arsenkin"
	"github.com/foxylis237/seo-pipeline/internal/llm"
	llmgemini "github.com/foxylis237/seo-pipeline/internal/llm/gemini"
	"github.com/foxylis237/seo-pipeline/internal/storage"
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

	articleRepository := repository.NewArticleRepository(pool)
	taskLogger := logger.With("task", "task_1", "operation", command.Name)
	writer := articleoutput.NewWriter(cfg.OutputDir)
	resultService := resultassembly.NewService(articleRepository, writer, taskLogger)

	taskStarted := time.Now()
	taskLogger.Info("task started", "stage", "start")
	switch command.Name {
	case "import":
		err = runImport(ctx, articleRepository, cfg.InputFilePath, "output/task1/import-reports", command.ImportLimit, taskLogger)

	case "prepare":
		writer := articleoutput.NewWriter(cfg.OutputDir)
		err = runPrepare(ctx, articleRepository, cfg, taskLogger, writer, command.ExternalID)

	case "result":
		_, err = resultService.Build(ctx, command.ExternalID)
		if err == nil {
			resultInput, loadErr := articleRepository.GetResultInput(ctx, command.ExternalID)
			if loadErr != nil {
				err = loadErr
			} else {
				err = articleRepository.CompleteGeneration(ctx, resultInput.Article.ID)
			}
		}

	case "run", "generate", "demo-generate", "article", "info", "review", "fix", "html":
		if command.DryRun {
			err = runDryRun(ctx, articleRepository, cfg, taskLogger, writer, resultService)
			break
		}
		llmConfig, configErr := config.LoadLLMConfig("config/config.yaml")
		if configErr != nil {
			err = configErr
			break
		}
		if configErr := useGeminiModel(&llmConfig, cfg.GeminiModel); configErr != nil {
			err = configErr
			break
		}
		clients := make(map[string]llm.Client)
		var geminiClient *llmgemini.Client
		for name, provider := range llmConfig.Providers {
			switch provider.Type {
			case "gemini":
				client, generatorErr := llmgemini.NewClient(ctx, os.Getenv(provider.APIKeyEnv), cfg.GeminiModel)
				if generatorErr != nil {
					err = fmt.Errorf("создать LLM provider %q: %w", name, generatorErr)
					break
				}
				clients[name] = client
				geminiClient = client
			case "openai_compatible":
				client, clientErr := llm.NewOpenAICompatibleClient(provider.BaseURL, os.Getenv(provider.APIKeyEnv), name, taskLogger)
				if clientErr != nil {
					err = fmt.Errorf("создать LLM provider %q: %w", name, clientErr)
					break
				}
				clients[name] = client
			}
			if err != nil {
				break
			}
		}
		if err != nil {
			break
		}
		if geminiClient == nil {
			err = fmt.Errorf("Gemini provider is not configured")
			break
		}
		router := llm.NewRouter(llmConfig, clients, taskLogger)
		generationPipeline := generation.NewPipeline(articleRepository, router, geminiClient, writer, taskLogger, resultService)
		switch command.Name {
		case "generate":
			err = runGenerate(ctx, generationPipeline, command.ExternalID)
		case "run":
			if command.ExternalID == "" {
				err = runAllDemo(ctx, articleRepository, func(ctx context.Context, externalID string) error {
					return runDemoGenerate(ctx, generationPipeline, externalID)
				}, taskLogger)
			} else {
				err = runDemoGenerate(ctx, generationPipeline, command.ExternalID)
			}
		case "demo-generate":
			err = runDemoGenerate(ctx, generationPipeline, command.ExternalID)
		case "article", "info":
			err = runArticle(ctx, generationPipeline, command.ExternalID)
		case "review":
			err = runReview(ctx, generationPipeline, command.ExternalID)
		case "fix":
			err = runFix(ctx, generationPipeline, command.ExternalID)
		case "html":
			err = runHTML(ctx, generationPipeline, command.ExternalID)
		}
		if closeErr := geminiClient.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("закрыть Gemini client: %w", closeErr))
		}

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

func isGracefulCancellation(ctx context.Context, err error) bool {
	return errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled)
}

func newLogger(levelValue, formatValue string) (*slog.Logger, error) {
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
	var handler slog.Handler
	switch strings.ToLower(strings.TrimSpace(formatValue)) {
	case "text":
		handler = slog.NewTextHandler(os.Stdout, options)
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, options)
	default:
		return nil, fmt.Errorf("неподдерживаемый LOG_FORMAT %q", formatValue)
	}
	return slog.New(handler), nil
}

type taskCommand struct {
	Name        string
	ExternalID  string
	ImportLimit int
	DryRun      bool
}

func parseCommand(args []string) (taskCommand, error) {
	dryRun := false
	filtered := make([]string, 0, len(args))
	for index, arg := range args {
		if index > 0 && arg == "--dry-run" {
			if dryRun {
				return taskCommand{}, fmt.Errorf("--dry-run may be specified only once")
			}
			dryRun = true
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
	command.DryRun = dryRun
	return command, nil
}

func parseTaskCommand(args []string) (taskCommand, error) {
	const available = "available task-1 operations: import, run, demo-generate, prepare, generate, article, info, review, fix, html, result"
	if len(args) < 3 || (args[1] != "task-1" && args[1] != "task_1") {
		return taskCommand{}, fmt.Errorf("usage: seo-pipeline task-1 <operation> [arguments]; %s", available)
	}
	task := args[2]
	switch task {
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
	case "prepare", "generate", "review", "fix", "info", "html", "result", "article", "demo-generate":
		if len(args) != 4 {
			return taskCommand{}, fmt.Errorf("usage: seo-pipeline task-1 %s <external_id>", task)
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

func useGeminiModel(cfg *config.LLMConfig, model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return fmt.Errorf("GEMINI_MODEL is required")
	}
	for name, stage := range cfg.Stages {
		if stage.Provider != "gemini" {
			return fmt.Errorf("LLM stage %q must use Gemini", name)
		}
		stage.Model = model
		cfg.Stages[name] = stage
	}
	return nil
}

func validateConfig(command string, cfg config.Config) error {
	switch command {
	case "import":
		return cfg.ValidateImport()
	case "prepare":
		return cfg.ValidatePrepare()
	case "run", "generate", "demo-generate", "article", "info", "review", "fix", "html":
		return cfg.ValidateGenerate()
	case "result":
		return cfg.ValidateReset()
	default:
		return fmt.Errorf("unknown task %q", command)
	}
}
