package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

// GetPublicationInput собирает всё, что нужно для публикации одной статьи, одним запросом.
func (r *ArticleRepository) GetPublicationInput(ctx context.Context, externalID string) (article.PublicationInput, error) {
	query := `
		SELECT
			a.id, a.external_id, a.title, COALESCE(i.image_slug, ''), COALESCE(i.reference_url, ''),
			a.status, a.current_step, a.error_message, a.created_at, a.updated_at,
			a.wordpress_status, a.wordpress_post_id, COALESCE(a.wordpress_url, ''),
			COALESCE(i.category, ''), ` + r.inputColumn("tags") + `, COALESCE(i.key_word, ''),
			COALESCE(i.meta_description, ''), COALESCE(i.header, ''),
			` + r.metadataTLDR() + `, COALESCE(m.faq, ''),
			` + r.inputColumn("seo_title") + `, ` + r.inputColumn("teachers") + `,
			` + r.inputColumn("profession") + `, ` + r.inputColumn("author") + `,
			` + r.inputColumn("professions") + `, ` + r.inputColumn("links") + `,
			` + r.inputColumn("post_type") + `, ` + r.inputColumn("image_source_url") + `,
			` + r.inputColumn("hours") + `, ` + r.inputColumn("duration") + `,
			` + r.inputColumn("price") + `, ` + r.inputColumn("document") + `,
			` + r.inputColumn("attestation") + `,
			COALESCE(o.html_path, '')
		FROM articles AS a
		LEFT JOIN article_inputs AS i ON i.article_id = a.id
		LEFT JOIN article_metadata AS m ON m.article_id = a.id
		LEFT JOIN article_outputs AS o ON o.article_id = a.id
		WHERE a.external_id = $1
	`
	var input article.PublicationInput
	err := r.pool.QueryRow(ctx, query, externalID).Scan(
		&input.Article.ID, &input.Article.ExternalID, &input.Article.Title,
		&input.Article.Slug, &input.Article.ReferenceURL,
		&input.Article.Status, &input.Article.CurrentStep, &input.Article.ErrorMessage,
		&input.Article.CreatedAt, &input.Article.UpdatedAt,
		&input.Publication.Status, &input.Publication.PostID, &input.Publication.URL,
		&input.Category, &input.Tags, &input.Keyword,
		&input.MetaDescription, &input.Header,
		&input.TLDR, &input.FAQ,
		&input.SEOTitle, &input.Teachers, &input.Profession, &input.Author,
		&input.Professions, &input.Links,
		&input.PostType, &input.ImageSourceURL,
		&input.Hours, &input.Duration, &input.Price, &input.Document, &input.Attestation,
		&input.HTMLPath,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return article.PublicationInput{}, fmt.Errorf("статья с external_id %s не найдена", externalID)
	}
	if err != nil {
		return article.PublicationInput{}, fmt.Errorf("прочитать данные публикации статьи %s: %w", externalID, err)
	}
	return input, nil
}

// ListPublishable возвращает статьи, готовые к массовой публикации; полноту данных проверяет
// ValidatePublicationInput, а не этот запрос.
func (r *ArticleRepository) ListPublishable(ctx context.Context) ([]string, error) {
	const query = `
		SELECT a.external_id
		FROM articles AS a
		WHERE a.status = 'completed'
		  AND a.wordpress_status = $1
		ORDER BY a.id ASC
	`
	rows, err := r.pool.Query(ctx, query, article.WordPressNotPublished)
	if err != nil {
		return nil, fmt.Errorf("выбрать статьи к публикации: %w", err)
	}
	defer rows.Close()

	var externalIDs []string
	for rows.Next() {
		var externalID string
		if err := rows.Scan(&externalID); err != nil {
			return nil, fmt.Errorf("прочитать статью к публикации: %w", err)
		}
		externalIDs = append(externalIDs, externalID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("выбрать статьи к публикации: %w", err)
	}
	return externalIDs, nil
}

// SavePublication запоминает созданную запись WordPress; вызывается сразу после wp.newPost,
// до обратной сверки, чтобы расхождение сверки не открыло дорогу второму посту.
func (r *ArticleRepository) SavePublication(ctx context.Context, externalID string, postID int64, url string) error {
	if postID <= 0 {
		return fmt.Errorf("сохранить публикацию статьи %s: идентификатор записи не задан", externalID)
	}
	const query = `
		UPDATE articles
		SET wordpress_status = $2,
			wordpress_post_id = $3,
			wordpress_url = NULLIF(BTRIM($4), ''),
			updated_at = NOW()
		WHERE external_id = $1
	`
	result, err := r.pool.Exec(ctx, query, externalID, article.WordPressPublished, postID, url)
	if err != nil {
		return fmt.Errorf("сохранить публикацию статьи %s: %w", externalID, err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("сохранить публикацию: статья с external_id %s не найдена", externalID)
	}
	return nil
}

// LinkPublication привязывает к статье существовавшую запись блога со статусом linked;
// нулевой postID сохраняет только состояние. Повторный вызов не ошибка.
func (r *ArticleRepository) LinkPublication(ctx context.Context, externalID string, postID int64, url string) error {
	const query = `
		UPDATE articles
		SET wordpress_status = $2,
			wordpress_post_id = COALESCE(NULLIF($3, 0), wordpress_post_id),
			wordpress_url = COALESCE(NULLIF(BTRIM($4), ''), wordpress_url),
			updated_at = NOW()
		WHERE external_id = $1
	`
	result, err := r.pool.Exec(ctx, query, externalID, article.WordPressLinked, postID, url)
	if err != nil {
		return fmt.Errorf("привязать запись WordPress к статье %s: %w", externalID, err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("привязать запись WordPress: статья с external_id %s не найдена", externalID)
	}
	return nil
}

// ValidatePublicationInput решает, годна ли статья к публикации, и называет первую причину отказа.
// Файл article.html и метаданные (ValidateArticleMetadata) проверяет вызывающий.
func ValidatePublicationInput(input article.PublicationInput) error {
	if input.Article.Status != "completed" {
		return fmt.Errorf("статья %s не прошла пайплайн: статус %q, а публикуются только completed",
			input.Article.ExternalID, input.Article.Status)
	}
	if input.Article.ErrorMessage != nil && strings.TrimSpace(*input.Article.ErrorMessage) != "" {
		return fmt.Errorf("на статье %s висит ошибка: %s", input.Article.ExternalID, *input.Article.ErrorMessage)
	}
	if input.Publication.InWordPress() {
		return fmt.Errorf("статья %s уже есть в WordPress (%s)%s — повторная публикация создала бы дубль",
			input.Article.ExternalID, input.Publication.Status, publishedPostSuffix(input.Publication))
	}
	return validatePublicationFields(input)
}

// ValidateRepublishInput решает, годна ли статья к перезаписи тела уже существующей записи.
func ValidateRepublishInput(input article.PublicationInput) error {
	if input.Article.Status != "completed" {
		return fmt.Errorf("статья %s не прошла пайплайн: статус %q, а переписываются только completed",
			input.Article.ExternalID, input.Article.Status)
	}
	if input.Article.ErrorMessage != nil && strings.TrimSpace(*input.Article.ErrorMessage) != "" {
		return fmt.Errorf("на статье %s висит ошибка: %s", input.Article.ExternalID, *input.Article.ErrorMessage)
	}
	if !input.Publication.InWordPress() || input.Publication.PostID == nil {
		return fmt.Errorf("статья %s в блоге не опубликована — переписывать нечего: её выкладывает publish",
			input.Article.ExternalID)
	}
	return validatePublicationFields(input)
}

func validatePublicationFields(input article.PublicationInput) error {
	required := []struct {
		name  string
		value string
	}{
		{"заголовок", input.Article.Title},
		{"HTML статьи", input.HTMLPath},
		{"рубрика", input.Category},
		{"фокусное ключевое слово", input.Keyword},
		{"мета-описание", input.MetaDescription},
		{"заголовок профблока", input.Header},
		{"слаг картинки (image_slug)", input.Article.Slug},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("у статьи %s не заполнено обязательное для публикации поле: %s",
				input.Article.ExternalID, field.name)
		}
	}
	return nil
}

// ValidateArticleMetadata проверяет разделы стадии info (TL;DR и FAQ); зовётся только у задач с этой стадией.
func ValidateArticleMetadata(input article.PublicationInput) error {
	required := []struct {
		name  string
		value string
	}{
		{"TL;DR", input.TLDR},
		{"FAQ", input.FAQ},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("у статьи %s не заполнено обязательное для публикации поле: %s",
				input.Article.ExternalID, field.name)
		}
	}
	return nil
}

func publishedPostSuffix(publication article.Publication) string {
	if publication.PostID == nil {
		return ""
	}
	return fmt.Sprintf(" (запись %d)", *publication.PostID)
}
