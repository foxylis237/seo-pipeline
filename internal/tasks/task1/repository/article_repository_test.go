package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestArticleRepositoryIdempotency(t *testing.T) {
	repository, pool := newTestRepository(t)
	ctx := context.Background()

	initial := article.Input{
		ExcelID: 11, Title: "Исходное название", Header: "Исходный заголовок",
		ImageSlug: "old-slug", MetaDescription: "Старое описание", Keyword: "старый ключ",
		ReferenceURL: "https://example.com/old", Category: "Старая категория",
		Author: "Старый автор", Links: "Старые ссылки", Professions: "Старые профессии",
	}
	first, err := repository.Create(ctx, initial)
	if err != nil {
		t.Fatal(err)
	}
	assertCount(t, pool, "articles", "external_id = '11'", 1)

	if err := repository.SaveCleanedKeywords(ctx, first.ID, []string{"старый запрос"}); err != nil {
		t.Fatal(err)
	}
	assertCount(t, pool, "article_research", fmt.Sprintf("article_id = %d", first.ID), 1)

	updated := article.Input{
		ExcelID: 11, Title: "Новое название", Header: "Новый заголовок",
		ImageSlug: "new-slug", MetaDescription: "Новое описание", Keyword: "новый ключ",
		ReferenceURL: "https://example.com/new", Category: "Новая категория",
		Author: "Новый автор", Links: "Новые ссылки", Professions: "Новые профессии",
	}
	second, err := repository.Create(ctx, updated)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("articles.id изменился после повторного импорта: %d -> %d", first.ID, second.ID)
	}
	assertCount(t, pool, "articles", "external_id = '11'", 1)
	assertImportedFields(t, pool, second.ID, updated)
	assertCleanedKeywords(t, pool, second.ID, []string{"старый запрос"})
	if _, err := pool.Exec(ctx, `
		UPDATE article_inputs
		SET seo_title = 'не использовать SEO', profession_name = 'не использовать профессию',
			image_name = 'не использовать имя', image_url = 'не использовать URL'
		WHERE article_id = $1
	`, second.ID); err != nil {
		t.Fatal(err)
	}
	resultInput, err := repository.GetResultInput(ctx, "11")
	if err != nil {
		t.Fatal(err)
	}
	if resultInput.SEOTitle != updated.Title || resultInput.ProfessionName != updated.Keyword ||
		resultInput.ImageName != updated.Header || resultInput.ImageURL != updated.ImageSlug {
		t.Fatalf("result mapping = SEO %q, profession %q, image %q, URL %q", resultInput.SEOTitle, resultInput.ProfessionName, resultInput.ImageName, resultInput.ImageURL)
	}

	if err := repository.SaveCleanedKeywords(ctx, first.ID, []string{"новый запрос"}); err != nil {
		t.Fatal(err)
	}
	assertCount(t, pool, "article_research", fmt.Sprintf("article_id = %d", first.ID), 1)
	assertCleanedKeywords(t, pool, first.ID, []string{"новый запрос"})

	arsenkinArticle, err := repository.Create(ctx, article.Input{ExcelID: 12, Title: "Arsenkin", ImageSlug: "arsenkin"})
	if err != nil {
		t.Fatal(err)
	}
	firstWordstat := []article.KeywordFrequency{{Query: "первый", Frequency: 100}}
	if err := repository.SaveArsenkinResearch(ctx, arsenkinArticle.ID, firstWordstat, []string{"слово"}, "H1 Первый"); err != nil {
		t.Fatal(err)
	}
	assertCount(t, pool, "article_research", fmt.Sprintf("article_id = %d", arsenkinArticle.ID), 1)

	secondWordstat := []article.KeywordFrequency{{Query: "второй", Frequency: 200}}
	if err := repository.SaveArsenkinResearch(ctx, arsenkinArticle.ID, secondWordstat, []string{"новое слово"}, "H1 Второй"); err != nil {
		t.Fatal(err)
	}
	assertCount(t, pool, "article_research", fmt.Sprintf("article_id = %d", arsenkinArticle.ID), 1)
	assertArsenkinResult(t, pool, arsenkinArticle.ID, secondWordstat, []string{"новое слово"}, "H1 Второй")
	generationInput, err := repository.GetGenerationInput(ctx, "12")
	if err != nil {
		t.Fatal(err)
	}
	if generationInput.Article.ID != arsenkinArticle.ID || generationInput.Article.Slug != "arsenkin" || generationInput.CompetitorStructure != "H1 Второй" {
		t.Fatalf("generation input = %+v", generationInput)
	}

	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION reject_article_update() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'forced article update failure';
		END
		$$;
		CREATE TRIGGER reject_article_update
		BEFORE UPDATE ON articles
		FOR EACH ROW EXECUTE FUNCTION reject_article_update();
	`); err != nil {
		t.Fatal(err)
	}
	err = repository.SaveArsenkinResearch(
		ctx,
		arsenkinArticle.ID,
		[]article.KeywordFrequency{{Query: "не должен сохраниться", Frequency: 300}},
		[]string{"не должно сохраниться"},
		"H1 Не должен сохраниться",
	)
	if err == nil {
		t.Fatal("ожидалась принудительная ошибка обновления articles")
	}
	if _, dropErr := pool.Exec(ctx, `DROP TRIGGER reject_article_update ON articles; DROP FUNCTION reject_article_update()`); dropErr != nil {
		t.Fatal(dropErr)
	}
	assertArsenkinResult(t, pool, arsenkinArticle.ID, secondWordstat, []string{"новое слово"}, "H1 Второй")

	articles, err := repository.GetAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(articles) != 2 || articles[0].ID != first.ID || articles[1].ID != arsenkinArticle.ID {
		t.Fatalf("GetAll() вернул статьи не в порядке ID: %+v", articles)
	}
	if articles[0].ExternalID != "11" || articles[0].Slug != "new-slug" {
		t.Fatalf("GetAll() не вернул Excel ID и slug: %+v", articles[0])
	}
	if err := repository.MarkArticlePromptBuilt(ctx, arsenkinArticle.ID); err != nil {
		t.Fatal(err)
	}
	var currentStep string
	if err := pool.QueryRow(ctx, `SELECT current_step FROM articles WHERE id = $1`, arsenkinArticle.ID).Scan(&currentStep); err != nil {
		t.Fatal(err)
	}
	if currentStep != "article_generation" {
		t.Fatalf("current_step = %q, want article_generation", currentStep)
	}
	const articlePath = "12-arsenkin/generated/article.txt"
	const structurePath = "12-arsenkin/generated/structure.txt"
	if err := repository.SaveStructurePath(ctx, arsenkinArticle.ID, structurePath); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveGenerationPaths(ctx, arsenkinArticle.ID, structurePath, articlePath); err != nil {
		t.Fatal(err)
	}
	var savedStructurePath, savedArticlePath string
	if err := pool.QueryRow(ctx, `SELECT structure_path, article_path FROM article_outputs WHERE article_id = $1`, arsenkinArticle.ID).Scan(&savedStructurePath, &savedArticlePath); err != nil {
		t.Fatal(err)
	}
	if savedStructurePath != structurePath || savedArticlePath != articlePath {
		t.Fatalf("saved paths = %q, %q", savedStructurePath, savedArticlePath)
	}
	if err := pool.QueryRow(ctx, `SELECT current_step FROM articles WHERE id = $1`, arsenkinArticle.ID).Scan(&currentStep); err != nil {
		t.Fatal(err)
	}
	if currentStep != "article_review" {
		t.Fatalf("current_step = %q, want article_review", currentStep)
	}
	if err := repository.BeginGenerationStage(ctx, arsenkinArticle.ID, "info"); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT current_step FROM articles WHERE id = $1`, arsenkinArticle.ID).Scan(&currentStep); err != nil {
		t.Fatal(err)
	}
	if currentStep != "metadata_generation" {
		t.Fatalf("current_step before info = %q, want metadata_generation", currentStep)
	}
	const articleInfo = "Метки: Логопед, Переподготовка, Как стать\nTLDR:\nИтог.\nFAQ:\nВопрос: Как?\nОтвет: Так."
	parsedInfo := article.ArticleInfo{Tags: "Логопед, Переподготовка, Как стать", TLDR: "Итог.", FAQ: "Вопрос: Как?\nОтвет: Так."}
	if err := repository.SaveArticleInfo(ctx, arsenkinArticle.ID, articleInfo, parsedInfo); err != nil {
		t.Fatal(err)
	}
	var savedArticleInfo, savedTags, savedTLDR, savedFAQ string
	var infoError *string
	if err := pool.QueryRow(ctx, `
		SELECT m.metadata_text, m.tags, m.tldr, m.faq, a.current_step, a.error_message
		FROM article_metadata AS m
		JOIN articles AS a ON a.id = m.article_id
		WHERE m.article_id = $1
	`, arsenkinArticle.ID).Scan(&savedArticleInfo, &savedTags, &savedTLDR, &savedFAQ, &currentStep, &infoError); err != nil {
		t.Fatal(err)
	}
	if savedArticleInfo != articleInfo || savedTags != parsedInfo.Tags || savedTLDR != parsedInfo.TLDR || savedFAQ != parsedInfo.FAQ || currentStep != "article_review" || infoError != nil {
		t.Fatalf("article info state = %q, %q, %v", savedArticleInfo, currentStep, infoError)
	}
	const reviewPath = "12-arsenkin/generated/review.txt"
	if err := repository.SaveReviewPath(ctx, arsenkinArticle.ID, reviewPath); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT current_step FROM articles WHERE id = $1`, arsenkinArticle.ID).Scan(&currentStep); err != nil {
		t.Fatal(err)
	}
	if currentStep != "article_review" {
		t.Fatalf("current_step after review = %q, want article_review until fix succeeds", currentStep)
	}
	const fixedArticlePath = "12-arsenkin/generated/fixed_article.txt"
	if err := repository.SaveFixedArticlePath(ctx, arsenkinArticle.ID, fixedArticlePath); err != nil {
		t.Fatal(err)
	}
	var savedReviewPath, savedFixedArticlePath string
	if err := pool.QueryRow(ctx, `SELECT metadata_path, final_path FROM article_outputs WHERE article_id = $1`, arsenkinArticle.ID).Scan(&savedReviewPath, &savedFixedArticlePath); err != nil {
		t.Fatal(err)
	}
	if savedReviewPath != reviewPath || savedFixedArticlePath != fixedArticlePath {
		t.Fatalf("review/fixed paths = %q, %q", savedReviewPath, savedFixedArticlePath)
	}
	if err := pool.QueryRow(ctx, `SELECT current_step FROM articles WHERE id = $1`, arsenkinArticle.ID).Scan(&currentStep); err != nil {
		t.Fatal(err)
	}
	if currentStep != "html_generation" {
		t.Fatalf("current_step after fix = %q, want html_generation", currentStep)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE articles
		SET status = 'completed', current_step = NULL, error_message = 'old error'
		WHERE id = $1
	`, arsenkinArticle.ID); err != nil {
		t.Fatal(err)
	}
	if err := repository.BeginGeneration(ctx, arsenkinArticle.ID); err != nil {
		t.Fatal(err)
	}
	var generationStatus string
	var generationError *string
	if err := pool.QueryRow(ctx, `
		SELECT status, current_step, error_message
		FROM articles WHERE id = $1
	`, arsenkinArticle.ID).Scan(&generationStatus, &currentStep, &generationError); err != nil {
		t.Fatal(err)
	}
	if generationStatus != "processing" || currentStep != "structure_generation" || generationError != nil {
		t.Fatalf("generation start state = status %q, step %q, error %v", generationStatus, currentStep, generationError)
	}
	if err := repository.SaveStructurePath(ctx, arsenkinArticle.ID, structurePath); err != nil {
		t.Fatal(err)
	}
	var clearedArticlePath *string
	if err := pool.QueryRow(ctx, `SELECT article_path FROM article_outputs WHERE article_id = $1`, arsenkinArticle.ID).Scan(&clearedArticlePath); err != nil {
		t.Fatal(err)
	}
	if clearedArticlePath != nil {
		t.Fatalf("stale article_path was not cleared: %q", *clearedArticlePath)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO article_metadata (article_id, metadata_text) VALUES ($1, 'old')`, arsenkinArticle.ID); err != nil {
		t.Fatal(err)
	}
	if err := repository.ResetArticleForRun(ctx, arsenkinArticle.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"article_research", "article_metadata", "article_outputs"} {
		assertCount(t, pool, table, fmt.Sprintf("article_id = %d", arsenkinArticle.ID), 0)
	}
	var status string
	var errorMessage *string
	if err := pool.QueryRow(ctx, `SELECT status, current_step, error_message FROM articles WHERE id = $1`, arsenkinArticle.ID).Scan(&status, &currentStep, &errorMessage); err != nil {
		t.Fatal(err)
	}
	if status != "processing" || currentStep != "arsenkin_collection" || errorMessage != nil {
		t.Fatalf("reset state = status %q, step %q, error %v", status, currentStep, errorMessage)
	}
	assertCount(t, pool, "article_inputs", fmt.Sprintf("article_id = %d", arsenkinArticle.ID), 1)
	missingResearch, err := repository.Create(ctx, article.Input{ExcelID: 13, Title: "Без исследования", ImageSlug: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetGenerationInput(ctx, missingResearch.ExternalID); err == nil || !strings.Contains(err.Error(), "отсутствует competitor_structure") {
		t.Fatalf("missing research error = %v", err)
	}
	if _, err := repository.GetGenerationInput(ctx, "404"); err == nil || !strings.Contains(err.Error(), "не найдена") {
		t.Fatalf("missing article error = %v", err)
	}
}

func newTestRepository(t *testing.T) (*ArticleRepository, *pgxpool.Pool) {
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

	schema := fmt.Sprintf("repository_test_%d", time.Now().UnixNano())
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

	for _, name := range []string{
		"000001_create_articles.up.sql",
		"000002_add_articles_external_id.up.sql",
		"000003_add_wordstat_keywords.up.sql",
		"000004_add_article_research_updated_at.up.sql",
		"000005_add_article_review_stage.up.sql",
	} {
		migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("применить миграцию %s: %v", name, err)
		}
	}
	if err := ValidateSchema(ctx, pool); err != nil {
		t.Fatalf("проверить схему после миграций: %v", err)
	}
	return NewArticleRepository(pool), pool
}

func assertCount(t *testing.T, pool *pgxpool.Pool, table, condition string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM "+table+" WHERE "+condition).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}

func assertImportedFields(t *testing.T, pool *pgxpool.Pool, articleID int64, want article.Input) {
	t.Helper()
	var got article.Input
	err := pool.QueryRow(context.Background(), `
		SELECT a.title, i.header, i.image_slug, i.meta_description, i.key_word,
			i.reference_url, i.category, i.author, i.links, i.professions
		FROM articles a JOIN article_inputs i ON i.article_id = a.id
		WHERE a.id = $1
	`, articleID).Scan(&got.Title, &got.Header, &got.ImageSlug, &got.MetaDescription, &got.Keyword,
		&got.ReferenceURL, &got.Category, &got.Author, &got.Links, &got.Professions)
	if err != nil {
		t.Fatal(err)
	}
	got.ExcelID = want.ExcelID
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("импортированные поля = %+v, want %+v", got, want)
	}
}

func assertCleanedKeywords(t *testing.T, pool *pgxpool.Pool, articleID int64, want []string) {
	t.Helper()
	var encoded []byte
	if err := pool.QueryRow(context.Background(), `SELECT cleaned_keywords FROM article_research WHERE article_id = $1`, articleID).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cleaned_keywords = %#v, want %#v", got, want)
	}
}

func assertArsenkinResult(t *testing.T, pool *pgxpool.Pool, articleID int64, wantWordstat []article.KeywordFrequency, wantLSI []string, wantStructure string) {
	t.Helper()
	var wordstatJSON, lsiJSON []byte
	var structure, currentStep string
	err := pool.QueryRow(context.Background(), `
		SELECT r.wordstat_keywords, r.lsi_words, r.competitor_structure, a.current_step
		FROM article_research r JOIN articles a ON a.id = r.article_id
		WHERE r.article_id = $1
	`, articleID).Scan(&wordstatJSON, &lsiJSON, &structure, &currentStep)
	if err != nil {
		t.Fatal(err)
	}
	var gotWordstat []article.KeywordFrequency
	var gotLSI []string
	if err := json.Unmarshal(wordstatJSON, &gotWordstat); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lsiJSON, &gotLSI); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotWordstat, wantWordstat) || !reflect.DeepEqual(gotLSI, wantLSI) || structure != wantStructure {
		t.Fatalf("результат Arsenkin не обновлён: wordstat=%#v lsi=%#v structure=%q", gotWordstat, gotLSI, structure)
	}
	if currentStep != "structure_generation" {
		t.Fatalf("current_step = %q, want structure_generation", currentStep)
	}
}
