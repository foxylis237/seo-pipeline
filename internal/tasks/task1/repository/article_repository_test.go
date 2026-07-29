package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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
	if err := repository.SavePreparedResearch(ctx, arsenkinArticle.ID, []string{"первый запрос"}, firstWordstat, []string{"слово"}, "H1 Первый"); err != nil {
		t.Fatal(err)
	}
	assertCount(t, pool, "article_research", fmt.Sprintf("article_id = %d", arsenkinArticle.ID), 1)

	secondWordstat := []article.KeywordFrequency{{Query: "второй", Frequency: 200}}
	if err := repository.SavePreparedResearch(ctx, arsenkinArticle.ID, []string{"второй запрос"}, secondWordstat, []string{"новое слово"}, "H1 Второй"); err != nil {
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
	err = repository.SavePreparedResearch(
		ctx,
		arsenkinArticle.ID,
		[]string{"не должен сохраниться"},
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
	if _, err := pool.Exec(ctx, `
		INSERT INTO article_metadata (article_id, metadata_text) VALUES ($1, 'old')
		ON CONFLICT (article_id) DO UPDATE SET metadata_text = EXCLUDED.metadata_text
	`, arsenkinArticle.ID); err != nil {
		t.Fatal(err)
	}
	if err := repository.PrepareArticleForRun(ctx, arsenkinArticle.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"article_research", "article_metadata", "article_outputs"} {
		assertCount(t, pool, table, fmt.Sprintf("article_id = %d", arsenkinArticle.ID), 1)
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

func TestGetResultInputMapsStructuredInputFields(t *testing.T) {
	repository, pool := newTestRepository(t)
	ctx := context.Background()
	created, err := repository.Create(ctx, article.Input{
		ExcelID: 21, Title: "старый title", Keyword: "старый key_word",
		Header: "старый header", ImageSlug: "старый-image-slug",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE article_inputs
		SET seo_title = 'новый seo_title', profession_name = 'новый profession_name',
			image_name = 'новый image_name', image_url = 'https://example.test/new-image.jpg'
		WHERE article_id = $1
	`, created.ID); err != nil {
		t.Fatal(err)
	}

	input, err := repository.GetResultInput(ctx, created.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	if input.SEOTitle != "новый seo_title" || input.ProfessionName != "новый profession_name" ||
		input.ImageName != "новый image_name" || input.ImageURL != "https://example.test/new-image.jpg" {
		t.Fatalf("structured result mapping = SEO %q, profession %q, image %q, URL %q",
			input.SEOTitle, input.ProfessionName, input.ImageName, input.ImageURL)
	}
	if input.SEOTitle == "старый title" || input.ProfessionName == "старый key_word" ||
		input.ImageName == "старый header" || input.ImageURL == "старый-image-slug" {
		t.Fatalf("structured fields were replaced by legacy values: %+v", input)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE article_inputs
		SET seo_title = NULL, profession_name = NULL, image_name = NULL, image_url = NULL
		WHERE article_id = $1
	`, created.ID); err != nil {
		t.Fatal(err)
	}
	nullInput, err := repository.GetResultInput(ctx, created.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	if nullInput.SEOTitle != "" || nullInput.ProfessionName != "" || nullInput.ImageName != "" || nullInput.ImageURL != "" {
		t.Fatalf("NULL structured mapping = SEO %q, profession %q, image %q, URL %q",
			nullInput.SEOTitle, nullInput.ProfessionName, nullInput.ImageName, nullInput.ImageURL)
	}
}

func TestSaveHTMLPathWaitsForResultBeforeCompletion(t *testing.T) {
	repository, pool := newTestRepository(t)
	ctx := context.Background()
	created, err := repository.Create(ctx, article.Input{ExcelID: 25, Title: "HTML", ImageSlug: "html"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO article_outputs (article_id) VALUES ($1)`, created.ID); err != nil {
		t.Fatal(err)
	}
	const htmlPath = "25-html/article.html"
	if err := repository.SaveHTMLPath(ctx, created.ID, htmlPath); err != nil {
		t.Fatal(err)
	}
	var status, currentStep, savedHTMLPath string
	if err := pool.QueryRow(ctx, `
		SELECT a.status, a.current_step, o.html_path
		FROM articles AS a JOIN article_outputs AS o ON o.article_id = a.id
		WHERE a.id = $1
	`, created.ID).Scan(&status, &currentStep, &savedHTMLPath); err != nil {
		t.Fatal(err)
	}
	if status != "processing" || currentStep != "final_file_assembly" || savedHTMLPath != htmlPath {
		t.Fatalf("state after HTML = status %q, step %q, path %q", status, currentStep, savedHTMLPath)
	}
	if err := repository.CompleteGeneration(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	var completedStep *string
	if err := pool.QueryRow(ctx, `SELECT status, current_step FROM articles WHERE id = $1`, created.ID).Scan(&status, &completedStep); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || completedStep != nil {
		t.Fatalf("completed state = status %q, step %v", status, completedStep)
	}

	failed, err := repository.Create(ctx, article.Input{ExcelID: 26, Title: "Result failure", ImageSlug: "result-failure"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO article_outputs (article_id) VALUES ($1)`, failed.ID); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveHTMLPath(ctx, failed.ID, "26-result-failure/article.html"); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveError(ctx, failed.ID, errors.New("result publication failed")); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT a.status, a.current_step, o.html_path
		FROM articles AS a JOIN article_outputs AS o ON o.article_id = a.id
		WHERE a.id = $1
	`, failed.ID).Scan(&status, &currentStep, &savedHTMLPath); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || currentStep != "final_file_assembly" || savedHTMLPath != "26-result-failure/article.html" {
		t.Fatalf("state after result failure = status %q, step %q, path %q", status, currentStep, savedHTMLPath)
	}
}

func TestClaimNextIncompleteMarksArticleProcessing(t *testing.T) {
	repository, pool := newTestRepository(t)
	ctx := context.Background()
	created, err := repository.Create(ctx, article.Input{ExcelID: 31, Title: "Ожидающая", ImageSlug: "pending"})
	if err != nil {
		t.Fatal(err)
	}

	claimed, found, err := repository.ClaimNextIncomplete(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !found || claimed.ID != created.ID || claimed.Status != "processing" {
		t.Fatalf("claim = %+v, found = %v", claimed, found)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM articles WHERE id = $1`, created.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "processing" {
		t.Fatalf("stored status = %q, want processing", status)
	}
}

func TestClaimNextIncompleteConcurrentClaimsGetDifferentArticles(t *testing.T) {
	repository, _ := newTestRepository(t)
	ctx := context.Background()
	first, err := repository.Create(ctx, article.Input{ExcelID: 41, Title: "Первая", ImageSlug: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.Create(ctx, article.Input{ExcelID: 42, Title: "Вторая", ImageSlug: "second"})
	if err != nil {
		t.Fatal(err)
	}

	results := runConcurrentClaims(t, repository, 2)
	if !results[0].found || !results[1].found || results[0].article.ID == results[1].article.ID {
		t.Fatalf("concurrent claims = %+v", results)
	}
	claimedIDs := map[int64]bool{results[0].article.ID: true, results[1].article.ID: true}
	if !claimedIDs[first.ID] || !claimedIDs[second.ID] {
		t.Fatalf("claimed IDs = %v, want %d and %d", claimedIDs, first.ID, second.ID)
	}
	t.Logf("concurrent claims received different article_id: %d and %d", results[0].article.ID, results[1].article.ID)
}

func TestClaimNextIncompleteConcurrentClaimsDoNotRepeatSingleArticle(t *testing.T) {
	repository, _ := newTestRepository(t)
	ctx := context.Background()
	created, err := repository.Create(ctx, article.Input{ExcelID: 51, Title: "Единственная", ImageSlug: "single"})
	if err != nil {
		t.Fatal(err)
	}

	results := runConcurrentClaims(t, repository, 2)
	found := 0
	for _, result := range results {
		if result.found {
			found++
			if result.article.ID != created.ID {
				t.Fatalf("claimed article_id = %d, want %d", result.article.ID, created.ID)
			}
		}
	}
	if found != 1 {
		t.Fatalf("successful claims = %d, want 1; results = %+v", found, results)
	}
	t.Logf("single article_id %d was received by exactly one claim", created.ID)
}

func TestClaimNextIncompleteStatusSemantics(t *testing.T) {
	repository, pool := newTestRepository(t)
	ctx := context.Background()
	completed := createArticleWithStatus(t, repository, pool, 61, "completed")
	processing := createArticleWithStatus(t, repository, pool, 62, "processing")
	failed := createArticleWithStatus(t, repository, pool, 63, "failed")
	pending := createArticleWithStatus(t, repository, pool, 64, "pending")

	first, found, err := repository.ClaimNextIncomplete(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !found || first.ID != pending.ID {
		t.Fatalf("first claim = %+v, found = %v; want pending article %d after failed article %d", first, found, pending.ID, failed.ID)
	}
	if _, found, err := repository.ClaimNextIncomplete(ctx); err != nil || found {
		t.Fatalf("second claim found = %v, err = %v; failed=%d processing=%d completed=%d must remain ineligible", found, err, failed.ID, processing.ID, completed.ID)
	}
}

func TestClaimNextIncompleteReturnsNoCandidateForFailedArticle(t *testing.T) {
	repository, pool := newTestRepository(t)
	failed := createArticleWithStatus(t, repository, pool, 65, "failed")

	claimed, found, err := repository.ClaimNextIncomplete(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatalf("claim = %+v, found = true; failed article %d must not be claimed automatically", claimed, failed.ID)
	}
}

func TestClaimNextIncompleteRollbackReleasesArticle(t *testing.T) {
	repository, pool := newTestRepository(t)
	ctx := context.Background()
	created, err := repository.Create(ctx, article.Input{ExcelID: 71, Title: "Rollback", ImageSlug: "rollback"})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	claimed, found, err := claimNextIncomplete(ctx, tx)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if !found || claimed.ID != created.ID || claimed.Status != "processing" {
		_ = tx.Rollback(ctx)
		t.Fatalf("transactional claim = %+v, found = %v", claimed, found)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM articles WHERE id = $1`, created.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("status after rollback = %q, want pending", status)
	}
	reclaimed, found, err := repository.ClaimNextIncomplete(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !found || reclaimed.ID != created.ID {
		t.Fatalf("claim after rollback = %+v, found = %v", reclaimed, found)
	}
}

func TestSavePreparedResearchReplacesCompleteResearchAfterSuccessfulPreparation(t *testing.T) {
	repository, pool := newTestRepository(t)
	ctx := context.Background()
	created, err := repository.Create(ctx, article.Input{ExcelID: 81, Title: "Research", ImageSlug: "research"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SavePreparedResearch(ctx, created.ID,
		[]string{"старый cleaned"},
		[]article.KeywordFrequency{{Query: "старый wordstat", Frequency: 10}},
		[]string{"старый lsi"},
		"старая структура",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO article_metadata (article_id, metadata_text) VALUES ($1, 'старые metadata');
		INSERT INTO article_outputs (article_id, article_path) VALUES ($1, 'old/article.txt');
	`, created.ID); err != nil {
		t.Fatal(err)
	}
	oldUpdatedAt := researchUpdatedAt(t, pool, created.ID)

	if err := repository.PrepareArticleForRun(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	assertCount(t, pool, "article_research", fmt.Sprintf("article_id = %d", created.ID), 1)
	assertCount(t, pool, "article_metadata", fmt.Sprintf("article_id = %d", created.ID), 1)
	assertCount(t, pool, "article_outputs", fmt.Sprintf("article_id = %d", created.ID), 1)
	assertCompleteResearch(t, pool, created.ID,
		[]string{"старый cleaned"},
		[]article.KeywordFrequency{{Query: "старый wordstat", Frequency: 10}},
		[]string{"старый lsi"},
		"старая структура",
	)
	if _, err := pool.Exec(ctx, `SELECT pg_sleep(0.01)`); err != nil {
		t.Fatal(err)
	}

	if err := repository.SavePreparedResearch(ctx, created.ID,
		[]string{"новый cleaned"},
		[]article.KeywordFrequency{{Query: "новый wordstat", Frequency: 20}},
		[]string{"новый lsi"},
		"новая структура",
	); err != nil {
		t.Fatal(err)
	}
	assertCompleteResearch(t, pool, created.ID,
		[]string{"новый cleaned"},
		[]article.KeywordFrequency{{Query: "новый wordstat", Frequency: 20}},
		[]string{"новый lsi"},
		"новая структура",
	)
	if updatedAt := researchUpdatedAt(t, pool, created.ID); !updatedAt.After(oldUpdatedAt) {
		t.Fatalf("research updated_at = %v, want after %v", updatedAt, oldUpdatedAt)
	}
	assertCount(t, pool, "article_metadata", fmt.Sprintf("article_id = %d", created.ID), 0)
	assertCount(t, pool, "article_outputs", fmt.Sprintf("article_id = %d", created.ID), 0)
}

type claimResult struct {
	article article.Article
	found   bool
	err     error
}

func runConcurrentClaims(t *testing.T, repository *ArticleRepository, count int) []claimResult {
	t.Helper()
	start := make(chan struct{})
	results := make([]claimResult, count)
	var waitGroup sync.WaitGroup
	waitGroup.Add(count)
	for index := range results {
		go func(index int) {
			defer waitGroup.Done()
			<-start
			results[index].article, results[index].found, results[index].err = repository.ClaimNextIncomplete(context.Background())
		}(index)
	}
	close(start)
	waitGroup.Wait()
	for _, result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
	}
	return results
}

func createArticleWithStatus(t *testing.T, repository *ArticleRepository, pool *pgxpool.Pool, excelID int, status string) article.Article {
	t.Helper()
	created, err := repository.Create(context.Background(), article.Input{ExcelID: excelID, Title: status, ImageSlug: status})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `
		UPDATE articles
		SET status = $2, current_step = CASE WHEN $2 = 'completed' THEN NULL ELSE current_step END
		WHERE id = $1
	`, created.ID, status); err != nil {
		t.Fatal(err)
	}
	return created
}

func researchUpdatedAt(t *testing.T, pool *pgxpool.Pool, articleID int64) time.Time {
	t.Helper()
	var updatedAt time.Time
	if err := pool.QueryRow(context.Background(), `SELECT updated_at FROM article_research WHERE article_id = $1`, articleID).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	return updatedAt
}

func assertCompleteResearch(t *testing.T, pool *pgxpool.Pool, articleID int64, cleaned []string, wordstat []article.KeywordFrequency, lsi []string, structure string) {
	t.Helper()
	var cleanedJSON, wordstatJSON, lsiJSON []byte
	var gotStructure string
	if err := pool.QueryRow(context.Background(), `
		SELECT cleaned_keywords, wordstat_keywords, lsi_words, competitor_structure
		FROM article_research WHERE article_id = $1
	`, articleID).Scan(&cleanedJSON, &wordstatJSON, &lsiJSON, &gotStructure); err != nil {
		t.Fatal(err)
	}
	var gotCleaned []string
	var gotWordstat []article.KeywordFrequency
	var gotLSI []string
	if err := json.Unmarshal(cleanedJSON, &gotCleaned); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(wordstatJSON, &gotWordstat); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lsiJSON, &gotLSI); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotCleaned, cleaned) || !reflect.DeepEqual(gotWordstat, wordstat) ||
		!reflect.DeepEqual(gotLSI, lsi) || gotStructure != structure {
		t.Fatalf("research = cleaned %#v, wordstat %#v, lsi %#v, structure %q", gotCleaned, gotWordstat, gotLSI, gotStructure)
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
		"000006_add_structured_article_metadata.up.sql",
		"000007_add_result_input_fields.up.sql",
	} {
		migration, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "migrations", name))
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
