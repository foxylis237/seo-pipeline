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
//
// Запрос читающий и ничего не решает: годна ли статья к публикации, выясняет вызывающий по
// собранным данным. Так проверка остаётся в одном месте и одинаково работает и для publish
// с идентификатором, и для массового прогона.
func (r *ArticleRepository) GetPublicationInput(ctx context.Context, externalID string) (article.PublicationInput, error) {
	const query = `
		SELECT
			a.id, a.external_id, a.title, COALESCE(i.image_slug, ''), COALESCE(i.reference_url, ''),
			a.status, a.current_step, a.error_message, a.created_at, a.updated_at,
			a.wordpress_status, a.wordpress_post_id, COALESCE(a.wordpress_url, ''),
			COALESCE(i.category, ''), COALESCE(i.tags, ''), COALESCE(i.key_word, ''),
			COALESCE(i.meta_description, ''), COALESCE(i.header, ''),
			COALESCE(m.tldr, ''), COALESCE(m.faq, ''),
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

// ListPublishable возвращает статьи, готовые к массовой публикации.
//
// Отдельный метод, а не восьмая ветка в GetPendingForOperation, и не по вкусовым причинам:
// все ветки того switch начинаются с status <> 'completed', потому что описывают
// незавершённый пайплайн. Здесь условие ровно обратное — публикуется только завершённое.
// Ветка с перевёрнутым смыслом внутри общего метода читалась бы как опечатка.
//
// Наличие артефактов и полноту данных метод не проверяет: это делает та же валидация, что и
// у publish с идентификатором, по данным GetPublicationInput. Иначе условий готовности стало
// бы два, и они разошлись бы.
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

// SavePublication запоминает созданную запись WordPress.
//
// Вызывается сразу после wp.newPost и до обратной сверки — намеренно. Запись уже создана,
// удалять её нельзя, повторять запрос нельзя; отметка, поставленная только после успешной
// сверки, означала бы, что расхождение в одном поле открывает дорогу второму посту.
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

// LinkPublication привязывает к статье запись, существовавшую в блоге до нас.
//
// Состояние ставится linked, а не published: эту запись собирал человек, и что в ней лежит,
// приложение не знает. Автоматически такие записи не ищутся — сопоставление по заголовку даёт
// ложные совпадения, а ошибка стоит либо дубля в блоге, либо молча пропущенной статьи.
//
// postID нулевой означает «запись неизвестна»: тогда сохраняется только состояние, а адрес и
// идентификатор остаются пустыми, и раздел со ссылкой в result.md у статьи будет пустым.
//
// Идемпотентна: повторный вызов на уже привязанной статье не ошибка.
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

// ValidatePublicationInput решает, годна ли статья к публикации.
//
// Проверка одна на обе команды и целиком локальная: WordPress о готовности статьи не
// спрашивают. Ошибка называет первую причину — человеку нужно знать, что чинить, а не
// список из восьми пунктов.
//
// Существование файла article.html здесь не проверяется: репозиторий о файловой системе не
// знает. Это делает вызывающий, у которого есть writer.
func ValidatePublicationInput(input article.PublicationInput) error {
	if input.Article.Status != "completed" {
		return fmt.Errorf("статья %s не прошла пайплайн: статус %q, а публикуются только completed",
			input.Article.ExternalID, input.Article.Status)
	}
	if input.Article.ErrorMessage != nil && strings.TrimSpace(*input.Article.ErrorMessage) != "" {
		return fmt.Errorf("на статье %s висит ошибка: %s", input.Article.ExternalID, *input.Article.ErrorMessage)
	}
	// Различать published и linked здесь незачем: дубль одинаково недопустим и для записи,
	// созданной нами, и для найденной вручную.
	if input.Publication.InWordPress() {
		return fmt.Errorf("статья %s уже есть в WordPress (%s)%s — повторная публикация создала бы дубль",
			input.Article.ExternalID, input.Publication.Status, publishedPostSuffix(input.Publication))
	}
	required := []struct {
		name  string
		value string
	}{
		{"заголовок", input.Article.Title},
		{"HTML статьи", input.HTMLPath},
		{"рубрика", input.Category},
		{"метки", input.Tags},
		{"фокусное ключевое слово", input.Keyword},
		{"мета-описание", input.MetaDescription},
		{"заголовок профблока", input.Header},
		// Слаг картинки уходит подписью вложения (title), заголовок H1 — его alt. Пустыми
		// они означали бы картинку без альтернативного текста в опубликованной статье, а
		// исправить её приложение не умеет.
		{"слаг картинки (image_slug)", input.Article.Slug},
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
