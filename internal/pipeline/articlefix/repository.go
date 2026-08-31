package articlefix

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Статусы статьи. Их четыре, и они те же, что у задач генерации: перевод слов на другой
// язык ради «правки вместо генерации» сделал бы логи двух задач несравнимыми.
const (
	StatusPending    = "pending"
	StatusProcessing = "processing"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
)

// Article — статья задачи целиком, как она лежит в её единственной таблице.
type Article struct {
	ID            int64
	ExternalID    string
	SourceURL     string
	Slug          string
	PostID        int64
	OldTitle      string
	NewTitle      string
	Status        string
	ErrorMessage  string
	OriginalPath  string
	PromptPath    string
	RewrittenPath string
	ResultPath    string
	UpdatedPostAt *time.Time
}

// Rewritten отвечает, переписана ли статья в блоге.
//
// Признак — момент записи, а не статус: статус снимает следующий прогон, а запись в живом
// блоге отменить нельзя, и второй проход отправил бы модели её же собственный вывод.
func (a Article) Rewritten() bool { return a.UpdatedPostAt != nil }

// Repository — доступ к таблице articles схемы задачи правки.
//
// Свой, а не общий repository.ArticleRepository: у той таблицы полтора десятка колонок про
// research, структуру и метаданные, которых у задач правки нет вовсе. Таблица у каждой задачи
// своя — разводит их search_path, — а запросы к ней общие, поэтому имя задачи нужно только
// сообщениям: подсказка «примени миграцию» обязана называть ту схему, в которой человек сейчас.
type Repository struct {
	pool *pgxpool.Pool
	task string
}

// NewRepository собирает доступ к таблице задачи. task — подчёркнутое имя задачи
// (pprof_fix_1): оно же имя схемы PostgreSQL и имя каталога миграций.
func NewRepository(pool *pgxpool.Pool, task string) *Repository {
	return &Repository{pool: pool, task: task}
}

// expectedColumns — колонки, которые обязаны быть в схеме задачи.
//
// Замена repository.ValidateSchema, который знает только таблицы движка. Проверка нужна по
// той же причине: не применённая миграция обязана останавливать команду на старте, а не
// падать «column does not exist» посреди прогона, уже потратив запрос к модели.
var expectedColumns = []string{
	"id", "external_id", "source_url", "slug", "post_id", "old_title", "new_title",
	"status", "error_message", "original_path", "prompt_path", "rewritten_path",
	"result_path", "updated_post_at", "created_at", "updated_at",
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
	if len(found) == 0 || len(missing) > 0 {
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
// восстанавливать входные данные. Адрес и слаг перезаписываются, состояние прогона — нет:
// заново скачивать и переписывать уже переписанную статью импорт права не имеет.
func (r *Repository) Import(ctx context.Context, sources []Source) (inserted, updated int, err error) {
	for _, source := range sources {
		var isInsert bool
		err := r.pool.QueryRow(ctx, `
			INSERT INTO articles (external_id, source_url, slug)
			VALUES ($1, $2, $3)
			ON CONFLICT (external_id) DO UPDATE
				SET source_url = EXCLUDED.source_url,
					slug = EXCLUDED.slug,
					updated_at = NOW()
			RETURNING (xmax = 0)`,
			source.ExternalID, source.URL, source.Slug).Scan(&isInsert)
		if err != nil {
			return inserted, updated, fmt.Errorf("сохранить статью %s: %w", source.ExternalID, err)
		}
		if isInsert {
			inserted++
			continue
		}
		updated++
	}
	return inserted, updated, nil
}

const articleColumns = `id, external_id, source_url, slug, COALESCE(post_id, 0),
	COALESCE(old_title, ''), COALESCE(new_title, ''), status, COALESCE(error_message, ''),
	COALESCE(original_path, ''), COALESCE(prompt_path, ''), COALESCE(rewritten_path, ''),
	COALESCE(result_path, ''), updated_post_at`

func scanArticle(row pgx.Row) (Article, error) {
	var article Article
	err := row.Scan(&article.ID, &article.ExternalID, &article.SourceURL, &article.Slug,
		&article.PostID, &article.OldTitle, &article.NewTitle, &article.Status,
		&article.ErrorMessage, &article.OriginalPath, &article.PromptPath,
		&article.RewrittenPath, &article.ResultPath, &article.UpdatedPostAt)
	return article, err
}

// ErrArticleNotFound — статьи с таким индексом в задаче нет.
var ErrArticleNotFound = errors.New("статья не найдена")

// Get возвращает одну статью по индексу из входного файла.
func (r *Repository) Get(ctx context.Context, externalID string) (Article, error) {
	article, err := scanArticle(r.pool.QueryRow(ctx,
		`SELECT `+articleColumns+` FROM articles WHERE external_id = $1`, externalID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Article{}, fmt.Errorf("%w: %s", ErrArticleNotFound, externalID)
	}
	if err != nil {
		return Article{}, fmt.Errorf("прочитать статью %s: %w", externalID, err)
	}
	return article, nil
}

// ListPending возвращает статьи, которые прогон ещё не переписал, в порядке индекса.
func (r *Repository) ListPending(ctx context.Context) ([]Article, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+articleColumns+` FROM articles WHERE updated_post_at IS NULL
		 ORDER BY NULLIF(regexp_replace(external_id, '\D', '', 'g'), '')::BIGINT NULLS LAST, external_id`)
	if err != nil {
		return nil, fmt.Errorf("выбрать статьи задачи: %w", err)
	}
	defer rows.Close()
	var articles []Article
	for rows.Next() {
		article, scanErr := scanArticle(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("прочитать статью задачи: %w", scanErr)
		}
		articles = append(articles, article)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("выбрать статьи задачи: %w", err)
	}
	return articles, nil
}

// MarkProcessing помечает статью выполняющейся и снимает прошлую ошибку.
func (r *Repository) MarkProcessing(ctx context.Context, externalID string) error {
	return r.exec(ctx, externalID, `UPDATE articles
		SET status = $2, error_message = NULL, updated_at = NOW() WHERE external_id = $1`,
		externalID, StatusProcessing)
}

// SaveFetched сохраняет то, что прочитано из блога, и заголовок, полученный правилом.
func (r *Repository) SaveFetched(ctx context.Context, externalID string, postID int64,
	oldTitle, newTitle, originalPath string) error {
	return r.exec(ctx, externalID, `UPDATE articles
		SET post_id = $2, old_title = $3, new_title = $4, original_path = $5, updated_at = NOW()
		WHERE external_id = $1`,
		externalID, postID, oldTitle, newTitle, originalPath)
}

// SaveRewritten сохраняет пути промпта и исправленного текста.
func (r *Repository) SaveRewritten(ctx context.Context, externalID, promptPath, rewrittenPath string) error {
	return r.exec(ctx, externalID, `UPDATE articles
		SET prompt_path = $2, rewritten_path = $3, updated_at = NOW() WHERE external_id = $1`,
		externalID, promptPath, rewrittenPath)
}

// MarkUpdated отмечает статью переписанной в блоге. Эта отметка и защищает от повтора.
func (r *Repository) MarkUpdated(ctx context.Context, externalID, resultPath string) error {
	return r.exec(ctx, externalID, `UPDATE articles
		SET status = $2, error_message = NULL, result_path = $3,
			updated_post_at = NOW(), updated_at = NOW()
		WHERE external_id = $1`,
		externalID, StatusCompleted, resultPath)
}

// MarkFailed сохраняет блокирующую ошибку статьи.
func (r *Repository) MarkFailed(ctx context.Context, externalID string, cause error) error {
	return r.exec(ctx, externalID, `UPDATE articles
		SET status = $2, error_message = $3, updated_at = NOW() WHERE external_id = $1`,
		externalID, StatusFailed, cause.Error())
}

// Count возвращает число статей задачи. Нужен reset: масштаб удаления человек должен
// увидеть до подтверждения, а не после.
func (r *Repository) Count(ctx context.Context) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM articles`).Scan(&count); err != nil {
		return 0, fmt.Errorf("посчитать статьи задачи: %w", err)
	}
	return count, nil
}

// Reset очищает таблицу задачи и обнуляет счётчик идентификаторов.
//
// TRUNCATE с RESTART IDENTITY, а не DELETE: после сброса задача начинается с нуля, и
// внутренние идентификаторы должны начинаться с единицы — иначе номера в логах продолжают
// прошлую жизнь и сравнивать прогоны становится не с чем. Блог этим не затрагивается:
// правки уже опубликованы, и вернуть их приложение не умеет.
func (r *Repository) Reset(ctx context.Context) error {
	if _, err := r.pool.Exec(ctx, `TRUNCATE TABLE articles RESTART IDENTITY`); err != nil {
		return fmt.Errorf("очистить таблицу задачи: %w", err)
	}
	return nil
}

func (r *Repository) exec(ctx context.Context, externalID, query string, args ...any) error {
	tag, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("обновить статью %s: %w", externalID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", ErrArticleNotFound, externalID)
	}
	return nil
}
