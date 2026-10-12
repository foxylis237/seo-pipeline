package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/demo"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/pipeline/output"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/repository"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/taskflow"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
	"github.com/foxylis237/seo-pipeline/internal/tasks/obuch1"
	"github.com/foxylis237/seo-pipeline/internal/tasks/obuch2"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pprof1"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pprof2"
)

// taskFlow — поток генерации задачи, у которой он свой.
//
// task_1 идёт общим конвейером generation.Pipeline и этому контракту не отвечает: у него
// шесть стадий и другой порядок. Задачи с собственным потоком выражаются тремя чатами, и
// именно этими тремя вызовами их видят раннер полного прогона и одностадийные команды.
// Интерфейс объявлен здесь, у потребителя: сами потоки друг о друге и об этом файле не знают.
type taskFlow interface {
	RunStructure(ctx context.Context, externalID string) error
	RunArticle(ctx context.Context, externalID string) error
	RunHTML(ctx context.Context, externalID string) error
}

// taskFlowDeps — то, что поток получает от composition root. Репозиторий, writer и роутер у
// задач общие: свой у потока порядок стадий и промпты, а не работа с базой и файлами.
type taskFlowDeps struct {
	repository *repository.ArticleRepository
	writer     *articleoutput.Writer
	router     *llm.Router
	logger     *slog.Logger
	publisher  *googlePublisher
	// programs — каталог услуг площадки задачи. Нужен пока одному потоку (obuch_1 сверяет по
	// нему адрес кнопки призыва), читается лениво, и задаче без собранного каталога ничего не
	// стоит: List зовётся только тогда, когда о каталоге спросили.
	programs *catalogCourses
	// linkNames reads program titles from the live site; nil keeps links as bare URLs.
	linkNames linkNames
}

type linkNames interface {
	Name(ctx context.Context, url string) (string, error)
}

// taskFlows — конструкторы потоков задач. Новая задача добавляется строкой здесь, без
// своего файла в cmd. Публикатор промптов поток принимает напрямую: googlePublisher уже
// отвечает контракту его очереди.
var taskFlows = map[string]func(taskFlowDeps) taskFlow{
	pprof1.Name: func(deps taskFlowDeps) taskFlow {
		return pprof1.NewFlow(deps.repository, deps.writer, taskflow.NewRouterChats(deps.router),
			deps.router, deps.logger, deps.publisher, deps.linkNames)
	},
	pprof2.Name: func(deps taskFlowDeps) taskFlow {
		return pprof2.NewFlow(deps.repository, deps.writer, taskflow.NewRouterChats(deps.router),
			deps.router, deps.logger, deps.publisher)
	},
	obuch1.Name: func(deps taskFlowDeps) taskFlow {
		flow := obuch1.NewFlow(deps.repository, deps.writer, taskflow.NewRouterChats(deps.router),
			deps.router, deps.logger, deps.publisher, deps.linkNames)
		// The catalog checks the CTA button URL against the site's programs.
		if deps.programs != nil {
			flow.UseProgramCatalog(deps.programs)
		}
		return flow
	},
	obuch2.Name:   obuch2Flow(obuch2.ExportPromptPath),
	obuch2.Name22: obuch2Flow(obuch2.ExportPrompt22Path),
}

func obuch2Flow(exportPromptPath string) func(taskFlowDeps) taskFlow {
	return func(deps taskFlowDeps) taskFlow {
		return obuch2.NewFlow(deps.repository, deps.writer, taskflow.NewRouterChats(deps.router),
			deps.router, deps.logger, deps.publisher).WithExportPromptPath(exportPromptPath)
	}
}

// newTaskFlow собирает поток задачи. nil без ошибки означает «у задачи своего потока нет» —
// так работает task_1.
func newTaskFlow(profile tasks.Profile, deps taskFlowDeps) (taskFlow, error) {
	build, found := taskFlows[profile.Name]
	if !found {
		return nil, nil
	}
	if deps.router == nil {
		return nil, fmt.Errorf("схема стадий %s не загружена", profile.Name)
	}
	return build(deps), nil
}

// demoPromptDataOf отдаёт сборщику DEMO данные промптов задачи, если её промпты просят свой
// набор полей. nil — общий набор: так работают task_1 и pprof_1.
//
// Спрашивается поток, а не имя задачи: второй switch по задачам стал бы вторым местом, где
// список задач нужно помнить, а поля промптов знает тот же пакет, что и промпты. Задача без
// своих полей метода просто не объявляет, и трогать этот файл ей не придётся.
func demoPromptDataOf(flow taskFlow) demo.PromptData {
	provider, found := flow.(interface{ DemoPromptData() demo.PromptData })
	if !found {
		return nil
	}
	return provider.DemoPromptData()
}

// taskFlowStageExecutor выполняет этапы возобновляемого раннера потоком задачи.
//
// Этапы review и fix отдельными вызовами модели не выполняются: их артефакты рождаются в
// чате 2 вместе с текстом. Раннер просит их только тогда, когда артефактов нет, — а
// единственный способ их получить — пройти чат 2 целиком, поэтому оба ведут туда же.
//
// Этапы prepare и result у задач общие и потому приходят готовыми функциями: собирать их
// заново поток не должен и не умеет.
func taskFlowStageExecutor(flow taskFlow, runPrepareStage, runResultStage func(context.Context, string) error) stageExecutor {
	return func(ctx context.Context, stage pipelineStage, externalID string) error {
		switch stage {
		case stagePrepare:
			return runPrepareStage(ctx, externalID)
		case stageStructure:
			return flow.RunStructure(ctx, externalID)
		case stageArticle, stageReview, stageFix:
			return flow.RunArticle(ctx, externalID)
		case stageHTML:
			return flow.RunHTML(ctx, externalID)
		case stageResult:
			return runResultStage(ctx, externalID)
		default:
			return fmt.Errorf("неизвестный этап пайплайна %q", stage)
		}
	}
}

// taskFlowStageRunner отдаёт функцию одностадийной команды.
func taskFlowStageRunner(flow taskFlow, operation string) (func(context.Context, string) error, error) {
	switch operation {
	case "article", "info", "review", "fix":
		// Все четыре — части одного чата 2 и по отдельности не выполняются: продолжить
		// оборванную беседу браузера нечем. Команда честно прогоняет чат целиком.
		return flow.RunArticle, nil
	case "html":
		return flow.RunHTML, nil
	default:
		return nil, fmt.Errorf("операция %q не поддерживается собственным потоком задачи", operation)
	}
}
