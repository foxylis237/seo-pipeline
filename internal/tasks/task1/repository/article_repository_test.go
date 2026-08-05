package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
	if err := pool.QueryRow(ctx, `SELECT review_path, fixed_article_path FROM article_outputs WHERE article_id = $1`, arsenkinArticle.ID).Scan(&savedReviewPath, &savedFixedArticlePath); err != nil {
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

func TestImportNeverUpdatesExistingArticle(t *testing.T) {
	repository, pool := newTestRepository(t)
	ctx := context.Background()
	original := article.Input{
		ExcelID: 901, Title: "Исходное название", ImageSlug: "original",
		ReferenceURL: "https://example.test/original", Category: "Исходная категория",
	}
	created, wasCreated, err := repository.Import(ctx, original)
	if err != nil || !wasCreated {
		t.Fatalf("первичный Import: created=%v err=%v", wasCreated, err)
	}
	repeated, wasCreated, err := repository.Import(ctx, article.Input{
		ExcelID: 901, Title: "Новое название", ImageSlug: "changed",
		ReferenceURL: "https://example.test/changed", Category: "Новая категория",
	})
	if err != nil || wasCreated {
		t.Fatalf("повторный Import: created=%v err=%v", wasCreated, err)
	}
	if repeated.ID != created.ID || repeated.Title != original.Title {
		t.Fatalf("существующая статья изменилась: %+v", repeated)
	}
	assertImportedFields(t, pool, created.ID, original)
}

func TestRecordErrorAppendsHistoryAndSaveErrorKeepsItAfterSuccess(t *testing.T) {
	repository, pool := newTestRepository(t)
	ctx := context.Background()
	created, err := repository.Create(ctx, article.Input{ExcelID: 920, Title: "История ошибок", ImageSlug: "errors"})
	if err != nil {
		t.Fatal(err)
	}
	step := "article_generation"
	operation := "gemini_article_generation"
	if err := repository.RecordError(ctx, article.ErrorRecord{
		ArticleID: created.ID, ExternalID: created.ExternalID, Step: &step, Operation: &operation,
		ErrorMessage: "first timeout", Retryable: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordError(ctx, article.ErrorRecord{
		ArticleID: created.ID, ExternalID: created.ExternalID, Step: &step,
		ErrorMessage: "invalid response", Retryable: false,
	}); err != nil {
		t.Fatal(err)
	}
	other, err := repository.Create(ctx, article.Input{ExcelID: 921, Title: "Другая статья", ImageSlug: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordError(ctx, article.ErrorRecord{
		ArticleID: other.ID, ExternalID: other.ExternalID, ErrorMessage: "other article error",
	}); err != nil {
		t.Fatal(err)
	}
	records, err := repository.ListErrors(ctx, created.ExternalID, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].ErrorMessage != "invalid response" || records[1].ErrorMessage != "first timeout" {
		t.Fatalf("error history = %+v", records)
	}
	if records[1].ArticleID != created.ID || records[1].ExternalID != created.ExternalID ||
		records[1].Step == nil || *records[1].Step != step || records[1].Operation == nil ||
		*records[1].Operation != operation || !records[1].Retryable {
		t.Fatalf("first error fields = %+v", records[1])
	}

	if _, err := pool.Exec(ctx, `UPDATE articles SET current_step = 'article_generation' WHERE id = $1`, created.ID); err != nil {
		t.Fatal(err)
	}
	processingErr := errors.New("request timeout")
	if err := repository.SaveError(ctx, created.ID, processingErr); err != nil {
		t.Fatal(err)
	}
	var status, currentStep, message string
	if err := pool.QueryRow(ctx, `SELECT status, current_step, error_message FROM articles WHERE id = $1`, created.ID).Scan(&status, &currentStep, &message); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || currentStep != "article_generation" || message != processingErr.Error() {
		t.Fatalf("failed state = %q %q %q", status, currentStep, message)
	}
	records, err = repository.ListErrors(ctx, created.ExternalID, 50)
	if err != nil || len(records) != 3 || !records[0].Retryable || records[0].Operation == nil || *records[0].Operation != "gemini_article_generation" {
		t.Fatalf("SaveError history = %+v, %v", records, err)
	}
	if err := repository.BeginGeneration(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	var clearedMessage *string
	if err := pool.QueryRow(ctx, `SELECT error_message FROM articles WHERE id = $1`, created.ID).Scan(&clearedMessage); err != nil {
		t.Fatal(err)
	}
	if clearedMessage != nil {
		t.Fatalf("current error was not cleared after restart: %q", *clearedMessage)
	}
	records, err = repository.ListErrors(ctx, created.ExternalID, 50)
	if err != nil || len(records) != 3 {
		t.Fatalf("history after successful restart = %+v, %v", records, err)
	}
}

func TestGetResultInputMapsImportedResultFields(t *testing.T) {
	repository, _ := newTestRepository(t)
	ctx := context.Background()
	created, err := repository.Create(ctx, article.Input{
		ExcelID: 21, Title: "старый title", Keyword: "старый key_word",
		Header: "старый header", ImageSlug: "старый-image-slug",
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := repository.GetResultInput(ctx, created.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Article.Title != "старый title" || input.Keyword != "старый key_word" || input.Article.Slug != "старый-image-slug" {
		t.Fatalf("imported result fields = title %q, keyword %q, image slug %q",
			input.Article.Title, input.Keyword, input.Article.Slug)
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

func TestGetPendingForOperationUsesPersistedPrerequisites(t *testing.T) {
	repository, pool := newTestRepository(t)
	ctx := context.Background()

	prepareReady, _ := repository.Create(ctx, article.Input{ExcelID: 1001, Title: "prepare ready", ImageSlug: "prepare-ready"})
	prepareDone, _ := repository.Create(ctx, article.Input{ExcelID: 1002, Title: "prepare done", ImageSlug: "prepare-done"})
	generateReady, _ := repository.Create(ctx, article.Input{ExcelID: 1003, Title: "generate ready", ImageSlug: "generate-ready"})
	articleReady, _ := repository.Create(ctx, article.Input{ExcelID: 1004, Title: "article ready", ImageSlug: "article-ready"})
	reviewReady, _ := repository.Create(ctx, article.Input{ExcelID: 1005, Title: "review ready", ImageSlug: "review-ready"})
	fixReady, _ := repository.Create(ctx, article.Input{ExcelID: 1006, Title: "fix ready", ImageSlug: "fix-ready"})
	htmlReady, _ := repository.Create(ctx, article.Input{ExcelID: 1007, Title: "html ready", ImageSlug: "html-ready"})
	htmlBlocked, _ := repository.Create(ctx, article.Input{ExcelID: 1008, Title: "html blocked", ImageSlug: "html-blocked"})
	resultReady, _ := repository.Create(ctx, article.Input{ExcelID: 1009, Title: "result ready", ImageSlug: "result-ready"})
	completed, _ := repository.Create(ctx, article.Input{ExcelID: 1010, Title: "completed", ImageSlug: "completed"})

	for _, id := range []int64{prepareDone.ID, generateReady.ID, articleReady.ID} {
		if _, err := pool.Exec(ctx, `INSERT INTO article_research (article_id, competitor_structure) VALUES ($1, 'H1 Structure')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO article_outputs (article_id, structure_path, article_path, review_path, fixed_article_path, html_path) VALUES
			($1, 'structure.txt', 'article.txt', NULL, NULL, NULL),
			($2, 'structure.txt', 'article.txt', NULL, NULL, NULL),
			($3, 'structure.txt', 'article.txt', 'review.txt', NULL, NULL),
			($4, 'structure.txt', 'article.txt', 'review.txt', 'fixed.txt', NULL),
			($5, 'structure.txt', 'article.txt', 'review.txt', NULL, NULL),
			($6, 'structure.txt', 'article.txt', 'review.txt', 'fixed.txt', 'article.html'),
			($7, 'structure.txt', 'article.txt', 'review.txt', 'fixed.txt', 'article.html')
	`, articleReady.ID, reviewReady.ID, fixReady.ID, htmlReady.ID, htmlBlocked.ID, resultReady.ID, completed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO article_metadata (article_id, metadata_text) VALUES ($1, 'metadata'), ($2, 'metadata')`, reviewReady.ID, fixReady.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE articles SET status = 'processing', current_step = CASE id
			WHEN $1 THEN 'structure_generation'
			WHEN $2 THEN 'article_review'
			WHEN $3 THEN 'article_review'
			WHEN $4 THEN 'article_review'
			WHEN $5 THEN 'html_generation'
			WHEN $6 THEN 'html_generation'
			WHEN $7 THEN 'final_file_assembly'
			ELSE current_step END
		WHERE id = ANY($8)
	`, generateReady.ID, articleReady.ID, reviewReady.ID, fixReady.ID, htmlReady.ID, htmlBlocked.ID, resultReady.ID,
		[]int64{generateReady.ID, articleReady.ID, reviewReady.ID, fixReady.ID, htmlReady.ID, htmlBlocked.ID, resultReady.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE articles SET status = 'completed', current_step = NULL WHERE id = $1`, completed.ID); err != nil {
		t.Fatal(err)
	}

	assertPendingIDs(t, repository, "prepare", prepareReady.ID)
	assertPendingIDs(t, repository, "generate", prepareDone.ID, generateReady.ID, articleReady.ID)
	assertPendingIDs(t, repository, "article", articleReady.ID)
	assertPendingIDs(t, repository, "info", articleReady.ID)
	assertPendingIDs(t, repository, "review", reviewReady.ID)
	assertPendingIDs(t, repository, "fix", fixReady.ID)
	assertPendingIDs(t, repository, "html", htmlReady.ID)
	assertPendingIDs(t, repository, "result", resultReady.ID)
	assertPendingIDs(t, repository, "demo-generate",
		prepareReady.ID, prepareDone.ID, generateReady.ID, articleReady.ID,
		reviewReady.ID, fixReady.ID, htmlReady.ID, htmlBlocked.ID, resultReady.ID,
	)
	prepared, err := repository.HasPreparedResearch(ctx, generateReady.ExternalID)
	if err != nil || !prepared {
		t.Fatalf("HasPreparedResearch(prepared) = %t, %v", prepared, err)
	}
	prepared, err = repository.HasPreparedResearch(ctx, prepareReady.ExternalID)
	if err != nil || prepared {
		t.Fatalf("HasPreparedResearch(unprepared) = %t, %v", prepared, err)
	}
	if _, err := repository.GetPendingForOperation(ctx, "unknown"); err == nil {
		t.Fatal("unknown operation must fail")
	}
}

func assertPendingIDs(t *testing.T, repository *ArticleRepository, operation string, want ...int64) {
	t.Helper()
	selected, err := repository.GetPendingForOperation(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]int64, len(selected))
	for index := range selected {
		got[index] = selected[index].ID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s pending IDs = %v, want %v", operation, got, want)
	}
}

func TestDemoGenerateSelectionExcludesOnlyNonEmptyRecordedErrors(t *testing.T) {
	repository, pool := newTestRepository(t)
	ctx := context.Background()
	nullError, _ := repository.Create(ctx, article.Input{ExcelID: 1101, Title: "null error", ImageSlug: "null-error"})
	emptyError, _ := repository.Create(ctx, article.Input{ExcelID: 1102, Title: "empty error", ImageSlug: "empty-error"})
	spaceError, _ := repository.Create(ctx, article.Input{ExcelID: 1103, Title: "space error", ImageSlug: "space-error"})
	recordedError, _ := repository.Create(ctx, article.Input{ExcelID: 1104, Title: "recorded error", ImageSlug: "recorded-error"})
	if _, err := pool.Exec(ctx, `
		UPDATE articles
		SET error_message = CASE id
			WHEN $1 THEN ''
			WHEN $2 THEN '   '
			WHEN $3 THEN 'Keys.so timeout'
			ELSE error_message
		END
		WHERE id = ANY($4)
	`, emptyError.ID, spaceError.ID, recordedError.ID, []int64{emptyError.ID, spaceError.ID, recordedError.ID}); err != nil {
		t.Fatal(err)
	}

	assertPendingIDs(t, repository, "demo-generate", nullError.ID, emptyError.ID, spaceError.ID)
}

func TestListArticlesWithErrorsAndClearOneForRetry(t *testing.T) {
	repository, pool := newTestRepository(t)
	ctx := context.Background()
	first, _ := repository.Create(ctx, article.Input{ExcelID: 1201, Title: "first", ImageSlug: "first"})
	nullError, _ := repository.Create(ctx, article.Input{ExcelID: 1202, Title: "null", ImageSlug: "null"})
	emptyError, _ := repository.Create(ctx, article.Input{ExcelID: 1203, Title: "empty", ImageSlug: "empty"})
	spaceError, _ := repository.Create(ctx, article.Input{ExcelID: 1204, Title: "space", ImageSlug: "space"})
	second, _ := repository.Create(ctx, article.Input{ExcelID: 1205, Title: "second", ImageSlug: "second"})
	if _, err := pool.Exec(ctx, `
		UPDATE articles SET status = 'failed', current_step = 'article_generation', error_message = CASE id
			WHEN $1 THEN 'first failure' WHEN $2 THEN '' WHEN $3 THEN '   ' WHEN $4 THEN 'second failure'
			ELSE error_message END
		WHERE id = ANY($5)
	`, first.ID, emptyError.ID, spaceError.ID, second.ID, []int64{first.ID, emptyError.ID, spaceError.ID, second.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO article_outputs (article_id, structure_path, article_path) VALUES ($1, 'structure.txt', 'article.txt')`, first.ID); err != nil {
		t.Fatal(err)
	}

	selected, err := repository.ListArticlesWithErrors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := []int64{selected[0].ID, selected[1].ID}; !reflect.DeepEqual(got, []int64{first.ID, second.ID}) {
		t.Fatalf("error IDs = %v", got)
	}
	if nullError.ID == 0 {
		t.Fatal("null-error fixture was not created")
	}

	cleared, err := repository.ClearArticleErrorForRetry(ctx, first.ID)
	if err != nil || !cleared {
		t.Fatalf("clear = %v, %v", cleared, err)
	}
	var status string
	var step, message *string
	var structurePath, articlePath string
	if err := pool.QueryRow(ctx, `
		SELECT a.status, a.current_step, a.error_message, o.structure_path, o.article_path
		FROM articles a JOIN article_outputs o ON o.article_id = a.id WHERE a.id = $1
	`, first.ID).Scan(&status, &step, &message, &structurePath, &articlePath); err != nil {
		t.Fatal(err)
	}
	if status != "processing" || step == nil || *step != "article_generation" || message != nil {
		t.Fatalf("state after clear: status=%q step=%v error=%v", status, step, message)
	}
	if structurePath != "structure.txt" || articlePath != "article.txt" {
		t.Fatalf("artifacts changed: %q %q", structurePath, articlePath)
	}
	if cleared, err := repository.ClearArticleErrorForRetry(ctx, first.ID); err != nil || cleared {
		t.Fatalf("second clear = %v, %v", cleared, err)
	}
	remaining, err := repository.ListArticlesWithErrors(ctx)
	if err != nil || len(remaining) != 1 || remaining[0].ID != second.ID {
		t.Fatalf("remaining=%+v err=%v", remaining, err)
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

	// Тот же набор файлов и в том же порядке, что применяет docker-entrypoint.
	migrations, err := filepath.Glob(filepath.Join("..", "..", "..", "..", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) == 0 {
		t.Fatal("не найдено ни одной миграции в migrations/*.up.sql")
	}
	sort.Strings(migrations)
	for _, name := range migrations {
		migration, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("применить миграцию %s: %v", filepath.Base(name), err)
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
