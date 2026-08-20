package pprof1

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/generation"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/pipeline/output"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/taskflow"
)

// Repository — то, что поток требует от хранилища. Интерфейс объявлен у потребителя и
// перечисляет ровно используемые методы: реализует его существующий репозиторий движка.
type Repository interface {
	GetGenerationInput(ctx context.Context, externalID string) (article.GenerationInput, error)
	GetSavedGenerationInput(ctx context.Context, externalID string) (article.SavedGenerationInput, error)
	BeginGeneration(ctx context.Context, articleID int64) error
	BeginGenerationStage(ctx context.Context, articleID int64, stage string) error
	SaveStructurePath(ctx context.Context, articleID int64, structurePath string) error
	SaveGenerationPaths(ctx context.Context, articleID int64, structurePath, articlePath string) error
	SaveArticleInfo(ctx context.Context, articleID int64, rawText string, info article.ArticleInfo) error
	SaveReviewPath(ctx context.Context, articleID int64, reviewPath string) error
	SaveFixedArticlePath(ctx context.Context, articleID int64, fixedArticlePath string) error
	SaveHTMLPath(ctx context.Context, articleID int64, htmlPath string) error
	SaveError(ctx context.Context, articleID int64, processingErr error) error
}

// Writer — то, что поток требует от файлового слоя. Все методы уже есть у writer движка:
// артефакты pprof_1 ложатся в те же слоты, что и артефакты task_1, поэтому сборка result.md
// и очистка статьи работают без изменений.
type Writer interface {
	StageStructure(externalID, slug, prompt, structure string) (*articleoutput.PendingArtifact, error)
	StageArticle(externalID, slug, prompt, text, model string) (*articleoutput.PendingArtifact, error)
	StageArticleInfo(externalID, slug, prompt, info string) (*articleoutput.PendingArtifact, error)
	StageReview(externalID, slug, prompt, review string) (*articleoutput.PendingArtifact, error)
	StageFixedArticle(externalID, slug, prompt, article string) (*articleoutput.PendingArtifact, error)
	StageHTML(externalID, slug, prompt, html string) (*articleoutput.PendingArtifact, error)
	Read(relativePath string) (string, error)
}

// Этапы возобновления в БД. Словарь здесь чужой: репозиторий знает про article, info, review,
// fix и html и сам переводит их в current_step, а стадии pprof_1 называются иначе. Передавать
// сюда значения current_step нельзя — репозиторий их не принимает.
const (
	stepArticle = "article"
	stepHTML    = "html"
)

// Flow — поток генерации pprof_1.
//
// Три чата, и границы между ними значимы:
//
//	Чат 1: structure
//	Чат 2: expert → seo_editor → info → review
//	Чат 3: html
//
// Внутри чата 2 каждое следующее сообщение опирается на историю: промпты так и написаны
// («работай со статьёй, сгенерированной выше»), и повторно текст статьи не передаётся.
type Flow struct {
	*taskflow.Base
	repository Repository
	writer     Writer
}

// NewFlow собирает поток. publisher необязателен и может быть nil.
//
// Репозиторий и writer уходят в общий каркас и остаются здесь: каркасу от них нужны чтение
// структуры и запись ошибки, а слоты артефактов — дело самого потока.
func NewFlow(repository Repository, writer Writer, chats taskflow.ChatFactory,
	prompts taskflow.PromptRenderer, logger *slog.Logger, publisher taskflow.PromptPublisher) *Flow {
	return &Flow{
		Base:       taskflow.NewBase(repository, writer, chats, prompts, logger, publisher),
		repository: repository,
		writer:     writer,
	}
}

// RunStructure выполняет чат 1. Отдельный чат нужен потому, что структура — вход для всей
// остальной работы, и историю её обсуждения тащить в текст статьи незачем.
func (f *Flow) RunStructure(ctx context.Context, externalID string) error {
	input, err := f.repository.GetGenerationInput(ctx, externalID)
	if err != nil {
		return taskflow.StageFailure(externalID, "load_generation_data", err)
	}
	logger := f.ArticleLogger(input.Article)
	// BeginGeneration сам ставит current_step = structure_generation, отдельный переход
	// этапа здесь не нужен и репозиторием не поддерживается.
	if err := f.repository.BeginGeneration(ctx, input.Article.ID); err != nil {
		return f.Fail(ctx, logger, input.Article, "begin_generation", err)
	}

	prompt, err := f.Render(StageStructure, structureData(input))
	if err != nil {
		return f.Fail(ctx, logger, input.Article, "structure_generation", err)
	}
	chat, err := f.NewChat(ctx, input.Article.ID, StageStructure)
	if err != nil {
		return f.Fail(ctx, logger, input.Article, "structure_generation", err)
	}
	defer f.CloseChat(chat, logger, "structure_generation")

	started := time.Now()
	logger.Info("structure generation started", "stage", "structure_generation", "chat", 1)
	structure, err := f.Answer(ctx, chat.Send, prompt, "structure")
	if err != nil {
		return f.Fail(ctx, logger, input.Article, "structure_generation", err)
	}
	pending, err := f.writer.StageStructure(input.Article.ExternalID, input.Article.Slug, prompt, structure)
	if err != nil {
		return f.Fail(ctx, logger, input.Article, "save_structure", err)
	}
	defer pending.Abort()
	if err := articleoutput.Commit(func() error {
		return f.repository.SaveStructurePath(ctx, input.Article.ID, pending.Paths.StructurePath)
	}, pending); err != nil {
		return f.Fail(ctx, logger, input.Article, "save_structure_path", err)
	}
	logger.Info("structure generation completed", "stage", "structure_generation",
		"duration_ms", time.Since(started).Milliseconds(), "result_path", pending.Paths.StructurePath)
	return nil
}

// RunArticle выполняет чат 2 целиком: expert → seo_editor → info → review.
//
// Чат неделим намеренно. Каждое сообщение опирается на историю предыдущих, а браузерная
// беседа не переживает завершения процесса — значит и возобновлять её посередине нечем.
// Поэтому все четыре артефакта публикуются одним Commit: статья либо прошла весь чат 2,
// либо не начинала его, и промежуточных состояний, из которых нельзя продолжить, не возникает.
func (f *Flow) RunArticle(ctx context.Context, externalID string) error {
	input, err := f.repository.GetGenerationInput(ctx, externalID)
	if err != nil {
		return taskflow.StageFailure(externalID, "load_generation_data", err)
	}
	logger := f.ArticleLogger(input.Article)
	structure, err := f.SavedStructure(ctx, externalID)
	if err != nil {
		return f.Fail(ctx, logger, input.Article, "load_structure_data", err)
	}
	if err := f.repository.BeginGenerationStage(ctx, input.Article.ID, stepArticle); err != nil {
		return f.Fail(ctx, logger, input.Article, "article_generation", err)
	}

	// Старый базовый промпт статьи собирается здесь и в модель не уходит: текст пишет
	// стадия expert. Промпт нужен как артефакт и как документ в Google Docs.
	basePrompt, err := f.Render(StageArticle, articleData(input, structure))
	if err != nil {
		return f.Fail(ctx, logger, input.Article, "article_generation", err)
	}

	started := time.Now()
	chat, err := f.runArticleChat(ctx, logger, input, structure)
	if err != nil {
		return err
	}
	if err := f.saveArticleChat(ctx, logger, input, externalID, basePrompt, chat); err != nil {
		return err
	}
	logger.Info("article chat completed", "stage", "article_generation", "chat", 2,
		"duration_ms", time.Since(started).Milliseconds())
	return nil
}

// articleChatOutput — то, что произвёл чат 2. Результаты названы по смыслу, а не по слотам
// хранения: слот и смысл у pprof_1 расходятся, соответствие описано в saveArticleChat.
type articleChatOutput struct {
	expertPrompt  string
	expertArticle string
	editorPrompt  string
	editedArticle string
	infoPrompt    string
	infoText      string
	parsedInfo    article.ArticleInfo
	reviewPrompt  string
	finalArticle  string
}

// runArticleChat проводит четыре сообщения чата 2. Чат открывается и закрывается здесь же:
// продолжать его снаружи нечем и незачем.
func (f *Flow) runArticleChat(ctx context.Context, logger *slog.Logger, input article.GenerationInput, structure string) (articleChatOutput, error) {
	var out articleChatOutput
	chat, err := f.NewChat(ctx, input.Article.ID, StageExpert, StageSEOEditor, StageInfo, StageReview)
	if err != nil {
		return out, f.Fail(ctx, logger, input.Article, "article_generation", err)
	}
	defer f.CloseChat(chat, logger, "article_generation")
	logger.Info("article chat started", "stage", "article_generation", "chat", 2)

	if out.expertPrompt, out.expertArticle, err = f.Message(ctx, chat.Send,
		StageExpert, expertData(input, structure)); err != nil {
		return out, f.Fail(ctx, logger, input.Article, "article_generation", err)
	}
	logger.Info("expert article generated", "stage", "article_generation", "prompt_size", len([]rune(out.expertPrompt)))

	if out.editorPrompt, out.editedArticle, err = f.Message(ctx, chat.Continue,
		StageSEOEditor, editorData(input)); err != nil {
		return out, f.Fail(ctx, logger, input.Article, "seo_editing", err)
	}
	logger.Info("seo editing completed", "stage", "seo_editing")

	if out.infoPrompt, out.infoText, err = f.Message(ctx, chat.Continue,
		StageInfo, struct{}{}); err != nil {
		return out, f.Fail(ctx, logger, input.Article, "metadata_generation", err)
	}
	if out.parsedInfo, err = article.ParseArticleInfo(out.infoText); err != nil {
		return out, f.Fail(ctx, logger, input.Article, "metadata_parsing", err)
	}
	if out.parsedInfo.FallbackUsed {
		logger.Warn("metadata parsing incomplete, recognized and raw response content saved", "stage", "metadata_generation",
			"has_tldr", out.parsedInfo.TLDR != "", "has_faq", out.parsedInfo.FAQ != "")
	}
	logger.Info("article info generated", "stage", "metadata_generation")

	if out.reviewPrompt, out.finalArticle, err = f.Message(ctx, chat.Continue,
		StageReview, struct{}{}); err != nil {
		return out, f.Fail(ctx, logger, input.Article, "article_review", err)
	}
	logger.Info("article review completed", "stage", "article_review")
	return out, nil
}

// saveArticleChat публикует артефакты чата 2 одним Commit и отдаёт базовый промпт в очередь
// публикации.
//
// Результаты названы по смыслу, а хранятся в существующих слотах движка. Слот и смысл здесь
// намеренно разведены: у pprof_1 текстов шесть, а слотов пять, и review.txt занят статьёй
// после SEO-редактуры, а не списком замечаний. Соответствие:
//
//	expertArticle → article.txt        (текст практикующего специалиста)
//	editedArticle → review.txt         (он же после SEO-редактуры)
//	finalArticle  → fixed_article.txt  (финальный текст после ревью)
//
// HTML и сборка result.md дальше читают именно finalArticle.
func (f *Flow) saveArticleChat(ctx context.Context, logger *slog.Logger, input article.GenerationInput, externalID, basePrompt string, chat articleChatOutput) error {
	selected := input.Article
	expertPending, err := f.writer.StageArticle(selected.ExternalID, selected.Slug, basePrompt, chat.expertArticle, "")
	if err != nil {
		return f.Fail(ctx, logger, selected, "save_article", err)
	}
	defer expertPending.Abort()
	editedPending, err := f.writer.StageReview(selected.ExternalID, selected.Slug, chat.editorPrompt, chat.editedArticle)
	if err != nil {
		return f.Fail(ctx, logger, selected, "save_seo_edited_article", err)
	}
	defer editedPending.Abort()
	infoPending, err := f.writer.StageArticleInfo(selected.ExternalID, selected.Slug, chat.infoPrompt, chat.infoText)
	if err != nil {
		return f.Fail(ctx, logger, selected, "save_article_info", err)
	}
	defer infoPending.Abort()
	finalPending, err := f.writer.StageFixedArticle(selected.ExternalID, selected.Slug, chat.reviewPrompt, chat.finalArticle)
	if err != nil {
		return f.Fail(ctx, logger, selected, "save_reviewed_article", err)
	}
	defer finalPending.Abort()

	structurePath, err := f.SavedStructurePath(ctx, externalID)
	if err != nil {
		return f.Fail(ctx, logger, selected, "load_structure_data", err)
	}
	commitErr := articleoutput.Commit(func() error {
		if err := f.repository.SaveGenerationPaths(ctx, selected.ID, structurePath, expertPending.Paths.ArticlePath); err != nil {
			return err
		}
		if err := f.repository.SaveArticleInfo(ctx, selected.ID, chat.infoText, chat.parsedInfo); err != nil {
			return err
		}
		if err := f.repository.SaveReviewPath(ctx, selected.ID, editedPending.Paths.ReviewPath); err != nil {
			return err
		}
		return f.repository.SaveFixedArticlePath(ctx, selected.ID, finalPending.Paths.FixedArticlePath)
	}, expertPending, editedPending, infoPending, finalPending)
	if commitErr != nil {
		return f.Fail(ctx, logger, selected, "save_article_state", commitErr)
	}

	// Публикация идёт после того, как промпт опубликован на диске и путь записан в состояние.
	// Ошибка публикации сюда не возвращается: за генерацию уже заплачено.
	f.PublishPrompt(generation.ArticlePromptJob{
		ArticleID:  selected.ID,
		ExternalID: selected.ExternalID,
		Title:      selected.Title,
		Prompt:     basePrompt,
		PromptPath: expertPending.Paths.ArticlePromptPath,
	})
	logger.Info("article artifacts saved", "stage", "article_generation",
		"result_path", finalPending.Paths.FixedArticlePath)
	return nil
}

// RunHTML выполняет чат 3. Отдельный чат нужен потому, что разметка не должна тянуть за
// собой историю правок текста: модель получает финальную статью и список профессий.
func (f *Flow) RunHTML(ctx context.Context, externalID string) error {
	input, err := f.repository.GetGenerationInput(ctx, externalID)
	if err != nil {
		return taskflow.StageFailure(externalID, "load_generation_data", err)
	}
	logger := f.ArticleLogger(input.Article)
	saved, err := f.repository.GetSavedGenerationInput(ctx, externalID)
	if err != nil {
		return f.Fail(ctx, logger, input.Article, "load_article_data", err)
	}
	if strings.TrimSpace(saved.FixedArticlePath) == "" {
		return f.Fail(ctx, logger, input.Article, "load_article_data",
			fmt.Errorf("финальный текст статьи не сохранён: сначала выполните этап article"))
	}
	finalText, err := f.writer.Read(saved.FixedArticlePath)
	if err != nil {
		return f.Fail(ctx, logger, input.Article, "load_article_data", err)
	}
	if err := f.repository.BeginGenerationStage(ctx, input.Article.ID, stepHTML); err != nil {
		return f.Fail(ctx, logger, input.Article, "html_generation", err)
	}

	prompt, err := f.Render(StageHTML, htmlData(input, finalText))
	if err != nil {
		return f.Fail(ctx, logger, input.Article, "html_generation", err)
	}
	chat, err := f.NewChat(ctx, input.Article.ID, StageHTML)
	if err != nil {
		return f.Fail(ctx, logger, input.Article, "html_generation", err)
	}
	defer f.CloseChat(chat, logger, "html_generation")

	started := time.Now()
	logger.Info("html generation started", "stage", "html_generation", "chat", 3)
	html, err := f.Answer(ctx, chat.Send, prompt, StageHTML)
	if err != nil {
		return f.Fail(ctx, logger, input.Article, "html_generation", err)
	}
	html, err = generation.NormalizeHTML(html)
	if err != nil {
		return f.Fail(ctx, logger, input.Article, "html_generation", err)
	}
	pending, err := f.writer.StageHTML(input.Article.ExternalID, input.Article.Slug, prompt, html)
	if err != nil {
		return f.Fail(ctx, logger, input.Article, "save_html", err)
	}
	defer pending.Abort()
	if err := articleoutput.Commit(func() error {
		return f.repository.SaveHTMLPath(ctx, input.Article.ID, pending.Paths.HTMLPath)
	}, pending); err != nil {
		return f.Fail(ctx, logger, input.Article, "save_html_path", err)
	}
	logger.Info("html generation completed", "stage", "html_generation",
		"duration_ms", time.Since(started).Milliseconds(), "result_path", pending.Paths.HTMLPath)
	return nil
}
