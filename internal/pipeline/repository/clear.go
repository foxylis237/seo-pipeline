package repository

import (
	"context"
	"fmt"
	"strings"
)

// clearArticleTables — таблицы ClearArticleState в порядке удаления; articles и article_inputs
// (результат импорта) не входят.
var clearArticleTables = []string{
	"article_errors",
	"article_outputs",
	"article_metadata",
	"article_research",
}

// resetArticleTables — таблицы clear плюс article_inputs; строка articles и её id переживают reset.
var resetArticleTables = []string{
	"article_errors",
	"article_outputs",
	"article_metadata",
	"article_research",
	"article_inputs",
}

// ClearCount — сколько строк одной таблицы принадлежит статье.
type ClearCount struct {
	Table string
	Rows  int64
}

// ClearArticleCounts считает строки статьи по таблицам, которые удалит ClearArticleState.
func (r *ArticleRepository) ClearArticleCounts(ctx context.Context, articleID int64) ([]ClearCount, error) {
	return r.articleTableCounts(ctx, clearArticleTables, articleID)
}

// ResetArticleCounts считает строки статьи по таблицам, которые удалит ResetArticleState.
func (r *ArticleRepository) ResetArticleCounts(ctx context.Context, articleID int64) ([]ClearCount, error) {
	return r.articleTableCounts(ctx, resetArticleTables, articleID)
}

func (r *ArticleRepository) articleTableCounts(
	ctx context.Context,
	tables []string,
	articleID int64,
) ([]ClearCount, error) {
	selects := make([]string, 0, len(tables))
	for _, table := range tables {
		selects = append(selects, fmt.Sprintf("(SELECT COUNT(*) FROM %s WHERE article_id = $1)", table))
	}
	query := "SELECT " + strings.Join(selects, ", ")

	rows := make([]int64, len(tables))
	targets := make([]any, len(rows))
	for index := range rows {
		targets[index] = &rows[index]
	}
	if err := r.pool.QueryRow(ctx, query, articleID).Scan(targets...); err != nil {
		return nil, fmt.Errorf("посчитать строки статьи %d: %w", articleID, err)
	}

	counts := make([]ClearCount, len(tables))
	for index, table := range tables {
		counts[index] = ClearCount{Table: table, Rows: rows[index]}
	}
	return counts, nil
}

// ClearArticleState возвращает одну статью к состоянию сразу после импорта одной транзакцией.
func (r *ArticleRepository) ClearArticleState(ctx context.Context, articleID int64) error {
	return r.deleteArticleState(ctx, clearArticleTables, articleID)
}

// ResetArticleState — ClearArticleState плюс article_inputs; id и external_id сохраняются до повторного импорта.
func (r *ArticleRepository) ResetArticleState(ctx context.Context, articleID int64) error {
	return r.deleteArticleState(ctx, resetArticleTables, articleID)
}

func (r *ArticleRepository) deleteArticleState(ctx context.Context, tables []string, articleID int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("начать очистку статьи %d: %w", articleID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, table := range tables {
		if _, err := tx.Exec(ctx, "DELETE FROM "+table+" WHERE article_id = $1", articleID); err != nil {
			return fmt.Errorf("очистить %s статьи %d: %w", table, articleID, err)
		}
	}

	result, err := tx.Exec(ctx, `
		UPDATE articles
		SET status = 'pending', current_step = NULL, error_message = NULL, updated_at = NOW()
		WHERE id = $1
	`, articleID)
	if err != nil {
		return fmt.Errorf("сбросить статус статьи %d: %w", articleID, err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("статья %d не найдена при очистке", articleID)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("завершить очистку статьи %d: %w", articleID, err)
	}
	return nil
}
