package obuch1

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"text/template"
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
// артефакты obuch_1 ложатся в те же слоты, что и артефакты остальных задач.
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

// Flow — поток генерации obuch_1.
//
// Три чата, и границы между ними значимы:
//
//	Чат 1: structure
//	Чат 2: expert → review → info
//	Чат 3: html + тексты карточки призыва
//
// Порядок стадий тот же, что у pprof_1, а расходится третий чат: площадка другая, у неё
// другая вёрстка и под статьёй стоит карточка призыва, которой у pprof_1 нет вовсе. Общий
// поток на две площадки означал бы флаг внутри чистки разметки, поэтому у задачи свой.
type Flow struct {
	*taskflow.Base
	repository Repository
	writer     Writer
	names      LinkNames
	// ctaCardPath — шаблон карточки призыва. Поле, а не константа по месту: тест подставляет
	// временный файл, а прогон — CTACardPath.
	ctaCardPath string
}

// NewFlow собирает поток. publisher необязателен и может быть nil.
func NewFlow(repository Repository, writer Writer, chats taskflow.ChatFactory,
	prompts taskflow.PromptRenderer, logger *slog.Logger, publisher taskflow.PromptPublisher,
	names LinkNames) *Flow {
	return &Flow{
		Base:        taskflow.NewBase(repository, writer, chats, prompts, logger, publisher),
		repository:  repository,
		writer:      writer,
		names:       names,
		ctaCardPath: CTACardPath,
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
	// Промпт просит структуру «готовой для копирования одним кликом в кодовом блоке», и модель
	// оборачивает её в ```html. Для артефакта и для следующей стадии это мусор.
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

// RunArticle выполняет чат 2 целиком: expert → review → info.
//
// Чат неделим намеренно. Каждое сообщение опирается на историю предыдущих, а браузерная
// беседа не переживает завершения процесса — значит и возобновлять её посередине нечем.
// Поэтому все артефакты публикуются одним Commit.
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

	// Базовый промпт статьи собирается здесь и в модель не уходит: текст пишет стадия expert.
	// Промпт нужен как артефакт и как документ в Google Docs.
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
// хранения: слот и смысл расходятся, соответствие описано в saveArticleChat.
type articleChatOutput struct {
	expertPrompt  string
	expertArticle string
	reviewPrompt  string
	finalArticle  string
	infoPrompt    string
	infoText      string
	parsedInfo    article.ArticleInfo
}

// runArticleChat проводит три сообщения чата 2. Чат открывается и закрывается здесь же:
// продолжать его снаружи нечем и незачем.
//
// Метаданные идут последними намеренно: TL;DR и FAQ обязаны описывать тот текст, который уйдёт
// в блог, а не черновик до редактуры.
func (f *Flow) runArticleChat(ctx context.Context, logger *slog.Logger, input article.GenerationInput, structure string) (articleChatOutput, error) {
	var out articleChatOutput
	chat, err := f.NewChat(ctx, input.Article.ID, StageExpert, StageReview, StageInfo)
	if err != nil {
		return out, f.Fail(ctx, logger, input.Article, "article_generation", err)
	}
	defer f.CloseChat(chat, logger, "article_generation")
	logger.Info("article chat started", "stage", "article_generation", "chat", 2)

	if out.expertPrompt, out.expertArticle, err = f.Message(ctx, chat.Send,
		StageExpert, expertData(input, structure)); err != nil {
		return out, f.Fail(ctx, logger, input.Article, "article_generation", err)
	}
	out.expertArticle = generation.NormalizeHeadings(out.expertArticle)
	logger.Info("expert article generated", "stage", "article_generation", "prompt_size", len([]rune(out.expertPrompt)))

	if out.reviewPrompt, out.finalArticle, err = f.Message(ctx, chat.Continue,
		StageReview, editorData(input)); err != nil {
		return out, f.Fail(ctx, logger, input.Article, "article_review", err)
	}
	// Запись заголовков приводится к одному виду до сохранения: стадия html расставляет теги
	// по ней, а модель за один прогон свободно переходит с «H2 - » на «H2:» и на Markdown.
	out.finalArticle = generation.NormalizeHeadings(out.finalArticle)
	logger.Info("article review completed", "stage", "article_review")

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
	return out, nil
}

// saveArticleChat публикует артефакты чата 2 одним Commit и отдаёт базовый промпт в очередь
// публикации.
//
// Результаты названы по смыслу, а хранятся в существующих слотах движка:
//
//	expertArticle → article.txt        (текст практикующего специалиста)
//	finalArticle  → fixed_article.txt  (он же после редактуры — финальный текст)
//
// Отдельного файла review.txt у задачи нет: редактура возвращает исправленный текст целиком, а
// не список замечаний. Слот review_path при этом пустым оставлять нельзя — раннер считает этап
// невыполненным по пустому пути, — поэтому он указывает на тот же финальный файл.
func (f *Flow) saveArticleChat(ctx context.Context, logger *slog.Logger, input article.GenerationInput, externalID, basePrompt string, chat articleChatOutput) error {
	selected := input.Article
	expertPending, err := f.writer.StageArticle(selected.ExternalID, selected.Slug, basePrompt, chat.expertArticle, "")
	if err != nil {
		return f.Fail(ctx, logger, selected, "save_article", err)
	}
	defer expertPending.Abort()
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
		if err := f.repository.SaveReviewPath(ctx, selected.ID, finalPending.Paths.FixedArticlePath); err != nil {
			return err
		}
		return f.repository.SaveFixedArticlePath(ctx, selected.ID, finalPending.Paths.FixedArticlePath)
	}, expertPending, infoPending, finalPending)
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

// RunHTML выполняет чат 3: разметку, перелинковку и тексты карточки призыва.
//
// Шаблон карточки читается первым, до перехода этапа и до единого сообщения модели: отказ
// файла обязан стоить нисколько, а не оплаченный ответ разметки. Так же устроена кнопка
// заявки у pprof_2.
func (f *Flow) RunHTML(ctx context.Context, externalID string) error {
	input, err := f.repository.GetGenerationInput(ctx, externalID)
	if err != nil {
		return taskflow.StageFailure(externalID, "load_generation_data", err)
	}
	logger := f.ArticleLogger(input.Article)
	card, err := readCTACard(f.ctaCardPath)
	if err != nil {
		return f.Fail(ctx, logger, input.Article, "load_article_data", err)
	}
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
	// Чат разметки принимает больше одного сообщения: оборванный ответ дописывается
	// продолжением той же стадии, а чат роутера принимает ровно столько сообщений, сколько
	// стадий ему названо при создании.
	//
	// Сверх первого ответа с продолжениями — два сообщения: одно на доспрос перелинковки,
	// второе на тексты карточки призыва. Оба ответа короткие, по строке на слот, и обрываться
	// им не на чем.
	htmlStages := append(generation.HTMLChatStages(StageHTML), StageHTML, StageHTML)
	chat, err := f.NewChat(ctx, input.Article.ID, htmlStages...)
	if err != nil {
		return f.Fail(ctx, logger, input.Article, "html_generation", err)
	}
	defer f.CloseChat(chat, logger, "html_generation")

	started := time.Now()
	logger.Info("html generation started", "stage", "html_generation", "chat", 3)
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
	html = f.completeLinks(ctx, logger, chat, named, f.completeHTMLPage(logger, finalText, html))
	html = f.appendCTA(ctx, logger, chat, card, input.Article.Title, html)
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
// Правило то же, что и у соседних задач: чинить, но не отказывать. Ни одна проверка не имеет
// права уронить стадию — за генерацию уже заплачено.
//
// Чистка своя: у этой площадки в теле статьи нет ни одного инлайнового стиля, поэтому
// CleanPlainBlogMarkup снимает <div> и style вместо того, чтобы переводить классы модели в
// инлайновое оформление, а ряд плашек возвращается списком, а не карточками.
func (f *Flow) completeHTMLPage(logger *slog.Logger, page, markup string) string {
	markup = generation.LinkSources(generation.RestoreStatsList(page,
		generation.CleanPlainBlogMarkup(generation.DropHeading1(markup))))
	if !generation.LeadKept(page, markup) {
		markup = generation.RestoreLead(page, markup)
		logger.Warn("вводный абзац возвращён в разметку кодом", "stage", "html_generation")
	}
	if left := generation.LeftoverBlockMarkers(markup); len(left) > 0 {
		logger.Warn("вёрстка не узнала метки визуальных блоков, они остались в разметке текстом",
			"stage", "html_generation", "markers", strings.Join(left, ", "))
	}
	return markup
}

// appendCTA дописывает карточку призыва в конец разметки.
//
// Тексты спрашиваются у модели отдельным коротким сообщением в том же чате: страница уже в
// истории, и второй раз её передавать нельзя. Отказ доспроса стадию не роняет — карточка
// собирается на умолчаниях: статья без призыва выбивается из блога сильнее, чем статья с
// типовым.
func (f *Flow) appendCTA(ctx context.Context, logger *slog.Logger, chat taskflow.Chat,
	tmpl *template.Template, title, markup string) string {
	var slots map[string]string
	answer, err := f.Answer(ctx, chat.Continue, ctaSlotsPrompt(title), StageHTML)
	if err != nil {
		logger.Warn("тексты карточки призыва не получены, карточка соберётся на умолчаниях",
			"stage", "html_generation", "error", err)
	} else {
		slots = parseCTASlots(answer)
	}
	card, missing := buildCTACard(slots, ctaFallbackURL)
	if len(missing) > 0 {
		logger.Warn("часть текстов карточки призыва заменена умолчаниями",
			"stage", "html_generation", "slots", strings.Join(missing, ", "))
	}
	rendered, err := renderCTACard(tmpl, card)
	if err != nil {
		logger.Warn("карточка призыва не собрана, статья уходит без неё",
			"stage", "html_generation", "error", err)
		return markup
	}
	result, added := appendCTACard(markup, rendered)
	if !added {
		logger.Warn("в разметке уже есть карточка призыва — свою не дописываем",
			"stage", "html_generation")
		return result
	}
	logger.Info("карточка призыва дописана кодом", "stage", "html_generation",
		"button_url", card.ButtonURL)
	return result
}

// enoughInternalLinks — столько ссылок перелинковки в тексте достаточно: недостающие к ним уже
// не доспрашиваются. Доспрос нужен статье, где перелинковки почти нет, а не той, где модель
// уронила одну ссылку из пяти.
const enoughInternalLinks = 3

// completeLinks доводит перелинковку.
//
// Своих предложений код не пишет: шаблонная строка «Подробнее о программе — …» заказчику не
// нужна. Если ссылок в тексте меньше enoughInternalLinks, недостающие просит у модели одно
// короткое сообщение в том же чате — по строке на ссылку, — а вставляет их код.
func (f *Flow) completeLinks(ctx context.Context, logger *slog.Logger, chat taskflow.Chat, links, markup string) string {
	if missing := generation.MissingInternalLinks(markup, links); len(missing) > 0 {
		if placed := generation.PlacedInternalLinks(markup, links); placed >= enoughInternalLinks {
			logger.Info("ссылок перелинковки в тексте достаточно, недостающие не доспрашиваются",
				"stage", "html_generation", "placed_links", placed, "missing_links", len(missing))
		} else {
			markup = f.repairLinks(ctx, logger, chat, links, markup, missing)
		}
	}
	spread, moved := generation.SpreadCrowdedLinks(markup, links)
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

// repairLinks доспрашивает недостающие ссылки у модели. Отказ стадию не роняет: за разметку
// уже заплачено, и страница без части ссылок полезнее, чем её отсутствие.
func (f *Flow) repairLinks(ctx context.Context, logger *slog.Logger, chat taskflow.Chat, links, markup string, missing []string) string {
	logger.Info("ссылок перелинковки в тексте мало, недостающие доспрашиваются у модели",
		"stage", "html_generation", "missing_links", len(missing))
	answer, err := f.Answer(ctx, chat.Continue, generation.RepairLinksPrompt(markup, links, missing), StageHTML)
	if err != nil {
		logger.Warn("доспрос перелинковки не удался", "stage", "html_generation", "error", err)
		return markup
	}
	inserts := generation.ParseLinkInserts(answer, links, missing)
	repaired, skipped := generation.InsertLinkSentences(markup, inserts)
	for _, insert := range skipped {
		logger.Warn("раздел, названный моделью для ссылки, в разметке не найден",
			"stage", "html_generation", "url", insert.URL, "heading", insert.Heading)
	}
	logger.Info("перелинковка дописана моделью", "stage", "html_generation",
		"asked_links", len(missing), "inserted_links", len(inserts)-len(skipped))
	return repaired
}
