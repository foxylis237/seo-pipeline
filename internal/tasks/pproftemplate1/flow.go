package pproftemplate1

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
// артефакты задачи ложатся в те же слоты, что и артефакты pprof_1.
type Writer interface {
	StageStructure(externalID, slug, prompt, structure string) (*articleoutput.PendingArtifact, error)
	StageArticle(externalID, slug, prompt, text, model string) (*articleoutput.PendingArtifact, error)
	StageArticleInfo(externalID, slug, prompt, info string) (*articleoutput.PendingArtifact, error)
	StageFixedArticle(externalID, slug, prompt, article string) (*articleoutput.PendingArtifact, error)
	StageHTML(externalID, slug, prompt, html string) (*articleoutput.PendingArtifact, error)
	Read(relativePath string) (string, error)
}

// Этапы возобновления в БД. Словарь здесь чужой: репозиторий знает про article, info, review,
// fix и html и сам переводит их в current_step, а стадии задачи называются иначе.
const (
	stepArticle = "article"
	stepHTML    = "html"
)

// reviewKeptShare — доля черновика, ниже которой ответ редактуры считается оборванным.
//
// Редактура возвращает статью целиком, и обрыв ответа внешне неотличим от готового текста:
// заголовки на месте, абзац закончен. Половина статьи, записанная поверх целой, — потеря
// оплаченной работы, поэтому короткий ответ отбрасывается в пользу черновика.
const reviewKeptShare = 0.6

// Flow — поток генерации pprof_template_1.
//
// Четыре чата, и границы между ними значимы:
//
//	Чат 1: structure       — каркас разделов, эталон документом
//	Чат 2: expert_seo      — текст статьи вместе с SEO, эталон документом
//	Чат 3: review → info   — правка смысла и метаданные по готовому тексту
//	Чат 4: html            — разметка и перелинковка
//
// Ревью вынесено из чата, который писал текст: в своей же беседе модель правит собственную
// работу и меняет в ней доли процента. Свежий чат видит только статью и промпт редактуры —
// и заодно упавшее ревью больше не заставляет переписывать статью заново.
//
// Метаданные идут после ревью и в том же чате: TL;DR и FAQ обязаны описывать тот текст,
// который уйдёт в блог, а не черновик до правки.
type Flow struct {
	*taskflow.Base
	repository Repository
	writer     Writer
	names      LinkNames
}

// NewFlow собирает поток. publisher необязателен и может быть nil.
func NewFlow(repository Repository, writer Writer, chats taskflow.ChatFactory,
	prompts taskflow.PromptRenderer, logger *slog.Logger, publisher taskflow.PromptPublisher,
	names LinkNames) *Flow {
	return &Flow{
		Base:       taskflow.NewBase(repository, writer, chats, prompts, logger, publisher),
		repository: repository,
		writer:     writer,
		names:      names,
	}
}

// LinkNames отдаёт название программы по адресу её страницы.
//
// Интерфейс объявлен здесь, у потребителя: потоку нужно ровно название, а откуда оно взято —
// со страницы сайта или из подмены в тесте — его не касается. nil означает прежнее поведение:
// в промпт уходят одни адреса.
type LinkNames interface {
	Name(ctx context.Context, url string) (string, error)
}

// namedLinks дополняет список перелинковки названиями программ с сайта.
//
// Отказ страницы стадию не роняет: за генерацию уже заплачено, а без названия модель всё ещё
// может восстановить его из адреса — хуже, но не фатально.
func (f *Flow) namedLinks(ctx context.Context, logger *slog.Logger, links string) string {
	urls := generation.LinkURLs(links)
	if f.names == nil || len(urls) == 0 {
		return links
	}
	lines := make([]string, 0, len(urls))
	for _, url := range urls {
		name, err := f.names.Name(ctx, url)
		if err != nil || strings.TrimSpace(name) == "" {
			logger.Warn("название программы не получено, ссылка уходит без него",
				"stage", "html_generation", "url", url, "error", err)
			lines = append(lines, url)
			continue
		}
		lines = append(lines, strings.TrimSpace(name)+" — "+url)
	}
	return strings.Join(lines, "\n")
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
	structure, err := f.Answer(ctx, chat.Send, prompt, StageStructure)
	if err != nil {
		return f.Fail(ctx, logger, input.Article, "structure_generation", err)
	}
	// Промпт просит структуру готовой для копирования в кодовом блоке, и модель оборачивает
	// её в ```html. Для артефакта и для следующей стадии это мусор.
	structure = generation.StripCodeFence(structure)
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

// RunArticle проводит чат 2 и чат 3: статью пишет один, правит и описывает другой.
//
// Команда одна на оба чата, потому что раннер и одностадийные команды видят у задачи три
// вызова — структура, статья, разметка. Внутри они разделены: черновик публикуется своим
// Commit до того, как начнётся редактура, и повторный запуск после упавшего ревью начинает
// с чата 3, а не переписывает оплаченный текст.
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

	draft, err := f.draftArticle(ctx, logger, input, externalID, structure)
	if err != nil {
		return err
	}
	return f.reviewArticle(ctx, logger, input, draft)
}

// draftArticle отдаёт черновик статьи: сохранённый, если он уже есть, иначе новый из чата 2.
//
// Черновик считается годным, только пока нет финального текста: как только ревью прошло,
// повторный запуск обязан переписать статью заново, иначе команда article перестала бы
// что-либо делать.
func (f *Flow) draftArticle(ctx context.Context, logger *slog.Logger, input article.GenerationInput,
	externalID, structure string) (string, error) {
	saved, err := f.repository.GetSavedGenerationInput(ctx, externalID)
	if err != nil {
		return "", f.Fail(ctx, logger, input.Article, "load_article_data", err)
	}
	if strings.TrimSpace(saved.ArticlePath) != "" && strings.TrimSpace(saved.FixedArticlePath) == "" {
		draft, err := f.writer.Read(saved.ArticlePath)
		if err != nil {
			return "", f.Fail(ctx, logger, input.Article, "load_article_data", err)
		}
		logger.Info("черновик статьи уже сохранён, чат 2 пропускаем",
			"stage", "article_generation", "result_path", saved.ArticlePath)
		return draft, nil
	}

	// Базовый промпт статьи собирается здесь и в модель не уходит: текст пишет стадия
	// expert_seo по эталону. Промпт нужен как артефакт и как документ выгрузки.
	basePrompt, err := f.Render(StageArticle, articleData(input, structure))
	if err != nil {
		return "", f.Fail(ctx, logger, input.Article, "article_generation", err)
	}

	started := time.Now()
	chat, err := f.NewChat(ctx, input.Article.ID, StageExpertSEO)
	if err != nil {
		return "", f.Fail(ctx, logger, input.Article, "article_generation", err)
	}
	defer f.CloseChat(chat, logger, "article_generation")
	logger.Info("article chat started", "stage", "article_generation", "chat", 2)

	writtenPrompt, draft, err := f.Message(ctx, chat.Send, StageExpertSEO, expertSEOData(input, structure))
	if err != nil {
		return "", f.Fail(ctx, logger, input.Article, "article_generation", err)
	}
	// Запись заголовков приводится к одному виду до сохранения: стадия html расставляет теги
	// по ней, а модель за один прогон свободно переходит с «H2 - » на «H2:» и на Markdown.
	draft = generation.NormalizeHeadings(draft)
	if err := f.saveDraft(ctx, logger, input, externalID, basePrompt, draft); err != nil {
		return "", err
	}
	logger.Info("article draft generated", "stage", "article_generation",
		"prompt_size", len([]rune(writtenPrompt)), "duration_ms", time.Since(started).Milliseconds())
	return draft, nil
}

// saveDraft публикует черновик и отдаёт базовый промпт в очередь выгрузки.
func (f *Flow) saveDraft(ctx context.Context, logger *slog.Logger, input article.GenerationInput,
	externalID, basePrompt, draft string) error {
	selected := input.Article
	pending, err := f.writer.StageArticle(selected.ExternalID, selected.Slug, basePrompt, draft, "")
	if err != nil {
		return f.Fail(ctx, logger, selected, "save_article", err)
	}
	defer pending.Abort()
	structurePath, err := f.SavedStructurePath(ctx, externalID)
	if err != nil {
		return f.Fail(ctx, logger, selected, "load_structure_data", err)
	}
	if err := articleoutput.Commit(func() error {
		return f.repository.SaveGenerationPaths(ctx, selected.ID, structurePath, pending.Paths.ArticlePath)
	}, pending); err != nil {
		return f.Fail(ctx, logger, selected, "save_article_state", err)
	}

	// Выгрузка идёт после того, как промпт опубликован на диске и путь записан в состояние.
	// Её ошибка сюда не возвращается: за генерацию уже заплачено.
	f.PublishPrompt(generation.ArticlePromptJob{
		ArticleID:  selected.ID,
		ExternalID: selected.ExternalID,
		Title:      selected.Title,
		Prompt:     basePrompt,
		PromptPath: pending.Paths.ArticlePromptPath,
	})
	return nil
}

// reviewArticle проводит чат 3: правка смысла и метаданные по её результату.
//
// Оба артефакта публикуются одним Commit: метаданные описывают именно тот текст, который
// лёг в fixed_article.txt, и порознь эти два состояния бессмысленны.
func (f *Flow) reviewArticle(ctx context.Context, logger *slog.Logger, input article.GenerationInput, draft string) error {
	selected := input.Article
	started := time.Now()
	chat, err := f.NewChat(ctx, selected.ID, StageReview, StageInfo)
	if err != nil {
		return f.Fail(ctx, logger, selected, "article_review", err)
	}
	defer f.CloseChat(chat, logger, "article_review")
	logger.Info("review chat started", "stage", "article_review", "chat", 3)

	reviewPrompt, reviewed, err := f.Message(ctx, chat.Send, StageReview, reviewData(draft))
	if err != nil {
		return f.Fail(ctx, logger, selected, "article_review", err)
	}
	finalArticle := f.acceptReview(logger, draft, generation.NormalizeHeadings(reviewed))

	infoPrompt, infoText, err := f.Message(ctx, chat.Continue, StageInfo, struct{}{})
	if err != nil {
		return f.Fail(ctx, logger, selected, "metadata_generation", err)
	}
	parsedInfo, err := article.ParseArticleInfo(infoText)
	if err != nil {
		return f.Fail(ctx, logger, selected, "metadata_parsing", err)
	}
	if parsedInfo.FallbackUsed {
		logger.Warn("metadata parsing incomplete, recognized and raw response content saved",
			"stage", "metadata_generation", "has_tldr", parsedInfo.TLDR != "", "has_faq", parsedInfo.FAQ != "")
	}

	finalPending, err := f.writer.StageFixedArticle(selected.ExternalID, selected.Slug, reviewPrompt, finalArticle)
	if err != nil {
		return f.Fail(ctx, logger, selected, "save_reviewed_article", err)
	}
	defer finalPending.Abort()
	infoPending, err := f.writer.StageArticleInfo(selected.ExternalID, selected.Slug, infoPrompt, infoText)
	if err != nil {
		return f.Fail(ctx, logger, selected, "save_article_info", err)
	}
	defer infoPending.Abort()

	// review_path указывает на тот же файл, что и fixed_article_path: списка замечаний
	// редактура не отдаёт, третьего текста статьи у задачи нет, а пустой слот раннер считает
	// невыполненным этапом. Так же устроен pprof_2.
	commitErr := articleoutput.Commit(func() error {
		if err := f.repository.SaveArticleInfo(ctx, selected.ID, infoText, parsedInfo); err != nil {
			return err
		}
		if err := f.repository.SaveReviewPath(ctx, selected.ID, finalPending.Paths.FixedArticlePath); err != nil {
			return err
		}
		return f.repository.SaveFixedArticlePath(ctx, selected.ID, finalPending.Paths.FixedArticlePath)
	}, finalPending, infoPending)
	if commitErr != nil {
		return f.Fail(ctx, logger, selected, "save_article_state", commitErr)
	}
	logger.Info("review chat completed", "stage", "article_review",
		"duration_ms", time.Since(started).Milliseconds(), "result_path", finalPending.Paths.FixedArticlePath)
	return nil
}

// acceptReview решает, чей текст уходит дальше — правленый или черновик.
//
// Правило здесь то же, что у разметки: чинить, но не отказывать. Оборванный ответ редактуры
// выглядит законченной статьёй, и единственный доступный признак — насколько он короче
// исходного текста и не растерял ли разделы. Отбрасываем его молча нельзя: разошедшиеся
// артефакты видно только по логу.
func (f *Flow) acceptReview(logger *slog.Logger, draft, reviewed string) string {
	draftHeadings := generation.CountHeadings(draft)
	if len([]rune(reviewed)) >= int(float64(len([]rune(draft)))*reviewKeptShare) &&
		generation.CountHeadings(reviewed) >= draftHeadings {
		return reviewed
	}
	logger.Warn("ответ редактуры короче статьи или потерял разделы, в блог уходит черновик",
		"stage", "article_review", "draft_runes", len([]rune(draft)), "reviewed_runes", len([]rune(reviewed)),
		"draft_headings", draftHeadings, "reviewed_headings", generation.CountHeadings(reviewed))
	return draft
}

// RunHTML выполняет чат 4. Отдельный чат нужен потому, что разметка не должна тянуть за
// собой историю правок текста: модель получает финальную статью и список ссылок.
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

	// Названия программ снимаются со страниц сайта один раз: их видит и промпт, и код,
	// дописывающий недостающие ссылки, — анкором становится ровно название программы.
	named := f.namedLinks(ctx, logger, input.Links)
	prompt, err := f.Render(StageHTML, htmlData(finalText, named))
	if err != nil {
		return f.Fail(ctx, logger, input.Article, "html_generation", err)
	}
	// Чат разметки открывается на два полных набора сообщений: первый ответ со своими
	// продолжениями и ремонт перелинковки со своими. Ремонт — такой же ответ модели и
	// обрывается так же.
	htmlStages := append(generation.HTMLChatStages(StageHTML), generation.HTMLChatStages(StageHTML)...)
	chat, err := f.NewChat(ctx, input.Article.ID, htmlStages...)
	if err != nil {
		return f.Fail(ctx, logger, input.Article, "html_generation", err)
	}
	defer f.CloseChat(chat, logger, "html_generation")

	started := time.Now()
	logger.Info("html generation started", "stage", "html_generation", "chat", 4)
	html, err := generation.BuildHTMLPage(ctx, generation.HTMLPageRequest{
		Page:   finalText,
		Prompt: prompt,
		// Неполная разметка стадию не роняет: пересобирать её целиком дороже, чем дописать
		// хвост руками, а предупреждение в логе показывает, что дописывать.
		AcceptIncomplete: true,
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
	html = f.completeHTMLPage(logger, finalText, named, html)
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

// completeHTMLPage доводит разметку до требований страницы блога.
//
// Правило здесь одно: чинить, но не отказывать. Ни одна из проверок не имеет права уронить
// стадию — за генерацию уже заплачено, и статья с неидеальной разметкой полезнее, чем
// отсутствие статьи.
func (f *Flow) completeHTMLPage(logger *slog.Logger, page, links, markup string) string {
	markup = generation.LinkSources(generation.RestoreStats(page, generation.CleanBlogMarkup(generation.DropHeading1(markup))))
	if !generation.LeadKept(page, markup) {
		markup = generation.RestoreLead(page, markup)
		logger.Warn("вводный абзац возвращён в разметку кодом", "stage", "html_generation")
	}
	if left := generation.LeftoverBlockMarkers(markup); len(left) > 0 {
		logger.Warn("вёрстка не узнала метки визуальных блоков, они остались в разметке текстом",
			"stage", "html_generation", "markers", strings.Join(left, ", "))
	}
	// Перелинковку расставляет код, у модели ничего не переспрашивается: сгрудившиеся ссылки
	// переезжают вместе со своим предложением в разделы без ссылок, а недостающие дописываются
	// предложением с названием программы — название пришло с сайта вместе с адресом.
	spread, moved := generation.SpreadInternalLinks(markup, links)
	if moved > 0 {
		logger.Info("перелинковка расставлена кодом",
			"stage", "html_generation", "links_moved", moved)
		markup = spread
	}
	if left := generation.MissingInternalLinks(markup, links); len(left) > 0 {
		logger.Warn("часть ссылок перелинковки в разметку так и не попала",
			"stage", "html_generation", "missing_links", len(left))
	}
	if crowded := generation.CrowdedLinks(markup, links); len(crowded) > 0 {
		logger.Warn("часть ссылок осталась в одном разделе: свободных разделов не хватило",
			"stage", "html_generation", "crowded_links", len(crowded))
	}
	return markup
}
