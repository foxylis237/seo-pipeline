package repository

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type schemaColumn struct {
	table    string
	name     string
	typeName string
	nullable bool
}

var expectedSchema = []schemaColumn{
	{"articles", "id", "bigint", false},
	{"articles", "external_id", "text", false},
	{"articles", "title", "text", false},
	{"articles", "status", "text", false},
	{"articles", "current_step", "text", true},
	{"articles", "error_message", "text", true},
	{"articles", "created_at", "timestamp with time zone", false},
	{"articles", "updated_at", "timestamp with time zone", false},
	// Отметка о публикации в WordPress, миграция 000004. Стоит на articles, а не на
	// article_outputs, потому что строку article_outputs удаляет не только clear/reset/
	// regenerate, но и штатный prepare через SaveResearch — а отметка, которую стирает
	// обычный повторный prepare, от дублей не защищает.
	//
	// wordpress_status NOT NULL с умолчанием 'not_published': NULL как состояние не
	// используется. post_id и url остаются NULL у статьи, отмеченной вручную.
	{"articles", "wordpress_status", "text", false},
	{"articles", "wordpress_post_id", "bigint", true},
	{"articles", "wordpress_url", "text", true},
	{"article_inputs", "article_id", "bigint", false},
	{"article_inputs", "image_slug", "text", false},
	{"article_inputs", "reference_url", "text", false},
	{"article_inputs", "category", "text", true},
	{"article_inputs", "header", "text", true},
	{"article_inputs", "meta_description", "text", true},
	{"article_inputs", "key_word", "text", true},
	// author, links, professions и tags здесь нет намеренно: они есть не у каждой задачи и
	// приходят из её профиля списком необязательных колонок — см. ValidateSchema.
	{"article_research", "article_id", "bigint", false},
	{"article_research", "competitor_structure", "text", true},
	{"article_research", "cleaned_keywords", "jsonb", false},
	{"article_research", "wordstat_keywords", "jsonb", false},
	{"article_research", "lsi_words", "jsonb", false},
	{"article_research", "updated_at", "timestamp with time zone", false},
	{"article_metadata", "article_id", "bigint", false},
	{"article_metadata", "metadata_text", "text", true},
	{"article_metadata", "faq", "text", true},
	{"article_metadata", "updated_at", "timestamp with time zone", false},
	{"article_outputs", "article_id", "bigint", false},
	{"article_outputs", "structure_path", "text", true},
	{"article_outputs", "article_path", "text", true},
	{"article_outputs", "review_path", "text", true},
	{"article_outputs", "fixed_article_path", "text", true},
	{"article_outputs", "html_path", "text", true},
	// google_doc_url добавлен миграцией 000002. NULL означает «промпт ещё не публиковался».
	{"article_outputs", "google_doc_url", "text", true},
	{"article_outputs", "updated_at", "timestamp with time zone", false},
	{"article_errors", "id", "bigint", false},
	{"article_errors", "article_id", "bigint", false},
	{"article_errors", "external_id", "text", false},
	{"article_errors", "step", "text", true},
	{"article_errors", "operation", "text", true},
	{"article_errors", "error_message", "text", false},
	{"article_errors", "retryable", "boolean", false},
	{"article_errors", "created_at", "timestamp with time zone", false},
}

// SchemaProfile — чем схема задачи отличается от обязательного набора колонок.
//
// Обе стороны важны одинаково: у задачи, объявившей колонку, её отсутствие — ошибка, а у
// задачи, которая не объявляла, лишняя колонка означает недоприменённую или чужую миграцию.
// Без этого pprof_2 падал бы на «unexpected column», а pprof_1 молча принимал бы в свою
// схему чужие колонки.
type SchemaProfile struct {
	// ExtraInputColumns — необязательные колонки article_inputs, которые у задачи есть.
	ExtraInputColumns []string
	// WithoutTLDR — в article_metadata задачи нет колонки tldr: TL;DR она не генерирует.
	WithoutTLDR bool
}

// ValidateSchema проверяет фактические колонки, PK и каскадные внешние ключи до запуска интеграций.
func ValidateSchema(ctx context.Context, pool *pgxpool.Pool, profile SchemaProfile) error {
	if err := ValidateExtraInputColumns(profile.ExtraInputColumns); err != nil {
		return err
	}
	expectedColumns := make([]schemaColumn, 0, len(expectedSchema)+len(profile.ExtraInputColumns)+1)
	expectedColumns = append(expectedColumns, expectedSchema...)
	for _, name := range profile.ExtraInputColumns {
		column := extraInputColumns[name]
		expectedColumns = append(expectedColumns,
			schemaColumn{"article_inputs", name, column.typeName, column.nullable})
	}
	if !profile.WithoutTLDR {
		expectedColumns = append(expectedColumns, schemaColumn{"article_metadata", "tldr", "text", true})
	}
	return validateSchema(ctx, pool, expectedColumns)
}

func validateSchema(ctx context.Context, pool *pgxpool.Pool, expectedSchema []schemaColumn) error {
	rows, err := pool.Query(ctx, `
		SELECT table_name, column_name, data_type, is_nullable = 'YES'
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = ANY($1)
	`, []string{"articles", "article_inputs", "article_research", "article_metadata", "article_outputs", "article_errors"})
	if err != nil {
		return fmt.Errorf("read database schema: %w", err)
	}
	defer rows.Close()

	actual := make(map[string]schemaColumn)
	for rows.Next() {
		var column schemaColumn
		if err := rows.Scan(&column.table, &column.name, &column.typeName, &column.nullable); err != nil {
			return fmt.Errorf("scan database schema: %w", err)
		}
		actual[column.table+"."+column.name] = column
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read database schema: %w", err)
	}

	var mismatches []string
	expected := make(map[string]schemaColumn, len(expectedSchema))
	for _, column := range expectedSchema {
		key := column.table + "." + column.name
		expected[key] = column
		got, found := actual[key]
		if !found {
			mismatches = append(mismatches, "missing column "+key)
			continue
		}
		if got.typeName != column.typeName || got.nullable != column.nullable {
			mismatches = append(mismatches, fmt.Sprintf("column %s is %s nullable=%t, want %s nullable=%t", key, got.typeName, got.nullable, column.typeName, column.nullable))
		}
	}
	for key := range actual {
		if _, found := expected[key]; !found {
			mismatches = append(mismatches, "unexpected column "+key)
		}
	}

	for _, table := range []string{"article_inputs", "article_research", "article_metadata", "article_outputs", "article_errors"} {
		var valid bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_constraint c
				JOIN pg_class child ON child.oid = c.conrelid
				JOIN pg_class parent ON parent.oid = c.confrelid
				JOIN pg_namespace n ON n.oid = child.relnamespace
				WHERE n.nspname = current_schema()
				  AND child.relname = $1
				  AND parent.relname = 'articles'
				  AND c.contype = 'f'
				  AND c.confdeltype = 'c'
			)
		`, table).Scan(&valid)
		if err != nil {
			return fmt.Errorf("check foreign key for %s: %w", table, err)
		}
		if !valid {
			mismatches = append(mismatches, table+" has no ON DELETE CASCADE foreign key to articles")
		}
	}

	if len(mismatches) > 0 {
		sort.Strings(mismatches)
		// Каталог миграций у задач разный: у одних общий migrations/, у других свой
		// migrations/<схема>/ вместо него. Называть в сообщении один путь значило бы
		// отправлять половину задач применять чужие миграции.
		return fmt.Errorf("database schema is inconsistent with code: %s; apply the migrations of this task's schema (see migrations/README.md)", strings.Join(mismatches, "; "))
	}
	return nil
}
