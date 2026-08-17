package generation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/diagnostics"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/tasks/task1/output"
)

type PipelineRepository interface {
	StructureRepository
	GetGenerationInput(ctx context.Context, externalID string) (article.GenerationInput, error)
	GetSavedGenerationInput(ctx context.Context, externalID string) (article.SavedGenerationInput, error)
	BeginGeneration(ctx context.Context, articleID int64) error
	BeginGenerationStage(ctx context.Context, articleID int64, stage string) error
	SaveGenerationPaths(ctx context.Context, articleID int64, structurePath, articlePath string) error
	SaveArticleInfo(ctx context.Context, articleID int64, rawText string, info article.ArticleInfo) error
	SaveDemoArticleInfo(ctx context.Context, articleID int64, articlePath, rawText string, info article.ArticleInfo) error
	SaveReviewPath(ctx context.Context, articleID int64, reviewPath string) error
	SaveFixedArticlePath(ctx context.Context, articleID int64, fixedArticlePath string) error
	SaveHTMLPath(ctx context.Context, articleID int64, htmlPath string) error
	SaveError(ctx context.Context, articleID int64, processingErr error) error
	GetDemoGenerationInput(ctx context.Context, externalID string) (article.GenerationInput, error)
	CompleteGeneration(ctx context.Context, articleID int64) error
	GetArticleTrace(ctx context.Context, articleID int64) (article.Trace, error)
}

type ResultBuilder interface {
	Build(ctx context.Context, externalID string) (articleoutput.ArticlePaths, error)
	BuildStaged(ctx context.Context, externalID string) (*articleoutput.PendingArtifact, error)
}

type PipelineWriter interface {
	StructureWriter
	StageArticle(externalID, slug, prompt, text, model string) (*articleoutput.PendingArtifact, error)
	StageArticleInfo(externalID, slug, prompt, info string) (*articleoutput.PendingArtifact, error)
	StageReview(externalID, slug, prompt, review string) (*articleoutput.PendingArtifact, error)
	StageFixedArticle(externalID, slug, prompt, article string) (*articleoutput.PendingArtifact, error)
	StageHTML(externalID, slug, prompt, html string) (*articleoutput.PendingArtifact, error)
	Read(relativePath string) (string, error)
}

type PipelineOutput struct {
	Paths articleoutput.ArticlePaths
}

type StageError struct {
	ArticleID  int64
	ExternalID string
	Stage      string
	Err        error
}

func (e *StageError) Error() string {
	return fmt.Sprintf("article_id=%d external_id=%s stage=%s: %v", e.ArticleID, e.ExternalID, e.Stage, e.Err)
}
func (e *StageError) Unwrap() error { return e.Err }

// Pipeline orchestrates all currently implemented generation stages.
type Pipeline struct {
	repository       PipelineRepository
	writer           PipelineWriter
	structureService *StructureService
	router           *llm.Router
	chatFactory      ChatFactory
	logger           *slog.Logger
	resultBuilder    ResultBuilder
	// promptPublisher выгружает готовый промпт статьи наружу. Необязателен: без него
	// пайплайн работает ровно как раньше. Подключается через SetPromptPublisher.
	promptPublisher PromptPublisher
}

func NewPipeline(repository PipelineRepository, router *llm.Router, chatFactory ChatFactory, writer PipelineWriter, logger *slog.Logger, resultBuilder ...ResultBuilder) *Pipeline {
	pipeline := &Pipeline{
		repository: repository, writer: writer,
		structureService: NewStructureService(repository, router, writer, logger), router: router,
		chatFactory: chatFactory, logger: logger,
	}
	if len(resultBuilder) > 0 {
		pipeline.resultBuilder = resultBuilder[0]
	}
	return pipeline
}

func (p *Pipeline) RunByExternalID(ctx context.Context, externalID string) (PipelineOutput, error) {
	input, err := p.repository.GetGenerationInput(ctx, externalID)
	if err != nil {
		wrapped := &StageError{ExternalID: externalID, Stage: "load_generation_data", Err: err}
		if !isContextCancellation(ctx, err) {
			p.logger.Error("generation pipeline failed", "article_id", int64(0), "external_id", externalID, "stage", wrapped.Stage, "error", err)
		}
		return PipelineOutput{}, wrapped
	}
	if err := assertLoadedArticle(externalID, input.Article); err != nil {
		return PipelineOutput{}, p.fail(ctx, p.stageLogger(input), input, "verify_article_identity", err)
	}
	return p.Run(ctx, input)
}

func (p *Pipeline) Run(ctx context.Context, input article.GenerationInput) (PipelineOutput, error) {
	started := time.Now()
	logger := p.logger.With("article_id", input.Article.ID, "external_id", input.Article.ExternalID)
	logger.Info("generation pipeline started", "stage", "generation_pipeline")
	if err := p.repository.BeginGeneration(ctx, input.Article.ID); err != nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "begin_generation", err)
	}
	logger.Info("article prepared for generation", "stage", "structure_generation")

	logger.Info("structure generation started", "stage", "structure_generation")
	structureOutput, err := p.structureService.Generate(ctx, input)
	if err != nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "structure_generation", err)
	}
	logger.Info("structure generation completed", "stage", "structure_generation", "result_path", structureOutput.Paths.StructurePath)
	if err := ctx.Err(); err != nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "structure_generation", err)
	}

	articleOutput, err := p.runArticleAndInfo(ctx, input, structureOutput.Structure, structureOutput.Paths.StructurePath, false)
	if err != nil {
		return PipelineOutput{}, err
	}
	articleText := articleOutput.Text
	paths := articleOutput.Paths

	_, reviewChat, err := p.runReview(ctx, input, articleText)
	if err != nil {
		return PipelineOutput{}, err
	}
	fixOutput, err := p.runFix(ctx, input, reviewChat, articleText)
	if closeErr := reviewChat.Close(); closeErr != nil {
		logger.Warn("не удалось закрыть чат ревью", "stage", "article_fix", "error", closeErr)
	}
	if err != nil {
		return PipelineOutput{}, err
	}
	htmlOutput, err := p.runHTML(ctx, input, fixOutput.Text)
	if err != nil {
		return PipelineOutput{}, err
	}
	paths = htmlOutput.Paths
	if p.resultBuilder == nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "result_generation", fmt.Errorf("result builder is not configured"))
	}
	// result.md печатает ссылку на документ с промптом, поэтому публикацию нужно дождаться.
	// Генерация к этому моменту закончена, задерживать больше нечего.
	p.waitPromptPublished()
	resultPaths, resultErr := p.resultBuilder.Build(ctx, input.Article.ExternalID)
	if resultErr != nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "result_generation", resultErr)
	}
	paths.ResultPath = resultPaths.ResultPath
	if err := p.repository.CompleteGeneration(ctx, input.Article.ID); err != nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "complete_generation", err)
	}
	logger.Info("generation pipeline completed", "stage", "generation_pipeline", "duration_ms", time.Since(started).Milliseconds())
	return PipelineOutput{Paths: paths}, nil
}

// RunDemoByExternalID resumes structure, article/info and result without review,
// fix or HTML. Persisted stages are never regenerated.
func (p *Pipeline) RunDemoByExternalID(ctx context.Context, externalID string) (PipelineOutput, error) {
	saved, err := p.repository.GetSavedGenerationInput(ctx, externalID)
	if err != nil {
		return PipelineOutput{}, &StageError{ExternalID: externalID, Stage: "load_demo_data", Err: err}
	}
	input := article.GenerationInput{Article: saved.Article, Professions: saved.Professions, Links: saved.Links}
	logger := p.stageLogger(input)
	if err := assertLoadedArticle(externalID, saved.Article); err != nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "verify_article_identity", err)
	}
	if input.Article.Status == "completed" {
		logger.Info("demo generation skipped: result already exists", "stage", "demo_complete")
		return PipelineOutput{}, nil
	}

	paths := articleoutput.ArticlePaths{}
	if strings.TrimSpace(saved.StructurePath) == "" {
		generationInput, loadErr := p.repository.GetGenerationInput(ctx, externalID)
		if loadErr != nil {
			return PipelineOutput{}, p.fail(ctx, logger, input, "load_structure_data", loadErr)
		}
		structureOutput, structureErr := p.structureService.Generate(ctx, generationInput)
		if structureErr != nil {
			return PipelineOutput{}, p.fail(ctx, logger, generationInput, "structure_generation", structureErr)
		}
		paths = structureOutput.Paths
		saved, err = p.repository.GetSavedGenerationInput(ctx, externalID)
		if err != nil {
			return PipelineOutput{}, p.fail(ctx, logger, input, "reload_structure_data", err)
		}
	}

	if strings.TrimSpace(saved.ArticlePath) == "" {
		articleOutput, articleErr := p.RunArticleByExternalID(ctx, externalID)
		if articleErr != nil {
			return PipelineOutput{}, articleErr
		}
		paths = articleOutput.Paths
	}
	if p.resultBuilder == nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "result_generation", fmt.Errorf("result builder is not configured"))
	}
	// result.md печатает ссылку на документ с промптом, поэтому публикацию нужно дождаться.
	// Генерация к этому моменту закончена, задерживать больше нечего.
	p.waitPromptPublished()
	resultPending, err := p.resultBuilder.BuildStaged(ctx, externalID)
	if err != nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "result_generation", err)
	}
	defer resultPending.Abort()
	if err := articleoutput.Commit(func() error {
		return p.repository.CompleteGeneration(ctx, input.Article.ID)
	}, resultPending); err != nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "complete_demo_generation", err)
	}
	paths.ResultPath = resultPending.Paths.ResultPath
	return PipelineOutput{Paths: paths}, nil
}

// RunQuickDemoByExternalID preserves the original run command flow: article,
// info and result without prepared research or a generated structure.
func (p *Pipeline) RunQuickDemoByExternalID(ctx context.Context, externalID string) (PipelineOutput, error) {
	input, err := p.repository.GetDemoGenerationInput(ctx, externalID)
	if err != nil {
		return PipelineOutput{}, &StageError{ExternalID: externalID, Stage: "load_demo_data", Err: err}
	}
	logger := p.stageLogger(input)
	if err := assertLoadedArticle(externalID, input.Article); err != nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "verify_article_identity", err)
	}
	if err := p.repository.BeginGenerationStage(ctx, input.Article.ID, "article"); err != nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "begin_demo_generation", err)
	}
	output, err := p.runArticleAndInfo(ctx, input, "", "", true)
	if err != nil {
		return PipelineOutput{}, err
	}
	if p.resultBuilder == nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "result_generation", fmt.Errorf("result builder is not configured"))
	}
	// result.md печатает ссылку на документ с промптом, поэтому публикацию нужно дождаться.
	// Генерация к этому моменту закончена, задерживать больше нечего.
	p.waitPromptPublished()
	resultPending, err := p.resultBuilder.BuildStaged(ctx, externalID)
	if err != nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "result_generation", err)
	}
	defer resultPending.Abort()
	if err := articleoutput.Commit(func() error {
		return p.repository.CompleteGeneration(ctx, input.Article.ID)
	}, resultPending); err != nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "complete_demo_generation", err)
	}
	output.Paths.ResultPath = resultPending.Paths.ResultPath
	return PipelineOutput{Paths: output.Paths}, nil
}

type stageOutput struct {
	Text  string
	Paths articleoutput.ArticlePaths
}

type articleStageOutput struct {
	stageOutput
	Info string
}

func (p *Pipeline) runArticleAndInfo(ctx context.Context, input article.GenerationInput, structure, structurePath string, atomicDemo bool) (output articleStageOutput, returnErr error) {
	started := time.Now()
	logger := p.stageLogger(input)
	if err := ctx.Err(); err != nil {
		return articleStageOutput{}, p.fail(ctx, logger, input, "article_generation", err)
	}
	logger.Info("article generation started", "stage", "article_generation")
	trace, err := p.articleTrace(ctx, logger, input)
	if err != nil {
		return articleStageOutput{}, p.fail(ctx, logger, input, "verify_article_identity", err)
	}
	diagnostics.LogStep(p.logger, "llm_article", "before", trace,
		"structure_fingerprint", diagnostics.Fingerprint(structure),
		"keywords_count", len(input.WordstatKeywords),
		"keywords_fingerprint", diagnostics.Fingerprint(formatKeywords(input.WordstatKeywords)),
	)
	articleResult, err := p.router.Generate(ctx, llm.Call{Stage: "article", ArticleID: input.Article.ID, Data: struct {
		Title              string
		Keywords           string
		LSIWords           string
		GeneratedStructure string
	}{
		Title: input.Article.Title, Keywords: formatKeywords(input.WordstatKeywords),
		LSIWords: strings.Join(input.LSIWords, "\n"), GeneratedStructure: structure,
	}})
	if err != nil {
		return articleStageOutput{}, p.fail(ctx, logger, input, "article_generation", err)
	}
	text := strings.TrimSpace(strings.ReplaceAll(articleResult.Text, "[[ARTICLE_COMPLETE]]", ""))
	if text == "" {
		return articleStageOutput{}, p.fail(ctx, logger, input, "article_generation", fmt.Errorf("article returned an empty response"))
	}
	if err := ctx.Err(); err != nil {
		return articleStageOutput{}, p.fail(ctx, logger, input, "article_generation", err)
	}
	logger.Info("article generated", "stage", "article_generation", "model", articleResult.Model, "prompt_size", len([]rune(articleResult.Prompt)), "input_tokens", articleResult.InputTokens, "output_tokens", articleResult.OutputTokens, "duration_ms", time.Since(started).Milliseconds())
	articlePending, err := p.writer.StageArticle(input.Article.ExternalID, input.Article.Slug, articleResult.Prompt, text, articleResult.Model)
	if err != nil {
		return articleStageOutput{}, p.fail(ctx, logger, input, "save_article", err)
	}
	defer articlePending.Abort()
	paths := articlePending.Paths
	if !atomicDemo {
		if err := articleoutput.Commit(func() error {
			return p.repository.SaveGenerationPaths(ctx, input.Article.ID, structurePath, paths.ArticlePath)
		}, articlePending); err != nil {
			return articleStageOutput{}, p.fail(ctx, logger, input, "save_article_path", err)
		}
	}
	logger.Info("article saved", "stage", "article_generation", "model", articleResult.Model, "result_path", paths.ArticlePath)
	// Промпт статьи уже опубликован на диске и записан в состояние, поэтому его можно
	// выгружать наружу. Вызов не блокирующий по контракту PromptPublisher: стадия info идёт
	// следом и не ждёт чужого браузера. Ошибки публикации сюда не возвращаются — они не имеют
	// права уронить генерацию, за которую уже заплачено.
	p.publishArticlePrompt(ArticlePromptJob{
		ArticleID:  input.Article.ID,
		ExternalID: input.Article.ExternalID,
		Title:      input.Article.Title,
		Prompt:     articleResult.Prompt,
		PromptPath: paths.ArticlePromptPath,
	})
	diagnostics.LogStep(p.logger, "llm_article", "after", trace,
		"prompt_fingerprint", diagnostics.Fingerprint(articleResult.Prompt),
		"response_fingerprint", diagnostics.Fingerprint(text),
		"result_path", paths.ArticlePath,
	)

	infoStarted := time.Now()
	if err := ctx.Err(); err != nil {
		return articleStageOutput{}, p.fail(ctx, logger, input, "metadata_generation", err)
	}
	logger.Info("article info generation started", "stage", "info")
	diagnostics.LogStep(p.logger, "llm_info", "before", trace,
		"article_fingerprint", diagnostics.Fingerprint(text),
	)
	// Информация для публикации собирается отдельным вызовом: текст статьи передаётся
	// в промпт явно, а не остаётся в истории общего чата. Стадия перестала зависеть
	// от того, помнит ли провайдер предыдущее сообщение.
	infoResult, err := p.router.Generate(ctx, llm.Call{Stage: "info", ArticleID: input.Article.ID, Data: struct {
		GeneratedStructure string
		GeneratedArticle   string
	}{GeneratedStructure: structure, GeneratedArticle: text}})
	if err != nil {
		return articleStageOutput{}, p.fail(ctx, logger, input, "metadata_generation", err)
	}
	articleInfo := infoResult.Text
	if strings.TrimSpace(articleInfo) == "" {
		err := fmt.Errorf("article info returned an empty response")
		return articleStageOutput{}, p.fail(ctx, logger, input, "metadata_generation", err)
	}
	if err := ctx.Err(); err != nil {
		return articleStageOutput{}, p.fail(ctx, logger, input, "metadata_generation", err)
	}
	logger.Info("article info generated", "stage", "info", "model", infoResult.Model, "prompt_size", len([]rune(infoResult.Prompt)), "input_tokens", infoResult.InputTokens, "output_tokens", infoResult.OutputTokens, "duration_ms", time.Since(infoStarted).Milliseconds())
	logger.Info("article info parsing started", "stage", "info")
	parsedInfo, err := article.ParseArticleInfo(articleInfo)
	if err != nil {
		return articleStageOutput{}, p.fail(ctx, logger, input, "metadata_parsing", err)
	}
	if parsedInfo.FallbackUsed {
		logger.Warn("metadata parsing incomplete, recognized and raw response content saved", "stage", "info",
			"has_tags", parsedInfo.Tags != "", "has_tldr", parsedInfo.TLDR != "", "has_faq", parsedInfo.FAQ != "",
			"has_additional_info", parsedInfo.AdditionalInfo != "")
	} else {
		logger.Info("article info parsed", "stage", "info")
	}
	if atomicDemo {
		infoPending, stageErr := p.writer.StageArticleInfo(input.Article.ExternalID, input.Article.Slug, infoResult.Prompt, articleInfo)
		err = stageErr
		if err != nil {
			return articleStageOutput{}, p.fail(ctx, logger, input, "save_article_info_files", err)
		}
		defer infoPending.Abort()
		paths = infoPending.Paths
		err = articleoutput.Commit(func() error {
			return p.repository.SaveDemoArticleInfo(ctx, input.Article.ID, paths.ArticlePath, articleInfo, parsedInfo)
		}, articlePending, infoPending)
	} else {
		err = p.repository.SaveArticleInfo(ctx, input.Article.ID, articleInfo, parsedInfo)
	}
	if err != nil {
		return articleStageOutput{}, p.fail(ctx, logger, input, "save_article_info_state", err)
	}
	logger.Info("article info saved", "stage", "info")
	diagnostics.LogStep(p.logger, "llm_info", "after", trace,
		"prompt_fingerprint", diagnostics.Fingerprint(infoResult.Prompt),
		"response_fingerprint", diagnostics.Fingerprint(articleInfo),
	)
	logger.Info("article stage completed", "stage", "article_generation", "duration_ms", time.Since(started).Milliseconds(), "result_path", paths.ArticlePath)
	return articleStageOutput{stageOutput: stageOutput{Text: text, Paths: paths}, Info: articleInfo}, nil
}

// runReview выполняет ревью и возвращает чат, в котором оно выполнено: стадия fix
// продолжает этот же чат и опирается на историю, а не на повторную передачу статьи.
func (p *Pipeline) runReview(ctx context.Context, input article.GenerationInput, articleText string) (stageOutput, llm.Chat, error) {
	started := time.Now()
	logger := p.logger.With("article_id", input.Article.ID, "external_id", input.Article.ExternalID)
	if err := ctx.Err(); err != nil {
		return stageOutput{}, nil, p.fail(ctx, logger, input, "article_review", err)
	}
	logger.Info("article review started", "stage", "article_review")
	trace, err := p.traceLLMStage(ctx, logger, input, "llm_review", articleText)
	if err != nil {
		return stageOutput{}, nil, p.fail(ctx, logger, input, "verify_article_identity", err)
	}
	reviewCall, err := p.reviewCall(articleText)
	if err != nil {
		return stageOutput{}, nil, p.fail(ctx, logger, input, "article_review", err)
	}
	chat, err := p.chatFactory.NewChat(ctx, input.Article.ID)
	if err != nil {
		return stageOutput{}, nil, p.fail(ctx, logger, input, "article_review", err)
	}
	result, err := chat.Generate(ctx, reviewCall.Prompt)
	if err != nil {
		_ = chat.Close()
		return stageOutput{}, nil, p.fail(ctx, logger, input, "article_review", err)
	}
	text := strings.TrimSpace(result.Text)
	if text == "" {
		_ = chat.Close()
		return stageOutput{}, nil, p.fail(ctx, logger, input, "article_review", fmt.Errorf("review returned an empty response"))
	}
	if err := ctx.Err(); err != nil {
		_ = chat.Close()
		return stageOutput{}, nil, p.fail(ctx, logger, input, "article_review", err)
	}
	pending, err := p.writer.StageReview(input.Article.ExternalID, input.Article.Slug, reviewCall.Prompt, text)
	if err != nil {
		_ = chat.Close()
		return stageOutput{}, nil, p.fail(ctx, logger, input, "save_article_review", err)
	}
	defer pending.Abort()
	paths := pending.Paths
	if err := articleoutput.Commit(func() error {
		return p.repository.SaveReviewPath(ctx, input.Article.ID, paths.ReviewPath)
	}, pending); err != nil {
		_ = chat.Close()
		return stageOutput{}, nil, p.fail(ctx, logger, input, "save_article_review_path", err)
	}
	logger.Info("article review completed", "stage", "article_review", "prompt_size", len([]rune(reviewCall.Prompt)), "input_tokens", result.InputTokens, "output_tokens", result.OutputTokens, "duration_ms", time.Since(started).Milliseconds(), "result_path", paths.ReviewPath)
	diagnostics.LogStep(p.logger, "llm_review", "after", trace,
		"prompt_fingerprint", diagnostics.Fingerprint(reviewCall.Prompt),
		"response_fingerprint", diagnostics.Fingerprint(text),
		"result_path", paths.ReviewPath,
	)
	return stageOutput{Text: text, Paths: paths}, chat, nil
}

// reviewCall рендерит промпт ревью. Он же служит первым сообщением истории, когда стадия
// fix выполняется отдельным прогоном.
func (p *Pipeline) reviewCall(articleText string) (llm.PreparedCall, error) {
	return p.router.Prepare(llm.Call{Stage: "review", Data: struct{ Article string }{articleText}})
}

// runFix продолжает чат ревью вторым сообщением. Промпт стадии больше не содержит статью
// и ревью — они уже в истории этого чата, поэтому повторно не передаются.
func (p *Pipeline) runFix(ctx context.Context, input article.GenerationInput, chat llm.Chat, articleText string) (stageOutput, error) {
	started := time.Now()
	logger := p.logger.With("article_id", input.Article.ID, "external_id", input.Article.ExternalID)
	if err := ctx.Err(); err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "article_fix", err)
	}
	logger.Info("article fix started", "stage", "article_fix")
	trace, err := p.traceLLMStage(ctx, logger, input, "llm_fix", articleText)
	if err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "verify_article_identity", err)
	}
	fixCall, err := p.router.Prepare(llm.Call{Stage: "fix", Data: struct {
		Professions, Links string
	}{input.Professions, input.Links}})
	if err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "article_fix", err)
	}
	result, err := chat.Generate(ctx, fixCall.Prompt)
	if err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "article_fix", err)
	}
	text := strings.TrimSpace(strings.ReplaceAll(result.Text, "[[ARTICLE_COMPLETE]]", ""))
	if text == "" {
		return stageOutput{}, p.fail(ctx, logger, input, "article_fix", fmt.Errorf("fix returned an empty response"))
	}
	if err := ctx.Err(); err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "article_fix", err)
	}
	pending, err := p.writer.StageFixedArticle(input.Article.ExternalID, input.Article.Slug, fixCall.Prompt, text)
	if err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "save_fixed_article", err)
	}
	defer pending.Abort()
	paths := pending.Paths
	if err := articleoutput.Commit(func() error {
		return p.repository.SaveFixedArticlePath(ctx, input.Article.ID, paths.FixedArticlePath)
	}, pending); err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "save_fixed_article_path", err)
	}
	logger.Info("article fix completed", "stage", "article_fix", "prompt_size", len([]rune(fixCall.Prompt)), "input_tokens", result.InputTokens, "output_tokens", result.OutputTokens, "duration_ms", time.Since(started).Milliseconds(), "result_path", paths.FixedArticlePath)
	diagnostics.LogStep(p.logger, "llm_fix", "after", trace,
		"prompt_fingerprint", diagnostics.Fingerprint(fixCall.Prompt),
		"response_fingerprint", diagnostics.Fingerprint(text),
		"result_path", paths.FixedArticlePath,
	)
	return stageOutput{Text: text, Paths: paths}, nil
}

// htmlResponseDumpName holds the model response that failed validation, next to the run logs:
// generated/ is wiped before a repeated run, and the dump has to outlive it.
const htmlResponseDumpName = "validate_html_failed.txt"

// htmlResponseDumper writes a plain-text diagnostics file into the article directory. Объявлен
// у потребителя и намеренно узкий: дамп нужен одной стадии, и реализовывать его в каждом
// тестовом writer-е незачем.
type htmlResponseDumper interface {
	SaveDiagnosticsText(externalID, slug, subdirectory, name, content string) (string, error)
}

// Дамп берётся type assertion-ом, поэтому расхождение с боевым writer-ом иначе всплыло бы
// только на упавшем прогоне — и ровно тогда, когда дамп и нужен.
var _ htmlResponseDumper = (*articleoutput.Writer)(nil)

// dumpHTMLResponse сохраняет ответ модели, на котором упала валидация: без него разбор
// падения остаётся вслепую. Ошибка записи дампа не подменяет ошибку стадии — она только
// уходит в лог, иначе диагностика скрыла бы настоящую причину остановки.
func (p *Pipeline) dumpHTMLResponse(logger *slog.Logger, input article.GenerationInput, response string) {
	dumper, ok := p.writer.(htmlResponseDumper)
	if !ok {
		return
	}
	path, err := dumper.SaveDiagnosticsText(input.Article.ExternalID, input.Article.Slug, articleoutput.LogsSubdirectory, htmlResponseDumpName, response)
	if err != nil {
		logger.Warn("failed to save raw HTML response", "stage", "validate_html", "error", err)
		return
	}
	logger.Info("raw HTML response saved", "stage", "validate_html", "result_path", path)
}

func (p *Pipeline) runHTML(ctx context.Context, input article.GenerationInput, fixedArticle string) (stageOutput, error) {
	started := time.Now()
	logger := p.logger.With("article_id", input.Article.ID, "external_id", input.Article.ExternalID)
	if err := ctx.Err(); err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "html_generation", err)
	}
	logger.Info("HTML generation started", "stage", "html_generation")
	trace, err := p.traceLLMStage(ctx, logger, input, "llm_html", fixedArticle)
	if err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "verify_article_identity", err)
	}
	result, err := p.router.Generate(ctx, llm.Call{Stage: "html", ArticleID: input.Article.ID, Data: struct{ Article string }{fixedArticle}})
	if err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "html_generation", err)
	}
	html, cleanup, err := normalizeAndValidateHTML(result.Text)
	if cleanup.Applied() {
		logger.Warn("markdown wrapper removed from HTML response", "stage", "validate_html", "cleanup", cleanup.Kind, "size_before", cleanup.SizeBefore, "size_after", cleanup.SizeAfter)
	}
	if err != nil {
		p.dumpHTMLResponse(logger, input, result.Text)
		return stageOutput{}, p.fail(ctx, logger, input, "validate_html", err)
	}
	if err := ctx.Err(); err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "html_generation", err)
	}
	pending, err := p.writer.StageHTML(input.Article.ExternalID, input.Article.Slug, result.Prompt, html)
	if err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "save_html", err)
	}
	defer pending.Abort()
	paths := pending.Paths
	if err := articleoutput.Commit(func() error {
		return p.repository.SaveHTMLPath(ctx, input.Article.ID, paths.HTMLPath)
	}, pending); err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "save_html_path", err)
	}
	logger.Info("HTML generation completed", "stage", "html_generation", "prompt_size", len([]rune(result.Prompt)), "input_tokens", result.InputTokens, "output_tokens", result.OutputTokens, "duration_ms", time.Since(started).Milliseconds(), "result_path", paths.HTMLPath)
	diagnostics.LogStep(p.logger, "llm_html", "after", trace,
		"prompt_fingerprint", diagnostics.Fingerprint(result.Prompt),
		"response_fingerprint", diagnostics.Fingerprint(html),
		"result_path", paths.HTMLPath,
	)
	return stageOutput{Text: html, Paths: paths}, nil
}

func (p *Pipeline) RunArticleByExternalID(ctx context.Context, externalID string) (PipelineOutput, error) {
	input, err := p.repository.GetGenerationInput(ctx, externalID)
	if err != nil {
		wrapped := &StageError{ExternalID: externalID, Stage: "load_article_data", Err: err}
		if !isContextCancellation(ctx, err) {
			p.logger.Error("generation stage failed", "article_id", int64(0), "external_id", externalID, "stage", wrapped.Stage, "error", err)
		}
		return PipelineOutput{}, wrapped
	}
	if err := assertLoadedArticle(externalID, input.Article); err != nil {
		return PipelineOutput{}, p.fail(ctx, p.stageLogger(input), input, "verify_article_identity", err)
	}
	saved, err := p.repository.GetSavedGenerationInput(ctx, externalID)
	if err != nil {
		return PipelineOutput{}, p.fail(ctx, p.stageLogger(input), input, "load_article_artifacts", err)
	}
	if err := assertLoadedArticle(externalID, saved.Article); err != nil {
		return PipelineOutput{}, p.fail(ctx, p.stageLogger(input), input, "verify_article_identity", err)
	}
	structure, err := p.readRequiredArtifact(ctx, input, "article", "structure", saved.StructurePath)
	if err != nil {
		return PipelineOutput{}, err
	}
	if err := p.repository.BeginGenerationStage(ctx, input.Article.ID, "article"); err != nil {
		return PipelineOutput{}, p.fail(ctx, p.stageLogger(input), input, "begin_article", err)
	}
	output, err := p.runArticleAndInfo(ctx, input, structure, saved.StructurePath, false)
	return PipelineOutput{Paths: output.Paths}, err
}

// RunStructureByExternalID generates only the structure from prepared research. Остальные
// стадии уже адресуемы по имени; структура была доступна лишь внутри полного прогона, из-за
// чего возобновление с неё требовало перегенерации всей статьи.
func (p *Pipeline) RunStructureByExternalID(ctx context.Context, externalID string) (PipelineOutput, error) {
	input, err := p.repository.GetGenerationInput(ctx, externalID)
	if err != nil {
		wrapped := &StageError{ExternalID: externalID, Stage: "load_structure_data", Err: err}
		if !isContextCancellation(ctx, err) {
			p.logger.Error("generation stage failed", "article_id", int64(0), "external_id", externalID, "stage", wrapped.Stage, "error", err)
		}
		return PipelineOutput{}, wrapped
	}
	if err := assertLoadedArticle(externalID, input.Article); err != nil {
		return PipelineOutput{}, p.fail(ctx, p.stageLogger(input), input, "verify_article_identity", err)
	}
	if err := p.repository.BeginGeneration(ctx, input.Article.ID); err != nil {
		return PipelineOutput{}, p.fail(ctx, p.stageLogger(input), input, "begin_structure", err)
	}
	output, err := p.structureService.Generate(ctx, input)
	if err != nil {
		return PipelineOutput{}, p.fail(ctx, p.stageLogger(input), input, "structure_generation", err)
	}
	return PipelineOutput{Paths: output.Paths}, nil
}

func (p *Pipeline) RunReviewByExternalID(ctx context.Context, externalID string) (PipelineOutput, error) {
	saved, input, err := p.loadSavedInput(ctx, externalID, "review")
	if err != nil {
		return PipelineOutput{}, err
	}
	articleText, err := p.readRequiredArtifact(ctx, input, "review", "article", saved.ArticlePath)
	if err != nil {
		return PipelineOutput{}, err
	}
	if err := p.repository.BeginGenerationStage(ctx, input.Article.ID, "review"); err != nil {
		return PipelineOutput{}, p.fail(ctx, p.stageLogger(input), input, "begin_review", err)
	}
	output, chat, err := p.runReview(ctx, input, articleText)
	if chat != nil {
		if closeErr := chat.Close(); closeErr != nil {
			p.stageLogger(input).Warn("не удалось закрыть чат ревью", "stage", "article_review", "error", closeErr)
		}
	}
	return PipelineOutput{Paths: output.Paths}, err
}

func (p *Pipeline) RunFixByExternalID(ctx context.Context, externalID string) (PipelineOutput, error) {
	saved, input, err := p.loadSavedInput(ctx, externalID, "fix")
	if err != nil {
		return PipelineOutput{}, err
	}
	articleText, err := p.readRequiredArtifact(ctx, input, "fix", "article", saved.ArticlePath)
	if err != nil {
		return PipelineOutput{}, err
	}
	reviewText, err := p.readRequiredArtifact(ctx, input, "fix", "review", saved.ReviewPath)
	if err != nil {
		return PipelineOutput{}, err
	}
	if err := p.repository.BeginGenerationStage(ctx, input.Article.ID, "fix"); err != nil {
		return PipelineOutput{}, p.fail(ctx, p.stageLogger(input), input, "begin_fix", err)
	}
	// Отдельный прогон стадии fix продолжает тот же чат: история восстанавливается из
	// сохранённых артефактов, без повторного запроса ревью у модели.
	chat, err := p.resumeReviewChat(ctx, input, articleText, reviewText)
	if err != nil {
		return PipelineOutput{}, p.fail(ctx, p.stageLogger(input), input, "article_fix", err)
	}
	output, err := p.runFix(ctx, input, chat, articleText)
	if closeErr := chat.Close(); closeErr != nil {
		p.stageLogger(input).Warn("не удалось закрыть чат ревью", "stage", "article_fix", "error", closeErr)
	}
	return PipelineOutput{Paths: output.Paths}, err
}

// resumeReviewChat восстанавливает чат ревью из сохранённых статьи и ревью.
func (p *Pipeline) resumeReviewChat(ctx context.Context, input article.GenerationInput, articleText, reviewText string) (llm.Chat, error) {
	reviewCall, err := p.reviewCall(articleText)
	if err != nil {
		return nil, err
	}
	return p.chatFactory.NewChatWithHistory(ctx, input.Article.ID,
		llm.Message{Role: "user", Content: reviewCall.Prompt},
		llm.Message{Role: "assistant", Content: reviewText},
	)
}

func (p *Pipeline) RunHTMLByExternalID(ctx context.Context, externalID string) (PipelineOutput, error) {
	saved, input, err := p.loadSavedInput(ctx, externalID, "html")
	if err != nil {
		return PipelineOutput{}, err
	}
	fixedArticle, err := p.readRequiredArtifact(ctx, input, "html", "fixed article", saved.FixedArticlePath)
	if err != nil {
		return PipelineOutput{}, err
	}
	if err := p.repository.BeginGenerationStage(ctx, input.Article.ID, "html"); err != nil {
		return PipelineOutput{}, p.fail(ctx, p.stageLogger(input), input, "begin_html", err)
	}
	output, err := p.runHTML(ctx, input, fixedArticle)
	return PipelineOutput{Paths: output.Paths}, err
}

func (p *Pipeline) loadSavedInput(ctx context.Context, externalID, stage string) (article.SavedGenerationInput, article.GenerationInput, error) {
	saved, err := p.repository.GetSavedGenerationInput(ctx, externalID)
	if err != nil {
		wrapped := &StageError{ExternalID: externalID, Stage: "load_" + stage + "_data", Err: err}
		if !isContextCancellation(ctx, err) {
			p.logger.Error("generation stage failed", "article_id", int64(0), "external_id", externalID, "stage", wrapped.Stage, "error", err)
		}
		return article.SavedGenerationInput{}, article.GenerationInput{}, wrapped
	}
	input := article.GenerationInput{Article: saved.Article, Professions: saved.Professions, Links: saved.Links}
	if err := assertLoadedArticle(externalID, saved.Article); err != nil {
		return article.SavedGenerationInput{}, article.GenerationInput{},
			p.fail(ctx, p.stageLogger(input), input, "verify_article_identity", err)
	}
	return saved, input, nil
}

// articleTraceReader re-reads one article identity straight from PostgreSQL.
type articleTraceReader interface {
	GetArticleTrace(ctx context.Context, articleID int64) (article.Trace, error)
}

func (p *Pipeline) articleTrace(ctx context.Context, logger *slog.Logger, input article.GenerationInput) (article.Trace, error) {
	return articleTrace(ctx, logger, p.repository, input)
}

// articleTrace re-reads the article identity from PostgreSQL right before the LLM call and
// verifies it still matches the input the stage is about to send.
func articleTrace(ctx context.Context, logger *slog.Logger, reader articleTraceReader, input article.GenerationInput) (article.Trace, error) {
	inMemory := article.Trace{
		ArticleID: input.Article.ID, ExternalID: input.Article.ExternalID, Title: input.Article.Title,
	}
	stored, err := reader.GetArticleTrace(ctx, input.Article.ID)
	if err != nil {
		if isContextCancellation(ctx, err) {
			return article.Trace{}, err
		}
		logger.Warn("не удалось прочитать идентичность статьи для трассировки", "stage", "identity_trace", "error", err)
		return inMemory, nil
	}
	if mismatchErr := diagnostics.TraceMismatch(inMemory, stored); mismatchErr != nil {
		return article.Trace{}, mismatchErr
	}
	return stored, nil
}

// traceLLMStage re-reads the article identity and records what one LLM stage is about to send.
func (p *Pipeline) traceLLMStage(ctx context.Context, logger *slog.Logger, input article.GenerationInput, stage, inputText string) (article.Trace, error) {
	trace, err := p.articleTrace(ctx, logger, input)
	if err != nil {
		return article.Trace{}, err
	}
	diagnostics.LogStep(p.logger, stage, "before", trace, "input_fingerprint", diagnostics.Fingerprint(inputText))
	return trace, nil
}

// assertLoadedArticle guards against a lookup answering with another article's data.
func assertLoadedArticle(requestedExternalID string, loaded article.Article) error {
	if loaded.ExternalID == requestedExternalID {
		return nil
	}
	return fmt.Errorf(
		"запрошена статья external_id=%s, а загружена article_id=%d external_id=%s title=%q",
		requestedExternalID, loaded.ID, loaded.ExternalID, loaded.Title,
	)
}

// assertArtifactOwner guards against reading an artifact from another article's directory:
// every path is <external_id>-<slug>/... relative to OUTPUT_DIR.
func assertArtifactOwner(externalID, name, path string) error {
	directory := strings.SplitN(strings.TrimPrefix(filepath.ToSlash(path), "/"), "/", 2)[0]
	if strings.HasPrefix(directory, externalID+"-") {
		return nil
	}
	return fmt.Errorf("сохранённый %s принадлежит другой статье: путь %q не начинается с %q", name, path, externalID+"-")
}

func (p *Pipeline) readRequiredArtifact(ctx context.Context, input article.GenerationInput, stage, name, path string) (string, error) {
	logger := p.stageLogger(input)
	if err := ctx.Err(); err != nil {
		return "", p.fail(ctx, logger, input, stage, err)
	}
	if strings.TrimSpace(path) == "" {
		return "", p.fail(ctx, logger, input, stage, fmt.Errorf("article_id=%d external_id=%s: missing saved %s result", input.Article.ID, input.Article.ExternalID, name))
	}
	if err := assertArtifactOwner(input.Article.ExternalID, name, path); err != nil {
		return "", p.fail(ctx, logger, input, stage, err)
	}
	text, err := p.writer.Read(path)
	if err != nil {
		return "", p.fail(ctx, logger, input, stage, fmt.Errorf("article_id=%d external_id=%s: read saved %s result: %w", input.Article.ID, input.Article.ExternalID, name, err))
	}
	if strings.TrimSpace(text) == "" {
		return "", p.fail(ctx, logger, input, stage, fmt.Errorf("article_id=%d external_id=%s: saved %s result is empty", input.Article.ID, input.Article.ExternalID, name))
	}
	return text, nil
}

func (p *Pipeline) stageLogger(input article.GenerationInput) *slog.Logger {
	return p.logger.With("article_id", input.Article.ID, "external_id", input.Article.ExternalID)
}

func (p *Pipeline) fail(ctx context.Context, logger *slog.Logger, input article.GenerationInput, stage string, err error) error {
	wrapped := &StageError{ArticleID: input.Article.ID, ExternalID: input.Article.ExternalID, Stage: stage, Err: err}
	if isContextCancellation(ctx, err) {
		return wrapped
	}
	logger.Error("generation pipeline failed", "stage", stage, "error", err)
	if saveErr := p.repository.SaveError(ctx, input.Article.ID, wrapped); saveErr != nil {
		return errors.Join(wrapped, fmt.Errorf("сохранить ошибку статьи: %w", saveErr))
	}
	return wrapped
}

func isContextCancellation(ctx context.Context, err error) bool {
	return errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled)
}

func formatKeywords(keywords []article.KeywordFrequency) string {
	return article.FormatKeywords(keywords)
}
