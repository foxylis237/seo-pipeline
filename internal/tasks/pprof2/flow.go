package pprof2

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
//
// SaveArticleInfo есть, хотя стадии info у задачи нет: FAQ вынимается из уже написанной
// страницы разбором и ложится в ту же таблицу, что и у задач со стадией info. Публикация и
// result.md читают его оттуда же, поэтому второго хранилища для частых вопросов не заводим.
type Repository interface {
	GetGenerationInput(ctx context.Context, externalID string) (article.GenerationInput, error)
	GetSavedGenerationInput(ctx context.Context, externalID string) (article.SavedGenerationInput, error)
	BeginGeneration(ctx context.Context, articleID int64) error
	BeginGenerationStage(ctx context.Context, articleID int64, stage string) error
	SaveStructurePath(ctx context.Context, articleID int64, structurePath string) error
	SaveGenerationPaths(ctx context.Context, articleID int64, structurePath, articlePath string) error
	SaveReviewPath(ctx context.Context, articleID int64, reviewPath string) error
	SaveFixedArticlePath(ctx context.Context, articleID int64, fixedArticlePath string) error
	SaveHTMLPath(ctx context.Context, articleID int64, htmlPath string) error
	SaveArticleInfo(ctx context.Context, articleID int64, rawText string, info article.ArticleInfo) error
	SaveError(ctx context.Context, articleID int64, processingErr error) error
}

// Writer — то, что поток требует от файлового слоя. Все методы уже есть у writer движка:
// артефакты pprof_2 ложатся в те же слоты, что и артефакты остальных задач, поэтому сборка
// result.md и очистка статьи работают без изменений.
type Writer interface {
	StageStructure(externalID, slug, prompt, structure string) (*articleoutput.PendingArtifact, error)
	StageArticle(externalID, slug, prompt, text, model string) (*articleoutput.PendingArtifact, error)
	StageReview(externalID, slug, prompt, review string) (*articleoutput.PendingArtifact, error)
	StageFixedArticle(externalID, slug, prompt, article string) (*articleoutput.PendingArtifact, error)
	StageHTML(externalID, slug, prompt, html string) (*articleoutput.PendingArtifact, error)
	Read(relativePath string) (string, error)
}

// Этапы возобновления в БД. Словарь здесь чужой: репозиторий знает про article, info, review,
// fix и html и сам переводит их в current_step. Передавать сюда имена стадий pprof_2 нельзя —
// репозиторий их не принимает.
const (
	stepArticle = "article"
	stepHTML    = "html"
)

// Flow — поток генерации pprof_2.
//
// Три чата, и границы между ними значимы:
//
//	Чат 1: structure
//	Чат 2: article → review
//	Чат 3: html
//
// Чат 2 состоит из двух сообщений: основной промпт пишет страницу, редактура возвращает её
// исправленной целиком. Отдельным чатом он остаётся потому, что историю обсуждения структуры
// в текст страницы тащить незачем.
//
// Частые вопросы после чата 2 вынимаются из написанного текста разбором и сохраняются в
// article_metadata: к стадии html они уже в базе, и разметка вправе убрать блок из страницы.
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
// остальной работы, и историю её обсуждения тащить в текст страницы незачем.
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
	structure, err := f.Answer(ctx, chat.Send, prompt, StageStructure)
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

// RunArticle выполняет чат 2 целиком: article → review. FAQ забирается из исправленного текста.
//
// Чат неделим намеренно. Ревью опирается на историю первого сообщения, а браузерная беседа не
// переживает завершения процесса — значит и возобновлять её посередине нечем. Поэтому оба
// артефакта и метаданные публикуются одним Commit: страница либо прошла чат 2 целиком, либо
// не начинала его, и промежуточных состояний, из которых нельзя продолжить, не возникает.
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

	started := time.Now()
	chat, err := f.runArticleChat(ctx, logger, input, structure)
	if err != nil {
		return err
	}
	if err := f.saveArticleChat(ctx, logger, input, externalID, chat); err != nil {
		return err
	}
	logger.Info("article chat completed", "stage", "article_generation", "chat", 2,
		"duration_ms", time.Since(started).Milliseconds())
	return nil
}

// articleChatOutput — то, что произвёл чат 2. Результаты названы по смыслу, а не по слотам
// хранения: соответствие описано в saveArticleChat.
type articleChatOutput struct {
	articlePrompt string
	articleText   string
	reviewPrompt  string
	reviewedPage  string
}

// runArticleChat проводит два сообщения чата 2. Чат открывается и закрывается здесь же:
// продолжать его снаружи нечем и незачем.
func (f *Flow) runArticleChat(ctx context.Context, logger *slog.Logger, input article.GenerationInput, structure string) (articleChatOutput, error) {
	var out articleChatOutput
	chat, err := f.NewChat(ctx, input.Article.ID, StageArticle, StageReview)
	if err != nil {
		return out, f.Fail(ctx, logger, input.Article, "article_generation", err)
	}
	defer f.CloseChat(chat, logger, "article_generation")
	logger.Info("article chat started", "stage", "article_generation", "chat", 2)

	if out.articlePrompt, out.articleText, err = f.Message(ctx, chat.Send,
		StageArticle, articleData(input, structure)); err != nil {
		return out, f.Fail(ctx, logger, input.Article, "article_generation", err)
	}
	logger.Info("article generated", "stage", "article_generation", "prompt_size", len([]rune(out.articlePrompt)))

	if out.reviewPrompt, out.reviewedPage, err = f.Message(ctx, chat.Continue,
		StageReview, reviewData(input, out.articleText)); err != nil {
		return out, f.Fail(ctx, logger, input.Article, "article_review", err)
	}
	logger.Info("article review completed", "stage", "article_review")
	return out, nil
}

// saveArticleChat публикует артефакты чата 2 одним Commit и отдаёт основной промпт в очередь
// публикации.
//
// Текстов у страницы два, и лежат они в существующих слотах движка:
//
//	articleText  → article.txt        (черновик основного промпта)
//	reviewedPage → fixed_article.txt  (страница после редактуры — финальный текст)
//
// Слот review_path указывает на тот же fixed_article.txt: списка замечаний ревью не отдаёт,
// отдельного текста у него нет, а пустым слот оставлять нельзя — раннер полного прогона
// считает этап невыполненным по пустому пути и возвращался бы на него вечно. Второй копии
// финального текста рядом не появляется — файл один на оба слота.
//
// FAQ вынимается из финального текста, а не из черновика: редактура вправе переписать блок
// частых вопросов, и в базу обязан попасть тот набор, который опубликован. Сохраняется он до
// стадии html — разметка вправе убрать блок со страницы, потому что в базе он уже есть.
// Пустой FAQ генерацию не роняет — страница написана и оплачена, — но и не прячется:
// он остаётся пустым, и публиковать статью без него не даст проверка публикации.
func (f *Flow) saveArticleChat(ctx context.Context, logger *slog.Logger, input article.GenerationInput, externalID string, chat articleChatOutput) error {
	selected := input.Article
	articlePending, err := f.writer.StageArticle(selected.ExternalID, selected.Slug, chat.articlePrompt, chat.articleText, "")
	if err != nil {
		return f.Fail(ctx, logger, selected, "save_article", err)
	}
	defer articlePending.Abort()
	finalPending, err := f.writer.StageFixedArticle(selected.ExternalID, selected.Slug, chat.reviewPrompt, chat.reviewedPage)
	if err != nil {
		return f.Fail(ctx, logger, selected, "save_reviewed_article", err)
	}
	defer finalPending.Abort()

	structurePath, err := f.SavedStructurePath(ctx, externalID)
	if err != nil {
		return f.Fail(ctx, logger, selected, "load_structure_data", err)
	}
	faq := ExtractFAQ(chat.reviewedPage)
	if strings.TrimSpace(faq) == "" {
		logger.Warn("блок частых вопросов не найден в тексте страницы, FAQ останется пустым",
			"stage", "article_review")
	}
	pagePath := finalPending.Paths.FixedArticlePath
	commitErr := articleoutput.Commit(func() error {
		if err := f.repository.SaveGenerationPaths(ctx, selected.ID, structurePath, articlePending.Paths.ArticlePath); err != nil {
			return err
		}
		if err := f.repository.SaveReviewPath(ctx, selected.ID, pagePath); err != nil {
			return err
		}
		if err := f.repository.SaveFixedArticlePath(ctx, selected.ID, pagePath); err != nil {
			return err
		}
		// Сырой текст метаданных — тот же FAQ: у pprof_2 нет ответа модели, из которого он
		// разбирался бы, и хранить в metadata_text нечего, кроме самого результата разбора.
		return f.repository.SaveArticleInfo(ctx, selected.ID, faq, article.ArticleInfo{FAQ: faq})
	}, articlePending, finalPending)
	if commitErr != nil {
		return f.Fail(ctx, logger, selected, "save_article_state", commitErr)
	}

	// Публикация идёт после того, как промпт опубликован на диске и путь записан в состояние.
	// Ошибка публикации сюда не возвращается: за генерацию уже заплачено.
	f.PublishPrompt(generation.ArticlePromptJob{
		ArticleID:  selected.ID,
		ExternalID: selected.ExternalID,
		Title:      selected.Title,
		Prompt:     chat.articlePrompt,
		PromptPath: articlePending.Paths.ArticlePromptPath,
	})
	logger.Info("article artifacts saved", "stage", "article_generation",
		"draft_path", articlePending.Paths.ArticlePath, "result_path", pagePath,
		"faq_saved", strings.TrimSpace(faq) != "")
	return nil
}

// RunHTML выполняет чат 3. Отдельный чат нужен потому, что разметка не должна тянуть за
// собой историю правок текста: модель получает финальную страницу и список ссылок.
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
			fmt.Errorf("финальный текст страницы не сохранён: сначала выполните этап article"))
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
	// Чат разметки принимает больше одного сообщения: оборванный ответ дописывается
	// продолжением той же стадии, а чат роутера принимает ровно столько сообщений, сколько
	// стадий ему названо при создании.
	chat, err := f.NewChat(ctx, input.Article.ID, generation.HTMLChatStages(StageHTML)...)
	if err != nil {
		return f.Fail(ctx, logger, input.Article, "html_generation", err)
	}
	defer f.CloseChat(chat, logger, "html_generation")

	started := time.Now()
	logger.Info("html generation started", "stage", "html_generation", "chat", 3)
	html, err := generation.BuildHTMLPage(ctx, generation.HTMLPageRequest{
		Page:   finalText,
		Prompt: prompt,
		Send: func(ctx context.Context, message string) (string, error) {
			return f.Answer(ctx, chat.Send, message, StageHTML)
		},
		Continue: func(ctx context.Context, message string) (string, error) {
			return f.Answer(ctx, chat.Continue, message, StageHTML)
		},
		Logger: logger,
	})
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
