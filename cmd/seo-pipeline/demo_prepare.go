package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/diagnostics"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/tasks/task1/output"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/repository"
)

// demoPrepareRepository нейтрализует переходы состояния боевого prepare, оставляя сбор и
// сохранение research нетронутыми.
//
// Заворачивать репозиторий, а не копировать prepare, обязательно: иначе у Keys.so и Arsenkin
// появился бы второй вызывающий со своими проверками идентичности и своей диагностикой.
// Здесь же demo пользуется ровно той реализацией, что и боевая команда, и отличается ровно
// двумя методами — теми, которые двигают статью по пайплайну.
type demoPrepareRepository struct {
	*repository.ArticleRepository
	logger *slog.Logger
}

// PrepareArticleForRun ничего не делает: боевой prepare переводит статью в processing и
// сбрасывает ошибку, а demo-сборка обязана оставить status, current_step и error_message
// такими, какими нашла.
func (r demoPrepareRepository) PrepareArticleForRun(context.Context, int64) error { return nil }

// SaveError только логирует: сохранённая ошибка перевела бы статью в failed. Ошибка сбора
// возвращается вызывающему и попадает в итог demo-сборки, а не в состояние статьи.
func (r demoPrepareRepository) SaveError(_ context.Context, articleID int64, processingErr error) error {
	r.logger.Warn("ошибка prepare не записана в статью: demo-сборка не меняет её состояние",
		"article_id", articleID, "stage", "demo_prepare", "error", processingErr)
	return nil
}

// runDemoPrepare собирает недостающий research существующей реализацией prepare: те же
// Keys.so и Arsenkin, те же проверки идентичности, та же диагностика в prepare/.
func runDemoPrepare(
	ctx context.Context,
	articleRepository *repository.ArticleRepository,
	cfg config.Config,
	logger *slog.Logger,
	writer *articleoutput.Writer,
	logRouter *diagnostics.ArticleLogRouter,
	newFallback keywordsFallbackFactory,
	externalID string,
) error {
	if err := cfg.ValidatePrepare(); err != nil {
		return err
	}
	selected, err := articleByExternalID(ctx, articleRepository, externalID)
	if err != nil {
		return err
	}
	logger.Info("research отсутствует, запускается prepare", "stage", "demo_prepare",
		"article_id", selected.ID, "external_id", externalID)
	return prepareArticle(ctx, demoPrepareRepository{ArticleRepository: articleRepository, logger: logger},
		cfg, logger, writer, logRouter, newFallback, selected)
}

// articleByExternalID находит статью со slug и reference_url: prepare нужны оба, а
// GetArticleByExternalID их не читает.
func articleByExternalID(ctx context.Context, articleRepository *repository.ArticleRepository, externalID string) (article.Article, error) {
	selectedArticles, err := articleRepository.GetAll(ctx)
	if err != nil {
		return article.Article{}, err
	}
	for _, selected := range selectedArticles {
		if selected.ExternalID == externalID {
			return selected, nil
		}
	}
	return article.Article{}, fmt.Errorf("статья с external_id %q не найдена", externalID)
}
