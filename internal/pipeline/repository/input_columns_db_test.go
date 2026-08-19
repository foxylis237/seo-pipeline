package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pprof2Columns — набор колонок, который объявляет профиль pprof_2. Дублируется здесь
// намеренно: пакет репозитория о задачах не знает и импортировать их не должен.
//
// links, professions и tags сюда не входят: у pprof_2 их нет, и его собственная baseline их
// не заводит. blogColumns — то же самое для задач, которые пишут статьи блога.
var pprof2Columns = []string{"seo_title", "section", "profession", "teachers", "service_name"}

var blogColumns = []string{"author", "links", "professions", "tags"}

// newTaskTestRepository собирает схему задачи: либо общими миграциями, либо собственной
// baseline каталога taskMigrations.
//
// Именно «либо», а не «плюс»: задача со своей baseline описывает схему целиком, и общие
// миграции поверх неё завели бы колонки, которых у задачи быть не должно. Отдельный
// помощник, а не параметр к общему: общий собирает схему статей блога и обязан продолжать
// это делать — так живут task_1 и pprof_1.
func newTaskTestRepository(t *testing.T, taskMigrations string, profile SchemaProfile) (*ArticleRepository, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)

	schema := fmt.Sprintf("task_columns_test_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE") })

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	// Каталог задачи — единственный источник её схемы. По умолчанию это task_1: его набор
	// колонок совпадает с pprof_1, и на нём проверяется путь задачи, пишущей статьи блога.
	root := filepath.Join("..", "..", "..", "migrations")
	if taskMigrations == "" {
		taskMigrations = "task_1"
	}
	patterns := []string{filepath.Join(root, taskMigrations, "*.up.sql")}
	for _, pattern := range patterns {
		files, globErr := filepath.Glob(pattern)
		if globErr != nil {
			t.Fatal(globErr)
		}
		if len(files) == 0 {
			t.Fatalf("не найдено миграций по образцу %s", pattern)
		}
		sort.Strings(files)
		for _, name := range files {
			migration, readErr := os.ReadFile(name)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if _, execErr := pool.Exec(ctx, string(migration)); execErr != nil {
				t.Fatalf("применить миграцию %s: %v", filepath.Base(name), execErr)
			}
		}
	}

	// Репозиторий настраивается ровно так же, как в composition root: набор колонок и
	// отсутствие tldr. Помощник, который об этом забывает, оставляет тест проверять не ту
	// конфигурацию, что работает в бою, — и запрос падает на «column does not exist».
	repository := NewArticleRepository(pool)
	if err := repository.UseExtraInputColumns(profile.ExtraInputColumns); err != nil {
		t.Fatal(err)
	}
	repository.UseMetadataWithoutTLDR(profile.WithoutTLDR)
	return repository, pool
}

// Схема со своими колонками проходит проверку только вместе со списком из профиля, а без
// него — нет. Обе половины важны: первая пускает pprof_2 работать, вторая ловит чужую или
// недоприменённую миграцию в схеме задачи, которая своих колонок не объявляла.
func TestValidateSchemaAcceptsOwnColumnsOnlyWithProfileList(t *testing.T) {
	profile := SchemaProfile{ExtraInputColumns: pprof2Columns, WithoutTLDR: true}
	_, pool := newTaskTestRepository(t, "pprof_2", profile)
	ctx := context.Background()
	if err := ValidateSchema(ctx, pool, profile); err != nil {
		t.Fatalf("схема со своими колонками отклонена: %v", err)
	}
	err := ValidateSchema(ctx, pool, SchemaProfile{})
	if err == nil {
		t.Fatal("колонки, которых нет в профиле, приняты молча")
	}
	for _, column := range pprof2Columns {
		if !strings.Contains(err.Error(), "unexpected column article_inputs."+column) {
			t.Fatalf("проверка не назвала лишнюю колонку %q: %v", column, err)
		}
	}
}

// Обратная половина: задача объявила колонки, а миграция в схему не накатана.
func TestValidateSchemaReportsMissingOwnColumns(t *testing.T) {
	_, pool := newTaskTestRepository(t, "", SchemaProfile{ExtraInputColumns: blogColumns})
	err := ValidateSchema(context.Background(), pool, SchemaProfile{ExtraInputColumns: pprof2Columns})
	if err == nil {
		t.Fatal("отсутствие объявленных колонок прошло молча")
	}
	if !strings.Contains(err.Error(), "missing column article_inputs.seo_title") {
		t.Fatalf("проверка не назвала отсутствующую колонку: %v", err)
	}
}

// Импорт кладёт свои поля в свои колонки, а сборка result.md читает их оттуда же. Это
// единственный тест, который проходит весь путь значения: Excel → INSERT → SELECT → result.
func TestImportAndResultRoundTripOwnColumns(t *testing.T) {
	repository, _ := newTaskTestRepository(t, "pprof_2", SchemaProfile{ExtraInputColumns: pprof2Columns, WithoutTLDR: true})
	ctx := context.Background()
	input := article.Input{
		ExcelID: 1, Title: "Обучение на стропальщика",
		ImageSlug: "obuchenie-na-stropalshchika", ReferenceURL: "https://example.test/a",
		Category: "Рабочие профессии", Header: "Обучение на стропальщика",
		MetaDescription: "Дистанционное обучение", Keyword: "обучение на стропальщика",
		SEOTitle: "Обучение на стропальщика — курсы", Section: "Профессиональное обучение",
		Profession: "Стропальщик", Teachers: "Иванов И. И.",
	}
	if _, imported, err := repository.Import(ctx, input); err != nil || !imported {
		t.Fatalf("импорт: imported=%t err=%v", imported, err)
	}
	result, err := repository.GetResultInput(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range map[string][2]string{
		"seo_title":  {result.SEOTitle, input.SEOTitle},
		"section":    {result.Section, input.Section},
		"profession": {result.Profession, input.Profession},
		"teachers":   {result.Teachers, input.Teachers},
	} {
		if pair[0] != pair[1] {
			t.Fatalf("колонка %s: прочитано %q, записано %q", name, pair[0], pair[1])
		}
	}
	// Базовые поля обязаны остаться на своих местах: сдвиг плейсхолдера на единицу разложил
	// бы значения по соседним колонкам, и заметить это можно только здесь.
	if result.Category != input.Category || result.Header != input.Header ||
		result.Keyword != input.Keyword || result.MetaDescription != input.MetaDescription {
		t.Fatalf("базовые колонки разъехались: %+v", result)
	}
	if result.Article.Slug != input.ImageSlug {
		t.Fatalf("image_slug = %q, ожидался %q", result.Article.Slug, input.ImageSlug)
	}
}

// Create повторяет тот же список колонок, что и Import: два пути записи одной строки не
// должны расходиться.
func TestCreateWritesOwnColumns(t *testing.T) {
	repository, pool := newTaskTestRepository(t, "pprof_2", SchemaProfile{ExtraInputColumns: pprof2Columns, WithoutTLDR: true})
	ctx := context.Background()
	if _, err := repository.Create(ctx, article.Input{
		ExcelID: 2, Title: "Профессия монтажник", ImageSlug: "professiya-montazhnik",
		ReferenceURL: "https://example.test/b", SEOTitle: "сео", Section: "раздел",
		Profession: "Монтажник", Teachers: "Сидоров С. С.",
	}); err != nil {
		t.Fatal(err)
	}
	var seoTitle, section, profession, teachers string
	err := pool.QueryRow(ctx, `
		SELECT COALESCE(i.seo_title, ''), COALESCE(i.section, ''),
			COALESCE(i.profession, ''), COALESCE(i.teachers, '')
		FROM articles a JOIN article_inputs i ON i.article_id = a.id
		WHERE a.external_id = '2'
	`).Scan(&seoTitle, &section, &profession, &teachers)
	if err != nil {
		t.Fatal(err)
	}
	if seoTitle != "сео" || section != "раздел" || profession != "Монтажник" || teachers != "Сидоров С. С." {
		t.Fatalf("Create записал %q/%q/%q/%q", seoTitle, section, profession, teachers)
	}
}
