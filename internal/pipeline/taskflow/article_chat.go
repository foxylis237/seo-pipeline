package taskflow

import (
	"context"
	"log/slog"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/generation"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/pipeline/output"
)

// ArticleChatRepository — то, что чату статьи нужно от хранилища.
type ArticleChatRepository interface {
	GetGenerationInput(ctx context.Context, externalID string) (article.GenerationInput, error)
	BeginGenerationStage(ctx context.Context, articleID int64, stage string) error
	SaveGenerationPaths(ctx context.Context, articleID int64, structurePath, articlePath string) error
	SaveReviewPath(ctx context.Context, articleID int64, reviewPath string) error
	SaveFixedArticlePath(ctx context.Context, articleID int64, fixedArticlePath string) error
}

// ArticleChatWriter — то, что чату статьи нужно от файлового слоя.
type ArticleChatWriter interface {
	StageArticle(externalID, slug, prompt, text, model string) (*articleoutput.PendingArtifact, error)
	StageFixedArticle(externalID, slug, prompt, article string) (*articleoutput.PendingArtifact, error)
}

// ArticleChatOutput — то, что произвёл чат статьи.
type ArticleChatOutput struct {
	ArticlePrompt string
	// ExportPrompt ложится в prompts/article_prompt.txt и уходит в публикацию промпта.
	ExportPrompt string
	ArticleText  string
	ReviewPrompt string
	ReviewedPage string
}

// ArticleChatExtras — артефакты и состояние задачи, публикуемые тем же Commit, что и тексты чата.
type ArticleChatExtras struct {
	Pending []*articleoutput.PendingArtifact
	Persist func() error
	// LogAttrs дописываются к записи о сохранённых артефактах.
	LogAttrs []any
}

// ArticleChat описывает чат статьи из двух сообщений: страница, затем её редактура целиком.
type ArticleChat struct {
	Repository   ArticleChatRepository
	Writer       ArticleChatWriter
	Step         string
	ArticleStage string
	ReviewStage  string
	ArticleData  func(input article.GenerationInput, structure string) any
	ReviewData   func(input article.GenerationInput, page string) any
	// ExportPrompt собирает выгружаемый промпт до первого сообщения; nil — выгружается промпт модели.
	ExportPrompt func(input article.GenerationInput, structure string) (string, error)
	// Normalize применяется к обоим ответам модели; nil — ответы как есть.
	Normalize func(text string) string
	// Extras зовётся после записи текстов; ошибку он сохраняет сам через Fail.
	Extras func(ctx context.Context, logger *slog.Logger, selected article.Article, out ArticleChatOutput) (ArticleChatExtras, error)
}

// RunArticleChat выполняет чат статьи целиком и публикует его артефакты одним Commit:
// браузерная беседа не переживает процесс, поэтому продолжить чат с середины нечем.
func (b *Base) RunArticleChat(ctx context.Context, externalID string, spec ArticleChat) error {
	input, err := spec.Repository.GetGenerationInput(ctx, externalID)
	if err != nil {
		return StageFailure(externalID, "load_generation_data", err)
	}
	logger := b.ArticleLogger(input.Article)
	structure, err := b.SavedStructure(ctx, externalID)
	if err != nil {
		return b.Fail(ctx, logger, input.Article, "load_structure_data", err)
	}
	if err := spec.Repository.BeginGenerationStage(ctx, input.Article.ID, spec.Step); err != nil {
		return b.Fail(ctx, logger, input.Article, "article_generation", err)
	}

	started := time.Now()
	out, err := b.runArticleChat(ctx, logger, spec, input, structure)
	if err != nil {
		return err
	}
	if err := b.saveArticleChat(ctx, logger, spec, input.Article, externalID, out); err != nil {
		return err
	}
	logger.Info("чат статьи завершён", "stage", spec.ArticleStage, "chat", 2,
		"duration_ms", time.Since(started).Milliseconds())
	return nil
}

func (b *Base) runArticleChat(ctx context.Context, logger *slog.Logger, spec ArticleChat,
	input article.GenerationInput, structure string) (ArticleChatOutput, error) {
	var out ArticleChatOutput
	chat, err := b.NewChat(ctx, input.Article.ID, spec.ArticleStage, spec.ReviewStage)
	if err != nil {
		return out, b.Fail(ctx, logger, input.Article, "article_generation", err)
	}
	defer b.CloseChat(chat, logger, spec.ArticleStage)
	logger.Info("чат статьи начат", "stage", spec.ArticleStage, "chat", 2)

	if spec.ExportPrompt != nil {
		if out.ExportPrompt, err = spec.ExportPrompt(input, structure); err != nil {
			return out, b.Fail(ctx, logger, input.Article, "article_generation", err)
		}
	}
	if out.ArticlePrompt, out.ArticleText, err = b.Message(ctx, chat.Send,
		spec.ArticleStage, spec.ArticleData(input, structure)); err != nil {
		return out, b.Fail(ctx, logger, input.Article, "article_generation", err)
	}
	if spec.ExportPrompt == nil {
		out.ExportPrompt = out.ArticlePrompt
	}
	out.ArticleText = spec.normalize(out.ArticleText)
	logger.Info("статья получена от модели", "stage", spec.ArticleStage, "prompt_size", len([]rune(out.ArticlePrompt)))

	if out.ReviewPrompt, out.ReviewedPage, err = b.Message(ctx, chat.Continue,
		spec.ReviewStage, spec.ReviewData(input, out.ArticleText)); err != nil {
		return out, b.Fail(ctx, logger, input.Article, "article_review", err)
	}
	out.ReviewedPage = spec.normalize(out.ReviewedPage)
	logger.Info("редактура завершена", "stage", spec.ReviewStage)
	return out, nil
}

// saveArticleChat кладёт черновик в article.txt, страницу после редактуры — в fixed_article.txt;
// review_path указывает на тот же файл, потому что пустой слот раннер считает невыполненным этапом.
func (b *Base) saveArticleChat(ctx context.Context, logger *slog.Logger, spec ArticleChat,
	selected article.Article, externalID string, out ArticleChatOutput) error {
	articlePending, err := spec.Writer.StageArticle(selected.ExternalID, selected.Slug, out.ExportPrompt, out.ArticleText, "")
	if err != nil {
		return b.Fail(ctx, logger, selected, "save_article", err)
	}
	defer articlePending.Abort()
	finalPending, err := spec.Writer.StageFixedArticle(selected.ExternalID, selected.Slug, out.ReviewPrompt, out.ReviewedPage)
	if err != nil {
		return b.Fail(ctx, logger, selected, "save_reviewed_article", err)
	}
	defer finalPending.Abort()

	var extras ArticleChatExtras
	if spec.Extras != nil {
		if extras, err = spec.Extras(ctx, logger, selected, out); err != nil {
			return err
		}
		for _, pending := range extras.Pending {
			defer pending.Abort()
		}
	}

	structurePath, err := b.SavedStructurePath(ctx, externalID)
	if err != nil {
		return b.Fail(ctx, logger, selected, "load_structure_data", err)
	}
	pagePath := finalPending.Paths.FixedArticlePath
	pending := append([]*articleoutput.PendingArtifact{articlePending, finalPending}, extras.Pending...)
	commitErr := articleoutput.Commit(func() error {
		if err := spec.Repository.SaveGenerationPaths(ctx, selected.ID, structurePath, articlePending.Paths.ArticlePath); err != nil {
			return err
		}
		if err := spec.Repository.SaveReviewPath(ctx, selected.ID, pagePath); err != nil {
			return err
		}
		if err := spec.Repository.SaveFixedArticlePath(ctx, selected.ID, pagePath); err != nil {
			return err
		}
		if extras.Persist == nil {
			return nil
		}
		return extras.Persist()
	}, pending...)
	if commitErr != nil {
		return b.Fail(ctx, logger, selected, "save_article_state", commitErr)
	}

	b.PublishPrompt(generation.ArticlePromptJob{
		ArticleID:  selected.ID,
		ExternalID: selected.ExternalID,
		Title:      selected.Title,
		Prompt:     out.ExportPrompt,
		PromptPath: articlePending.Paths.ArticlePromptPath,
	})
	attrs := append([]any{"stage", spec.ArticleStage,
		"draft_path", articlePending.Paths.ArticlePath, "path", pagePath}, extras.LogAttrs...)
	logger.Info("артефакты чата статьи сохранены", attrs...)
	return nil
}

func (spec ArticleChat) normalize(text string) string {
	if spec.Normalize == nil {
		return text
	}
	return spec.Normalize(text)
}
