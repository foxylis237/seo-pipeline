package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

// resetArticleInput — строка Excel, которой импортируется статья в тестах сброса.
func resetArticleInput() article.Input {
	return article.Input{
		ExcelID: 23, Title: "Как выбрать фрезу", Header: "Заголовок",
		ImageSlug: "kak-vybrat-frezu", MetaDescription: "Описание", Keyword: "фреза",
		ReferenceURL: "https://example.com/frezy", Category: "Инструменты",
		Author: "Автор", Links: "Ссылки", Professions: "Профессии", Tags: "метки",
	}
}

func TestResetArticleStateWipesImportAndKeepsArticleRow(t *testing.T) {
	repository, pool := newTestRepository(t)
	ctx := context.Background()
	created := newClearedArticle(t, repository)
	condition := fmt.Sprintf("article_id = %d", created.ID)

	assertCount(t, pool, "article_inputs", condition, 1)

	if err := repository.ResetArticleState(ctx, created.ID); err != nil {
		t.Fatalf("ResetArticleState вернул ошибку: %v", err)
	}

	// Всё, что связано со статьёй, исчезло, включая результат импорта.
	assertCount(t, pool, "article_research", condition, 0)
	assertCount(t, pool, "article_metadata", condition, 0)
	assertCount(t, pool, "article_outputs", condition, 0)
	assertCount(t, pool, "article_errors", condition, 0)
	assertCount(t, pool, "article_inputs", condition, 0)

	// Строка статьи и её внутренний id — то единственное, что сброс обязан сохранить.
	assertCount(t, pool, "articles", fmt.Sprintf("id = %d", created.ID), 1)

	reset, err := repository.GetArticleByExternalID(ctx, "23")
	if err != nil {
		t.Fatalf("статья должна остаться доступной по external_id: %v", err)
	}
	if reset.ID != created.ID {
		t.Fatalf("articles.id изменился: %d -> %d", created.ID, reset.ID)
	}
	if reset.ExternalID != "23" {
		t.Fatalf("external_id изменился: %q", reset.ExternalID)
	}
	if reset.Status != "pending" {
		t.Fatalf("статус после сброса = %q, ожидался pending", reset.Status)
	}
	if reset.CurrentStep != nil {
		t.Fatalf("этап после сброса = %q, ожидался NULL", *reset.CurrentStep)
	}
	if reset.ErrorMessage != nil {
		t.Fatalf("ошибка после сброса = %q, ожидался NULL", *reset.ErrorMessage)
	}
}

// Сброс идемпотентен: повтор на уже сброшенной статье проходит и ничего не ломает.
func TestResetArticleStateIsIdempotent(t *testing.T) {
	repository, pool := newTestRepository(t)
	ctx := context.Background()
	created := newClearedArticle(t, repository)

	if err := repository.ResetArticleState(ctx, created.ID); err != nil {
		t.Fatalf("первый сброс вернул ошибку: %v", err)
	}
	if err := repository.ResetArticleState(ctx, created.ID); err != nil {
		t.Fatalf("повторный сброс вернул ошибку: %v", err)
	}
	assertCount(t, pool, "articles", fmt.Sprintf("id = %d", created.ID), 1)
}

// Сброс одной статьи не должен задевать соседнюю: это главный риск команды.
func TestResetArticleStateDoesNotTouchOtherArticles(t *testing.T) {
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

	if err := repository.ResetArticleState(ctx, target.ID); err != nil {
		t.Fatalf("ResetArticleState вернул ошибку: %v", err)
	}

	assertCount(t, pool, "article_inputs", fmt.Sprintf("article_id = %d", neighbor.ID), 1)
}

func TestResetArticleStateFailsForUnknownArticle(t *testing.T) {
	repository, _ := newTestRepository(t)

	if err := repository.ResetArticleState(context.Background(), 999999); err == nil {
		t.Fatal("сброс несуществующей статьи должен возвращать ошибку")
	}
}

// Отчёт считает те же таблицы и в том же порядке, в каком сброс их удалит.
func TestResetArticleCountsMatchesResetTables(t *testing.T) {
	repository, _ := newTestRepository(t)
	ctx := context.Background()
	created := newClearedArticle(t, repository)

	counts, err := repository.ResetArticleCounts(ctx, created.ID)
	if err != nil {
		t.Fatalf("ResetArticleCounts вернул ошибку: %v", err)
	}
	if len(counts) != len(resetArticleTables) {
		t.Fatalf("ожидалось %d таблиц, получено %d", len(resetArticleTables), len(counts))
	}
	for index, count := range counts {
		if count.Table != resetArticleTables[index] {
			t.Fatalf("таблица %d = %q, ожидалась %q", index, count.Table, resetArticleTables[index])
		}
		if count.Rows != 1 {
			t.Fatalf("в таблице %s ожидалась 1 строка, получено %d", count.Table, count.Rows)
		}
	}
}

// articles не должна попасть в список сброса: строка статьи и её id переживают команду.
// article_inputs, наоборот, обязана в нём быть — этим сброс и отличается от очистки.
func TestResetArticleTablesKeepArticlesAndWipeInputs(t *testing.T) {
	var hasInputs bool
	for _, table := range resetArticleTables {
		if table == "articles" {
			t.Fatal("таблица articles не должна очищаться командой reset")
		}
		if table == "article_inputs" {
			hasInputs = true
		}
	}
	if !hasInputs {
		t.Fatal("article_inputs обязана очищаться командой reset")
	}
}

// Главный сценарий команды: после сброса статью можно импортировать заново, и входные данные
// возвращаются на место. Без этого сброс оставлял бы статью навсегда без article_inputs —
// строка articles есть, а импорт проходит мимо неё по ON CONFLICT DO NOTHING.
func TestImportRestoresInputsAfterResetArticleState(t *testing.T) {
	repository, pool := newTestRepository(t)
	ctx := context.Background()

	imported, created, err := repository.Import(ctx, resetArticleInput())
	if err != nil {
		t.Fatalf("первый импорт вернул ошибку: %v", err)
	}
	if !created {
		t.Fatal("первый импорт обязан создать статью")
	}

	if err := repository.ResetArticleState(ctx, imported.ID); err != nil {
		t.Fatalf("ResetArticleState вернул ошибку: %v", err)
	}
	assertCount(t, pool, "article_inputs", fmt.Sprintf("article_id = %d", imported.ID), 0)

	reimported, createdAgain, err := repository.Import(ctx, resetArticleInput())
	if err != nil {
		t.Fatalf("повторный импорт вернул ошибку: %v", err)
	}
	// Строка articles не создавалась заново — её id тот же, что был до сброса.
	if createdAgain {
		t.Fatal("повторный импорт не должен создавать вторую строку статьи")
	}
	if reimported.ID != imported.ID {
		t.Fatalf("articles.id изменился: %d -> %d", imported.ID, reimported.ID)
	}
	assertCount(t, pool, "articles", "external_id = '23'", 1)
	assertCount(t, pool, "article_inputs", fmt.Sprintf("article_id = %d", imported.ID), 1)

	restored, err := repository.GetArticleInput(ctx, imported.ID)
	if err != nil {
		t.Fatalf("входные данные должны читаться после повторного импорта: %v", err)
	}
	if restored.Keyword != "фреза" || restored.ReferenceURL != "https://example.com/frezy" {
		t.Fatalf("входные данные восстановлены неверно: %+v", restored)
	}
}

// Импорт по-прежнему не переписывает уже импортированную статью: восстановление входных
// данных срабатывает только там, где их нет.
func TestImportDoesNotOverwriteExistingInputs(t *testing.T) {
	repository, _ := newTestRepository(t)
	ctx := context.Background()

	imported, _, err := repository.Import(ctx, resetArticleInput())
	if err != nil {
		t.Fatalf("первый импорт вернул ошибку: %v", err)
	}

	changed := resetArticleInput()
	changed.Keyword = "другой ключ"
	changed.Title = "Другое название"
	if _, created, importErr := repository.Import(ctx, changed); importErr != nil {
		t.Fatalf("повторный импорт вернул ошибку: %v", importErr)
	} else if created {
		t.Fatal("повторный импорт не должен создавать вторую строку статьи")
	}

	kept, err := repository.GetArticleInput(ctx, imported.ID)
	if err != nil {
		t.Fatal(err)
	}
	if kept.Keyword != "фреза" {
		t.Fatalf("импорт переписал существующие входные данные: %q", kept.Keyword)
	}
	existing, err := repository.GetArticleByExternalID(ctx, "23")
	if err != nil {
		t.Fatal(err)
	}
	if existing.Title != "Как выбрать фрезу" {
		t.Fatalf("импорт переписал название существующей статьи: %q", existing.Title)
	}
}
