package repository

import (
	"context"
	"fmt"
	"strings"
)

// SaveGoogleDocURL запоминает адрес документа с промптом статьи.
//
// Строка article_outputs создаётся здесь при необходимости: публикация идёт сразу после
// стадии article и обгоняет этапы, которые эту строку обычно заводят. Обновляется только
// одна колонка — пути артефактов пишут свои методы, и затирать их отсюда нельзя.
//
// Пустой адрес отвергается: пустая строка неотличима от «не публиковалось», а затирать уже
// сохранённую ссылку неудачной попыткой нельзя — result.md потерял бы рабочий адрес.
func (r *ArticleRepository) SaveGoogleDocURL(ctx context.Context, articleID int64, documentURL string) error {
	trimmed := strings.TrimSpace(documentURL)
	if trimmed == "" {
		return fmt.Errorf("адрес документа Google для статьи %d пуст", articleID)
	}

	const query = `
		INSERT INTO article_outputs (article_id, google_doc_url, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (article_id) DO UPDATE
		SET google_doc_url = EXCLUDED.google_doc_url,
			updated_at = NOW()
	`
	result, err := r.pool.Exec(ctx, query, articleID, trimmed)
	if err != nil {
		return fmt.Errorf("сохранить адрес документа Google для статьи %d: %w", articleID, err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("статья %d не найдена при сохранении адреса документа Google", articleID)
	}
	return nil
}
