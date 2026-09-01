package pprof1

import (
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

// Поля шаблонов промптов pprof_1. Набор полей структуры и набор плейсхолдеров в шаблоне
// обязаны совпадать: несовпадение даёт `<no value>` в готовом промпте, а не ошибку.

// structureData — поля deepseek/structure.txt.
func structureData(input article.GenerationInput) any {
	return struct {
		Title     string
		Structure string
	}{input.Article.Title, input.CompetitorStructure}
}

// articleData — поля старого базового article.txt.
//
// Промпт собирается полностью, из тех же входных данных и research, что и раньше, но в
// модель не уходит: он нужен как артефакт и как документ в Google Docs.
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

// expertData — поля deepseek/1_expert.txt. Ключей и LSI здесь нет намеренно: специалист
// пишет текст по структуре, а SEO-требования применяет стадия редактуры.
func expertData(input article.GenerationInput, structure string) any {
	return struct {
		Title              string
		GeneratedStructure string
	}{input.Article.Title, structure}
}

// editorData — поля 2_editor.txt. Статью стадия не получает: она уже в чате, как и
// регламент, приложенный к первому сообщению.
func editorData(input article.GenerationInput) any {
	return struct {
		Keywords string
		LSIWords string
	}{article.FormatKeywords(input.WordstatKeywords), strings.Join(input.LSIWords, "\n")}
}

// htmlData — поля deepseek/4_html.txt. Здесь статья передаётся явно: чат 3 начинается с
// чистой истории и текста ещё не видел.
//
// Перелинковка идёт из Links, а не из Professions: в Professions лежит список слов-меток
// («профессии, карьера, зарплата»), адресов там нет вообще, и стадия получала задание
// расставить ссылки без единой ссылки.
func htmlData(input article.GenerationInput, finalText, links string) any {
	return struct {
		Article string
		Links   string
	}{finalText, links}
}
