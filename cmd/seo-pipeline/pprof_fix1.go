package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"text/tabwriter"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/integrations/wordpress"
	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/taskflow"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pproffix1"
)

// pprofFix1Deps — всё, что задаче правки нужно от composition root.
type pprofFix1Deps struct {
	profile tasks.Profile
	command taskCommand
	cfg     config.Config
	pool    *pgxpool.Pool
	logger  *slog.Logger
	output  io.Writer
}

// runPProfFix1 — точка входа задачи pprof_fix_1.
//
// Задача обрабатывается отдельной веткой, до общей обвязки: у неё свои таблицы, и проверка
// схемы движка (repository.ValidateSchema) искала бы в её схеме article_inputs и
// article_metadata, которых там нет и быть не должно. Операций у неё две — import и run;
// остальные общие операции ей не принадлежат и отбиваются здесь, а не падают внутри.
func runPProfFix1(ctx context.Context, deps pprofFix1Deps) error {
	repository := pproffix1.NewRepository(deps.pool)
	if err := repository.EnsureSchema(ctx); err != nil {
		return err
	}
	switch deps.command.Name {
	case "import":
		return runPProfFix1Import(ctx, repository, deps)
	case "run":
		return runPProfFix1Run(ctx, repository, deps)
	default:
		return fmt.Errorf("задача %s меняет уже опубликованные статьи, и операций у неё две: "+
			"import и run. Операция %q ей не принадлежит", pproffix1.Command, deps.command.Name)
	}
}

// runPProfFix1Import читает вход задачи: индексы и ссылки на статьи.
func runPProfFix1Import(ctx context.Context, repository *pproffix1.Repository, deps pprofFix1Deps) error {
	path, err := pproffix1.ResolveInputFile(deps.cfg.InputFilePath, deps.cfg.InputDir)
	if err != nil {
		return err
	}
	sources, err := pproffix1.ReadSources(path)
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

// runPProfFix1Run проводит статьи через правку: из блога в модель и обратно в блог.
func runPProfFix1Run(ctx context.Context, repository *pproffix1.Repository, deps pprofFix1Deps) error {
	articles, err := pprofFix1Articles(ctx, repository, deps.command.ExternalID)
	if err != nil {
		return err
	}
	if len(articles) == 0 {
		fmt.Fprintf(deps.output, "Статей к правке нет: либо вход не импортирован "+
			"(make %s import), либо все статьи уже переписаны.\n", pproffix1.Command)
		return nil
	}
	rule, err := pproffix1.LoadTitleRule(pproffix1.TitleRulePath)
	if err != nil {
		return err
	}
	// Площадка проверяется здесь, а не в общей validateConfig: у остальных задач run уходит
	// только в модель, и требовать от них credentials WordPress нельзя. У этой прогон без
	// блога бессмыслен — читать статью неоткуда и возвращать её некуда.
	if err := deps.cfg.ValidateWordPress(); err != nil {
		return err
	}
	client, err := newWordPressClient(deps.cfg.WordPress)
	if err != nil {
		return err
	}
	blog := pprofFix1Blog{client: client}

	// План не трогает ни модель, ни блог: он только читает статьи и показывает, во что
	// превратит их заголовки правило переименования. Поэтому и клиента модели не поднимает —
	// браузерный профиль под flock стоит дорого, а плану он не нужен.
	if deps.command.Plan {
		return runPProfFix1Plan(ctx, blog, rule, articles, deps)
	}

	chats, closeLLM, err := newPProfFix1Chats(ctx, deps.profile, newDiagnosticsDirs(deps.profile), deps.logger)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := closeLLM(); closeErr != nil {
			deps.logger.Warn("не удалось закрыть LLM client", "error", closeErr)
		}
	}()

	flow, err := pproffix1.NewFlow(repository, blog, chats, pproffix1.NewArtifacts(deps.cfg.OutputDir),
		rule, pproffix1.RewritePromptPath, deps.profile.TemplatePath, deps.logger)
	if err != nil {
		return err
	}
	var failed int
	for _, article := range articles {
		if err := flow.Run(ctx, article.ExternalID); err != nil {
			if ctx.Err() != nil {
				return err
			}
			failed++
			deps.logger.Error("статья не переписана", "external_id", article.ExternalID, "error", err)
			fmt.Fprintf(deps.output, "%s — ошибка: %v\n", article.ExternalID, err)
			continue
		}
		fmt.Fprintf(deps.output, "%s — переписана\n", article.ExternalID)
	}
	if failed > 0 {
		return fmt.Errorf("не переписано статей: %d из %d", failed, len(articles))
	}
	return nil
}

// runPProfFix1Plan печатает, что произойдёт со статьями, ничего не меняя.
func runPProfFix1Plan(ctx context.Context, blog pprofFix1Blog, rule pproffix1.TitleRule,
	articles []pproffix1.Article, deps pprofFix1Deps) error {
	flow, err := pproffix1.NewFlow(nil, blog, nil, pproffix1.NewArtifacts(deps.cfg.OutputDir),
		rule, pproffix1.RewritePromptPath, deps.profile.TemplatePath, deps.logger)
	if err != nil {
		return err
	}
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
		fmt.Fprintf(writer, "%s\t%d\t%d\t%d\t%s\n  →\t\t\t\t%s\n",
			planned.ExternalID, planned.PostID, planned.Headings, planned.HTMLRunes,
			planned.OldTitle, planned.NewTitle)
		if planned.OldHeader != "" {
			header := planned.NewHeader
			if header == "" {
				header = "не меняется"
			}
			fmt.Fprintf(writer, "  H1\t\t\t\t%s\n  →\t\t\t\t%s\n", planned.OldHeader, header)
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(deps.output, "\nСтатей к правке: %d, из них с вопросами: %d. "+
		"Ничего не изменено — это план.\n", len(articles), problems)
	return nil
}

// pprofFix1Articles выбирает статьи прогона: одну по индексу или все непереписанные.
func pprofFix1Articles(ctx context.Context, repository *pproffix1.Repository, externalID string) ([]pproffix1.Article, error) {
	if strings.TrimSpace(externalID) == "" {
		return repository.ListPending(ctx)
	}
	article, err := repository.Get(ctx, externalID)
	if err != nil {
		return nil, err
	}
	return []pproffix1.Article{article}, nil
}

// pprofFix1Blog — переходник от контракта потока к клиенту WordPress.
//
// Он и есть единственное место, где задача встречается с площадкой: сам поток знает только
// «найти по слагу, прочитать, переписать» и о XML-RPC не подозревает.
type pprofFix1Blog struct{ client *wordpress.Client }

// pprofFix1PostTypes — где искать статью по слагу и в каком порядке.
//
// Список, а не один тип: статьи площадки живут в разных типах записей. Первым идёт тип
// страниц услуг — тот же, в который публикует pprof_2, и именно в нём лежат страницы из
// входного файла; за ним обычные записи блога и страницы. Берётся первое совпадение.
var pprofFix1PostTypes = []string{coursePostType, "post", "page"}

func (b pprofFix1Blog) Find(ctx context.Context, slug string) (pproffix1.Post, error) {
	found, err := b.client.FindPostBySlug(ctx, pprofFix1PostTypes, slug)
	if err != nil {
		return pproffix1.Post{}, err
	}
	return pproffix1.Post{ID: found.ID, Link: found.Link}, nil
}

func (b pprofFix1Blog) Read(ctx context.Context, postID int64) (pproffix1.Post, error) {
	stored, err := b.client.GetPost(ctx, postID)
	if err != nil {
		return pproffix1.Post{}, err
	}
	return pproffix1.Post{
		ID: stored.ID, Title: stored.Title, ContentHTML: stored.ContentHTML, Link: stored.Link,
		Fields: stored.Fields, FieldIDs: stored.FieldIDs,
	}, nil
}

func (b pprofFix1Blog) Write(ctx context.Context, postID int64, title, contentHTML string,
	fields []pproffix1.Field) error {
	updates := make([]wordpress.FieldUpdate, 0, len(fields))
	for _, field := range fields {
		updates = append(updates, wordpress.FieldUpdate{ID: field.ID, Key: field.Key, Value: field.Value})
	}
	return b.client.EditPost(ctx, wordpress.PostUpdate{
		PostID: postID, Title: title, ContentHTML: contentHTML, Fields: updates,
	})
}

// newPProfFix1Chats поднимает диалоги с моделью по схеме стадий задачи.
//
// Схема одна (наложения у задачи нет), поэтому резолвер режимов здесь не нужен: выбирать
// между Gemini и DeepSeek не из чего, а конвейер generation.Pipeline задача не использует
// вовсе — ей нужен только чат.
func newPProfFix1Chats(ctx context.Context, profile tasks.Profile, debugDirs diagnosticsDirs,
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
