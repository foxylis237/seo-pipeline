package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/generation"
	"github.com/foxylis237/seo-pipeline/internal/integrations/arsenkin"
	"github.com/foxylis237/seo-pipeline/internal/llm"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/output"
	"github.com/foxylis237/seo-pipeline/internal/repository"
	"github.com/foxylis237/seo-pipeline/internal/storage"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	command, err := parseCommand(os.Args)
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}

	cfg, err := config.Load()
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
	if command.Name == "import" && command.ImportPath != "" {
		cfg.InputFilePath = command.ImportPath
	}
	if err := validateConfig(command.Name, cfg); err != nil {
		logger.Error("не удалось загрузить конфигурацию", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	pool, err := storage.NewPostgres(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("не удалось подключиться к PostgreSQL", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	logger.Info("подключение к PostgreSQL успешно установлено")
	if err := repository.ValidateSchema(ctx, pool); err != nil {
		logger.Error("схема PostgreSQL не согласована с кодом", "error", err)
		os.Exit(1)
	}

	articleRepository := repository.NewArticleRepository(pool)

	taskLogger := logger.With("task", "task_1", "operation", command.Name)
	taskStarted := time.Now()
	taskLogger.Info("task started", "stage", "start")
	switch command.Name {
	case "import":
		err = runImport(ctx, articleRepository, cfg.InputFilePath, taskLogger)

	case "prepare":
		writer := articleoutput.NewWriter(cfg.OutputDir)
		err = runPrepare(ctx, articleRepository, cfg, taskLogger, writer, command.ExternalID)

	case "generate", "article", "info", "review", "fix", "html":
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
		var geminiClient *generation.GeminiGenerator
		for name, provider := range llmConfig.Providers {
			switch provider.Type {
			case "gemini":
				client, generatorErr := generation.NewGeminiGenerator(ctx, os.Getenv(provider.APIKeyEnv), cfg.GeminiModel)
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
		writer := articleoutput.NewWriter(cfg.OutputDir)
		router := llm.NewRouter(llmConfig, clients, taskLogger)
		generationPipeline := generation.NewPipeline(articleRepository, router, writer, taskLogger)
		switch command.Name {
		case "generate":
			err = runGenerate(ctx, generationPipeline, command.ExternalID)
		case "article":
			err = runArticle(ctx, generationPipeline, command.ExternalID)
		case "info":
			err = runInfo(ctx, generationPipeline, command.ExternalID)
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
	Name       string
	ExternalID string
	ImportPath string
}

func parseCommand(args []string) (taskCommand, error) {
	const available = "available task_1 operations: import, prepare, generate, article, info, review, fix, html"
	if len(args) == 3 && isStandaloneLLMOperation(args[1]) {
		return parseExternalIDCommand(args[1], args[2])
	}
	if len(args) < 3 || args[1] != "task_1" {
		return taskCommand{}, fmt.Errorf("usage: seo-pipeline <article|info|review|fix|html> <external_id> or seo-pipeline task_1 <operation> [arguments]; %s", available)
	}
	task := args[2]
	switch task {
	case "import":
		if len(args) > 4 {
			return taskCommand{}, fmt.Errorf("usage: seo-pipeline task_1 import [excel_path]")
		}
		command := taskCommand{Name: task}
		if len(args) == 4 {
			command.ImportPath = strings.TrimSpace(args[3])
			if command.ImportPath == "" {
				return taskCommand{}, fmt.Errorf("excel_path must not be empty")
			}
		}
		return command, nil
	case "prepare", "generate", "article", "info", "review", "fix", "html":
		if len(args) != 4 {
			return taskCommand{}, fmt.Errorf("usage: seo-pipeline task_1 %s <external_id>", task)
		}
		return parseExternalIDCommand(task, args[3])
	default:
		return taskCommand{}, fmt.Errorf("unknown task_1 operation %q; %s", task, available)
	}
}

func isStandaloneLLMOperation(operation string) bool {
	switch operation {
	case "article", "info", "review", "fix", "html":
		return true
	default:
		return false
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
	case "generate", "article", "info", "review", "fix", "html":
		return cfg.ValidateGenerate()
	default:
		return fmt.Errorf("unknown task %q", command)
	}
}
