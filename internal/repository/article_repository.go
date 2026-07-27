package repository

import (
	"context"
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

// ClaimNextPending атомарно резервирует одну ожидающую статью для обработки.
func (r *ArticleRepository) ClaimNextPending(
	ctx context.Context,
) (article.Article, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return article.Article{}, false, fmt.Errorf("начать транзакцию получения статьи: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	const selectQuery = `
		SELECT
			id,
			external_id,
			title,
			status,
			current_step,
			error_message,
			created_at,
			updated_at
		FROM articles
		WHERE status = 'pending'
		ORDER BY id
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`

	var claimed article.Article
	err = tx.QueryRow(ctx, selectQuery).Scan(
		&claimed.ID,
		&claimed.ExternalID,
		&claimed.Title,
		&claimed.Status,
		&claimed.CurrentStep,
		&claimed.ErrorMessage,
		&claimed.CreatedAt,
		&claimed.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if err := tx.Commit(ctx); err != nil {
				return article.Article{}, false, fmt.Errorf("завершить пустую транзакцию получения статьи: %w", err)
			}
			return article.Article{}, false, nil
		}
		return article.Article{}, false, fmt.Errorf("выбрать ожидающую статью: %w", err)
	}

	const updateQuery = `
		UPDATE articles
		SET
			status = 'processing',
			updated_at = NOW()
		WHERE id = $1
		RETURNING status, updated_at
	`
	if err := tx.QueryRow(ctx, updateQuery, claimed.ID).Scan(
		&claimed.Status,
		&claimed.UpdatedAt,
	); err != nil {
		return article.Article{}, false, fmt.Errorf("перевести статью в обработку: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return article.Article{}, false, fmt.Errorf("зафиксировать получение статьи: %w", err)
	}

	return claimed, true, nil
}
