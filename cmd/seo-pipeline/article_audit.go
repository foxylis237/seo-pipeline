package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/foxylis237/seo-pipeline/internal/catalog"
	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/integrations/wordpress"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/articleaudit"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/pagebatch"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/taskflow"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
)

// articleAuditDeps — всё, что задаче аудита нужно от composition root.
type articleAuditDeps struct {
	profile tasks.Profile
	command taskCommand
	cfg     config.Config
	pool    *pgxpool.Pool
	logger  *slog.Logger
	output  io.Writer
}

// runArticleAudit — точка входа задач, которые проверяют уже опубликованные страницы.
//
// Одна ветка на все такие задачи: поток у них общий (internal/pipeline/articleaudit), а
// различаются они регламентом проверки, списком обязательных полей и каталогами — всё это
// приходит профилем. Ветка стоит до общей обвязки: у задач аудита свои таблицы, и проверка
// схемы движка (repository.ValidateSchema) искала бы в их схемах article_inputs и
// article_metadata, которых там нет и быть не должно. Операций у них три — import, run и
// reset; остальные общие операции им не принадлежат и отбиваются здесь, а не падают внутри.
func runArticleAudit(ctx context.Context, deps articleAuditDeps) error {
	repository := articleaudit.NewRepository(deps.pool, deps.profile.Name)
	if err := repository.EnsureSchema(ctx); err != nil {
		return err
	}
	switch deps.command.Name {
	case "import":
		return runArticleAuditImport(ctx, repository, deps)
	case "run":
		return runArticleAuditRun(ctx, repository, deps)
	case "reset":
		return runArticleAuditReset(ctx, repository, deps)
	case "report":
		return runArticleAuditReport(ctx, repository, deps)
	default:
		return fmt.Errorf("задача %s проверяет уже опубликованные страницы, и операций у неё четыре: "+
			"import, run, report и reset. Операция %q ей не принадлежит", deps.profile.Command, deps.command.Name)
	}
}

// runArticleAuditImport читает вход задачи: индексы, ссылки и, если они есть, идентификаторы
// записей и темы страниц.
func runArticleAuditImport(ctx context.Context, repository *articleaudit.Repository, deps articleAuditDeps) error {
	path, err := pagebatch.ResolveInputFile(deps.cfg.InputFilePath, deps.cfg.InputDir)
	if err != nil {
		return err
	}
	sources, kind, err := pagebatch.ReadTable(path)
	if err != nil {
		return err
	}
	if limit := deps.command.ImportLimit; limit > 0 && limit < len(sources) {
		sources = sources[:limit]
	}
	inserted, updated, err := repository.Import(ctx, sources)
	if err != nil {
		return err
	}
	// Каким разбором прочитан файл — обязательная часть отчёта: у книги с колонками и у
	// списка строк правила разные, и молча прочитать идентификатор записи как индекс статьи
	// нельзя.
	deps.logger.Info("вход задачи импортирован", "file", path, "input_parse", string(kind),
		"articles", len(sources), "inserted", inserted, "updated", updated)
	fmt.Fprintf(deps.output, "Импортировано из %s (%s): %d страниц (новых %d, обновлено %d)\n",
		path, parseKindText(kind), len(sources), inserted, updated)
	return nil
}

// parseKindText объясняет человеку, каким разбором прочитан файл.
func parseKindText(kind pagebatch.ParseKind) string {
	if kind == pagebatch.ParseColumns {
		return "по колонкам с заголовками"
	}
	return "построчно, «индекс + ссылка»"
}

// pageBatch — общая часть зависимостей и слова, которыми задача аудита называет свою работу.
func (deps articleAuditDeps) pageBatch() pageBatchDeps {
	return pageBatchDeps{
		profile:   deps.profile,
		command:   deps.command,
		outputDir: deps.cfg.OutputDir,
		logger:    deps.logger,
		output:    deps.output,
		words: pageBatchWords{
			ItemsGenitive: "страниц",
			ItemGenitive:  "страницы",
			States:        "проверена или нет",
			Done:          "проверена",
			DoneMany:      "проверено",
			FailedMany:    "не проверено",
			FailedOne:     "страница не проверена",
			DoneCounter:   "audited",
			ResetConsequence: []string{
				"Вместе с ними удалятся отчёты, за которые уже заплачено:",
				"следующий прогон соберёт их заново и спросит модель ещё раз.",
			},
		},
	}
}

// runArticleAuditReset возвращает задачу к нулю: пустая таблица и пустой каталог артефактов.
//
// Блог этим не затрагивается вовсе — аудит в него ничего и не писал. Но отчёты, за которые
// заплачено, сброс удаляет безвозвратно, и следующий run заплатит за них снова.
//
// Сам сброс общий у задач правки и аудита (runPageBatchReset): различаются только слова.
func runArticleAuditReset(ctx context.Context, repository *articleaudit.Repository, deps articleAuditDeps) error {
	return runPageBatchReset(ctx, repository, deps.pageBatch())
}

// runArticleAuditReport собирает сводку по всей пачке.
//
// Ни сети, ни модели: всё берётся из базы и уже сохранённых артефактов. Поэтому пересобирать
// её можно сколько угодно раз — в том числе после того, как список обязательного изменили.
//
// Сводка нужна там, где отчёты по одной статье бессильны: одна и та же ошибка на сорока
// страницах выглядит в отчёте так же, как ошибка на одной, и увидеть, что чинить всей пачкой,
// можно только сложив их вместе.
func runArticleAuditReport(ctx context.Context, repository *articleaudit.Repository, deps articleAuditDeps) error {
	articles, err := repository.List(ctx)
	if err != nil {
		return err
	}
	if len(articles) == 0 {
		fmt.Fprintf(deps.output, "Страниц в задаче нет: сначала %s import.\n", deps.profile.Command)
		return nil
	}
	summary := articleaudit.BuildSummary(articles, articleaudit.NewArtifacts(deps.cfg.OutputDir),
		deps.profile.ArticleAudit.RequiredFields)
	text := summary.Render(deps.profile.Name)
	path := filepath.Join(deps.cfg.OutputDir, articleaudit.SummaryFile)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return fmt.Errorf("записать сводку %q: %w", path, err)
	}
	deps.logger.Info("сводка собрана", "pages", len(summary.Pages), "failed", len(summary.Failed),
		"field_gaps", len(summary.MissingFields), "common_issues", len(summary.CommonIssues), "file", path)
	fmt.Fprint(deps.output, text)
	fmt.Fprintf(deps.output, "\nСводка сохранена: %s\n", path)
	return nil
}

// runArticleAuditRun проводит страницы через проверку: из блога в модель и в отчёт.
//
// Порядок шагов тот же, что у задач правки: список страниц → площадка → план (до модели) →
// промпт → чаты → поток → предохранитель. План возвращает управление раньше подъёма LLM
// намеренно: браузерный профиль под flock стоит дорого, а плану он не нужен.
func runArticleAuditRun(ctx context.Context, repository *articleaudit.Repository, deps articleAuditDeps) error {
	articles, err := articleAuditArticles(ctx, repository, deps.command.ExternalID)
	if err != nil {
		return err
	}
	if len(articles) == 0 {
		fmt.Fprintf(deps.output, "Страниц к проверке нет: либо вход не импортирован "+
			"(make %s import), либо все страницы уже проверены.\n", deps.profile.Command)
		return nil
	}
	// Площадка проверяется здесь, а не в общей validateConfig: у задач генерации run уходит
	// только в модель, и требовать от них credentials WordPress нельзя. У этой прогон без
	// блога бессмыслен — читать страницу неоткуда.
	if err := deps.cfg.ValidateWordPress(); err != nil {
		return err
	}
	client, err := newWordPressClient(deps.cfg.WordPress)
	if err != nil {
		return err
	}
	blog := articleAuditBlog{client: client}

	if deps.command.Plan {
		return runArticleAuditPlan(ctx, blog, articles, deps)
	}

	// Промпт проверяется здесь, после плана и до модели: заведённая, но ещё не согласованная
	// задача обязана останавливаться до первого запроса — за каждый ответ платим. План на неё
	// при этом работает: он затем и нужен, чтобы посмотреть пачку и пустые обязательные поля,
	// пока регламент проверки пишется.
	if err := articleaudit.EnsurePromptFilled(deps.profile.ArticleAudit.AuditPromptPath); err != nil {
		return err
	}
	chats, closeLLM, err := newArticleAuditChats(ctx, deps.profile, newDiagnosticsDirs(deps.profile), deps.logger)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := closeLLM(); closeErr != nil {
			deps.logger.Warn("не удалось закрыть LLM client", "error", closeErr)
		}
	}()

	flow, err := newArticleAuditFlow(repository, blog, chats, deps)
	if err != nil {
		return err
	}
	externalIDs := make([]string, 0, len(articles))
	for _, article := range articles {
		externalIDs = append(externalIDs, article.ExternalID)
	}
	return runPageBatch(ctx, externalIDs, flow.Run, deps.pageBatch())
}

// newArticleAuditFlow собирает поток проверки из профиля задачи.
//
// Сборка одна на прогон и на план: промпт и шаблон отчёта читаются здесь, поэтому ошибка в
// любом из них роняет команду одинаково в обоих случаях — до первого запроса наружу.
func newArticleAuditFlow(repository articleaudit.Articles, blog articleaudit.Blog,
	chats taskflow.ChatFactory, deps articleAuditDeps) (*articleaudit.Flow, error) {
	return articleaudit.NewFlow(repository, blog, chats, articleaudit.NewArtifacts(deps.cfg.OutputDir),
		deps.profile.ArticleAudit.AuditPromptPath, deps.profile.TemplatePath,
		deps.profile.ArticleAudit.RequiredFields, deps.logger)
}

// runArticleAuditPlan печатает, что будет проверено, ничего не меняя и не спрашивая модель.
//
// Проверка обязательных полей попадает сюда целиком: она ничего не стоит, а половину пользы
// отчёта даёт до первого рубля за модель.
func runArticleAuditPlan(ctx context.Context, blog articleAuditBlog,
	articles []articleaudit.Article, deps articleAuditDeps) error {
	flow, err := newArticleAuditFlow(nil, blog, nil, deps)
	if err != nil {
		return err
	}
	required := len(deps.profile.ArticleAudit.RequiredFields)
	writer := tabwriter.NewWriter(deps.output, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tЗАПИСЬ\tНАЙДЕНА\tЗАГОЛОВКОВ\tСИМВОЛОВ\tЗАГОЛОВОК")
	var problems, checked int
	for _, article := range articles {
		planned, planErr := flow.Plan(ctx, article)
		if planErr != nil {
			problems++
			fmt.Fprintf(writer, "%s\t—\t—\t—\t—\tошибка: %v\n", article.ExternalID, planErr)
			continue
		}
		if planned.Audited {
			checked++
		}
		fmt.Fprintf(writer, "%s\t%d\t%s\t%d\t%d\t%s\n",
			planned.ExternalID, planned.PostID, foundByText(planned.FoundBy),
			planned.Headings, planned.HTMLRunes, planned.Title)
		if planned.Topic != "" {
			fmt.Fprintf(writer, "  тема\t\t\t\t\t%s\n", planned.Topic)
		}
		// Пустые обязательные поля — половина пользы отчёта, и достаётся она бесплатно.
		if len(planned.Fields.Missing) > 0 {
			fmt.Fprintf(writer, "  !\t\t\t\t\tнет в записи: %s\n", strings.Join(planned.Fields.Missing, ", "))
			problems++
		}
		if len(planned.Fields.Empty) > 0 {
			fmt.Fprintf(writer, "  !\t\t\t\t\tне заполнены: %s\n", strings.Join(planned.Fields.Empty, ", "))
			problems++
		}
		// Сколько в блоке вопросов — число, а не находка: сколько их должно быть, никто не
		// решал, и человек смотрит на него сам.
		fmt.Fprintf(writer, "  ·\t\t\t\t\tвопросов в блоке FAQ: %d\n", planned.Fields.FAQ)
		// Поля выдачи показываются всегда, с запасом: длину человек на глаз не отмеряет, а
		// обрезанное описание увидит только в выдаче и через недели. «58 из 60» формально
		// проходит, но следующая правка его переполнит — и это видно заранее.
		for _, measure := range planned.Fields.Measured {
			switch {
			case !measure.Filled():
				fmt.Fprintf(writer, "  ·\t\t\t\t\t%s: не заполнено\n", measure.Field)
			case !measure.Fits():
				fmt.Fprintf(writer, "  !\t\t\t\t\t%s: %d знаков при пределе %d — в выдаче обрежется\n",
					measure.Field, measure.Length, measure.Limit)
				problems++
			default:
				fmt.Fprintf(writer, "  ·\t\t\t\t\t%s: %d из %d знаков\n",
					measure.Field, measure.Length, measure.Limit)
			}
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(deps.output, "\nСтраниц к проверке: %d, из них уже проверенных: %d, с вопросами: %d.\n",
		len(articles), checked, problems)
	if required == 0 {
		fmt.Fprintf(deps.output, "Проверка обязательных полей выключена: список у задачи %s пуст.\n",
			deps.profile.Name)
	} else {
		fmt.Fprintf(deps.output, "Обязательных полей в списке: %d.\n", required)
	}
	fmt.Fprintf(deps.output, "Длина полей выдачи проверена всегда: %s до %d знаков, %s до %d.\n",
		articleaudit.SEOTitleField, articleaudit.SEOTitleLimit,
		articleaudit.SEOMetaDescriptionField, articleaudit.SEOMetaDescriptionLimit)
	fmt.Fprintf(deps.output, "Проверка пойдёт промптом %s. Ничего не изменено — это план.\n",
		deps.profile.ArticleAudit.AuditPromptPath)
	return nil
}

// foundByText объясняет, каким путём нашлась запись: по идентификатору из книги это один
// запрос, перебором по слагу — до тридцати страниц по сотне записей.
func foundByText(foundBy string) string {
	if foundBy == "id" {
		return "по ID"
	}
	return "по слагу"
}

// articleAuditArticles выбирает страницы прогона: одну по индексу или все непроверенные.
func articleAuditArticles(ctx context.Context, repository *articleaudit.Repository, externalID string) (
	[]articleaudit.Article, error) {
	if strings.TrimSpace(externalID) == "" {
		return repository.ListPending(ctx)
	}
	article, err := repository.Get(ctx, externalID)
	if err != nil {
		return nil, err
	}
	return []articleaudit.Article{article}, nil
}

// articleAuditBlog — переходник от контракта потока к клиенту WordPress.
//
// Методов два, и оба читающие. Своего типа, а не общего с задачами правки, именно поэтому:
// у того есть Write, и один переходник на обе задачи означал бы, что «аудит не пишет в блог»
// держится тем, что его никто не позвал.
type articleAuditBlog struct{ client *wordpress.Client }

// articleAuditPostTypes — где искать страницу по слагу и в каком порядке.
//
// Список свой, а не общий с задачами правки: у тех пачки медицинские, и трёх типов им хватает,
// а аудит проверяет услуги площадки целиком — повышение квалификации, переподготовку,
// безопасность, аттестацию. Их слаги в списке правки не ищутся вовсе, и страница падала бы с
// «в WordPress нет записи типа obuch_med, post, page».
//
// Типы услуг берутся у каталога (catalog.PostTypes) — там этот список закрытый и уже описан;
// второй его копии в composition root быть не должно. За ними идут обычные записи и страницы:
// на них услуга не заводится, но искать её там дешевле, чем объявлять ненайденной.
var articleAuditPostTypes = append(catalog.PostTypes(), "post", "page")

func (b articleAuditBlog) Find(ctx context.Context, slug string) (articleaudit.Post, error) {
	found, err := b.client.FindPostBySlug(ctx, articleAuditPostTypes, slug)
	if err != nil {
		return articleaudit.Post{}, err
	}
	return articleaudit.Post{ID: found.ID, Slug: found.Slug, PostType: found.PostType, Link: found.Link}, nil
}

func (b articleAuditBlog) Read(ctx context.Context, postID int64) (articleaudit.Post, error) {
	stored, err := b.client.GetPost(ctx, postID)
	if err != nil {
		return articleaudit.Post{}, err
	}
	return articleaudit.Post{
		ID: stored.ID, Title: stored.Title, Slug: stored.Slug, PostType: stored.PostType,
		ContentHTML: stored.ContentHTML, Link: stored.Link, Fields: stored.Fields,
		ThumbnailID: stored.ThumbnailID, TermIDs: stored.TermIDs,
	}, nil
}

// newArticleAuditChats поднимает диалоги с моделью по схеме стадий задачи.
//
// Тот же подъём, что у задач правки, и по той же причине: схема одна, наложения нет, выбирать
// между провайдерами не из чего, а конвейер generation.Pipeline задача не использует вовсе —
// ей нужен только чат.
func newArticleAuditChats(ctx context.Context, profile tasks.Profile, debugDirs diagnosticsDirs,
	logger *slog.Logger) (taskflow.ChatFactory, func() error, error) {
	return newSingleSchemeChats(ctx, profile, debugDirs, logger)
}
