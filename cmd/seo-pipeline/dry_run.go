package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
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

// dryRunClient — локальный провайдер офлайн-прогона.
//
// responses задаёт ответ на каждую стадию и приходит от задачи: набор у задач разный, и
// общего на всех быть не может — см. dryRunStageResponses. Нулевое значение поля означает
// набор task_1: так заглушка остаётся годной там, где профиля под рукой нет.
type dryRunClient struct{ responses map[string]string }

func (c dryRunClient) stageResponses() map[string]string {
	if c.responses != nil {
		return c.responses
	}
	return dryRunStageResponses(false)
}

func (c dryRunClient) Generate(_ context.Context, request llm.Request) (llm.Response, error) {
	// Стадия берётся из запроса, а не из имени модели: в чате все сообщения после первого
	// уходят к target, ответившему на первое, и модель у них одна на весь чат. По модели
	// заглушка отвечала бы стадии info текстом статьи, и TL;DR с FAQ в офлайн-прогоне
	// оставались бы пустыми — то есть проверка молча пропускала бы разбор метаданных.
	stage := request.Stage
	if stage == "" {
		stage = strings.TrimPrefix(request.Model, dryRunModelPrefix)
	}
	text, found := c.stageResponses()[stage]
	if !found {
		return llm.Response{}, fmt.Errorf("dry-run has no response for stage %q (model %q)", stage, request.Model)
	}
	return llm.Response{Text: text, Model: request.Model, InputTokens: 120, OutputTokens: 80}, nil
}

// SupportsAttachments объявляет приём документов стадии.
//
// Без этого офлайн-прогон не доходил до стадии html ни у одной задачи: роутер отказывает
// стадии с вложениями, если провайдер их не принимает, и dry-run падал на регламенте вёрстки
// вместо того, чтобы проверить поток. Содержимое документа заглушке безразлично — значимо
// только то, что он найден и дошёл до провайдера.
func (dryRunClient) SupportsAttachments() bool { return true }

func (c dryRunClient) NewChat(context.Context, int64) (llm.Chat, error) {
	stages := c.stageResponses()
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
		// Ненайденный документ стадии офлайн-прогон не роняет: регламент вёрстки — файл
		// боевого запуска, и его отсутствие уже названо в отчёте маршрутизации отдельной
		// строкой. Прогон при этом обязан дойти до конца и проверить поток, поэтому каталог
		// остаётся у стадии ровно тогда, когда документ в нём действительно есть.
		if stage.AttachmentsDir != "" {
			if _, err := config.ResolveStageAttachments(name, stage.AttachmentsDir); err != nil {
				stage.AttachmentsDir = ""
			}
		}
		stages[name] = stage
	}
	return config.LLMConfig{Providers: source.Providers, Stages: stages}
}

// dryRunStageResponses — ответ заглушки на каждую стадию.
//
// Ключ — имя стадии: его заглушка берёт из запроса. Набор обязан покрывать стадии всех задач,
// а не только task_1: стадия без ответа роняет прогон с «dry-run has no response for stage».
//
// reviewReturnsArticle разводит два смысла стадии review. У task_1 она возвращает замечания,
// а исправленный текст приходит отдельной стадией fix. У задач без fix (pprof_1, pprof_2)
// готовый текст возвращает сама review — и заглушка с замечаниями положила бы их в
// fixed_article.txt вместо статьи, то есть прогон «прошёл бы» с мусором вместо результата.
func dryRunStageResponses(reviewReturnsArticle bool) map[string]string {
	reviewedArticle := strings.ReplaceAll(dryRunArticle, "[[ARTICLE_COMPLETE]]", "") + "\n\n[[ARTICLE_COMPLETE]]"
	responses := map[string]string{
		"structure": "H1 - Тестовая статья\nH2 - Основной раздел\nH2 - FAQ",
		"article":   dryRunArticle,
		"info":      dryRunInfo,
		"review":    "Структура и содержание подходят для локальной проверки. Критичных замечаний нет.",
		"fix":       reviewedArticle,
		"html":      dryRunHTML(),
		// Стадии собственных потоков задач. expert и seo_editor возвращают текст статьи:
		// в потоке pprof_1 они и есть автор и редактор.
		"expert":     dryRunArticle,
		"seo_editor": reviewedArticle,
		// keywords — резервный подбор запросов в prepare. В офлайн-прогоне research
		// подставляется напрямую, но ответ пусть будет: стадия объявлена в схеме задач.
		"keywords": "тестовый запрос\nпроверка dry-run",
	}
	if reviewReturnsArticle {
		responses["review"] = reviewedArticle
	}
	return responses
}

// dryRunHTML собирает разметку заглушки из её же текста статьи.
//
// Собранная отдельно, она разошлась бы с текстом: стадия html сверяет конец страницы с
// исходником и оборванную разметку не принимает, а офлайн-прогон обязан проходить ровно тот
// же путь, что и боевой. Пока разметка выводится из текста, правка заглушки не может
// уронить прогон на проверке покрытия.
func dryRunHTML() string {
	var markup strings.Builder
	for line := range strings.SplitSeq(strings.ReplaceAll(dryRunArticle, "[[ARTICLE_COMPLETE]]", ""), "\n") {
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		level := len(text) - len(strings.TrimLeft(text, "#"))
		if level == 0 {
			fmt.Fprintf(&markup, "<p>%s</p>\n", text)
			continue
		}
		fmt.Fprintf(&markup, "<h%d>%s</h%d>\n", level, strings.TrimSpace(text[level:]), level)
	}
	return strings.TrimSpace(markup.String())
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
	if err := writeRoutingReport(report, routing, articleStageOrder(profile)); err != nil {
		return fmt.Errorf("dry-run write routing report: %w", err)
	}

	workbook, err := importer.ResolveWorkbook(cfg.InputFilePath, cfg.InputDir)
	if err != nil {
		return fmt.Errorf("dry-run resolve Excel: %w", err)
	}
	inputs, err := importer.ReadArticles(workbook)
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
	// Набор ответов заглушки зависит от задачи: стадия review возвращает готовый текст
	// везде, где у задачи нет отдельной стадии fix.
	stub := dryRunClient{responses: dryRunStageResponses(!slices.Contains(profile.LLMStages, string(stageFix)))}
	clients := make(map[string]llm.Client, len(llmConfig.Providers))
	for name := range llmConfig.Providers {
		clients[name] = stub
	}
	router := llm.NewRouter(llmConfig, clients, logger)

	// Прогон идёт тем же путём, что и в бою: у задачи со своим потоком — её потоком, у
	// task_1 — общим конвейером. Иначе dry-run проверял бы не ту схему, которая выполнится:
	// у pprof_1 он падал на стадии fix, которой у задачи нет вовсе, а у pprof_2 — на полях
	// чужого промпта.
	flow, err := newTaskFlow(profile, taskFlowDeps{
		repository: articleRepository, writer: writer, router: router, logger: logger,
	})
	if err != nil {
		return fmt.Errorf("dry-run build task flow: %w", err)
	}
	runArticle := func(ctx context.Context, externalID string) (articleoutput.ArticlePaths, error) {
		if flow != nil {
			return runDryRunTaskFlow(ctx, flow, articleRepository, resultService, externalID)
		}
		output, runErr := generation.NewPipeline(articleRepository, router, stub, writer, logger, resultService).
			RunByExternalID(ctx, externalID)
		return output.Paths, runErr
	}

	for _, input := range inputs {
		selected, _, err := articleRepository.Import(ctx, input)
		if err != nil {
			return fmt.Errorf("dry-run import external_id %d: %w", input.ExcelID, err)
		}
		if err := seedDryRunResearch(ctx, articleRepository, selected.ID); err != nil {
			return err
		}
		paths, err := runArticle(ctx, selected.ExternalID)
		if err != nil {
			return fmt.Errorf("dry-run pipeline external_id %s: %w", selected.ExternalID, err)
		}
		if err := verifyDryRunResult(ctx, articleRepository, writer, selected.ExternalID, paths,
			!profile.WithoutMetadataStage, slices.Contains(profile.LLMStages, string(stageReview))); err != nil {
			return err
		}
		logger.Info("dry-run article verified", "article_id", selected.ID, "external_id", selected.ExternalID, "status", "completed", "result_path", paths.ResultPath)
	}
	return nil
}

// runDryRunTaskFlow проводит статью по собственному потоку задачи: три чата, затем сборка
// result.md и завершение статьи.
//
// Этапы те же и в том же порядке, что у боевого раннера, — отличается только источник
// research: в офлайн-прогоне он подставлен напрямую, без Keys.so и Arsenkin.
func runDryRunTaskFlow(
	ctx context.Context,
	flow taskFlow,
	articleRepository *repository.ArticleRepository,
	resultService *resultassembly.Service,
	externalID string,
) (articleoutput.ArticlePaths, error) {
	if err := flow.RunStructure(ctx, externalID); err != nil {
		return articleoutput.ArticlePaths{}, err
	}
	if err := flow.RunArticle(ctx, externalID); err != nil {
		return articleoutput.ArticlePaths{}, err
	}
	if err := flow.RunHTML(ctx, externalID); err != nil {
		return articleoutput.ArticlePaths{}, err
	}
	paths, err := resultService.Build(ctx, externalID)
	if err != nil {
		return articleoutput.ArticlePaths{}, err
	}
	input, err := articleRepository.GetResultInput(ctx, externalID)
	if err != nil {
		return articleoutput.ArticlePaths{}, err
	}
	if err := articleRepository.CompleteGeneration(ctx, input.Article.ID); err != nil {
		return articleoutput.ArticlePaths{}, err
	}
	return paths, nil
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

// verifyDryRunResult проверяет, что прогон оставил все артефакты и завершил статью.
//
// withMetadata снимает требование article_info у задачи без стадии info: файла у неё не
// будет никогда, и требовать его — значит объявить исправный прогон сломанным.
func verifyDryRunResult(ctx context.Context, articleRepository *repository.ArticleRepository, writer *articleoutput.Writer, externalID string, paths articleoutput.ArticlePaths, withMetadata, withReviewArtifacts bool) error {
	input, err := articleRepository.GetResultInput(ctx, externalID)
	if err != nil {
		return fmt.Errorf("dry-run verify state for external_id %s: %w", externalID, err)
	}
	if input.Article.Status != "completed" || input.Article.CurrentStep != nil {
		return fmt.Errorf("dry-run external_id %s finished with status=%q current_step=%v", externalID, input.Article.Status, input.Article.CurrentStep)
	}
	required := []string{
		paths.StructurePromptPath, paths.StructurePath,
		paths.ArticlePromptPath, paths.ArticlePath,
		paths.HTMLPromptPath, paths.HTMLPath, paths.ResultPath,
	}
	// Артефакты ревью спрашиваются только у задачи, которая его выполняет. У потока без
	// ревью отдельного текста и промпта нет вовсе: финальным текстом остаётся сама статья,
	// и требование сорвало бы офлайн-прогон на файле, которого никто не обещал.
	if withReviewArtifacts {
		required = append(required,
			paths.ReviewPromptPath, paths.ReviewPath,
			paths.FixPromptPath, paths.FixedArticlePath)
	}
	if withMetadata {
		required = append(required, paths.GenerationInfoPath)
	}
	for _, path := range required {
		if !writer.Exists(path) {
			return fmt.Errorf("dry-run required artifact %q is missing", path)
		}
	}
	return nil
}
