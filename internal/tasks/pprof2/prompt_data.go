package pprof2

import (
	"fmt"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

// Поля шаблонов промптов pprof_2. Набор полей структуры и набор плейсхолдеров в шаблоне
// обязаны совпадать: несовпадение даёт `<no value>` в готовом промпте, а не ошибку.

// structureData — поля structure.txt.
func structureData(input article.GenerationInput) any {
	return struct {
		Title     string
		Structure string
	}{input.Article.Title, input.CompetitorStructure}
}

// articleData — поля основного промпта article.txt.
//
// В отличие от pprof_1 этот промпт уходит в модель: страницу пишет он. Тот же отрендеренный
// текст сохраняется в prompts/article_prompt.txt и выгружается в Google Docs — второго
// «базового промпта» у задачи нет, и расходиться им негде.
//
// Раздел «дополнительные факты» промпта — цена, особые условия, смежные программы — среди
// полей нет намеренно: этих данных в книге импорта нет, а промпт запрещает их придумывать.
// Сам сборщик раздела никуда не делся, это ServiceFacts ниже: вернуть его значит дописать
// поле сюда и плейсхолдер в промпт.
func articleData(input article.GenerationInput, structure string) any {
	return struct {
		Title              string
		Keywords           string
		LSIWords           string
		GeneratedStructure string
		Teachers           string
	}{
		Title:              input.Article.Title,
		Keywords:           article.FormatKeywords(input.WordstatKeywords),
		LSIWords:           strings.Join(input.LSIWords, "\n"),
		GeneratedStructure: structure,
		Teachers:           teacherFacts(input.Teachers),
	}
}

// teacherFacts готовит раздел «преподаватель» основного промпта.
//
// Промпт требует блок о преподавателе всегда и строит его на этих данных, поэтому пустое
// место здесь опаснее отсутствия раздела: незаполненный раздел модель читает как приглашение
// придумать имя, должность и стаж — то есть выдумать живого человека на боевой странице.
// Прямой запрет она читает как запрет.
func teacherFacts(teachers string) string {
	if strings.TrimSpace(teachers) == "" {
		return "Данных о преподавателе нет. Блок о преподавателе не пиши, имён и регалий не придумывай."
	}
	return teachers
}

// reviewData — поля review.txt: редактура получает написанную страницу целиком.
//
// Страница передаётся явно, хотя ревью — второе сообщение того же чата и текст уже в истории:
// промпт этого ждёт («Статья:») и обязан править именно то, что сохранено артефактом, а не
// свой пересказ. Ключей и LSI здесь нет: они пришли в первом сообщении этого же чата, и
// второй их копией промпт можно было бы только рассинхронизировать.
func reviewData(_ article.GenerationInput, page string) any {
	return struct {
		Article string
	}{page}
}

// htmlData — поля html.txt. Здесь страница передаётся явно: чат 3 начинается с чистой
// истории и текста ещё не видел.
//
// Ссылок в поле нет намеренно: страница услуги выходит без перелинковки, и промпт прямо
// запрещает любые ссылки — внутренние и внешние. Колонка links книги в промпт pprof_2 не
// уходит; передавать её сюда «на всякий случай» значило бы держать поле, которого нет в
// шаблоне, а расхождение полей и плейсхолдеров ловится только глазами.
func htmlData(_ article.GenerationInput, finalText string) any {
	return struct {
		Article string
	}{finalText}
}

// ServiceFacts собирает раздел «факты об услуге» основного промпта.
//
// Сегодня промпт этот раздел не печатает — пункт 5 оставлен пустым, пока фактов об услуге в
// книге импорта нет. Функция остаётся готовой: она описывает, как эти факты обязаны выглядеть,
// когда вернутся, и проверена тестами.
//
// Источник — строка книги импорта, а не выдумка модели: раздел и категория задают место
// страницы в каталоге, профессия и преподаватели — то, о чём и от чьего имени она написана,
// H1 и мета-описание — уже утверждённые человеком формулировки. Промпт прямо запрещает
// придумывать факты, поэтому пустые поля сюда не попадают вовсе: строка «Стоимость: » была бы
// приглашением её заполнить.
//
// Порядок полей фиксирован и не зависит от того, какие из них заполнены: промпт — вход
// модели, и одинаковые данные обязаны давать одинаковый текст.
func ServiceFacts(input article.ResultInput) string {
	fields := []struct {
		name  string
		value string
	}{
		{"Раздел каталога", input.Section},
		{"Категория", input.Category},
		{"Профессия", input.Profession},
		{"Преподаватели", input.Teachers},
		{"Заголовок H1, утверждённый человеком", input.Header},
		{"Мета-описание, утверждённое человеком", input.MetaDescription},
		{"Фокусное ключевое слово", input.Keyword},
	}
	var facts strings.Builder
	for _, field := range fields {
		value := strings.TrimSpace(field.value)
		if value == "" {
			continue
		}
		if facts.Len() > 0 {
			facts.WriteByte('\n')
		}
		fmt.Fprintf(&facts, "%s: %s", field.name, value)
	}
	if facts.Len() == 0 {
		// Явная строка, а не пустое место: пустой раздел промпта модель читает как приглашение
		// придумать факты, а прямой запрет — как запрет.
		return "Дополнительных данных об услуге нет. Не придумывай сроки, цены, документы и нормативные требования."
	}
	return facts.String()
}
