package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/integrations/wordpress"
	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/articlefix"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/taskflow"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
)

// articleFixDeps — всё, что задаче правки нужно от composition root.
type articleFixDeps struct {
	profile tasks.Profile
	command taskCommand
	cfg     config.Config
	pool    *pgxpool.Pool
	logger  *slog.Logger
	output  io.Writer
}

// runArticleFix — точка входа задач, которые правят уже опубликованные статьи.
//
// Одна ветка на все такие задачи: поток у них общий (internal/pipeline/articlefix), а
// различаются они промптом, правилом переименования и каталогами — всё это приходит профилем.
// Ветка стоит до общей обвязки: у задач правки свои таблицы, и проверка схемы движка
// (repository.ValidateSchema) искала бы в их схемах article_inputs и article_metadata,
// которых там нет и быть не должно. Операций у них две — import и run; остальные общие
// операции им не принадлежат и отбиваются здесь, а не падают внутри.
func runArticleFix(ctx context.Context, deps articleFixDeps) error {
	repository := articlefix.NewRepository(deps.pool, deps.profile.Name)
	if err := repository.EnsureSchema(ctx); err != nil {
		return err
	}
	switch deps.command.Name {
	case "import":
		return runArticleFixImport(ctx, repository, deps)
	case "run":
		return runArticleFixRun(ctx, repository, deps)
	case "reset":
		return runArticleFixReset(ctx, repository, deps)
	default:
		return fmt.Errorf("задача %s меняет уже опубликованные статьи, и операций у неё три: "+
			"import, run и reset. Операция %q ей не принадлежит", deps.profile.Command, deps.command.Name)
	}
}

// runArticleFixReset возвращает задачу к нулю: пустая таблица и пустой каталог артефактов.
//
// Блог он не откатывает и откатить не может: правки уже опубликованы, а команды, возвращающей
// прежний текст, у приложения нет. Поэтому сброс означает «прогнать заново» — следующий run
// снова заплатит за модель и снова перезапишет те же записи. Об этом сказано прямо в
// подтверждении: перепутать «отменить» и «переделать» здесь стоит дорого.
//
// Только без ID. Сброс одной статьи не поддерживается намеренно: у задачи нет ни промежуточных
// состояний, ни этапов, которые имело бы смысл переигрывать поодиночке, — есть переписанная
// статья и непереписанная.
func runArticleFixReset(ctx context.Context, repository *articlefix.Repository, deps articleFixDeps) error {
	if strings.TrimSpace(deps.command.ExternalID) != "" {
		return fmt.Errorf("%s reset сбрасывает всю задачу и ID не принимает: "+
			"состояний у статьи два — переписана или нет", deps.profile.Command)
	}
	articles, err := repository.Count(ctx)
	if err != nil {
		return err
	}
	folders, err := countDirectoryEntries(deps.cfg.OutputDir)
	if err != nil {
		return err
	}
	fmt.Fprintln(deps.output)
	fmt.Fprintln(deps.output, "Будет удалено безвозвратно:")
	fmt.Fprintf(deps.output, "  статей в схеме %s: %d (счётчик идентификаторов обнулится)\n", deps.profile.Name, articles)
	fmt.Fprintf(deps.output, "  каталогов артефактов в %s: %d\n", deps.cfg.OutputDir, folders)
	fmt.Fprintln(deps.output)
	fmt.Fprintln(deps.output, "Опубликованные правки это не отменяет: статьи в блоге останутся такими,")
	fmt.Fprintln(deps.output, "какими их переписал прогон. Сброс означает «пройти заново», а не «вернуть как было».")
	fmt.Fprintln(deps.output)
	confirmed, err := confirmDestructive(confirmationWord, deps.command.AssumeYes,
		isCharDevice(os.Stdin), os.Stdin, deps.output)
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Fprintln(deps.output, "Отменено.")
		return nil
	}
	if err := repository.Reset(ctx); err != nil {
		return err
	}
	if err := clearDirectoryContents(deps.cfg.OutputDir); err != nil {
		return err
	}
	deps.logger.Info("задача сброшена", "articles", articles, "folders", folders, "output_dir", deps.cfg.OutputDir)
	fmt.Fprintf(deps.output, "Задача %s сброшена: таблица пуста, счётчик идентификаторов с единицы, "+
		"артефакты удалены. Следующий шаг — %s import.\n", deps.profile.Name, deps.profile.Command)
	return nil
}

// runArticleFixImport читает вход задачи: индексы и ссылки на статьи.
func runArticleFixImport(ctx context.Context, repository *articlefix.Repository, deps articleFixDeps) error {
	path, err := articlefix.ResolveInputFile(deps.cfg.InputFilePath, deps.cfg.InputDir)
	if err != nil {
		return err
	}
	sources, err := articlefix.ReadSources(path)
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
	deps.logger.Info("вход задачи импортирован", "file", path,
		"articles", len(sources), "inserted", inserted, "updated", updated)
	fmt.Fprintf(deps.output, "Импортировано из %s: %d статей (новых %d, обновлено %d)\n",
		path, len(sources), inserted, updated)
	return nil
}

// runArticleFixRun проводит статьи через правку: из блога в модель и обратно в блог.
func runArticleFixRun(ctx context.Context, repository *articlefix.Repository, deps articleFixDeps) error {
	articles, err := articleFixArticles(ctx, repository, deps.command.ExternalID)
	if err != nil {
		return err
	}
	if len(articles) == 0 {
		fmt.Fprintf(deps.output, "Статей к правке нет: либо вход не импортирован "+
			"(make %s import), либо все статьи уже переписаны.\n", deps.profile.Command)
		return nil
	}
	rule, err := articleFixTitleRule(deps.profile)
	if err != nil {
		return err
	}
	// Площадка проверяется здесь, а не в общей validateConfig: у задач генерации run уходит
	// только в модель, и требовать от них credentials WordPress нельзя. У этой прогон без
	// блога бессмыслен — читать статью неоткуда и возвращать её некуда.
	if err := deps.cfg.ValidateWordPress(); err != nil {
		return err
	}
	client, err := newWordPressClient(deps.cfg.WordPress)
	if err != nil {
		return err
	}
	blog := articleFixBlog{client: client}

	// План не трогает ни модель, ни блог: он только читает статьи и показывает, во что
	// превратит их заголовки правило переименования. Поэтому и клиента модели не поднимает —
	// браузерный профиль под flock стоит дорого, а плану он не нужен.
	if deps.command.Plan {
		return runArticleFixPlan(ctx, blog, rule, articles, deps)
	}

	// Задача, которая правит только заголовок, к модели не ходит вовсе: ни промпт ей не
	// нужен (он лежит черновиком), ни браузерный профиль под flock, который стоит дорого и
	// поднимался бы ради ни одного запроса.
	var chats taskflow.ChatFactory
	if !deps.profile.ArticleFix.TextUnchanged {
		// Промпт проверяется здесь, после плана и до модели: заведённая, но ещё не заполненная
		// задача правки обязана останавливаться до первого запроса, а не переписывать статьи
		// наугад. План на неё при этом работает — правило переименования проверяют раньше текста.
		if err := articlefix.EnsurePromptFilled(deps.profile.ArticleFix.RewritePromptPath); err != nil {
			return err
		}

		openedChats, closeLLM, err := newArticleFixChats(ctx, deps.profile, newDiagnosticsDirs(deps.profile), deps.logger)
		if err != nil {
			return err
		}
		defer func() {
			if closeErr := closeLLM(); closeErr != nil {
				deps.logger.Warn("не удалось закрыть LLM client", "error", closeErr)
			}
		}()
		chats = openedChats
	} else {
		deps.logger.Info("задача правит только заголовок: текст статей не меняется",
			"task", deps.profile.Name)
		fmt.Fprintf(deps.output, "%s меняет только заголовок: текст статей не трогается.\n", deps.profile.Command)
	}

	flow, err := newArticleFixFlow(repository, blog, chats, rule, deps)
	if err != nil {
		return err
	}
	// Предохранитель считает подряд идущие отказы: единичные не останавливают пачку, а
	// сплошные — останавливают. Без него прогон, у которого слёг провайдер, честно перебирает
	// все оставшиеся статьи по пятнадцать минут на каждую.
	guard := articlefix.NewFailureGuard()
	var failed, rewritten int
	for index, article := range articles {
		if err := flow.Run(ctx, article.ExternalID); err != nil {
			if ctx.Err() != nil {
				return err
			}
			failed++
			deps.logger.Error("статья не переписана", "external_id", article.ExternalID, "error", err)
			fmt.Fprintf(deps.output, "%s — ошибка: %v\n", article.ExternalID, err)
			if stop := guard.Failed(err); stop != nil {
				// Оставшиеся статьи не тронуты: их не пробовали, отметки о правке у них нет,
				// и следующий run возьмёт их сам. Сказать об этом надо здесь — по коду
				// возврата отличить брошенную пачку от пройденной нельзя.
				untouched := len(articles) - index - 1
				deps.logger.Error("прогон остановлен предохранителем",
					"error", stop, "rewritten", rewritten, "failed", failed, "untouched", untouched)
				fmt.Fprintf(deps.output, "\n%v.\nОстальные %d статей не тронуты — их возьмёт следующий %s run.\n",
					stop, untouched, deps.profile.Command)
				return fmt.Errorf("%w (переписано %d, не переписано %d, не тронуто %d)",
					stop, rewritten, failed, untouched)
			}
			continue
		}
		guard.Passed()
		rewritten++
		fmt.Fprintf(deps.output, "%s — переписана\n", article.ExternalID)
	}
	if failed > 0 {
		return fmt.Errorf("не переписано статей: %d из %d", failed, len(articles))
	}
	return nil
}

// newArticleFixFlow собирает поток правки из профиля задачи.
//
// Сборка одна на прогон и на план: файлы промпта и шаблона читаются здесь, поэтому
// отсутствующий промпт роняет команду одинаково в обоих случаях — до первого запроса наружу.
func newArticleFixFlow(repository articlefix.Articles, blog articlefix.Blog, chats taskflow.ChatFactory,
	rule articlefix.TitleRule, deps articleFixDeps) (*articlefix.Flow, error) {
	var options []articlefix.Option
	// Признак приходит из профиля, а не из имени задачи: «правит только заголовок» — это
	// решение задачи, и следующая такая добавляется одной строкой своего профиля.
	if deps.profile.ArticleFix.TextUnchanged {
		options = append(options, articlefix.KeepText())
	}
	// Тем же способом и по той же причине: «промпт переписывает заголовки» — черта задачи,
	// а не имя в switch.
	if deps.profile.ArticleFix.HeadingsRewritten {
		options = append(options, articlefix.RewriteHeadings())
	}
	return articlefix.NewFlow(repository, blog, chats, articlefix.NewArtifacts(deps.cfg.OutputDir),
		rule, deps.profile.ArticleFix.RewritePromptPath, deps.profile.TemplatePath, deps.logger, options...)
}

// articleFixTitleRule выбирает правило переименования по профилю задачи.
//
// Пустой TitleRulePath — не упущение, а решение задачи: заголовок она не трогает вовсе
// (pprof_fix_3). Признак — отсутствие файла в профиле, а не имя задачи в switch: правило это
// данные, и третья задача правки добавляется тем же способом, что первые две.
func articleFixTitleRule(profile tasks.Profile) (articlefix.TitleRule, error) {
	if strings.TrimSpace(profile.ArticleFix.TitleRulePath) == "" {
		return articlefix.KeepTitle{}, nil
	}
	return articlefix.LoadPairRule(profile.ArticleFix.TitleRulePath)
}

// runArticleFixPlan печатает, что произойдёт со статьями, ничего не меняя.
func runArticleFixPlan(ctx context.Context, blog articleFixBlog, rule articlefix.TitleRule,
	articles []articlefix.Article, deps articleFixDeps) error {
	flow, err := newArticleFixFlow(nil, blog, nil, rule, deps)
	if err != nil {
		return err
	}
	// Переименовывает ли задача вообще: у той, что правит один лишь текст (pprof_fix_3,
	// pprof_fix_5), совпавший заголовок — это норма, а не находка плана.
	renames := strings.TrimSpace(deps.profile.ArticleFix.TitleRulePath) != ""
	writer := tabwriter.NewWriter(deps.output, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tЗАПИСЬ\tЗАГОЛОВКОВ\tСИМВОЛОВ\tЗАГОЛОВОК")
	var problems int
	for _, article := range articles {
		planned, planErr := flow.Plan(ctx, article)
		if planErr != nil {
			problems++
			fmt.Fprintf(writer, "%s\t—\t—\t—\tошибка: %v\n", article.ExternalID, planErr)
			continue
		}
		if planned.TitleProblem != "" {
			problems++
			fmt.Fprintf(writer, "%s\t%d\t%d\t%d\tзаголовок не меняется: %s\n",
				planned.ExternalID, planned.PostID, planned.Headings, planned.HTMLRunes, planned.TitleProblem)
			continue
		}
		// Название записи печатается всегда, а стрелка под ним — только если оно меняется:
		// у задачи, которая правит один лишь текст, стрелка «в самого себя» повторялась бы
		// на всей пачке.
		fmt.Fprintf(writer, "%s\t%d\t%d\t%d\t%s%s\n",
			planned.ExternalID, planned.PostID, planned.Headings, planned.HTMLRunes,
			planned.OldTitle, unchangedMark(planned.NewTitle == planned.OldTitle))
		if planned.NewTitle != planned.OldTitle {
			fmt.Fprintf(writer, "  →\t\t\t\t%s\n", planned.NewTitle)
		}
		// H1 и SEO-заголовок печатаются независимо от названия записи: это три разных
		// значения, и переименовать могли уже какое-то одно. Молчание про H1 у статьи с
		// совпавшим названием означало бы «ничего не изменится», а меняется как раз он —
		// именно его человек и читает на странице.
		printPlannedField(writer, "H1", planned.OldHeader, planned.NewHeader)
		printPlannedField(writer, "SEO", planned.OldSEOTitle, planned.NewSEOTitle)
		if renames && !planned.Changes() {
			fmt.Fprintf(writer, "  !\t\t\t\tни один заголовок не меняется — правка не нужна\n")
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(deps.output, "\nСтатей к правке: %d, из них с вопросами: %d. "+
		"Ничего не изменено — это план.\n", len(articles), problems)
	return nil
}

// articleFixArticles выбирает статьи прогона: одну по индексу или все непереписанные.
func articleFixArticles(ctx context.Context, repository *articlefix.Repository, externalID string) ([]articlefix.Article, error) {
	if strings.TrimSpace(externalID) == "" {
		return repository.ListPending(ctx)
	}
	article, err := repository.Get(ctx, externalID)
	if err != nil {
		return nil, err
	}
	return []articlefix.Article{article}, nil
}

// articleFixBlog — переходник от контракта потока к клиенту WordPress.
//
// Он и есть единственное место, где задача встречается с площадкой: сам поток знает только
// «найти по слагу, прочитать, переписать» и о XML-RPC не подозревает.
type articleFixBlog struct{ client *wordpress.Client }

// articleFixPostTypes — где искать статью по слагу и в каком порядке.
//
// Список, а не один тип: статьи площадки живут в разных типах записей. Первым идёт тип
// страниц услуг — тот же, в который публикует pprof_2, и именно в нём лежат страницы из
// входного файла; за ним обычные записи блога и страницы. Берётся первое совпадение.
//
// Список общий у всех задач правки: площадка одна, и второй копии порядка типов быть не должно.
var articleFixPostTypes = []string{coursePostType, "post", "page"}

func (b articleFixBlog) Find(ctx context.Context, slug string) (articlefix.Post, error) {
	found, err := b.client.FindPostBySlug(ctx, articleFixPostTypes, slug)
	if err != nil {
		return articlefix.Post{}, err
	}
	return articlefix.Post{ID: found.ID, Link: found.Link}, nil
}

func (b articleFixBlog) Read(ctx context.Context, postID int64) (articlefix.Post, error) {
	stored, err := b.client.GetPost(ctx, postID)
	if err != nil {
		return articlefix.Post{}, err
	}
	return articlefix.Post{
		ID: stored.ID, Title: stored.Title, ContentHTML: stored.ContentHTML, Link: stored.Link,
		Fields: stored.Fields, FieldIDs: stored.FieldIDs,
	}, nil
}

func (b articleFixBlog) Write(ctx context.Context, postID int64, title, contentHTML string,
	fields []articlefix.Field) error {
	updates := make([]wordpress.FieldUpdate, 0, len(fields))
	for _, field := range fields {
		updates = append(updates, wordpress.FieldUpdate{ID: field.ID, Key: field.Key, Value: field.Value})
	}
	return b.client.EditPost(ctx, wordpress.PostUpdate{
		PostID: postID, Title: title, ContentHTML: contentHTML, Fields: updates,
	})
}

// newArticleFixChats поднимает диалоги с моделью по схеме стадий задачи.
//
// Схема одна (наложения у задач правки нет), поэтому резолвер режимов здесь не нужен:
// выбирать между Gemini и DeepSeek не из чего, а конвейер generation.Pipeline задача не
// использует вовсе — ей нужен только чат.
func newArticleFixChats(ctx context.Context, profile tasks.Profile, debugDirs diagnosticsDirs,
	logger *slog.Logger) (taskflow.ChatFactory, func() error, error) {
	noop := func() error { return nil }
	configs, err := loadStageConfigs(profile, logger, true)
	if err != nil {
		return nil, noop, err
	}
	scheme := configs.deepseek
	providers := configs.providers()
	models := configs.models()
	used := make(map[string]struct{})
	for _, stage := range scheme.Stages {
		for _, target := range stage.Targets {
			used[target.Provider] = struct{}{}
		}
	}
	clients := make(map[string]llm.Client, len(used))
	var closers []func() error
	closeAll := func() error {
		var errs []error
		for _, closeClient := range closers {
			if closeErr := closeClient(); closeErr != nil {
				errs = append(errs, fmt.Errorf("закрыть LLM client: %w", closeErr))
			}
		}
		return errors.Join(errs...)
	}
	for _, name := range sortedKeys(used) {
		client, closer, clientErr := newLLMClient(ctx, name, providers[name], models[name], debugDirs.deepseek, logger)
		if clientErr != nil {
			return nil, closeAll, fmt.Errorf("создать LLM provider %q: %w", name, clientErr)
		}
		clients[name] = client
		if closer != nil {
			closers = append(closers, closer)
		}
	}
	return taskflow.NewRouterChats(llm.NewRouter(scheme, clients, logger)), closeAll, nil
}

// printPlannedField печатает одно поле заголовка: было и, если меняется, стало.
//
// Пустое «было» означает, что поля у записи нет вовсе, — печатать нечего. Непустое «было»
// при пустом «стало» означает, что правило к нему не подошло: для SEO-заголовка это норма
// (формулировка у него своя), и человек должен видеть, что там осталось прежним.
func printPlannedField(writer io.Writer, name, old, renamed string) {
	if old == "" {
		return
	}
	fmt.Fprintf(writer, "  %s\t\t\t\t%s%s\n", name, old, unchangedMark(renamed == ""))
	if renamed != "" {
		fmt.Fprintf(writer, "  →\t\t\t\t%s\n", renamed)
	}
}

// unchangedMark — пометка «не меняется» там, где стрелки со «стало» не будет.
func unchangedMark(unchanged bool) string {
	if unchanged {
		return " (не меняется)"
	}
	return ""
}
