package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/generation"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/importer"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/pipeline/output"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/repository"
	resultassembly "github.com/foxylis237/seo-pipeline/internal/pipeline/result"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
)

const dryRunModelPrefix = "dry-run-"

type dryRunClient struct{}

func (dryRunClient) Generate(_ context.Context, request llm.Request) (llm.Response, error) {
	stage := strings.TrimPrefix(request.Model, dryRunModelPrefix)
	text, found := dryRunStageResponses()[stage]
	if !found {
		return llm.Response{}, fmt.Errorf("dry-run has no response for model %q", request.Model)
	}
	return llm.Response{Text: text, Model: request.Model, InputTokens: 120, OutputTokens: 80}, nil
}

func (c dryRunClient) NewChat(context.Context, int64) (llm.Chat, error) {
	stages := dryRunStageResponses()
	return &dryRunChat{responses: []llm.Response{
		{Text: stages["review"], Model: dryRunModelPrefix + "review", InputTokens: 240, OutputTokens: 420},
		{Text: stages["fix"], Model: dryRunModelPrefix + "fix", InputTokens: 80, OutputTokens: 60},
	}}, nil
}

// NewChatWithHistory продолжает чат review в отдельном прогоне стадии fix.
func (c dryRunClient) NewChatWithHistory(ctx context.Context, articleID int64, history ...llm.Message) (llm.Chat, error) {
	chat, err := c.NewChat(ctx, articleID)
	if err != nil {
		return nil, err
	}
	if session, ok := chat.(*dryRunChat); ok {
		session.next = len(history) / 2
	}
	return chat, nil
}

type dryRunChat struct {
	responses []llm.Response
	next      int
}

func (c *dryRunChat) Generate(_ context.Context, prompt string) (llm.Response, error) {
	if strings.TrimSpace(prompt) == "" {
		return llm.Response{}, fmt.Errorf("dry-run chat prompt is empty")
	}
	if c.responses == nil {
		return llm.Response{}, fmt.Errorf("dry-run chat is closed")
	}
	if c.next >= len(c.responses) {
		return llm.Response{}, fmt.Errorf("dry-run chat has no response for request %d", c.next+1)
	}
	response := c.responses[c.next]
	c.next++
	return response, nil
}

func (c *dryRunChat) Close() error {
	c.responses = nil
	return nil
}

const (
	dryRunArticle = `# Тестовая статья

## Основной раздел

Это локально сгенерированный текст для проверки полного SEO-пайплайна без внешних запросов.

## FAQ

### Как работает dry-run?

Он использует детерминированные локальные ответы вместо платных сервисов.

[[ARTICLE_COMPLETE]]`
	dryRunInfo = `TLDR:
Локальная проверка полного пайплайна без внешних запросов.
FAQ:
Вопрос: Выполняются ли сетевые запросы?
Ответ: Нет, используются только локальные тестовые данные.`
)

// dryRunStageSet подменяет модель каждого target на локальную заглушку, сохраняя провайдеров.
//
// Подменяется именно Targets: раньше dry-run писал в stage.Model, которого роутер не читает,
// и заглушка получала настоящее имя модели. Копия делается целиком — разрешённая схема
// используется и отчётом, и её нельзя портить на месте.
func dryRunStageSet(source config.LLMConfig) config.LLMConfig {
	stages := make(map[string]config.LLMStageConfig, len(source.Stages))
	for name, stage := range source.Stages {
		targets := make([]config.LLMTargetConfig, len(stage.Targets))
		for index, target := range stage.Targets {
			targets[index] = config.LLMTargetConfig{Provider: target.Provider, Model: dryRunModelPrefix + name}
		}
		stage.Targets = targets
		stages[name] = stage
	}
	return config.LLMConfig{Providers: source.Providers, Stages: stages}
}

func dryRunStageResponses() map[string]string {
	return map[string]string{
		"structure": "H1 - Тестовая статья\nH2 - Основной раздел\nH2 - FAQ",
		"article":   dryRunArticle,
		"info":      dryRunInfo,
		"review":    "Структура и содержание подходят для локальной проверки. Критичных замечаний нет.",
		"fix":       strings.ReplaceAll(dryRunArticle, "[[ARTICLE_COMPLETE]]", "") + "\n\n[[ARTICLE_COMPLETE]]",
		"html":      "<h1>Тестовая статья</h1>\n<h2>Основной раздел</h2>\n<p>Локальная проверка SEO-пайплайна без внешних запросов.</p>",
	}
}

// runDryRun — безопасная проверка перед дорогим прогоном.
//
// Сначала печатается разрешённая маршрутизация: тем же резолвером, что и в бою, поэтому отчёт
// показывает схему, которая действительно будет выбрана. Затем эта же схема прогоняется
// целиком на локальной заглушке. Внешних запросов нет ни на одном шаге: доступность
// провайдеров определяется по маркерам и окружению, а не обращением к ним.
func runDryRun(
	ctx context.Context,
	articleRepository *repository.ArticleRepository,
	cfg config.Config,
	profile tasks.Profile,
	logger *slog.Logger,
	writer *articleoutput.Writer,
	resultService *resultassembly.Service,
	report io.Writer,
) error {
	// Учётные данные не требуются: отчёт обязан назвать причину недоступности провайдера,
	// а не упасть на ней.
	stages, err := loadStageConfigs(profile, logger, false)
	if err != nil {
		return fmt.Errorf("dry-run load LLM config: %w", err)
	}
	routing := newLLMResolver(stages, newGeminiAvailability()).Resolve()
	if err := writeRoutingReport(report, routing); err != nil {
		return fmt.Errorf("dry-run write routing report: %w", err)
	}

	inputs, err := importer.ReadArticles(cfg.InputFilePath)
	if err != nil {
		return fmt.Errorf("dry-run read Excel: %w", err)
	}
	llmConfig := dryRunStageSet(routing.Config)
	if filepath.Base(filepath.Clean(cfg.OutputDir)) != "dry-run" {
		return fmt.Errorf("refuse to clean unsafe dry-run output directory %q", cfg.OutputDir)
	}
	if err := os.RemoveAll(cfg.OutputDir); err != nil {
		return fmt.Errorf("dry-run clean output directory %q: %w", cfg.OutputDir, err)
	}
	if err := articleRepository.Reset(ctx); err != nil {
		return fmt.Errorf("dry-run reset isolated database: %w", err)
	}
	stub := dryRunClient{}
	clients := make(map[string]llm.Client, len(llmConfig.Providers))
	for name := range llmConfig.Providers {
		clients[name] = stub
	}
	router := llm.NewRouter(llmConfig, clients, logger)
	pipeline := generation.NewPipeline(articleRepository, router, stub, writer, logger, resultService)

	for _, input := range inputs {
		selected, _, err := articleRepository.Import(ctx, input)
		if err != nil {
			return fmt.Errorf("dry-run import external_id %d: %w", input.ExcelID, err)
		}
		if err := seedDryRunResearch(ctx, articleRepository, selected.ID); err != nil {
			return err
		}
		output, err := pipeline.RunByExternalID(ctx, selected.ExternalID)
		if err != nil {
			return fmt.Errorf("dry-run pipeline external_id %s: %w", selected.ExternalID, err)
		}
		if err := verifyDryRunResult(ctx, articleRepository, writer, selected.ExternalID, output.Paths); err != nil {
			return err
		}
		logger.Info("dry-run article verified", "article_id", selected.ID, "external_id", selected.ExternalID, "status", "completed", "result_path", output.Paths.ResultPath)
	}
	return nil
}

func seedDryRunResearch(ctx context.Context, articleRepository *repository.ArticleRepository, articleID int64) error {
	if err := articleRepository.PrepareArticleForRun(ctx, articleID); err != nil {
		return fmt.Errorf("dry-run reset article %d: %w", articleID, err)
	}
	if err := articleRepository.SavePreparedResearch(ctx, articleID, []string{"тестовый запрос", "проверка dry-run"}, []article.KeywordFrequency{
		{Query: "тестовый запрос", Frequency: 120},
		{Query: "проверка dry-run", Frequency: 60},
	}, []string{"тестирование", "локальный запуск"}, "H1 - Тестовая статья\nH2 - Основной раздел\nH2 - FAQ"); err != nil {
		return fmt.Errorf("dry-run save local research for article %d: %w", articleID, err)
	}
	return nil
}

func verifyDryRunResult(ctx context.Context, articleRepository *repository.ArticleRepository, writer *articleoutput.Writer, externalID string, paths articleoutput.ArticlePaths) error {
	input, err := articleRepository.GetResultInput(ctx, externalID)
	if err != nil {
		return fmt.Errorf("dry-run verify state for external_id %s: %w", externalID, err)
	}
	if input.Article.Status != "completed" || input.Article.CurrentStep != nil {
		return fmt.Errorf("dry-run external_id %s finished with status=%q current_step=%v", externalID, input.Article.Status, input.Article.CurrentStep)
	}
	required := []string{
		paths.StructurePromptPath, paths.StructurePath,
		paths.ArticlePromptPath, paths.ArticlePath, paths.GenerationInfoPath,
		paths.ReviewPromptPath, paths.ReviewPath,
		paths.FixPromptPath, paths.FixedArticlePath,
		paths.HTMLPromptPath, paths.HTMLPath, paths.ResultPath,
	}
	for _, path := range required {
		if !writer.Exists(path) {
			return fmt.Errorf("dry-run required artifact %q is missing", path)
		}
	}
	return nil
}
