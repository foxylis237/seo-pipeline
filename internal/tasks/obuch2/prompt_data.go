package obuch2

import (
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

// Поля шаблонов промптов obuch_2. Набор полей структуры и набор плейсхолдеров в шаблоне
// обязаны совпадать: несовпадение даёт `<no value>` в готовом промпте, а не ошибку.

// structureData — поля structure.txt.
//
// Скелет страницы в полях не участвует: он зашит в сам промпт. Чат 1 заполняет в нём три
// вещи — формулировки заголовков, список модулей и портреты аудитории, — и для этого ему
// нужны только название страницы и структура конкурентов.
func structureData(input article.GenerationInput) any {
	return struct {
		Title     string
		Structure string
	}{input.Article.Title, input.CompetitorStructure}
}

// articleData — поля основного промпта article.txt.
//
// Промпт уходит в модель: страницу пишет он. Тот же отрендеренный текст сохраняется в
// prompts/article_prompt.txt — второго «базового промпта» у задачи нет, и расходиться им негде.
//
// Числа программы приходят отдельными полями, а не одной строкой фактов: промпт подставляет
// каждое в своё место — плашку параметров, раздел документов, последний модуль, — и
// склеенная строка заставила бы модель разбирать её обратно.
func articleData(input article.GenerationInput, structure string) any {
	return struct {
		Title              string
		Profession         string
		Keywords           string
		LSIWords           string
		GeneratedStructure string
		Hours              string
		Duration           string
		Price              string
		Document           string
		Attestation        string
	}{
		Title:              input.Article.Title,
		Profession:         programFact(input.Profession, "Профессия в книге не указана — бери её из названия страницы."),
		Keywords:           article.FormatKeywords(input.WordstatKeywords),
		LSIWords:           strings.Join(input.LSIWords, "\n"),
		GeneratedStructure: structure,
		Hours:              programFact(input.Hours, missingNumber("объём программы в академических часах")),
		Duration:           programFact(input.Duration, missingNumber("срок обучения")),
		Price:              programFact(input.Price, missingNumber("стоимость")),
		Document:           programFact(input.Document, missingNumber("документ по итогам обучения")),
		Attestation:        programFact(input.Attestation, missingNumber("форма итоговой аттестации")),
	}
}

// programFact подставляет значение колонки книги или явный запрет вместо него.
//
// Пустое место в промпте опаснее отсутствия строки: незаполненный параметр модель читает как
// приглашение придумать число, а прямой запрет она читает как запрет. Ровно по этой причине
// так же устроен раздел преподавателя у соседней задачи.
func programFact(value, absent string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return absent
}

// missingNumber — текст на месте незаполненной числовой колонки.
//
// Числа программы стоят и в тексте страницы, и в полях записи, которые человек заполняет
// руками. Выдуманное моделью число разошлось бы с админкой, а заметить это можно только
// глазами на самой странице.
func missingNumber(what string) string {
	return "не указан (" + what + ") — этого параметра в плашке и в тексте быть не должно, придумывать его нельзя"
}

// reviewData — поля review.txt: редактура получает написанную страницу целиком.
//
// Страница передаётся явно, хотя ревью — второе сообщение того же чата и текст уже в истории:
// промпт этого ждёт и обязан править именно то, что сохранено артефактом, а не свой пересказ.
// Ключей и LSI здесь нет: они пришли в первом сообщении этого же чата, и второй их копией
// промпт можно было бы только рассинхронизировать.
func reviewData(page string) any {
	return struct {
		Article string
	}{page}
}

// htmlData — поля html.txt. Здесь страница передаётся явно: чат 3 начинается с чистой
// истории и текста ещё не видел.
//
// Ссылок в поле нет намеренно: на всех просмотренных живых страницах площадки в теле нет ни
// одной внутренней ссылки, и промпт прямо запрещает любые. Колонки links у задачи нет вовсе.
func htmlData(finalText string) any {
	return struct {
		Article string
	}{finalText}
}
