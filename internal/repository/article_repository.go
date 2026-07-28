package repository

import (
	"context"
	"encoding/json"
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

// Reset удаляет все данные проекта и сбрасывает счётчики идентификаторов.
func (r *ArticleRepository) Reset(ctx context.Context) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("начать транзакцию очистки базы данных: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const query = `
		TRUNCATE TABLE
			article_outputs,
			article_metadata,
			article_research,
			article_inputs,
			articles
		RESTART IDENTITY CASCADE
	`
	if _, err := tx.Exec(ctx, query); err != nil {
		return fmt.Errorf("очистить таблицы проекта: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("завершить транзакцию очистки базы данных: %w", err)
	}
	return nil
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

// GetAll возвращает статьи с их URL конкурентов в порядке ID.
func (r *ArticleRepository) GetAll(
	ctx context.Context,
) ([]article.Article, error) {
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
	`
	rows, err := r.pool.Query(ctx, selectQuery)
	if err != nil {
		return nil, fmt.Errorf("получить статьи: %w", err)
	}
	defer rows.Close()

	articles := make([]article.Article, 0)
	for rows.Next() {
		var selected article.Article
		if err := rows.Scan(
			&selected.ID,
			&selected.ExternalID,
			&selected.Title,
			&selected.ReferenceURL,
			&selected.Status,
			&selected.CurrentStep,
			&selected.ErrorMessage,
			&selected.CreatedAt,
			&selected.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("прочитать статью: %w", err)
		}
		articles = append(articles, selected)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("получить статьи: %w", err)
	}
	return articles, nil
}

// MarkArticlePromptBuilt переводит статью к генерации после успешной сборки промпта.
func (r *ArticleRepository) MarkArticlePromptBuilt(ctx context.Context, articleID int64) error {
	const query = `
		UPDATE articles
		SET current_step = 'article_generation', error_message = NULL, updated_at = NOW()
		WHERE id = $1
	`
	result, err := r.pool.Exec(ctx, query, articleID)
	if err != nil {
		return fmt.Errorf("обновить этап статьи после сборки промпта: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("статья %d не найдена при обновлении этапа", articleID)
	}
	return nil
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
		INSERT INTO article_research (article_id, cleaned_keywords, collected_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT (article_id) DO UPDATE
		SET
			cleaned_keywords = EXCLUDED.cleaned_keywords,
			collected_at = NOW(),
			updated_at = NOW()
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

// SaveArsenkinResearch сохраняет Wordstat и Copywriters одной транзакцией.
func (r *ArticleRepository) SaveArsenkinResearch(
	ctx context.Context,
	articleID int64,
	wordstat []article.KeywordFrequency,
	lsiWords []string,
	competitorStructure string,
) error {
	wordstatJSON, err := json.Marshal(wordstat)
	if err != nil {
		return fmt.Errorf("закодировать частотности Wordstat: %w", err)
	}
	lsiJSON, err := json.Marshal(lsiWords)
	if err != nil {
		return fmt.Errorf("закодировать LSI Copywriters: %w", err)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("начать транзакцию сохранения Arsenkin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const researchQuery = `
		INSERT INTO article_research (
			article_id, wordstat_keywords, lsi_words, competitor_structure, collected_at, updated_at
		)
		VALUES ($1, $2, $3, $4, NOW(), NOW())
		ON CONFLICT (article_id) DO UPDATE
		SET
			wordstat_keywords = EXCLUDED.wordstat_keywords,
			lsi_words = EXCLUDED.lsi_words,
			competitor_structure = EXCLUDED.competitor_structure,
			collected_at = NOW(),
			updated_at = NOW()
	`
	if _, err := tx.Exec(ctx, researchQuery, articleID, string(wordstatJSON), string(lsiJSON), competitorStructure); err != nil {
		return fmt.Errorf("сохранить исследование Arsenkin: %w", err)
	}

	const articleQuery = `
		UPDATE articles
		SET current_step = 'structure_generation', error_message = NULL, updated_at = NOW()
		WHERE id = $1
	`
	result, err := tx.Exec(ctx, articleQuery, articleID)
	if err != nil {
		return fmt.Errorf("обновить текущий этап статьи после Copywriters: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("статья %d не найдена при обновлении этапа", articleID)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("завершить сохранение Arsenkin: %w", err)
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
