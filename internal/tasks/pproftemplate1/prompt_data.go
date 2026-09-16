package pproftemplate1

import (
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

// Поля шаблонов промптов pprof_template_1. Набор полей структуры и набор плейсхолдеров в
// шаблоне обязаны совпадать: несовпадение даёт `<no value>` в готовом промпте, а не ошибку.

// structureData — поля structure.txt. Эталон сюда не попадает: он приходит документом.
func structureData(input article.GenerationInput) any {
	return struct {
		Title     string
		Structure string
	}{input.Article.Title, input.CompetitorStructure}
}

// articleData — поля базового article.txt.
//
// Промпт собирается полностью, из тех же входных данных и research, что и у pprof_1, но в
// модель не уходит: он нужен как артефакт и как документ выгрузки.
func articleData(input article.GenerationInput, structure string) any {
	return struct {
		Title              string
		Keywords           string
		LSIWords           string
		GeneratedStructure string
	}{
		Title:              input.Article.Title,
		Keywords:           article.FormatKeywords(input.WordstatKeywords),
		LSIWords:           strings.Join(input.LSIWords, "\n"),
		GeneratedStructure: structure,
	}
}

// expertSEOData — поля 1_expert_seo.txt.
//
// Ключи и LSI здесь есть, в отличие от pprof_1: редактура этой задачи идёт отдельным чатом и
// правит смысл, а не SEO, — значит применить запросы обязан тот, кто пишет текст.
func expertSEOData(input article.GenerationInput, structure string) any {
	return struct {
		Title              string
		GeneratedStructure string
		Keywords           string
		LSIWords           string
	}{
		Title:              input.Article.Title,
		GeneratedStructure: structure,
		Keywords:           article.FormatKeywords(input.WordstatKeywords),
		LSIWords:           strings.Join(input.LSIWords, "\n"),
	}
}

// reviewData — поля 2_review.txt. Статья передаётся текстом: чат 3 начинается с чистой
// истории и черновика ещё не видел.
//
// Ключей и LSI здесь нет намеренно. Редактура правит смысл; список запросов перед глазами
// превращает её в дополнительную SEO-оптимизацию, а плотность ключей уже задана стадией,
// которая писала текст.
func reviewData(draft string) any {
	return struct {
		Article string
	}{draft}
}

// htmlData — поля 4_html.txt. Статья передаётся явно: чат 4 тоже начинается с чистой истории.
//
// Перелинковка идёт из Links, а не из Professions: в Professions лежит список слов-меток,
// адресов там нет вовсе.
func htmlData(finalText, links string) any {
	return struct {
		Article string
		Links   string
	}{finalText, links}
}
