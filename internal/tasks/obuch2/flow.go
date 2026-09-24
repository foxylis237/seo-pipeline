package obuch2

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
	"github.com/foxylis237/seo-pipeline/internal/tasks"
)

// Repository — то, что поток требует от хранилища. Интерфейс объявлен у потребителя и
// перечисляет ровно используемые методы: реализует его существующий репозиторий движка.
//
// SaveArticleInfo здесь нет, в отличие от pprof_2: блок частых вопросов остаётся в теле
// страницы, потому что поля под него у площадки не существует, и складывать в
// article_metadata нечего.
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
	SaveError(ctx context.Context, articleID int64, processingErr error) error
}

// Writer — то, что поток требует от файлового слоя. Все методы уже есть у writer движка:
// артефакты obuch_2 ложатся в те же слоты, что и артефакты остальных задач.
type Writer interface {
	StageStructure(externalID, slug, prompt, structure string) (*articleoutput.PendingArtifact, error)
	StageArticle(externalID, slug, prompt, text, model string) (*articleoutput.PendingArtifact, error)
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

// Flow — поток генерации obuch_2.
//
// Три чата, и границы между ними значимы:
//
//	Чат 1: structure
//	Чат 2: article → review
//	Чат 3: html + надпись кнопки заявки
//
// Порядок стадий тот же, что у pprof_2, а расходится третий чат: площадка другая, у неё
// другая вёрстка — классы темы вместо инлайновых стилей, — и надпись кнопки заявки у каждой
// страницы своя. Общий поток на две площадки означал бы флаг внутри чистки разметки, а
// править работающий поток соседа ради новой задачи нельзя.
type Flow struct {
	*taskflow.Base
	repository Repository
	writer     Writer
	// ctaCardPath — шаблон карточки призыва. Поле, а не константа по месту: тест подставляет
	// временный файл, а прогон — CTACardPath.
	ctaCardPath string
	// blockTemplatesDir — каталог шаблонов визуальных блоков тела страницы. Поле по той же
	// причине, что и ctaCardPath: тест подставляет свой каталог.
	blockTemplatesDir string
}

// NewFlow собирает поток. publisher необязателен и может быть nil.
//
// Каталог услуг потоку не передаётся, в отличие от obuch_1: адреса у кнопки нет вовсе, а
// перелинковки на странице услуги не бывает — сверять нечего.
func NewFlow(repository Repository, writer Writer, chats taskflow.ChatFactory,
	prompts taskflow.PromptRenderer, logger *slog.Logger, publisher taskflow.PromptPublisher) *Flow {
	return &Flow{
		Base:              taskflow.NewBase(repository, writer, chats, prompts, logger, publisher),
		repository:        repository,
		writer:            writer,
		ctaCardPath:       CTACardPath,
		blockTemplatesDir: tasks.CommonBlockTemplatesDir,
	}
}

// RunStructure выполняет чат 1. Отдельный чат нужен потому, что структура — вход для всей
// остальной работы, и историю её обсуждения тащить в текст страницы незачем.
//
// Сама композиция страницы здесь не сочиняется: скелет из десяти H2 снят с живых страниц и
// зашит в промпт. Чат заполняет в нём формулировки заголовков, список модулей под конкретную
// профессию и портреты аудитории.
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
	// Промпт просит структуру готовой к копированию одним кликом, и модель оборачивает её в
	// кодовый блок. Для артефакта и для следующей стадии это мусор.
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

// RunArticle выполняет чат 2 целиком: article → review.
//
// Чат неделим намеренно. Ревью опирается на историю первого сообщения, а браузерная беседа не
// переживает завершения процесса — значит и возобновлять её посередине нечем. Поэтому оба
// артефакта публикуются одним Commit: страница либо прошла чат 2 целиком, либо не начинала
// его, и промежуточных состояний, из которых нельзя продолжить, не возникает.
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
	// Запись заголовков приводится к одному виду до сохранения: стадия html расставляет теги
	// по ней, а модель за один прогон свободно переходит с «H2 - » на «H2:» и на Markdown.
	out.articleText = generation.NormalizeHeadings(out.articleText)
	logger.Info("article generated", "stage", "article_generation", "prompt_size", len([]rune(out.articlePrompt)))

	if out.reviewPrompt, out.reviewedPage, err = f.Message(ctx, chat.Continue,
		StageReview, reviewData(out.articleText)); err != nil {
		return out, f.Fail(ctx, logger, input.Article, "article_review", err)
	}
	out.reviewedPage = generation.NormalizeHeadings(out.reviewedPage)
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
// считает этап невыполненным по пустому пути и возвращался бы на него вечно.
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
	pagePath := finalPending.Paths.FixedArticlePath
	commitErr := articleoutput.Commit(func() error {
		if err := f.repository.SaveGenerationPaths(ctx, selected.ID, structurePath, articlePending.Paths.ArticlePath); err != nil {
			return err
		}
		if err := f.repository.SaveReviewPath(ctx, selected.ID, pagePath); err != nil {
			return err
		}
		return f.repository.SaveFixedArticlePath(ctx, selected.ID, pagePath)
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
		"draft_path", articlePending.Paths.ArticlePath, "result_path", pagePath)
	return nil
}

// RunHTML выполняет чат 3: разметку и надпись кнопки заявки.
//
// Шаблон кнопки читается первым, до перехода этапа и до единого сообщения модели: отказ файла
// обязан стоить нисколько, а не оплаченный ответ разметки.
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
			fmt.Errorf("финальный текст страницы не сохранён: сначала выполните этап article"))
	}
	finalText, err := f.writer.Read(saved.FixedArticlePath)
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
	//
	// Сверх первого ответа с продолжениями — одно сообщение на надпись кнопки. Ответ на него
	// короткий, в одну строку, и обрываться ему не на чем. Доспроса перелинковки здесь нет:
	// ссылок у страницы услуги не бывает.
	htmlStages := append(generation.HTMLChatStages(StageHTML), StageHTML)
	chat, err := f.NewChat(ctx, input.Article.ID, htmlStages...)
	if err != nil {
		return f.Fail(ctx, logger, input.Article, "html_generation", err)
	}
	defer f.CloseChat(chat, logger, "html_generation")

	started := time.Now()
	logger.Info("html generation started", "stage", "html_generation", "chat", 3)
	// AcceptIncomplete не включается намеренно: у коммерческой страницы половина текста в
	// блоге хуже отказа — читатель видит обрыв на середине модулей, а цена страницы стоит в
	// плашке над ним. Так же устроен pprof_2.
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
	// Порядок обязателен: чистка снимает <div> и style, поэтому оформление блоков ставится
	// после неё, а карточка призыва — последней, уже поверх оформленного тела.
	html = f.decorate(logger, f.completeHTMLPage(logger, finalText, html))
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

// completeHTMLPage доводит разметку до вёрстки площадки.
//
// Правило то же, что и у соседних задач: чинить, но не отказывать. Ни одна проверка здесь не
// имеет права уронить стадию — за разметку уже заплачено. Отказывает выше только обрыв
// ответа, и это другая история: там половины страницы нет вовсе.
//
// Чистка своя: у этой площадки в теле нет ни одного инлайнового стиля, поэтому
// CleanPlainBlogMarkup снимает <div> и style вместо того, чтобы переводить классы модели в
// инлайновое оформление. Плашка параметров возвращается одноколоночной таблицей, а не
// списком, как у статей блога той же площадки: на живых страницах услуг это единственная
// таблица, и выглядит она именно так.
func (f *Flow) completeHTMLPage(logger *slog.Logger, page, markup string) string {
	markup = generation.RestoreStatsTable(page,
		generation.CleanPlainBlogMarkup(generation.DropHeading1(markup)))
	// Лид ищется в тексте без строк-маркеров. Строка «ПЛАШКИ: …» длиннее вводных абзацев и
	// сходит за первый абзац страницы — а вернуть её в разметку значит отправить в блог саму
	// инструкцию вёрстке, текстом. В разметке её к этому моменту нет по построению: она уже
	// стала таблицей.
	lead := generation.DropBlockMarkerLines(page)
	if !generation.LeadKept(lead, markup) {
		markup = generation.RestoreLead(lead, markup)
		logger.Warn("вводный абзац возвращён в разметку кодом", "stage", "html_generation")
	}
	if left := generation.LeftoverBlockMarkers(markup); len(left) > 0 {
		logger.Warn("вёрстка не узнала метки визуальных блоков, они остались в разметке текстом",
			"stage", "html_generation", "markers", strings.Join(left, ", "))
	}
	return markup
}

// decorate разворачивает помеченные визуальные блоки в оформление площадки.
//
// Идёт после чистки: CleanPlainBlogMarkup снимает <div> и style, и поставленное до неё
// оформление она бы и сняла. Отказ шаблонов стадию не роняет — страница остаётся с обычными
// таблицами и списками, это хуже видом, но не содержанием.
//
// Оформление общее с obuch_1 и живёт в движке: площадка одна, правила у статьи блога и у
// страницы услуги совпадают, и второй копии этих правил быть не должно. Страница услуги
// реально приводит сюда таблицы, пары «вопрос — ответ» и строку источника; метки sp-note,
// sp-stats и sp-steps её промпты не просят, и на такой разметке DecorateBlocks ничего не
// меняет.
func (f *Flow) decorate(logger *slog.Logger, markup string) string {
	blocks, err := generation.ReadBlockTemplates(f.blockTemplatesDir)
	if err != nil {
		logger.Warn("шаблоны визуальных блоков не прочитаны, страница уходит без оформления блоков",
			"stage", "html_generation", "error", err)
		return markup
	}
	return generation.DecorateBlocks(markup, blocks)
}

// appendCTA дописывает карточку призыва в конец разметки.
//
// Заголовок раздела и абзац перед карточкой модель уже написала — они часть страницы и стоят
// в её скелете. Кодом ставится только карточка: у неё фиксированная вёрстка с инлайновыми
// стилями, снятая с живых страниц площадки, и класс service на кнопке, за который цепляется
// форма заявки.
//
// Строки карточки спрашиваются отдельным коротким сообщением в том же чате: страница уже в
// истории, и второй раз её передавать нельзя. Отказ доспроса стадию не роняет — карточка
// собирается на умолчаниях: страница с типовой карточкой полезнее отказа, за генерацию уже
// заплачено.
func (f *Flow) appendCTA(ctx context.Context, logger *slog.Logger, chat taskflow.Chat,
	tmpl *template.Template, title, markup string) string {
	var slots map[string][]string
	answer, err := f.Answer(ctx, chat.Continue, ctaSlotsPrompt(title), StageHTML)
	if err != nil {
		logger.Warn("строки карточки призыва не получены, карточка соберётся на умолчаниях",
			"stage", "html_generation", "error", err)
	} else {
		slots = parseCTASlots(answer)
	}
	card, missing := buildCTACard(slots)
	if len(missing) > 0 {
		logger.Warn("строки карточки призыва заменены умолчаниями",
			"stage", "html_generation", "slots", strings.Join(missing, ", "))
	}
	rendered, err := renderCTACard(tmpl, card)
	if err != nil {
		logger.Warn("карточка призыва не собрана, страница уходит без неё",
			"stage", "html_generation", "error", err)
		return markup
	}
	result, added := appendCTACard(markup, rendered)
	if !added {
		logger.Warn("в разметке уже есть призыв — свою карточку не дописываем",
			"stage", "html_generation", "cta_card_path", f.ctaCardPath)
		return result
	}
	logger.Info("карточка призыва дописана кодом", "stage", "html_generation",
		"button_text", card.ButtonText)
	return result
}
