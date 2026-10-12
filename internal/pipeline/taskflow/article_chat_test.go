package taskflow

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/generation"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/pipeline/output"
)

type fakeChats struct{ chat Chat }

func (c fakeChats) NewChat(context.Context, int64, ...string) (Chat, error) { return c.chat, nil }

type stageRenderer struct{}

func (stageRenderer) Prepare(call llm.Call) (llm.PreparedCall, error) {
	return llm.PreparedCall{Prompt: "промпт " + call.Stage}, nil
}

type fakeArticleChatRepository struct {
	saved map[string]string
}

func (r *fakeArticleChatRepository) GetGenerationInput(context.Context, string) (article.GenerationInput, error) {
	return article.GenerationInput{Article: article.Article{ID: 7, ExternalID: "7", Slug: "s", Title: "Тема"}}, nil
}
func (r *fakeArticleChatRepository) BeginGenerationStage(context.Context, int64, string) error {
	return nil
}
func (r *fakeArticleChatRepository) SaveGenerationPaths(_ context.Context, _ int64, structurePath, articlePath string) error {
	r.saved["structure"], r.saved["article"] = structurePath, articlePath
	return nil
}
func (r *fakeArticleChatRepository) SaveReviewPath(_ context.Context, _ int64, path string) error {
	r.saved["review"] = path
	return nil
}
func (r *fakeArticleChatRepository) SaveFixedArticlePath(_ context.Context, _ int64, path string) error {
	r.saved["fixed"] = path
	return nil
}

type fakeArticleChatWriter struct{ prompts map[string]string }

func (w fakeArticleChatWriter) StageArticle(_, _, prompt, text, _ string) (*articleoutput.PendingArtifact, error) {
	w.prompts["article"], w.prompts["draft"] = prompt, text
	return &articleoutput.PendingArtifact{Paths: articleoutput.ArticlePaths{
		ArticlePath: "draft.txt", ArticlePromptPath: "article_prompt.txt"}}, nil
}
func (w fakeArticleChatWriter) StageFixedArticle(_, _, _, page string) (*articleoutput.PendingArtifact, error) {
	w.prompts["fixed"] = page
	return &articleoutput.PendingArtifact{Paths: articleoutput.ArticlePaths{FixedArticlePath: "final.txt"}}, nil
}

type recordingPublisher struct{ prompts []string }

func (p *recordingPublisher) PublishArticlePrompt(job generation.ArticlePromptJob) {
	p.prompts = append(p.prompts, job.Prompt)
}

func newArticleChatSpec(repository *fakeArticleChatRepository, writer fakeArticleChatWriter) ArticleChat {
	return ArticleChat{
		Repository:   repository,
		Writer:       writer,
		Step:         "article",
		ArticleStage: "article",
		ReviewStage:  "review",
		ArticleData:  func(article.GenerationInput, string) any { return nil },
		ReviewData:   func(article.GenerationInput, string) any { return nil },
	}
}

func newArticleChatBase(chat Chat, publisher *recordingPublisher) (*Base, *fakeRepository) {
	repository := &fakeRepository{saved: article.SavedGenerationInput{StructurePath: "structure.txt"}}
	writer := fakeWriter{files: map[string]string{"structure.txt": "структура"}}
	return NewBase(repository, writer, fakeChats{chat: chat}, stageRenderer{}, nil, publisher), repository
}

func TestRunArticleChatCommitsBothTextsAndExtras(t *testing.T) {
	publisher := &recordingPublisher{}
	base, _ := newArticleChatBase(&fakeChat{answer: "## Заголовок"}, publisher)
	repository := &fakeArticleChatRepository{saved: map[string]string{}}
	writer := fakeArticleChatWriter{prompts: map[string]string{}}
	spec := newArticleChatSpec(repository, writer)
	spec.ExportPrompt = func(article.GenerationInput, string) (string, error) { return "выгружаемый", nil }
	spec.Normalize = strings.ToUpper
	persisted := false
	spec.Extras = func(context.Context, *slog.Logger, article.Article, ArticleChatOutput) (ArticleChatExtras, error) {
		return ArticleChatExtras{Persist: func() error { persisted = true; return nil }}, nil
	}

	if err := base.RunArticleChat(context.Background(), "7", spec); err != nil {
		t.Fatalf("RunArticleChat() = %v", err)
	}

	if repository.saved["review"] != "final.txt" || repository.saved["fixed"] != "final.txt" {
		t.Fatalf("review_path и fixed_article_path = %q, %q; want final.txt",
			repository.saved["review"], repository.saved["fixed"])
	}
	if repository.saved["article"] != "draft.txt" || repository.saved["structure"] != "structure.txt" {
		t.Fatalf("пути черновика и структуры = %v", repository.saved)
	}
	if !persisted {
		t.Fatal("состояние задачи не записано тем же Commit")
	}
	if writer.prompts["article"] != "выгружаемый" || len(publisher.prompts) != 1 || publisher.prompts[0] != "выгружаемый" {
		t.Fatalf("выгружен %q / %q, want выгружаемый промпт", writer.prompts["article"], publisher.prompts)
	}
	if writer.prompts["draft"] != "## ЗАГОЛОВОК" || writer.prompts["fixed"] != "## ЗАГОЛОВОК" {
		t.Fatalf("черновик %q, страница %q; want оба нормализованные", writer.prompts["draft"], writer.prompts["fixed"])
	}
}

func TestRunArticleChatExportsModelPromptByDefault(t *testing.T) {
	publisher := &recordingPublisher{}
	base, _ := newArticleChatBase(&fakeChat{answer: "страница"}, publisher)
	writer := fakeArticleChatWriter{prompts: map[string]string{}}
	spec := newArticleChatSpec(&fakeArticleChatRepository{saved: map[string]string{}}, writer)

	if err := base.RunArticleChat(context.Background(), "7", spec); err != nil {
		t.Fatalf("RunArticleChat() = %v", err)
	}
	if writer.prompts["article"] != "промпт article" || publisher.prompts[0] != "промпт article" {
		t.Fatalf("выгружен %q / %q, want промпт модели", writer.prompts["article"], publisher.prompts)
	}
}

func TestRunArticleChatReviewFailureSavesNothing(t *testing.T) {
	chat := &failingContinueChat{fakeChat: fakeChat{answer: "страница"}}
	base, failures := newArticleChatBase(chat, nil)
	repository := &fakeArticleChatRepository{saved: map[string]string{}}
	spec := newArticleChatSpec(repository, fakeArticleChatWriter{prompts: map[string]string{}})

	err := base.RunArticleChat(context.Background(), "7", spec)

	var stageErr *StageError
	if !errors.As(err, &stageErr) || stageErr.Stage != "article_review" {
		t.Fatalf("RunArticleChat() = %v, want StageError article_review", err)
	}
	if len(repository.saved) != 0 || len(failures.failures) != 1 {
		t.Fatalf("сохранено %v, ошибок %d; want ничего и одну ошибку", repository.saved, len(failures.failures))
	}
}

type failingContinueChat struct{ fakeChat }

func (c *failingContinueChat) Continue(context.Context, string) (string, error) {
	return "", errors.New("редактура оборвалась")
}
