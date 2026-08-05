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
	{"article_inputs", "article_id", "bigint", false},
	{"article_inputs", "image_slug", "text", false},
	{"article_inputs", "reference_url", "text", false},
	{"article_inputs", "category", "text", true},
	{"article_inputs", "header", "text", true},
	{"article_inputs", "meta_description", "text", true},
	{"article_inputs", "key_word", "text", true},
	{"article_inputs", "author", "text", true},
	{"article_inputs", "links", "text", true},
	{"article_inputs", "professions", "text", true},
	{"article_research", "article_id", "bigint", false},
	{"article_research", "competitor_structure", "text", true},
	{"article_research", "cleaned_keywords", "jsonb", false},
	{"article_research", "wordstat_keywords", "jsonb", false},
	{"article_research", "lsi_words", "jsonb", false},
	{"article_research", "updated_at", "timestamp with time zone", false},
	{"article_metadata", "article_id", "bigint", false},
	{"article_metadata", "metadata_text", "text", true},
	{"article_metadata", "tags", "text", true},
	{"article_metadata", "tldr", "text", true},
	{"article_metadata", "faq", "text", true},
	{"article_metadata", "updated_at", "timestamp with time zone", false},
	{"article_outputs", "article_id", "bigint", false},
	{"article_outputs", "structure_path", "text", true},
	{"article_outputs", "article_path", "text", true},
	{"article_outputs", "review_path", "text", true},
	{"article_outputs", "fixed_article_path", "text", true},
	{"article_outputs", "html_path", "text", true},
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

// ValidateSchema проверяет фактические колонки, PK и каскадные внешние ключи до запуска интеграций.
func ValidateSchema(ctx context.Context, pool *pgxpool.Pool) error {
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
		return fmt.Errorf("database schema is inconsistent with code: %s; apply all migrations from migrations/*.up.sql", strings.Join(mismatches, "; "))
	}
	return nil
}
