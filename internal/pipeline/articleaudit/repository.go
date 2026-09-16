package articleaudit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/pagebatch"
)

// Статусы страницы. Те же слова, что у остальных задач: перевод их на другой язык ради
// «проверки вместо генерации» сделал бы логи двух задач несравнимыми.
const (
	StatusPending    = "pending"
	StatusProcessing = "processing"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
)

// Article — страница задачи целиком, как она лежит в её единственной таблице.
type Article struct {
	ID           int64
	ExternalID   string
	SourceURL    string
	Slug         string
	Topic        string
	PostID       int64
	PostType     string
	Status       string
	ErrorMessage string
	OriginalPath string
	FieldsPath   string
	PromptPath   string
	AuditPath    string
	ResultPath   string
	// Score и ScoreMax — итоговая оценка последней проверки. NULL означает, что оценки в
	// ответе не нашлось: подставлять ноль нельзя, по оценке сортируют пачку.
	Score    *int
	ScoreMax *int
	// Findings и MissingFields — счётчики находок модели и незаполненных обязательных полей.
	Findings      int
	MissingFields int
	CheckedAt     *time.Time
}

// Audited отвечает, проверена ли страница.
//
// Признак — момент проверки, а не статус: статус снимает следующий прогон, а за ответ модели
// уже заплачено, и второй проход платил бы снова за тот же отчёт.
func (a Article) Audited() bool { return a.CheckedAt != nil }

// Repository — доступ к таблице articles схемы задачи аудита.
//
// Свой, а не общий repository.ArticleRepository: у той таблицы полтора десятка колонок про
// research, структуру и метаданные, которых у аудита нет вовсе. Таблица у каждой задачи своя —
// разводит их search_path, — а запросы к ней общие, поэтому имя задачи нужно только
// сообщениям: подсказка «примени миграцию» обязана называть ту схему, в которой человек сейчас.
type Repository struct {
	pool *pgxpool.Pool
	task string
}

// NewRepository собирает доступ к таблице задачи. task — подчёркнутое имя задачи
// (pprof_audit_1): оно же имя схемы PostgreSQL и имя каталога миграций.
func NewRepository(pool *pgxpool.Pool, task string) *Repository {
	return &Repository{pool: pool, task: task}
}

// expectedColumns — колонки, которые обязаны быть в схеме задачи.
//
// Замена repository.ValidateSchema, который знает только таблицы движка. Проверка нужна по
// той же причине: не применённая миграция обязана останавливать команду на старте, а не
// падать «column does not exist» посреди прогона, уже потратив запрос к модели.
var expectedColumns = []string{
	"id", "external_id", "source_url", "slug", "topic", "post_id", "post_type",
	"status", "error_message", "original_path", "fields_path", "prompt_path",
	"audit_path", "result_path", "score", "score_max", "findings_count",
	"missing_fields_count", "checked_at", "created_at", "updated_at",
}

// EnsureSchema сверяет схему задачи до первого запроса.
func (r *Repository) EnsureSchema(ctx context.Context) error {
	rows, err := r.pool.Query(ctx, `
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'articles'`)
	if err != nil {
		return fmt.Errorf("прочитать схему задачи: %w", err)
	}
	defer rows.Close()
	found := make(map[string]struct{})
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			return fmt.Errorf("прочитать схему задачи: %w", err)
		}
		found[column] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("прочитать схему задачи: %w", err)
	}
	var missing []string
	for _, column := range expectedColumns {
		if _, ok := found[column]; !ok {
			missing = append(missing, column)
		}
	}
	if len(found) == 0 {
		missing = expectedColumns
	}
	if len(missing) > 0 {
		return fmt.Errorf("схема PostgreSQL задачи %s не готова (нет: %s). Применить:\n"+
			"  docker exec -i seo-postgres psql -U seo -d seo -c 'CREATE SCHEMA IF NOT EXISTS %s'\n"+
			"  docker exec -i seo-postgres psql -U seo -d seo -v ON_ERROR_STOP=1 -c 'SET search_path TO %s' -f - < migrations/%s/000001_schema.up.sql",
			r.task, strings.Join(missing, ", "), r.task, r.task, r.task)
	}
	return nil
}

// Import сохраняет разобранный вход.
//
// Вставка безусловна по той же причине, что и у остальных задач: повторный импорт обязан
// восстанавливать входные данные. Адрес, слаг, тема и идентификатор записи перезаписываются,
// состояние проверки — нет: заново платить за уже проверенную страницу импорт права не имеет.
//
// Идентификатор записи и тема перезаписываются только непустыми: человек дозаполняет книгу по
// ходу работы, и пустая ячейка означает «не знаю», а не «сотри, что было».
func (r *Repository) Import(ctx context.Context, sources []pagebatch.Source) (inserted, updated int, err error) {
	for _, source := range sources {
		var isInsert bool
		var postID *int64
		if source.PostID > 0 {
			value := source.PostID
			postID = &value
		}
		err := r.pool.QueryRow(ctx, `
			INSERT INTO articles (external_id, source_url, slug, post_id, topic)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''))
			ON CONFLICT (external_id) DO UPDATE
				SET source_url = EXCLUDED.source_url,
					slug = EXCLUDED.slug,
					post_id = COALESCE(EXCLUDED.post_id, articles.post_id),
					topic = COALESCE(EXCLUDED.topic, articles.topic),
					updated_at = NOW()
			RETURNING (xmax = 0)`,
			source.ExternalID, source.URL, source.Slug, postID, source.Topic).Scan(&isInsert)
		if err != nil {
			return inserted, updated, fmt.Errorf("сохранить страницу %s: %w", source.ExternalID, err)
		}
		if isInsert {
			inserted++
			continue
		}
		updated++
	}
	return inserted, updated, nil
}

const articleColumns = `id, external_id, source_url, slug, COALESCE(topic, ''),
	COALESCE(post_id, 0), COALESCE(post_type, ''), status, COALESCE(error_message, ''),
	COALESCE(original_path, ''), COALESCE(fields_path, ''), COALESCE(prompt_path, ''),
	COALESCE(audit_path, ''), COALESCE(result_path, ''), score, score_max,
	COALESCE(findings_count, 0), COALESCE(missing_fields_count, 0), checked_at`

func scanArticle(row pgx.Row) (Article, error) {
	var article Article
	err := row.Scan(&article.ID, &article.ExternalID, &article.SourceURL, &article.Slug,
		&article.Topic, &article.PostID, &article.PostType, &article.Status,
		&article.ErrorMessage, &article.OriginalPath, &article.FieldsPath,
		&article.PromptPath, &article.AuditPath, &article.ResultPath,
		&article.Score, &article.ScoreMax, &article.Findings, &article.MissingFields,
		&article.CheckedAt)
	return article, err
}

// ErrArticleNotFound — страницы с таким индексом в задаче нет.
var ErrArticleNotFound = errors.New("страница не найдена")

// Get возвращает одну страницу по индексу из входного файла.
func (r *Repository) Get(ctx context.Context, externalID string) (Article, error) {
	article, err := scanArticle(r.pool.QueryRow(ctx,
		`SELECT `+articleColumns+` FROM articles WHERE external_id = $1`, externalID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Article{}, fmt.Errorf("%w: %s", ErrArticleNotFound, externalID)
	}
	if err != nil {
		return Article{}, fmt.Errorf("прочитать страницу %s: %w", externalID, err)
	}
	return article, nil
}

// List возвращает все страницы задачи в порядке индекса — и проверенные, и нет.
//
// Отдельно от ListPending: сводке нужны все, включая упавшие, иначе «не проверено» в ней
// взяться неоткуда.
func (r *Repository) List(ctx context.Context) ([]Article, error) {
	return r.selectArticles(ctx, "")
}

// ListPending возвращает страницы, которых прогон ещё не проверил, в порядке индекса.
func (r *Repository) ListPending(ctx context.Context) ([]Article, error) {
	return r.selectArticles(ctx, "WHERE checked_at IS NULL")
}

// selectArticles читает страницы задачи в порядке индекса.
//
// Порядок числовой, а не строковый: иначе десятая страница встаёт между первой и второй, и
// человек, сверяющий прогон со своей книгой, теряет строку.
func (r *Repository) selectArticles(ctx context.Context, where string) ([]Article, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+articleColumns+` FROM articles `+where+`
		 ORDER BY NULLIF(regexp_replace(external_id, '\D', '', 'g'), '')::BIGINT NULLS LAST, external_id`)
	if err != nil {
		return nil, fmt.Errorf("выбрать страницы задачи: %w", err)
	}
	defer rows.Close()
	var articles []Article
	for rows.Next() {
		article, scanErr := scanArticle(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("прочитать страницу задачи: %w", scanErr)
		}
		articles = append(articles, article)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("выбрать страницы задачи: %w", err)
	}
	return articles, nil
}

// MarkProcessing помечает страницу выполняющейся и снимает прошлую ошибку.
func (r *Repository) MarkProcessing(ctx context.Context, externalID string) error {
	return r.exec(ctx, externalID, `UPDATE articles
		SET status = $2, error_message = NULL, updated_at = NOW() WHERE external_id = $1`,
		externalID, StatusProcessing)
}

// SaveFetched сохраняет то, что прочитано из блога, вместе с путями копии записи.
func (r *Repository) SaveFetched(ctx context.Context, externalID string, postID int64,
	postType, originalPath, fieldsPath string) error {
	return r.exec(ctx, externalID, `UPDATE articles
		SET post_id = $2, post_type = $3, original_path = $4, fields_path = $5, updated_at = NOW()
		WHERE external_id = $1`,
		externalID, postID, postType, originalPath, fieldsPath)
}

// MarkAudited отмечает страницу проверенной. Эта отметка и защищает от повтора.
//
// Оценка передаётся указателем: ненайденная в ответе оценка обязана лечь в базу как NULL, а
// не как ноль — иначе сортировка пачки поставила бы неразобранный отчёт впереди худшей
// страницы.
func (r *Repository) MarkAudited(ctx context.Context, externalID string, paths Paths,
	score, scoreMax *int, findings, missingFields int) error {
	return r.exec(ctx, externalID, `UPDATE articles
		SET status = $2, error_message = NULL,
			prompt_path = $3, audit_path = $4, result_path = $5,
			score = $6, score_max = $7, findings_count = $8, missing_fields_count = $9,
			checked_at = NOW(), updated_at = NOW()
		WHERE external_id = $1`,
		externalID, StatusCompleted, paths.PromptPath, paths.AuditPath, paths.ResultPath,
		score, scoreMax, findings, missingFields)
}

// MarkFailed сохраняет блокирующую ошибку страницы.
func (r *Repository) MarkFailed(ctx context.Context, externalID string, cause error) error {
	return r.exec(ctx, externalID, `UPDATE articles
		SET status = $2, error_message = $3, updated_at = NOW() WHERE external_id = $1`,
		externalID, StatusFailed, cause.Error())
}

// Count возвращает число страниц задачи. Нужен reset: масштаб удаления человек должен
// увидеть до подтверждения, а не после.
func (r *Repository) Count(ctx context.Context) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM articles`).Scan(&count); err != nil {
		return 0, fmt.Errorf("посчитать страницы задачи: %w", err)
	}
	return count, nil
}

// Reset очищает таблицу задачи и обнуляет счётчик идентификаторов.
func (r *Repository) Reset(ctx context.Context) error {
	if _, err := r.pool.Exec(ctx, `TRUNCATE TABLE articles RESTART IDENTITY`); err != nil {
		return fmt.Errorf("очистить таблицу задачи: %w", err)
	}
	return nil
}

func (r *Repository) exec(ctx context.Context, externalID, query string, args ...any) error {
	tag, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("обновить страницу %s: %w", externalID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", ErrArticleNotFound, externalID)
	}
	return nil
}
