package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/generation"
	"github.com/foxylis237/seo-pipeline/internal/integrations/arsenkin"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/output"
	"github.com/foxylis237/seo-pipeline/internal/repository"
	"github.com/foxylis237/seo-pipeline/internal/storage"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	command, err := parseCommand(os.Args)
	if err != nil {
		logger.Error(err.Error(), "available_commands", "import, run, generate, reset")
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
	if err := validateConfig(command, cfg); err != nil {
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

	switch command {
	case "import":
		err = runImport(ctx, articleRepository, cfg.InputFilePath, logger)

	case "run", "generate":
		generator, generatorErr := generation.NewGeminiGenerator(ctx, cfg.GeminiAPIKey, cfg.GeminiModel)
		if generatorErr != nil {
			err = generatorErr
			break
		}
		writer := articleoutput.NewWriter(cfg.OutputDir)
		generationPipeline := generation.NewPipeline(articleRepository, generator, writer, cfg.GeminiModel, logger)
		if command == "generate" {
			err = runGenerate(ctx, generationPipeline, strings.TrimSpace(os.Args[2]))
		} else {
			targetExternalID := ""
			if len(os.Args) == 3 {
				targetExternalID = strings.TrimSpace(os.Args[2])
			}
			err = runPipeline(ctx, articleRepository, cfg, logger, generationPipeline, writer, targetExternalID)
		}
		if closeErr := generator.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("закрыть Gemini client: %w", closeErr))
		}

	case "reset":
		err = runReset(ctx, articleRepository, logger)

	}

	if err != nil {
		var arsenkinErr *arsenkin.StageError
		if errors.As(err, &arsenkinErr) {
			logger.Error(
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
			logger.Error(
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
			logger.Error("ошибка выполнения команды", "error", err)
		}
		os.Exit(1)
	}
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

func parseCommand(args []string) (string, error) {
	if len(args) < 2 {
		return "", fmt.Errorf("не указана команда")
	}

	switch args[1] {
	case "import", "reset":
		if len(args) != 2 {
			return "", fmt.Errorf("лишние аргументы команды %q", args[1])
		}
		return args[1], nil
	case "run":
		if len(args) > 3 || len(args) == 3 && strings.TrimSpace(args[2]) == "" {
			return "", fmt.Errorf("неверные аргументы команды run")
		}
		return args[1], nil
	case "generate":
		if len(args) != 3 || strings.TrimSpace(args[2]) == "" {
			return "", fmt.Errorf("использование: generate <external_id>")
		}
		return args[1], nil
	default:
		return "", fmt.Errorf("неизвестная команда %q", args[1])
	}
}

func validateConfig(command string, cfg config.Config) error {
	switch command {
	case "import":
		return cfg.ValidateImport()
	case "reset":
		return cfg.ValidateReset()
	case "generate":
		return cfg.ValidateGenerate()
	default:
		return cfg.ValidateRun()
	}
}
