package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

func newGoogleDocArticle(t *testing.T, repository *ArticleRepository) article.Article {
	t.Helper()
	created, err := repository.Create(context.Background(), article.Input{
		ExcelID: 45, Title: "Как выбрать фрезу", Header: "Заголовок",
		ImageSlug: "kak-vybrat-frezu", MetaDescription: "Описание", Keyword: "фреза",
		ReferenceURL: "https://example.com/frezy", Category: "Инструменты",
		Author: "Автор", Links: "Ссылки", Professions: "Профессии",
	})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

// Строка article_outputs создаётся на лету: публикация идёт сразу после стадии article и
// обгоняет этапы, которые обычно эту строку заводят.
func TestSaveGoogleDocURLCreatesRowWhenMissing(t *testing.T) {
	repository, pool := newTestRepository(t)
	ctx := context.Background()
	created := newGoogleDocArticle(t, repository)
	const url = "https://docs.google.com/document/d/AbC123/edit"

	if err := repository.SaveGoogleDocURL(ctx, created.ID, url); err != nil {
		t.Fatalf("SaveGoogleDocURL вернул ошибку: %v", err)
	}
	assertCount(t, pool, "article_outputs", fmt.Sprintf("article_id = %d", created.ID), 1)

	input, err := repository.GetResultInput(ctx, "45")
	if err != nil {
		t.Fatal(err)
	}
	if input.GoogleDocURL != url {
		t.Fatalf("адрес документа = %q, ожидался %q", input.GoogleDocURL, url)
	}
}

// Повторная публикация заменяет адрес, а не плодит строки.
func TestSaveGoogleDocURLOverwritesPreviousAddress(t *testing.T) {
	repository, pool := newTestRepository(t)
	ctx := context.Background()
	created := newGoogleDocArticle(t, repository)

	if err := repository.SaveGoogleDocURL(ctx, created.ID, "https://docs.google.com/document/d/old/edit"); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveGoogleDocURL(ctx, created.ID, "https://docs.google.com/document/d/new/edit"); err != nil {
		t.Fatal(err)
	}
	assertCount(t, pool, "article_outputs", fmt.Sprintf("article_id = %d", created.ID), 1)

	input, err := repository.GetResultInput(ctx, "45")
	if err != nil {
		t.Fatal(err)
	}
	if input.GoogleDocURL != "https://docs.google.com/document/d/new/edit" {
		t.Fatalf("адрес не обновился: %q", input.GoogleDocURL)
	}
}

// Пути артефактов не должны страдать от записи адреса: их пишут свои методы.
func TestSaveGoogleDocURLKeepsArtifactPaths(t *testing.T) {
	repository, _ := newTestRepository(t)
	ctx := context.Background()
	created := newGoogleDocArticle(t, repository)

	if err := repository.SaveGenerationPaths(ctx, created.ID,
		"45-kak-vybrat-frezu/generated/structure.txt",
		"45-kak-vybrat-frezu/generated/article.txt"); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveHTMLPath(ctx, created.ID, "45-kak-vybrat-frezu/article.html"); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveGoogleDocURL(ctx, created.ID, "https://docs.google.com/document/d/AbC123/edit"); err != nil {
		t.Fatal(err)
	}

	input, err := repository.GetResultInput(ctx, "45")
	if err != nil {
		t.Fatal(err)
	}
	if input.ArticlePath != "45-kak-vybrat-frezu/generated/article.txt" {
		t.Fatalf("путь статьи затёрт: %q", input.ArticlePath)
	}
	if input.HTMLPath != "45-kak-vybrat-frezu/article.html" {
		t.Fatalf("путь HTML затёрт: %q", input.HTMLPath)
	}
	if input.GoogleDocURL == "" {
		t.Fatal("адрес документа не сохранён")
	}
}

// Пустой адрес отвергается: он неотличим от «не публиковалось», а затирать рабочую ссылку
// неудачной попыткой нельзя.
func TestSaveGoogleDocURLRejectsEmptyAddress(t *testing.T) {
	repository, _ := newTestRepository(t)
	ctx := context.Background()
	created := newGoogleDocArticle(t, repository)

	for _, empty := range []string{"", "   "} {
		if err := repository.SaveGoogleDocURL(ctx, created.ID, empty); err == nil {
			t.Fatalf("для %q ожидалась ошибка", empty)
		}
	}
}

func TestSaveGoogleDocURLFailsForUnknownArticle(t *testing.T) {
	repository, _ := newTestRepository(t)

	if err := repository.SaveGoogleDocURL(context.Background(), 999999,
		"https://docs.google.com/document/d/AbC123/edit"); err == nil {
		t.Fatal("для несуществующей статьи ожидалась ошибка")
	}
}

// Статья без публикации отдаёт пустой адрес, а не ошибку: раздел в result.md всё равно
// печатается, просто пустым.
func TestGetResultInputReturnsEmptyURLWithoutPublication(t *testing.T) {
	repository, _ := newTestRepository(t)
	created := newGoogleDocArticle(t, repository)
	_ = created

	input, err := repository.GetResultInput(context.Background(), "45")
	if err != nil {
		t.Fatal(err)
	}
	if input.GoogleDocURL != "" {
		t.Fatalf("ожидался пустой адрес, получен %q", input.GoogleDocURL)
	}
}

// clear уносит адрес вместе с остальным состоянием статьи.
func TestClearRemovesGoogleDocURL(t *testing.T) {
	repository, pool := newTestRepository(t)
	ctx := context.Background()
	created := newGoogleDocArticle(t, repository)
	if err := repository.SaveGoogleDocURL(ctx, created.ID, "https://docs.google.com/document/d/AbC123/edit"); err != nil {
		t.Fatal(err)
	}

	if err := repository.ClearArticleState(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	assertCount(t, pool, "article_outputs", fmt.Sprintf("article_id = %d", created.ID), 0)
}
