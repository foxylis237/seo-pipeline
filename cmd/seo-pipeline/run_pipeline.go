package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

// maxStageIterations страхует цикл раннера: этапов семь, поэтому больше девяти проходов
// означает, что состояние перестало двигаться.
const maxStageIterations = 9

// runStateRepository reads what the article has already finished.
type runStateRepository interface {
	GetArticleByExternalID(ctx context.Context, externalID string) (article.Article, error)
	HasPreparedResearch(ctx context.Context, externalID string) (bool, error)
	GetSavedGenerationInput(ctx context.Context, externalID string) (article.SavedGenerationInput, error)
	GetResultInput(ctx context.Context, externalID string) (article.ResultInput, error)
}

// stageExecutor выполняет один этап статьи. Реализация живёт в main.go и просто зовёт
// существующие сервисы: runPrepare, методы Pipeline и сборку result.
type stageExecutor func(ctx context.Context, stage pipelineStage, externalID string) error

// pipelineStage — этап полного прогона статьи. Порядок здесь и есть порядок пайплайна.
type pipelineStage string

const (
	stagePrepare   pipelineStage = "prepare"
	stageStructure pipelineStage = "structure"
	stageArticle   pipelineStage = "article"
	stageReview    pipelineStage = "review"
	stageFix       pipelineStage = "fix"
	stageHTML      pipelineStage = "html"
	stageResult    pipelineStage = "result"
	stageDone      pipelineStage = "done"
)

// pipelineState — то, что статья уже прошла, по данным PostgreSQL и сохранённым артефактам.
type pipelineState struct {
	Status           string
	CurrentStep      string
	ErrorMessage     string
	ResearchReady    bool
	StructurePath    string
	ArticlePath      string
	MetadataText     string
	ReviewPath       string
	FixedArticlePath string
	HTMLPath         string
	ResultReady      bool
	// MetadataOptional снимает требование метаданных с этапа article.
	//
	// Признак статьи, а не глобальная настройка: у задачи со стадией info готовым этап
	// считается только вместе с TL;DR и FAQ, у задачи без неё их не будет никогда, и раннер
	// выбирал бы article бесконечно. Нулевое значение — прежнее поведение: метаданные нужны.
	MetadataOptional bool
}

// metadataReady отвечает, выполнено ли то, за что отвечает стадия info.
//
// У задачи без такой стадии ответ всегда «да»: спрашивать не о чем.
func metadataReady(state pipelineState) bool {
	return state.MetadataOptional || !blank(state.MetadataText)
}

// nextStage returns the first stage the article has not finished yet.
//
// Признак готовности этапа — сохранённый артефакт, а не current_step: этап в БД говорит,
// куда статья шла, а путь к файлу — что она действительно сделала. Стадии article и info
// выполняются одним вызовом в общем чате, поэтому и возобновляются вместе.
func nextStage(state pipelineState) pipelineStage {
	switch {
	case state.Status == "completed":
		return stageDone
	case !state.ResearchReady:
		return stagePrepare
	case blank(state.StructurePath):
		return stageStructure
	case blank(state.ArticlePath) || !metadataReady(state):
		return stageArticle
	case blank(state.ReviewPath):
		return stageReview
	case blank(state.FixedArticlePath):
		return stageFix
	case blank(state.HTMLPath):
		return stageHTML
	default:
		return stageResult
	}
}

// completedStages lists the stages already done, in pipeline order, for the plan output.
func completedStages(state pipelineState) []pipelineStage {
	if state.Status == "completed" {
		return []pipelineStage{stagePrepare, stageStructure, stageArticle, stageReview, stageFix, stageHTML, stageResult}
	}
	done := make([]pipelineStage, 0, 7)
	if state.ResearchReady {
		done = append(done, stagePrepare)
	}
	if !blank(state.StructurePath) {
		done = append(done, stageStructure)
	}
	if !blank(state.ArticlePath) && metadataReady(state) {
		done = append(done, stageArticle)
	}
	if !blank(state.ReviewPath) {
		done = append(done, stageReview)
	}
	if !blank(state.FixedArticlePath) {
		done = append(done, stageFix)
	}
	if !blank(state.HTMLPath) {
		done = append(done, stageHTML)
	}
	return done
}

func blank(value string) bool { return strings.TrimSpace(value) == "" }

// incompleteArticlesRepository выбирает статьи для батча полного прогона.
type incompleteArticlesRepository interface {
	GetAll(ctx context.Context) ([]article.Article, error)
}

// incompleteArticles returns every article that is not completed yet, in stable id order.
// Отдельного предиката в репозитории для этого нет, а вводить его ради одной выборки
// незачем: список статей и так читается целиком.
func incompleteArticles(ctx context.Context, repository incompleteArticlesRepository) ([]article.Article, error) {
	all, err := repository.GetAll(ctx)
	if err != nil {
		return nil, err
	}
	pending := make([]article.Article, 0, len(all))
	for _, selected := range all {
		if selected.Status != "completed" {
			pending = append(pending, selected)
		}
	}
	return pending, nil
}

// loadPipelineState собирает состояние статьи существующими чтениями репозитория.
func loadPipelineState(ctx context.Context, repository runStateRepository, externalID string, metadataOptional bool) (pipelineState, error) {
	selected, err := repository.GetArticleByExternalID(ctx, externalID)
	if err != nil {
		return pipelineState{}, err
	}
	state := pipelineState{Status: selected.Status, MetadataOptional: metadataOptional}
	if selected.CurrentStep != nil {
		state.CurrentStep = *selected.CurrentStep
	}
	if selected.ErrorMessage != nil {
		state.ErrorMessage = strings.TrimSpace(*selected.ErrorMessage)
	}
	if state.ResearchReady, err = repository.HasPreparedResearch(ctx, externalID); err != nil {
		return pipelineState{}, err
	}
	saved, err := repository.GetSavedGenerationInput(ctx, externalID)
	if err != nil {
		return pipelineState{}, err
	}
	state.StructurePath = saved.StructurePath
	state.ArticlePath = saved.ArticlePath
	state.ReviewPath = saved.ReviewPath
	state.FixedArticlePath = saved.FixedArticlePath
	result, err := repository.GetResultInput(ctx, externalID)
	if err != nil {
		return pipelineState{}, err
	}
	state.HTMLPath = result.HTMLPath
	// Сырой metadata_text наружу не отдаётся, поэтому признаком выполненного info служит
	// любая разобранная из него секция.
	// Метки сюда не входят: они приходят из Excel при импорте и появляются раньше стадии
	// info, поэтому признаком её выполнения служить не могут.
	state.MetadataText = strings.TrimSpace(result.TLDR + result.FAQ + result.AdditionalInfo)
	state.ResultReady = state.Status == "completed"
	return state, nil
}

// stageArtifactReady reports whether the stage left the result it is responsible for.
func stageArtifactReady(stage pipelineStage, state pipelineState) bool {
	switch stage {
	case stagePrepare:
		return state.ResearchReady
	case stageStructure:
		return !blank(state.StructurePath)
	case stageArticle:
		return !blank(state.ArticlePath) && metadataReady(state)
	case stageReview:
		return !blank(state.ReviewPath)
	case stageFix:
		return !blank(state.FixedArticlePath)
	case stageHTML:
		return !blank(state.HTMLPath)
	case stageResult:
		return state.Status == "completed"
	default:
		return true
	}
}

// runFullPipeline проводит одну статью по всем незавершённым этапам.
//
// После каждого этапа состояние перечитывается из PostgreSQL: этап, который отработал без
// ошибки, но не сохранил свой артефакт, останавливает прогон — иначе раннер бесконечно
// выбирал бы один и тот же этап.
func runFullPipeline(
	ctx context.Context,
	repository runStateRepository,
	execute stageExecutor,
	logger *slog.Logger,
	externalID string,
	metadataOptional bool,
) error {
	state, err := loadPipelineState(ctx, repository, externalID, metadataOptional)
	if err != nil {
		return err
	}
	logArticleStart(logger, externalID, state)
	if nextStage(state) == stageDone {
		logger.Info("article", "external_id", externalID, "action", "skipped", "reason", "already_completed")
		return nil
	}
	for _, done := range completedStages(state) {
		logger.Info("article", "external_id", externalID, "stage", string(done),
			"action", "skipped", "reason", "already_completed")
	}
	for iteration := 1; ; iteration++ {
		stage := nextStage(state)
		if stage == stageDone {
			logger.Info("article", "external_id", externalID, "action", "completed", "status", state.Status)
			return nil
		}
		if iteration > maxStageIterations {
			return fmt.Errorf(
				"external_id=%s: превышено число этапов (%d), состояние перестало двигаться на этапе %s",
				externalID, maxStageIterations, stage,
			)
		}
		logger.Info("article", "external_id", externalID, "stage", string(stage), "action", "started")
		if err := execute(ctx, stage, externalID); err != nil {
			return err
		}
		if state, err = loadPipelineState(ctx, repository, externalID, metadataOptional); err != nil {
			return err
		}
		if !stageArtifactReady(stage, state) {
			return fmt.Errorf(
				"external_id=%s: этап %s завершился без ошибки, но результат не сохранён; прогон остановлен",
				externalID, stage,
			)
		}
		logger.Info("article", "external_id", externalID, "stage", string(stage), "action", "completed")
	}
}

// planRepository объединяет чтения, нужные режиму --plan.
type planRepository interface {
	runStateRepository
	incompleteArticlesRepository
}

// runPipelinePlan печатает план возобновления: для одной статьи или для всех незавершённых.
// Ни один этап не выполняется и ни один LLM не вызывается.
func runPipelinePlan(ctx context.Context, repository planRepository, writer io.Writer, externalID string, metadataOptional bool) error {
	if externalID != "" {
		return printPipelinePlan(ctx, repository, writer, externalID, metadataOptional)
	}
	pending, err := incompleteArticles(ctx, repository)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		fmt.Fprintln(writer, "Незавершённых статей нет.")
		return nil
	}
	fmt.Fprintf(writer, "Незавершённых статей: %d\n\n", len(pending))
	for _, selected := range pending {
		if err := printPipelinePlan(ctx, repository, writer, selected.ExternalID, metadataOptional); err != nil {
			return err
		}
		fmt.Fprintln(writer)
	}
	return nil
}

// printPipelinePlan показывает, что раннер сделал бы со статьёй, ничего не выполняя.
func printPipelinePlan(ctx context.Context, repository runStateRepository, writer io.Writer, externalID string, metadataOptional bool) error {
	state, err := loadPipelineState(ctx, repository, externalID, metadataOptional)
	if err != nil {
		return err
	}
	done := completedStages(state)
	ready := make([]string, 0, len(done))
	for _, stage := range done {
		ready = append(ready, string(stage))
	}
	fmt.Fprintf(writer, "external_id=%s\n", externalID)
	fmt.Fprintf(writer, "  статус:        %s\n", state.Status)
	if state.CurrentStep != "" {
		fmt.Fprintf(writer, "  текущий этап:  %s\n", state.CurrentStep)
	}
	if state.ErrorMessage != "" {
		fmt.Fprintf(writer, "  ошибка:        %s\n", shortenPlanError(state.ErrorMessage))
	}
	if len(ready) == 0 {
		fmt.Fprintln(writer, "  готовые этапы: нет")
	} else {
		fmt.Fprintf(writer, "  готовые этапы: %s\n", strings.Join(ready, ", "))
	}
	next := nextStage(state)
	if next == stageDone {
		fmt.Fprintln(writer, "  продолжит с:   пропуск, статья завершена")
		return nil
	}
	fmt.Fprintf(writer, "  продолжит с:   %s\n", next)
	return nil
}

// shortenPlanError оставляет от сообщения об ошибке одну строку, чтобы план оставался читаемым.
func shortenPlanError(message string) string {
	if line, _, found := strings.Cut(message, "\n"); found {
		message = line
	}
	const maxRunes = 120
	if runes := []rune(message); len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	return message
}

// logArticleStart печатает состояние статьи перед возобновлением.
func logArticleStart(logger *slog.Logger, externalID string, state pipelineState) {
	fields := []any{"external_id", externalID, "status", state.Status, "action", "start"}
	if state.CurrentStep != "" {
		fields = append(fields, "current_step", state.CurrentStep)
	}
	if state.Status == "failed" {
		fields = append(fields, "previous_error", state.ErrorMessage, "retry_started", true)
	}
	fields = append(fields, "resume_from", string(nextStage(state)))
	logger.Info("article", fields...)
}
