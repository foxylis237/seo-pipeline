package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/article"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ArticleRepository работает со статьями в PostgreSQL.
type ArticleRepository struct {
	pool *pgxpool.Pool
}

// GetSavedGenerationInput loads only persisted artifacts needed to resume generation.
func (r *ArticleRepository) GetSavedGenerationInput(ctx context.Context, externalID string) (article.SavedGenerationInput, error) {
	const query = `
		SELECT a.id, a.external_id, a.title, COALESCE(i.image_slug, ''),
			a.status, a.current_step, a.error_message, a.created_at, a.updated_at,
			COALESCE(i.professions, ''), COALESCE(i.links, ''),
			COALESCE(o.structure_path, ''), COALESCE(o.article_path, ''),
			COALESCE(o.metadata_path, ''), COALESCE(o.final_path, '')
		FROM articles AS a
		LEFT JOIN article_inputs AS i ON i.article_id = a.id
		LEFT JOIN article_outputs AS o ON o.article_id = a.id
		WHERE a.external_id = $1
	`
	var input article.SavedGenerationInput
	err := r.pool.QueryRow(ctx, query, externalID).Scan(
		&input.Article.ID, &input.Article.ExternalID, &input.Article.Title, &input.Article.Slug,
		&input.Article.Status, &input.Article.CurrentStep, &input.Article.ErrorMessage,
		&input.Article.CreatedAt, &input.Article.UpdatedAt,
		&input.Professions, &input.Links, &input.StructurePath, &input.ArticlePath,
		&input.ReviewPath, &input.FixedArticlePath,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return article.SavedGenerationInput{}, fmt.Errorf("статья с external_id %q не найдена", externalID)
	}
	if err != nil {
		return article.SavedGenerationInput{}, fmt.Errorf("загрузить сохранённые результаты для external_id %q: %w", externalID, err)
	}
	if strings.TrimSpace(input.Article.Slug) == "" {
		return article.SavedGenerationInput{}, fmt.Errorf("для статьи external_id %q отсутствует image_slug", externalID)
	}
	return input, nil
}

// GetGenerationInput loads persisted data required by implemented LLM stages.
func (r *ArticleRepository) GetGenerationInput(ctx context.Context, externalID string) (article.GenerationInput, error) {
	const query = `
		SELECT a.id, a.external_id, a.title, COALESCE(i.image_slug, ''),
			a.status, a.current_step, a.error_message, a.created_at, a.updated_at,
			COALESCE(i.professions, ''), COALESCE(i.links, ''),
			r.competitor_structure,
			COALESCE(r.wordstat_keywords, '[]'::jsonb),
			COALESCE(r.lsi_words, '[]'::jsonb)
		FROM articles AS a
		LEFT JOIN article_inputs AS i ON i.article_id = a.id
		LEFT JOIN article_research AS r ON r.article_id = a.id
		WHERE a.external_id = $1
	`
	var input article.GenerationInput
	var competitorStructure *string
	var wordstatJSON, lsiJSON []byte
	err := r.pool.QueryRow(ctx, query, externalID).Scan(
		&input.Article.ID, &input.Article.ExternalID, &input.Article.Title, &input.Article.Slug,
		&input.Article.Status, &input.Article.CurrentStep, &input.Article.ErrorMessage,
		&input.Article.CreatedAt, &input.Article.UpdatedAt, &input.Professions, &input.Links,
		&competitorStructure, &wordstatJSON, &lsiJSON,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return article.GenerationInput{}, fmt.Errorf("статья с external_id %q не найдена", externalID)
	}
	if err != nil {
		return article.GenerationInput{}, fmt.Errorf("загрузить данные генерации для external_id %q: %w", externalID, err)
	}
	if competitorStructure == nil || strings.TrimSpace(*competitorStructure) == "" {
		return article.GenerationInput{}, fmt.Errorf("для статьи external_id %q отсутствует competitor_structure в article_research", externalID)
	}
	if strings.TrimSpace(input.Article.Slug) == "" {
		return article.GenerationInput{}, fmt.Errorf("для статьи external_id %q отсутствует image_slug", externalID)
	}
	if err := json.Unmarshal(wordstatJSON, &input.WordstatKeywords); err != nil {
		return article.GenerationInput{}, fmt.Errorf("прочитать wordstat_keywords для external_id %q: %w", externalID, err)
	}
	if err := json.Unmarshal(lsiJSON, &input.LSIWords); err != nil {
		return article.GenerationInput{}, fmt.Errorf("прочитать lsi_words для external_id %q: %w", externalID, err)
	}
	input.CompetitorStructure = *competitorStructure
	return input, nil
}

// NewArticleRepository создаёт репозиторий статей.
func NewArticleRepository(pool *pgxpool.Pool) *ArticleRepository {
	return &ArticleRepository{
		pool: pool,
	}
}

// BeginGeneration atomically makes completed, failed, or processing articles ready for a fresh generation run.
func (r *ArticleRepository) BeginGeneration(ctx context.Context, articleID int64) error {
	const query = `
		UPDATE articles
		SET status = 'processing', current_step = 'structure_generation',
			error_message = NULL, updated_at = NOW()
		WHERE id = $1
	`
	result, err := r.pool.Exec(ctx, query, articleID)
	if err != nil {
		return fmt.Errorf("подготовить статью %d к генерации: %w", articleID, err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("статья %d не найдена при подготовке к генерации", articleID)
	}
	return nil
}

// BeginGenerationStage prepares one resumable stage without resetting saved artifacts.
func (r *ArticleRepository) BeginGenerationStage(ctx context.Context, articleID int64, stage string) error {
	var currentStep string
	switch stage {
	case "article":
		currentStep = "article_generation"
	case "info":
		currentStep = "metadata_generation"
	case "review", "fix":
		currentStep = "article_review"
	case "html":
		currentStep = "html_generation"
	default:
		return fmt.Errorf("неподдерживаемый отдельный этап генерации %q", stage)
	}
	const query = `
		UPDATE articles
		SET status = 'processing', current_step = $2, error_message = NULL, updated_at = NOW()
		WHERE id = $1
	`
	result, err := r.pool.Exec(ctx, query, articleID, currentStep)
	if err != nil {
		return fmt.Errorf("подготовить статью %d к этапу %s: %w", articleID, stage, err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("статья %d не найдена при подготовке этапа %s", articleID, stage)
	}
	return nil
}

// SaveArticleInfo stores publication information and advances to the review stage.
func (r *ArticleRepository) SaveArticleInfo(ctx context.Context, articleID int64, info string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("начать сохранение информации для публикации: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO article_metadata (article_id, metadata_text, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (article_id) DO UPDATE
		SET metadata_text = EXCLUDED.metadata_text, updated_at = NOW()
	`, articleID, info); err != nil {
		return fmt.Errorf("сохранить информацию для публикации: %w", err)
	}
	result, err := tx.Exec(ctx, `
		UPDATE articles
		SET status = 'processing', current_step = 'article_review',
			error_message = NULL, updated_at = NOW()
		WHERE id = $1
	`, articleID)
	if err != nil {
		return fmt.Errorf("обновить этап после информации для публикации: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("статья %d не найдена при сохранении информации для публикации", articleID)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("завершить сохранение информации для публикации: %w", err)
	}
	return nil
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
			COALESCE(i.image_slug, ''),
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
			&selected.Slug,
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

// ResetArticleForRun removes all derived data and resets processing state.
// The article row and its imported input remain the current source of the run.
func (r *ArticleRepository) ResetArticleForRun(ctx context.Context, articleID int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin resetting article for run: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, table := range []string{"article_outputs", "article_metadata", "article_research"} {
		if _, err := tx.Exec(ctx, "DELETE FROM "+table+" WHERE article_id = $1", articleID); err != nil {
			return fmt.Errorf("clear %s for article %d: %w", table, articleID, err)
		}
	}
	const articleQuery = `
		UPDATE articles
		SET status = 'processing', current_step = 'arsenkin_collection',
			error_message = NULL, updated_at = NOW()
		WHERE id = $1
	`
	result, err := tx.Exec(ctx, articleQuery, articleID)
	if err != nil {
		return fmt.Errorf("reset article processing state: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("article %d not found while resetting run", articleID)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("finish resetting article for run: %w", err)
	}
	return nil
}

// SaveStructurePath stores the generated structure path and advances to article generation.
func (r *ArticleRepository) SaveStructurePath(ctx context.Context, articleID int64, structurePath string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("начать сохранение пути структуры: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const outputQuery = `
		INSERT INTO article_outputs (article_id, structure_path, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (article_id) DO UPDATE
		SET structure_path = EXCLUDED.structure_path,
			article_path = NULL,
			metadata_path = NULL,
			html_path = NULL,
			final_path = NULL,
			word_count = NULL,
			updated_at = NOW()
	`
	if _, err := tx.Exec(ctx, outputQuery, articleID, structurePath); err != nil {
		return fmt.Errorf("сохранить путь структуры: %w", err)
	}
	const articleQuery = `
		UPDATE articles
		SET current_step = 'article_generation', error_message = NULL, updated_at = NOW()
		WHERE id = $1
	`
	result, err := tx.Exec(ctx, articleQuery, articleID)
	if err != nil {
		return fmt.Errorf("обновить этап после генерации структуры: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("статья %d не найдена при сохранении структуры", articleID)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("завершить сохранение пути структуры: %w", err)
	}
	return nil
}

// SaveGenerationPaths stores only relative paths of generated responses.
func (r *ArticleRepository) SaveGenerationPaths(ctx context.Context, articleID int64, structurePath, articlePath string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin saving article path: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const outputQuery = `
		INSERT INTO article_outputs (article_id, structure_path, article_path, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (article_id) DO UPDATE
		SET structure_path = EXCLUDED.structure_path,
			article_path = EXCLUDED.article_path, updated_at = NOW()
	`
	if _, err := tx.Exec(ctx, outputQuery, articleID, structurePath, articlePath); err != nil {
		return fmt.Errorf("save generation paths: %w", err)
	}
	const articleQuery = `
		UPDATE articles
		SET current_step = 'article_review', error_message = NULL, updated_at = NOW()
		WHERE id = $1
	`
	result, err := tx.Exec(ctx, articleQuery, articleID)
	if err != nil {
		return fmt.Errorf("update article stage after generation: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("article %d not found while saving article path", articleID)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("finish saving article path: %w", err)
	}
	return nil
}

// SaveReviewPath stores the review artifact without advancing past the fix stage.
func (r *ArticleRepository) SaveReviewPath(ctx context.Context, articleID int64, reviewPath string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("начать сохранение пути ревью: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	outputResult, err := tx.Exec(ctx, `
		UPDATE article_outputs
		SET metadata_path = $2, updated_at = NOW()
		WHERE article_id = $1
	`, articleID, reviewPath)
	if err != nil {
		return fmt.Errorf("сохранить путь ревью: %w", err)
	}
	if outputResult.RowsAffected() != 1 {
		return fmt.Errorf("результаты статьи %d не найдены при сохранении ревью", articleID)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("завершить сохранение пути ревью: %w", err)
	}
	return nil
}

// SaveFixedArticlePath stores the corrected article and advances to HTML generation.
func (r *ArticleRepository) SaveFixedArticlePath(ctx context.Context, articleID int64, fixedArticlePath string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("начать сохранение исправленной статьи: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	outputResult, err := tx.Exec(ctx, `
		UPDATE article_outputs
		SET final_path = $2, updated_at = NOW()
		WHERE article_id = $1
	`, articleID, fixedArticlePath)
	if err != nil {
		return fmt.Errorf("сохранить путь исправленной статьи: %w", err)
	}
	if outputResult.RowsAffected() != 1 {
		return fmt.Errorf("результаты статьи %d не найдены при сохранении исправленной статьи", articleID)
	}
	result, err := tx.Exec(ctx, `
		UPDATE articles
		SET current_step = 'html_generation', error_message = NULL, updated_at = NOW()
		WHERE id = $1
	`, articleID)
	if err != nil {
		return fmt.Errorf("обновить этап после исправления статьи: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("статья %d не найдена при сохранении исправленной статьи", articleID)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("завершить сохранение исправленной статьи: %w", err)
	}
	return nil
}

// SaveHTMLPath stores final HTML and marks the article completed.
func (r *ArticleRepository) SaveHTMLPath(ctx context.Context, articleID int64, htmlPath string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("начать сохранение HTML: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	outputResult, err := tx.Exec(ctx, `
		UPDATE article_outputs
		SET html_path = $2, updated_at = NOW()
		WHERE article_id = $1
	`, articleID, htmlPath)
	if err != nil {
		return fmt.Errorf("сохранить путь HTML: %w", err)
	}
	if outputResult.RowsAffected() != 1 {
		return fmt.Errorf("результаты статьи %d не найдены при сохранении HTML", articleID)
	}
	result, err := tx.Exec(ctx, `
		UPDATE articles
		SET status = 'completed', current_step = NULL, error_message = NULL, updated_at = NOW()
		WHERE id = $1
	`, articleID)
	if err != nil {
		return fmt.Errorf("завершить обработку статьи: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("статья %d не найдена при сохранении HTML", articleID)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("завершить сохранение HTML: %w", err)
	}
	return nil
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

// SaveError marks processing as failed and stores the last error without advancing the stage.
func (r *ArticleRepository) SaveError(ctx context.Context, articleID int64, processingErr error) error {
	const query = `
		UPDATE articles
		SET status = 'failed', error_message = $2, updated_at = NOW()
		WHERE id = $1
	`
	if _, err := r.pool.Exec(ctx, query, articleID, processingErr.Error()); err != nil {
		return fmt.Errorf("сохранить ошибку статьи: %w", err)
	}
	return nil
}
