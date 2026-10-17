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
	// ctaButtonPath — файл кнопки заявки, дописываемой в конец разметки. Поле, а не
	// константа прямо в стадии, ради тестов: путь относителен корня проекта, а тест пакета
	// запускается в своём каталоге.
	ctaButtonPath string
}

// NewFlow собирает поток. publisher необязателен и может быть nil.
//
// Репозиторий и writer уходят в общий каркас и остаются здесь: каркасу от них нужны чтение
// структуры и запись ошибки, а слоты артефактов — дело самого потока.
func NewFlow(repository Repository, writer Writer, chats taskflow.ChatFactory,
	prompts taskflow.PromptRenderer, logger *slog.Logger, publisher taskflow.PromptPublisher) *Flow {
	return &Flow{
		Base:          taskflow.NewBase(repository, writer, chats, prompts, logger, publisher),
		repository:    repository,
		writer:        writer,
		ctaButtonPath: CTAButtonPath,
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
	defer f.CloseChat(chat, logger, StageStructure)

	started := time.Now()
	logger.Info("стадия начата", "stage", StageStructure, "chat", 1)
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
	logger.Info("стадия завершена", "stage", StageStructure,
		"duration_ms", time.Since(started).Milliseconds(), "path", pending.Paths.StructurePath)
	return nil
}

// RunArticle выполняет чат 2 целиком: article → review. FAQ забирается из исправленного текста.
//
// Чат неделим намеренно. Ревью опирается на историю первого сообщения, а браузерная беседа не
// переживает завершения процесса — значит и возобновлять её посередине нечем. Поэтому оба
// артефакта и метаданные публикуются одним Commit: страница либо прошла чат 2 целиком, либо
// не начинала его, и промежуточных состояний, из которых нельзя продолжить, не возникает.
func (f *Flow) RunArticle(ctx context.Context, externalID string) error {
	return f.RunArticleChat(ctx, externalID, taskflow.ArticleChat{
		Repository:   f.repository,
		Writer:       f.writer,
		Step:         stepArticle,
		ArticleStage: StageArticle,
		ReviewStage:  StageReview,
		ArticleData:  articleData,
		ReviewData:   reviewData,
		Extras:       f.articleFAQ,
	})
}

// articleFAQ вынимает FAQ из страницы после редактуры: в базу попадает опубликованный набор.
// Пустой FAQ генерацию не роняет — публиковать без него не даст проверка публикации.
func (f *Flow) articleFAQ(ctx context.Context, logger *slog.Logger, selected article.Article,
	out taskflow.ArticleChatOutput) (taskflow.ArticleChatExtras, error) {
	faq := ExtractFAQ(out.ReviewedPage)
	if strings.TrimSpace(faq) == "" {
		logger.Warn("блок частых вопросов не найден в тексте страницы, FAQ останется пустым",
			"stage", StageReview)
	}
	return taskflow.ArticleChatExtras{
		// Сырой текст метаданных — тот же FAQ: ответа модели, из которого он разбирался бы, нет.
		Persist: func() error {
			return f.repository.SaveArticleInfo(ctx, selected.ID, faq, article.ArticleInfo{FAQ: faq})
		},
		LogAttrs: []any{"faq_saved", strings.TrimSpace(faq) != ""},
	}, nil
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
	// Кнопка спрашивается до первого сообщения модели: отказ файла обязан стоить нисколько,
	// а не оплаченный ответ разметки.
	button, err := readCTAButton(f.ctaButtonPath)
	if err != nil {
		return f.Fail(ctx, logger, input.Article, "load_article_data", err)
	}
	if err := f.repository.BeginGenerationStage(ctx, input.Article.ID, stepHTML); err != nil {
		return f.Fail(ctx, logger, input.Article, "html_generation", err)
	}

	prompt, err := f.Render(StageHTML, htmlData(finalText))
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
	defer f.CloseChat(chat, logger, StageHTML)

	started := time.Now()
	logger.Info("стадия начата", "stage", StageHTML, "chat", 3)
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
	html, added := appendCTAButton(generation.KeepExternalLinks(generation.ScrollTables(html), siteHost, externalLinkDomains), button)
	if !added {
		logger.Warn("в разметке уже есть кнопка — свою не дописываем",
			"stage", StageHTML, "cta_button_path", f.ctaButtonPath)
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
	logger.Info("стадия завершена", "stage", StageHTML,
		"duration_ms", time.Since(started).Milliseconds(), "path", pending.Paths.HTMLPath)
	return nil
}
