package repository

import (
	"context"
	"fmt"
	"strings"
)

// clearArticleTables перечисляет таблицы, которые очищает ClearArticleState, в порядке
// удаления: от зависимых к article_research.
//
// articles и article_inputs сюда не входят намеренно — это и есть результат импорта,
// который команда обязана сохранить вместе с id и external_id статьи. Список отдельный от
// resetTables: очистка одной статьи и сброс всей базы не должны меняться вместе.
var clearArticleTables = []string{
	"article_errors",
	"article_outputs",
	"article_metadata",
	"article_research",
}

// ClearCount — сколько строк одной таблицы принадлежит статье.
type ClearCount struct {
	Table string
	Rows  int64
}

// ClearArticleCounts считает строки статьи по тем же таблицам и в том же порядке, в каком их
// удалит ClearArticleState. Один список обслуживает и подсчёт, и удаление, поэтому отчёт не
// может разойтись с тем, что команда действительно сотрёт.
func (r *ArticleRepository) ClearArticleCounts(ctx context.Context, articleID int64) ([]ClearCount, error) {
	selects := make([]string, 0, len(clearArticleTables))
	for _, table := range clearArticleTables {
		selects = append(selects, fmt.Sprintf("(SELECT COUNT(*) FROM %s WHERE article_id = $1)", table))
	}
	query := "SELECT " + strings.Join(selects, ", ")

	rows := make([]int64, len(clearArticleTables))
	targets := make([]any, len(rows))
	for index := range rows {
		targets[index] = &rows[index]
	}
	if err := r.pool.QueryRow(ctx, query, articleID).Scan(targets...); err != nil {
		return nil, fmt.Errorf("посчитать строки статьи %d: %w", articleID, err)
	}

	counts := make([]ClearCount, len(clearArticleTables))
	for index, table := range clearArticleTables {
		counts[index] = ClearCount{Table: table, Rows: rows[index]}
	}
	return counts, nil
}

// ClearArticleState возвращает одну статью к состоянию сразу после импорта: удаляет research,
// metadata, outputs и историю ошибок, сбрасывает статус в pending и снимает этап с ошибкой.
//
// Строка articles и её article_inputs остаются на месте, id не переиспользуется и не
// сдвигается — место статьи в базе сохраняется, повторный импорт ей не нужен.
//
// Всё делается одной транзакцией: наполовину очищенная статья хуже неочищенной, потому что
// пайплайн увидит research без outputs и посчитает часть этапов готовыми.
func (r *ArticleRepository) ClearArticleState(ctx context.Context, articleID int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("начать очистку статьи %d: %w", articleID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, table := range clearArticleTables {
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
