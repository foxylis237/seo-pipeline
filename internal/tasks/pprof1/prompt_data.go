package pprof1

import (
	"fmt"
	"strings"
	"unicode"

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
// регламент, приложенный к первому сообщению. Объём считает код: свой текст модель
// не меряет, и за лимит уходила почти половина статей.
func editorData(input article.GenerationInput, draft string) any {
	return struct {
		Keywords string
		LSIWords string
		Volume   string
	}{article.FormatKeywords(input.WordstatKeywords), strings.Join(input.LSIWords, "\n"), volumeTask(articleVolume(draft))}
}

// Норма объёма статьи в знаках без пробелов — та же, что в регламенте.
const (
	minArticleVolume = 15000
	maxArticleVolume = 18000
)

// articleVolume — знаки без пробелов, как их считает регламент.
func articleVolume(text string) int {
	n := 0
	for _, r := range text {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}

// volumeTask — задание редактуре по объёму черновика.
func volumeTask(volume int) string {
	switch {
	case volume > maxArticleVolume:
		cut := volume - (minArticleVolume+maxArticleVolume)/2
		return fmt.Sprintf("Объём статьи сейчас %d знаков без пробелов — больше нормы %d–%d. Подрежь её примерно на %d знаков (около %d%% текста): пиши лаконичнее, не теряя экспертности. Убирай повторы, пересказ одного и того же разными словами, вводные разгоны и общие фразы; длинное предложение сжимай до сути. Цифры, нормативы, профессиональные детали, таблицы, заметки и FAQ сохраняй.",
			volume, minArticleVolume, maxArticleVolume, cut, cut*100/volume)
	case volume < minArticleVolume:
		return fmt.Sprintf("Объём статьи сейчас %d знаков без пробелов — меньше нормы %d–%d. Допиши примерно %d знаков конкретики в разделы, где её не хватает: параметры, нормативы, разбор типичной ошибки. Воду и новые разделы ради объёма не добавляй.",
			volume, minArticleVolume, maxArticleVolume, minArticleVolume-volume)
	default:
		return fmt.Sprintf("Объём статьи сейчас %d знаков без пробелов — в норме %d–%d. Правки не должны увести его за эти границы.",
			volume, minArticleVolume, maxArticleVolume)
	}
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
