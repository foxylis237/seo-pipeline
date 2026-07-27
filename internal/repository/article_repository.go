package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/foxylis237/seo-pipeline/internal/article"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ArticleRepository работает со статьями в PostgreSQL.
type ArticleRepository struct {
	pool *pgxpool.Pool
}

// NewArticleRepository создаёт репозиторий статей.
func NewArticleRepository(pool *pgxpool.Pool) *ArticleRepository {
	return &ArticleRepository{
		pool: pool,
	}
}

// Create сохраняет статью и её исходные данные в одной транзакции.
func (r *ArticleRepository) Create(
	ctx context.Context,
	input article.Input,
) (article.Article, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return article.Article{}, fmt.Errorf("начать транзакцию: %w", err)
	}

	defer func() {
		_ = tx.Rollback(ctx)
	}()

	var created article.Article

	const createArticleQuery = `
		INSERT INTO articles (
			external_id,
			title
		)
		VALUES ($1, $2)
		ON CONFLICT (external_id) DO UPDATE
		SET
			title = EXCLUDED.title,
			updated_at = NOW()
		RETURNING
			id,
			external_id,
			title,
			status,
			current_step,
			error_message,
			created_at,
			updated_at
	`

	err = tx.QueryRow(
		ctx,
		createArticleQuery,
		fmt.Sprint(input.ExcelID),
		input.Title,
	).Scan(
		&created.ID,
		&created.ExternalID,
		&created.Title,
		&created.Status,
		&created.CurrentStep,
		&created.ErrorMessage,
		&created.CreatedAt,
		&created.UpdatedAt,
	)
	if err != nil {
		return article.Article{}, fmt.Errorf("сохранить статью: %w", err)
	}

	const createInputQuery = `
		INSERT INTO article_inputs (
			article_id,
			category,
			header,
			image_slug,
			meta_description,
			key_word,
			reference_url,
			author,
			links,
			professions
		)
		VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10
		)
		ON CONFLICT (article_id) DO UPDATE
		SET
			category = EXCLUDED.category,
			header = EXCLUDED.header,
			image_slug = EXCLUDED.image_slug,
			meta_description = EXCLUDED.meta_description,
			key_word = EXCLUDED.key_word,
			reference_url = EXCLUDED.reference_url,
			author = EXCLUDED.author,
			links = EXCLUDED.links,
			professions = EXCLUDED.professions
	`

	_, err = tx.Exec(
		ctx,
		createInputQuery,
		created.ID,
		input.Category,
		input.Header,
		input.ImageSlug,
		input.MetaDescription,
		input.Keyword,
		input.ReferenceURL,
		input.Author,
		input.Links,
		input.Professions,
	)
	if err != nil {
		return article.Article{}, fmt.Errorf("сохранить входные данные статьи: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return article.Article{}, fmt.Errorf("завершить транзакцию: %w", err)
	}

	return created, nil
}

// GetFirst возвращает только первую статью и её URL конкурента.
func (r *ArticleRepository) GetFirst(
	ctx context.Context,
) (article.Article, bool, error) {
	const selectQuery = `
		SELECT
			a.id,
			a.external_id,
			a.title,
			COALESCE(i.reference_url, ''),
			a.status,
			a.current_step,
			a.error_message,
			a.created_at,
			a.updated_at
		FROM articles AS a
		LEFT JOIN article_inputs AS i ON i.article_id = a.id
		ORDER BY a.id
		LIMIT 1
	`

	var claimed article.Article
	err := r.pool.QueryRow(ctx, selectQuery).Scan(
		&claimed.ID,
		&claimed.ExternalID,
		&claimed.Title,
		&claimed.ReferenceURL,
		&claimed.Status,
		&claimed.CurrentStep,
		&claimed.ErrorMessage,
		&claimed.CreatedAt,
		&claimed.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return article.Article{}, false, nil
		}
		return article.Article{}, false, fmt.Errorf("получить первую статью: %w", err)
	}

	return claimed, true, nil
}

// SaveCleanedKeywords сохраняет очищенные запросы исследования статьи.
func (r *ArticleRepository) SaveCleanedKeywords(
	ctx context.Context,
	articleID int64,
	keywords []string,
) error {
	encoded, err := json.Marshal(keywords)
	if err != nil {
		return fmt.Errorf("закодировать очищенные запросы: %w", err)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("начать транзакцию сохранения исследования: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const researchQuery = `
		INSERT INTO article_research (article_id, cleaned_keywords, collected_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (article_id) DO UPDATE
		SET
			cleaned_keywords = EXCLUDED.cleaned_keywords,
			collected_at = NOW()
	`
	if _, err := tx.Exec(ctx, researchQuery, articleID, string(encoded)); err != nil {
		return fmt.Errorf("сохранить очищенные запросы: %w", err)
	}

	const clearErrorQuery = `
		UPDATE articles
		SET error_message = NULL, updated_at = NOW()
		WHERE id = $1
	`
	if _, err := tx.Exec(ctx, clearErrorQuery, articleID); err != nil {
		return fmt.Errorf("очистить ошибку статьи: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("завершить сохранение исследования: %w", err)
	}
	return nil
}

// SaveError сохраняет последнюю ошибку обработки статьи без смены этапа.
func (r *ArticleRepository) SaveError(ctx context.Context, articleID int64, processingErr error) error {
	const query = `
		UPDATE articles
		SET error_message = $2, updated_at = NOW()
		WHERE id = $1
	`
	if _, err := r.pool.Exec(ctx, query, articleID, processingErr.Error()); err != nil {
		return fmt.Errorf("сохранить ошибку статьи: %w", err)
	}
	return nil
}
