package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

// newClearedArticle импортирует статью и доводит её состояние до «прогон был и упал»:
// research, metadata, outputs и сохранённая ошибка на месте.
func newClearedArticle(t *testing.T, repository *ArticleRepository) article.Article {
	t.Helper()
	ctx := context.Background()

	created, err := repository.Create(ctx, article.Input{
		ExcelID: 23, Title: "Как выбрать фрезу", Header: "Заголовок",
		ImageSlug: "kak-vybrat-frezu", MetaDescription: "Описание", Keyword: "фреза",
		ReferenceURL: "https://example.com/frezy", Category: "Инструменты",
		Author: "Автор", Links: "Ссылки", Professions: "Профессии",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveManualKeywords(ctx, created.ID, []string{"как выбрать фрезу"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveStructurePath(ctx, created.ID, "23-kak-vybrat-frezu/generated/structure.txt"); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveArticleInfo(ctx, created.ID, "сырой ответ", article.ArticleInfo{
		TLDR: "коротко", FAQ: "вопросы",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveError(ctx, created.ID, errors.New("stage=article_review: обрыв генерации")); err != nil {
		t.Fatal(err)
	}
	return created
}

func TestClearArticleStateKeepsImportAndWipesTheRest(t *testing.T) {
	repository, pool := newTestRepository(t)
	ctx := context.Background()
	created := newClearedArticle(t, repository)
	condition := fmt.Sprintf("article_id = %d", created.ID)

	assertCount(t, pool, "article_research", condition, 1)
	assertCount(t, pool, "article_metadata", condition, 1)
	assertCount(t, pool, "article_outputs", condition, 1)
	assertCount(t, pool, "article_errors", condition, 1)

	if err := repository.ClearArticleState(ctx, created.ID); err != nil {
		t.Fatalf("ClearArticleState вернул ошибку: %v", err)
	}

	// Всё, что произвёл пайплайн, исчезло.
	assertCount(t, pool, "article_research", condition, 0)
	assertCount(t, pool, "article_metadata", condition, 0)
	assertCount(t, pool, "article_outputs", condition, 0)
	assertCount(t, pool, "article_errors", condition, 0)

	// Импорт на месте: строка статьи, её id, external_id и исходные данные.
	assertCount(t, pool, "articles", fmt.Sprintf("id = %d", created.ID), 1)
	assertCount(t, pool, "article_inputs", condition, 1)

	cleared, err := repository.GetArticleByExternalID(ctx, "23")
	if err != nil {
		t.Fatalf("статья должна остаться доступной по external_id: %v", err)
	}
	if cleared.ID != created.ID {
		t.Fatalf("articles.id изменился: %d -> %d", created.ID, cleared.ID)
	}
	if cleared.ExternalID != "23" {
		t.Fatalf("external_id изменился: %q", cleared.ExternalID)
	}
	if cleared.Title != "Как выбрать фрезу" {
		t.Fatalf("название статьи потеряно: %q", cleared.Title)
	}
	if cleared.Status != "pending" {
		t.Fatalf("статус после очистки = %q, ожидался pending", cleared.Status)
	}
	if cleared.CurrentStep != nil {
		t.Fatalf("этап после очистки = %q, ожидался NULL", *cleared.CurrentStep)
	}
	if cleared.ErrorMessage != nil {
		t.Fatalf("ошибка после очистки = %q, ожидался NULL", *cleared.ErrorMessage)
	}
}

// Очистка идемпотентна: повтор на уже чистой статье проходит и ничего не ломает.
func TestClearArticleStateIsIdempotent(t *testing.T) {
	repository, pool := newTestRepository(t)
	ctx := context.Background()
	created := newClearedArticle(t, repository)

	if err := repository.ClearArticleState(ctx, created.ID); err != nil {
		t.Fatalf("первая очистка вернула ошибку: %v", err)
	}
	if err := repository.ClearArticleState(ctx, created.ID); err != nil {
		t.Fatalf("повторная очистка вернула ошибку: %v", err)
	}
	assertCount(t, pool, "articles", fmt.Sprintf("id = %d", created.ID), 1)
}

// Очистка одной статьи не должна задевать соседнюю: это главный риск команды.
func TestClearArticleStateDoesNotTouchOtherArticles(t *testing.T) {
	repository, pool := newTestRepository(t)
	ctx := context.Background()
	target := newClearedArticle(t, repository)

	neighbor, err := repository.Create(ctx, article.Input{
		ExcelID: 24, Title: "Соседняя статья", Header: "Заголовок",
		ImageSlug: "sosednyaya", MetaDescription: "Описание", Keyword: "ключ",
		ReferenceURL: "https://example.com/other", Category: "Категория",
		Author: "Автор", Links: "Ссылки", Professions: "Профессии",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveManualKeywords(ctx, neighbor.ID, []string{"соседний запрос"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveError(ctx, neighbor.ID, errors.New("stage=structure: сбой")); err != nil {
		t.Fatal(err)
	}

	if err := repository.ClearArticleState(ctx, target.ID); err != nil {
		t.Fatalf("ClearArticleState вернул ошибку: %v", err)
	}

	neighborCondition := fmt.Sprintf("article_id = %d", neighbor.ID)
	assertCount(t, pool, "article_research", neighborCondition, 1)
	assertCount(t, pool, "article_errors", neighborCondition, 1)

	untouched, err := repository.GetArticleByExternalID(ctx, "24")
	if err != nil {
		t.Fatal(err)
	}
	if untouched.Status != "failed" {
		t.Fatalf("статус соседней статьи = %q, ожидался failed", untouched.Status)
	}
	if untouched.ErrorMessage == nil {
		t.Fatal("ошибка соседней статьи не должна очищаться")
	}
}

func TestClearArticleStateFailsForUnknownArticle(t *testing.T) {
	repository, _ := newTestRepository(t)

	if err := repository.ClearArticleState(context.Background(), 999999); err == nil {
		t.Fatal("очистка несуществующей статьи должна возвращать ошибку")
	}
}

// Отчёт считает те же таблицы и в том же порядке, в каком очистка их удалит.
func TestClearArticleCountsMatchesClearedTables(t *testing.T) {
	repository, _ := newTestRepository(t)
	ctx := context.Background()
	created := newClearedArticle(t, repository)

	counts, err := repository.ClearArticleCounts(ctx, created.ID)
	if err != nil {
		t.Fatalf("ClearArticleCounts вернул ошибку: %v", err)
	}
	if len(counts) != len(clearArticleTables) {
		t.Fatalf("ожидалось %d таблиц, получено %d", len(clearArticleTables), len(counts))
	}
	for index, count := range counts {
		if count.Table != clearArticleTables[index] {
			t.Fatalf("таблица %d = %q, ожидалась %q", index, count.Table, clearArticleTables[index])
		}
		if count.Rows != 1 {
			t.Fatalf("в таблице %s ожидалась 1 строка, получено %d", count.Table, count.Rows)
		}
	}

	if err := repository.ClearArticleState(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	cleared, err := repository.ClearArticleCounts(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, count := range cleared {
		if count.Rows != 0 {
			t.Fatalf("после очистки в %s осталось %d строк", count.Table, count.Rows)
		}
	}
}

// articles и article_inputs не должны попасть в список очистки: это результат импорта.
func TestClearArticleTablesExcludeImport(t *testing.T) {
	for _, table := range clearArticleTables {
		if table == "articles" || table == "article_inputs" {
			t.Fatalf("таблица %s не должна очищаться командой clear", table)
		}
	}
}
