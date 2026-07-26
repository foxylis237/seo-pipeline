package repository

import (
	"context"
	"fmt"

	"github.com/foxylis237/seo-pipeline/internal/article"
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
			title
		)
		VALUES ($1)
		RETURNING
			id,
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
		input.Title,
	).Scan(
		&created.ID,
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
