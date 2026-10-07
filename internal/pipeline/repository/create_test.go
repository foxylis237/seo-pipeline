package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

// Create is a test fixture: it upserts an article and its inputs, overwriting both.
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

	// Список колонок собирается из базового набора и необщих колонок задачи: запрос обязан
	// называть ровно те колонки, которые есть в её схеме PostgreSQL.
	inputColumns, inputValues := r.insertInputColumns(input)
	createInputQuery := fmt.Sprintf(`
		INSERT INTO article_inputs (article_id, %s)
		VALUES ($1, %s)
		ON CONFLICT (article_id) DO UPDATE
		SET %s
	`, strings.Join(inputColumns, ", "), placeholders(2, len(inputColumns)), excludedAssignments(inputColumns))

	_, err = tx.Exec(ctx, createInputQuery, append([]any{created.ID}, inputValues...)...)
	if err != nil {
		return article.Article{}, fmt.Errorf("сохранить входные данные статьи: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return article.Article{}, fmt.Errorf("завершить транзакцию: %w", err)
	}

	return created, nil
}

// excludedAssignments собирает `колонка = EXCLUDED.колонка` для ON CONFLICT DO UPDATE.
func excludedAssignments(columns []string) string {
	parts := make([]string, 0, len(columns))
	for _, name := range columns {
		parts = append(parts, name+" = EXCLUDED."+name)
	}
	return strings.Join(parts, ", ")
}
