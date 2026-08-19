package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

func readyThrough(stage pipelineStage) pipelineState {
	state := pipelineState{Status: "processing"}
	order := []pipelineStage{stagePrepare, stageStructure, stageArticle, stageReview, stageFix, stageHTML}
	for _, done := range order {
		switch done {
		case stagePrepare:
			state.ResearchReady = true
		case stageStructure:
			state.StructurePath = "37-tema/generated/structure.txt"
		case stageArticle:
			state.ArticlePath = "37-tema/generated/article.txt"
			state.MetadataText = "TLDR: …"
		case stageReview:
			state.ReviewPath = "37-tema/generated/review.txt"
		case stageFix:
			state.FixedArticlePath = "37-tema/generated/fixed_article.txt"
		case stageHTML:
			state.HTMLPath = "37-tema/article.html"
		}
		if done == stage {
			break
		}
	}
	return state
}

func TestNextStageResumesFromFirstUnfinished(t *testing.T) {
	tests := []struct {
		name  string
		state pipelineState
		want  pipelineStage
	}{
		{name: "новая статья", state: pipelineState{Status: "pending"}, want: stagePrepare},
		{name: "research собран", state: readyThrough(stagePrepare), want: stageStructure},
		{name: "структура готова", state: readyThrough(stageStructure), want: stageArticle},
		{name: "статья и метаданные готовы", state: readyThrough(stageArticle), want: stageReview},
		{name: "ревью готово", state: readyThrough(stageReview), want: stageFix},
		{name: "исправление готово", state: readyThrough(stageFix), want: stageHTML},
		{name: "html готов", state: readyThrough(stageHTML), want: stageResult},
		{name: "статья завершена", state: pipelineState{Status: "completed"}, want: stageDone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := nextStage(test.state); got != test.want {
				t.Fatalf("nextStage = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNextStageRepeatsArticleWhenMetadataIsMissing(t *testing.T) {
	// Падение между сохранением article_path и метаданных: article и info выполняются
	// одним вызовом, поэтому возобновляются вместе.
	state := readyThrough(stageArticle)
	state.MetadataText = ""
	if got := nextStage(state); got != stageArticle {
		t.Fatalf("nextStage = %q, want %q", got, stageArticle)
	}
}

func TestNextStageIgnoresCurrentStepWhenArtifactsAreMissing(t *testing.T) {
	// current_step говорит, куда статья шла, а не что она сделала: после аварийной
	// остановки этап может указывать далеко вперёд от реально сохранённых файлов.
	state := readyThrough(stagePrepare)
	state.CurrentStep = "html_generation"
	state.Status = "failed"
	state.ErrorMessage = "browser closed"
	if got := nextStage(state); got != stageStructure {
		t.Fatalf("nextStage = %q, want %q", got, stageStructure)
	}
}

func TestNextStageTakesFailedArticleBack(t *testing.T) {
	state := readyThrough(stageFix)
	state.Status = "failed"
	state.ErrorMessage = "перед HTML обнаружен поясняющий текст"
	if got := nextStage(state); got != stageHTML {
		t.Fatalf("nextStage = %q, want %q", got, stageHTML)
	}
}

func TestCompletedStagesListsFinishedWork(t *testing.T) {
	got := completedStages(readyThrough(stageReview))
	want := []pipelineStage{stagePrepare, stageStructure, stageArticle, stageReview}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("completedStages = %v, want %v", got, want)
	}
	if done := completedStages(pipelineState{Status: "completed"}); len(done) != 7 {
		t.Fatalf("для завершённой статьи готово %d этапов, ожидалось 7", len(done))
	}
	if done := completedStages(pipelineState{Status: "pending"}); len(done) != 0 {
		t.Fatalf("для новой статьи готово %v", done)
	}
}

// fakeRunRepository отдаёт состояние, меняющееся после выполнения этапов.
type fakeRunRepository struct {
	state pipelineState
	reads int
}

func (r *fakeRunRepository) GetArticleByExternalID(_ context.Context, externalID string) (article.Article, error) {
	r.reads++
	selected := article.Article{ID: 7, ExternalID: externalID, Title: "Тема", Status: r.state.Status}
	if r.state.CurrentStep != "" {
		step := r.state.CurrentStep
		selected.CurrentStep = &step
	}
	if r.state.ErrorMessage != "" {
		message := r.state.ErrorMessage
		selected.ErrorMessage = &message
	}
	return selected, nil
}

func (r *fakeRunRepository) HasPreparedResearch(context.Context, string) (bool, error) {
	return r.state.ResearchReady, nil
}

func (r *fakeRunRepository) GetSavedGenerationInput(context.Context, string) (article.SavedGenerationInput, error) {
	return article.SavedGenerationInput{
		StructurePath: r.state.StructurePath, ArticlePath: r.state.ArticlePath,
		ReviewPath: r.state.ReviewPath, FixedArticlePath: r.state.FixedArticlePath,
	}, nil
}

func (r *fakeRunRepository) GetResultInput(context.Context, string) (article.ResultInput, error) {
	return article.ResultInput{HTMLPath: r.state.HTMLPath, TLDR: r.state.MetadataText}, nil
}

// advance имитирует сохранение артефакта этапом.
func (r *fakeRunRepository) advance(stage pipelineStage) {
	switch stage {
	case stagePrepare:
		r.state.ResearchReady = true
	case stageStructure:
		r.state.StructurePath = "37-tema/generated/structure.txt"
	case stageArticle:
		r.state.ArticlePath = "37-tema/generated/article.txt"
		r.state.MetadataText = "TLDR: …"
	case stageReview:
		r.state.ReviewPath = "37-tema/generated/review.txt"
	case stageFix:
		r.state.FixedArticlePath = "37-tema/generated/fixed_article.txt"
	case stageHTML:
		r.state.HTMLPath = "37-tema/article.html"
	case stageResult:
		r.state.Status = "completed"
	}
}

func runPipelineTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRunFullPipelineRunsEveryStageOnce(t *testing.T) {
	repository := &fakeRunRepository{state: pipelineState{Status: "pending"}}
	var executed []pipelineStage
	execute := func(_ context.Context, stage pipelineStage, _ string) error {
		executed = append(executed, stage)
		repository.advance(stage)
		return nil
	}

	if err := runFullPipeline(context.Background(), repository, execute, runPipelineTestLogger(), "37", false); err != nil {
		t.Fatal(err)
	}
	want := []pipelineStage{stagePrepare, stageStructure, stageArticle, stageReview, stageFix, stageHTML, stageResult}
	if !reflect.DeepEqual(executed, want) {
		t.Fatalf("выполнены этапы %v, ожидались %v", executed, want)
	}
}

func TestRunFullPipelineResumesAndSkipsFinishedStages(t *testing.T) {
	repository := &fakeRunRepository{state: readyThrough(stageFix)}
	repository.state.Status = "failed"
	repository.state.ErrorMessage = "перед HTML обнаружен поясняющий текст"
	var executed []pipelineStage
	execute := func(_ context.Context, stage pipelineStage, _ string) error {
		executed = append(executed, stage)
		repository.advance(stage)
		return nil
	}

	if err := runFullPipeline(context.Background(), repository, execute, runPipelineTestLogger(), "37", false); err != nil {
		t.Fatal(err)
	}
	want := []pipelineStage{stageHTML, stageResult}
	if !reflect.DeepEqual(executed, want) {
		t.Fatalf("выполнены этапы %v, ожидались только %v", executed, want)
	}
}

func TestRunFullPipelineSkipsCompletedArticle(t *testing.T) {
	repository := &fakeRunRepository{state: pipelineState{Status: "completed"}}
	execute := func(_ context.Context, stage pipelineStage, _ string) error {
		t.Fatalf("для завершённой статьи выполнен этап %s", stage)
		return nil
	}
	if err := runFullPipeline(context.Background(), repository, execute, runPipelineTestLogger(), "37", false); err != nil {
		t.Fatal(err)
	}
}

func TestRunFullPipelineStopsWhenStageSavesNothing(t *testing.T) {
	// Этап отвечает «успех», но артефакт не появился: без защиты раннер выбирал бы его вечно.
	repository := &fakeRunRepository{state: readyThrough(stageFix)}
	calls := 0
	execute := func(_ context.Context, _ pipelineStage, _ string) error {
		calls++
		return nil
	}

	err := runFullPipeline(context.Background(), repository, execute, runPipelineTestLogger(), "37", false)
	if err == nil {
		t.Fatal("прогон не остановлен")
	}
	if !strings.Contains(err.Error(), "результат не сохранён") {
		t.Fatalf("error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("этап выполнен %d раз, ожидался один", calls)
	}
}

func TestRunFullPipelineStopsOnStageError(t *testing.T) {
	repository := &fakeRunRepository{state: readyThrough(stageStructure)}
	wantErr := errors.New("LLM недоступен")
	execute := func(_ context.Context, stage pipelineStage, _ string) error {
		if stage != stageArticle {
			t.Fatalf("неожиданный этап %s", stage)
		}
		return wantErr
	}
	if err := runFullPipeline(context.Background(), repository, execute, runPipelineTestLogger(), "37", false); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestIncompleteArticlesTakesEverythingButCompleted(t *testing.T) {
	repository := &fakeIncompleteRepository{articles: []article.Article{
		{ExternalID: "37", Status: "completed"},
		{ExternalID: "38", Status: "failed"},
		{ExternalID: "39", Status: "processing"},
		{ExternalID: "40", Status: "pending"},
	}}
	pending, err := incompleteArticles(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, selected := range pending {
		ids = append(ids, selected.ExternalID)
	}
	if !reflect.DeepEqual(ids, []string{"38", "39", "40"}) {
		t.Fatalf("выбраны статьи %v", ids)
	}
}

type fakeIncompleteRepository struct{ articles []article.Article }

func (r *fakeIncompleteRepository) GetAll(context.Context) ([]article.Article, error) {
	return r.articles, nil
}

// У задачи без стадии info метаданных не будет никогда, и требовать их от этапа article
// нельзя: раннер выбирал бы его бесконечно и остановился бы на «результат не сохранён».
func TestNextStageSkipsMetadataForTaskWithoutInfoStage(t *testing.T) {
	state := readyThrough(stageArticle)
	state.MetadataText = ""
	state.MetadataOptional = true
	if got := nextStage(state); got != stageReview {
		t.Fatalf("nextStage = %q, want %q", got, stageReview)
	}
	if !stageArtifactReady(stageArticle, state) {
		t.Fatal("этап article считается незавершённым без метаданных, которых у задачи не бывает")
	}
	done := completedStages(state)
	if len(done) == 0 || done[len(done)-1] != stageArticle {
		t.Fatalf("готовые этапы %v, ожидался article последним", done)
	}
}

// Нулевое значение признака — прежнее поведение: метаданные нужны. Иначе задача со стадией
// info молча проскочила бы этап, не сохранив TL;DR и FAQ.
func TestMetadataIsStillRequiredByDefault(t *testing.T) {
	state := readyThrough(stageArticle)
	state.MetadataText = ""
	if state.MetadataOptional {
		t.Fatal("признак «метаданных не бывает» включён по умолчанию")
	}
	if got := nextStage(state); got != stageArticle {
		t.Fatalf("nextStage = %q, want %q", got, stageArticle)
	}
}

// Полный прогон задачи без метаданных обязан дойти до конца, а не остановиться на article.
func TestRunFullPipelineCompletesWithoutMetadata(t *testing.T) {
	repository := &fakeRunRepository{state: pipelineState{Status: "pending", MetadataOptional: true}}
	var executed []pipelineStage
	execute := func(_ context.Context, stage pipelineStage, _ string) error {
		executed = append(executed, stage)
		repository.advance(stage)
		// Стадии info у задачи нет: метаданные не появляются ни на одном этапе.
		repository.state.MetadataText = ""
		repository.state.MetadataOptional = true
		return nil
	}
	if err := runFullPipeline(context.Background(), repository, execute, runPipelineTestLogger(), "37", true); err != nil {
		t.Fatal(err)
	}
	want := []pipelineStage{stagePrepare, stageStructure, stageArticle, stageReview, stageFix, stageHTML, stageResult}
	if !reflect.DeepEqual(executed, want) {
		t.Fatalf("выполнены этапы %v, ожидались %v", executed, want)
	}
}
