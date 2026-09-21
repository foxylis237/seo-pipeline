package articleaudit

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// FieldCheck — что код нашёл в полях записи до всякой модели.
//
// Два списка, а не один: поля, которого в записи нет вовсе, и поля пустого — это разные
// болезни. Первое означает, что поле не заведено в теме или в шаблоне записи, второе — что
// его забыли заполнить. Лечатся они по-разному, и сводить их в одну строку отчёта нельзя.
type FieldCheck struct {
	// Missing — обязательных полей нет в записи вовсе.
	Missing []string
	// Empty — поля есть, но пусты.
	Empty []string
	// TooLong — SEO-поля, которые не влезают в выдачу. Проверка независимая: длина не про
	// обязательность, и переполненный заголовок бывает у записи, где заполнено всё.
	TooLong []LengthMeasure
	// Measured — длина полей выдачи целиком, включая те, что в пределах. Нужна плану: он
	// показывает запас, а не только нарушения.
	Measured []LengthMeasure
	// FAQ — блок частых вопросов: сколько вопросов нашлось и сколько требует задача.
	//
	// Норму называет профиль, а не движок: у статьи блога она есть, у страницы услуги её
	// никто не назначал. Нулевой минимум означает «считаем, но не судим» — прежнее
	// поведение, при котором человек смотрит на число сам.
	FAQ FAQCheck
	// Links — перелинковка в теле страницы: сколько внутренних ссылок нашлось и сколько их
	// требует задача. В отличие от вопросов здесь есть норма, и её называет профиль: статья
	// блога без ссылок на программы не ведёт читателя никуда, а у страницы услуги
	// перелинковки может не быть вовсе.
	Links LinkCheck
}

// RequiredProblems — сколько обязательных полей не в порядке.
//
// Считаются только незаведённые и незаполненные: это счётчик обязательности, и он идёт в
// свою колонку базы. Переполненная длина сюда не входит — она к обязательности отношения не
// имеет, а сложенные вместе два разных счёта не расшифровать обратно.
func (c FieldCheck) RequiredProblems() int { return len(c.Missing) + len(c.Empty) }

// OK отвечает, чисто ли в полях: и обязательные заполнены, и длина в пределах.
func (c FieldCheck) OK() bool { return c.RequiredProblems() == 0 && len(c.TooLong) == 0 }

// Проверяемое, что полем записи не является.
//
// Рубрика, обложка и название записи живут не в custom fields, а в самой записи, — но для
// человека это такие же графы, которые бывают не заполнены. Поэтому они называются в том же
// списке обязательного, что и поля, и проверяются вместе с ними.
const (
	// RecordCategory — рубрика записи, любая таксономия площадки.
	RecordCategory = "category"
	// RecordTitle — название записи, то, что видно в списке админки.
	RecordTitle = "post_title"
	// RecordThumbnail — обложка записи.
	RecordThumbnail = "post_thumbnail"
	// RecordTags — метки записи. Отдельно от рубрики: рубрика у записи одна и обязательна
	// у всех, метки площадка заводит по требованию, и пустой их список — своя находка.
	RecordTags = tagTaxonomy
)

// tagTaxonomy — таксономия меток WordPress. Рубрика у каждого типа записи своя
// («category» у статьи блога, «cat_rabprof» у услуги), а метки у всех одни и те же, поэтому
// рубрикой считается любой термин любой другой таксономии.
const tagTaxonomy = "post_tag"

// fieldLabels переводит имя проверяемого в то, как его называет человек.
//
// Нужны отчёту: «рубрика записи не задана» человек понимает сразу, а «поле category не
// заполнено» заставляет вспоминать, что такое category и где оно в админке.
var fieldLabels = map[string]string{
	RecordCategory:          "рубрика записи",
	RecordTitle:             "название записи",
	RecordThumbnail:         "обложка записи",
	RecordTags:              "метки записи",
	"prof_title":            "видимый заголовок страницы",
	"prof_name":             "название профессии в карточке",
	"blog_tldr":             "краткое содержание",
	"blog_read":             "время чтения",
	"author_link":           "автор статьи",
	"related_courses":       "связанные курсы",
	"teachers":              "преподаватели",
	"faq_loop":              "блок частых вопросов",
	SEOTitleField:           "SEO-заголовок",
	SEOMetaDescriptionField: "мета-описание",
	"_yoast_wpseo_focuskw":  "фокусное слово",
	"image_alt":             "подпись обложки",
}

// Label — как назвать проверяемое в отчёте.
func Label(name string) string {
	if label, known := fieldLabels[name]; known {
		return label + " (" + name + ")"
	}
	return "поле " + name
}

// CheckRequired проверяет заполненность обязательного в записи.
//
// Это делает код, а не модель: значение либо пустое, либо нет — факт, а не суждение.
// Спрашивать такое у модели значило бы платить за ответ, который она вдобавок даст неверно,
// потому что «пусто» она путает с «коротко».
//
// Принимается вся запись, а не одни custom fields: рубрика, обложка и название записи полями
// не являются, но человек ждёт их в том же списке. Пустой список обязательного — законное
// состояние: проверка выключена, пока набор не назван. Пустым считается значение из одних
// пробелов: в админке оно выглядит заполненным, а на странице не даёт ничего.
func CheckRequired(post Post, required []string) FieldCheck {
	var check FieldCheck
	for _, name := range required {
		switch name {
		case RecordCategory:
			if !hasCategory(post.TermIDs) {
				check.Empty = append(check.Empty, name)
			}
		case RecordTags:
			if len(post.TermIDs[tagTaxonomy]) == 0 {
				check.Empty = append(check.Empty, name)
			}
		case RecordTitle:
			if strings.TrimSpace(post.Title) == "" {
				check.Empty = append(check.Empty, name)
			}
		case RecordThumbnail:
			if post.ThumbnailID == 0 {
				check.Empty = append(check.Empty, name)
			}
		default:
			value, present := post.Fields[name]
			switch {
			case !present:
				check.Missing = append(check.Missing, name)
			case strings.TrimSpace(value) == "":
				check.Empty = append(check.Empty, name)
			}
		}
	}
	return check
}

// hasCategory отвечает, лежит ли запись хоть в одной рубрике.
//
// Рубрикой считается термин любой таксономии, кроме меток: имя таксономии у каждого типа
// записи своё («category» у статьи блога, «cat_rabprof» у услуги), и перечислять их здесь
// значило бы держать вторую копию списка, который живёт у каталога площадки.
func hasCategory(terms map[string][]int64) bool {
	for taxonomy, ids := range terms {
		if taxonomy != tagTaxonomy && len(ids) > 0 {
			return true
		}
	}
	return false
}

// Поля выдачи и их пределы.
//
// Пределы взяты из того, как выдача режет сниппет: заголовок примерно на шестидесяти знаках,
// описание — примерно на ста шестидесяти. Числа приблизительные по своей природе — поисковик
// считает пиксели, а не буквы, — поэтому проверка ловит явный перебор, а не борется за
// последний знак. Смысл её в том, что длину человек на глаз не отмеряет, а обрезанное на
// середине фразы описание он увидит только в выдаче и через недели.
//
// Считает это код, а не модель: длина — факт. Модель же, если её спросить, отвечает про
// количество знаков наугад и уверенно.
const (
	// SEOTitleField — заголовок записи для выдачи.
	SEOTitleField = "_yoast_wpseo_title"
	// SEOMetaDescriptionField — описание записи для выдачи.
	SEOMetaDescriptionField = "_yoast_wpseo_metadesc"
	// SEOTitleLimit — предел заголовка в знаках.
	SEOTitleLimit = 60
	// SEOMetaDescriptionLimit — предел описания в знаках.
	SEOMetaDescriptionLimit = 160
)

// LengthMeasure — измеренная длина поля выдачи.
type LengthMeasure struct {
	Field  string
	Length int
	Limit  int
}

// Fits отвечает, помещается ли поле в выдачу.
func (m LengthMeasure) Fits() bool { return m.Length <= m.Limit }

// Filled отвечает, заполнено ли поле вообще.
func (m LengthMeasure) Filled() bool { return m.Length > 0 }

// seoLimits — какие поля и по какому пределу проверяются.
var seoLimits = []struct {
	field string
	limit int
}{
	{SEOTitleField, SEOTitleLimit},
	{SEOMetaDescriptionField, SEOMetaDescriptionLimit},
}

// MeasureSEO измеряет поля выдачи — все, независимо от того, влезают они или нет.
//
// Длина считается в знаках, а не в байтах: тексты русские, и в байтах кириллица вдвое
// длиннее — проверка объявляла бы перебор у половины нормальных описаний.
//
// Возвращаются и те поля, что в пределах: план показывает запас, а не только нарушения.
// Человеку важно видеть «58 из 60» — такой заголовок формально проходит, но следующая правка
// его переполнит.
func MeasureSEO(fields map[string]string) []LengthMeasure {
	measures := make([]LengthMeasure, 0, len(seoLimits))
	for _, limit := range seoLimits {
		value := strings.TrimSpace(fields[limit.field])
		measures = append(measures, LengthMeasure{
			Field: limit.field, Length: len([]rune(value)), Limit: limit.limit,
		})
	}
	return measures
}

// CheckLength находит поля выдачи, которые в неё не помещаются.
//
// Пустое поле не проверяется: незаполненность — вопрос обязательности, и о ней отвечает
// CheckRequired. Иначе одно и то же поле попадало бы в отчёт дважды с разными диагнозами.
func CheckLength(fields map[string]string) []LengthMeasure {
	var problems []LengthMeasure
	for _, measure := range MeasureSEO(fields) {
		if measure.Filled() && !measure.Fits() {
			problems = append(problems, measure)
		}
	}
	return problems
}

// FAQScheme — как площадка называет поля блока частых вопросов и сколько их обязано быть.
//
// Имена полей разные у разных тем: у статьи блога это blog_faq_0_question, у страницы услуги —
// faq_loop_0_faq_question. Знать об этом обязан профиль задачи, а не движок: ошибись здесь — и
// проверка молча увидит блок пустым там, где он заполнен.
//
// Min — норма, и она тоже приходит из профиля. Нулевая означает «считаем, но не судим»:
// сколько вопросов должно быть, решает человек, а не эта проверка.
type FAQScheme struct {
	// Question и Answer — форматы имён полей с одним %d, номером вопроса.
	Question string
	Answer   string
	// Min — сколько заполненных вопросов задача считает минимумом.
	Min int
}

// Формат полей по умолчанию — репитер ACF faq_loop, как он заведён на страницах услуг.
// Задача, которая о схеме не знает, ведёт себя как раньше.
const (
	defaultFAQQuestion = "faq_loop_%d_faq_question"
	defaultFAQAnswer   = "faq_loop_%d_faq_answer"
)

// withDefaults подставляет прежние имена полей вместо незаполненных.
func (s FAQScheme) withDefaults() FAQScheme {
	if strings.TrimSpace(s.Question) == "" {
		s.Question = defaultFAQQuestion
	}
	if strings.TrimSpace(s.Answer) == "" {
		s.Answer = defaultFAQAnswer
	}
	return s
}

// questionRE собирает из формата имени выражение, которым вопросы узнаются в полях записи.
//
// Формат экранируется целиком, и только %d становится числом: имена полей содержат
// подчёркивания, а завтра могут содержать точку — регулярным выражением формат не является.
func (s FAQScheme) questionRE() *regexp.Regexp {
	format := s.withDefaults().Question
	parts := strings.SplitN(format, "%d", 2)
	if len(parts) != 2 {
		return regexp.MustCompile(`^` + regexp.QuoteMeta(format) + `$`)
	}
	return regexp.MustCompile(`^` + regexp.QuoteMeta(parts[0]) + `(\d+)` + regexp.QuoteMeta(parts[1]) + `$`)
}

// FAQCheck — блок частых вопросов, посчитанный кодом.
type FAQCheck struct {
	Count int
	Min   int
}

// Enabled отвечает, задана ли у задачи норма.
func (c FAQCheck) Enabled() bool { return c.Min > 0 }

// Enough отвечает, набралось ли вопросов до нормы. Без нормы — всегда да: непроверяемое
// число ошибкой быть не может.
func (c FAQCheck) Enough() bool { return !c.Enabled() || c.Count >= c.Min }

// CountFAQ считает вопросы в блоке частых вопросов.
//
// Считаются заполненные вопросы, а не счётчик репитера: счётчик пишет админка, и он переживает
// вычищенный вопрос — тогда в блоке пусто, а в счётчике по-прежнему шесть.
func CountFAQ(fields map[string]string, scheme FAQScheme) FAQCheck {
	check := FAQCheck{Min: scheme.Min}
	pattern := scheme.questionRE()
	for name, value := range fields {
		if pattern.MatchString(name) && strings.TrimSpace(value) != "" {
			check.Count++
		}
	}
	return check
}

// FormatFAQ собирает блок частых вопросов в читаемый вид.
//
// Вопросы идут парами и по порядку номеров, а не по порядку карты: ответ модели сравнивают
// между прогонами, и разный порядок сделал бы два одинаковых промпта разными.
//
// Блок отдаётся отдельно от тела статьи потому, что в теле его нет: вопросы живут полями
// записи, а на странице их рисует тема. Для читателя это часть статьи, и проверять их надо
// вместе с ней.
func FormatFAQ(fields map[string]string, scheme FAQScheme) string {
	type pair struct {
		index            int
		question, answer string
	}
	scheme = scheme.withDefaults()
	pattern := scheme.questionRE()
	pairs := make([]pair, 0, 8)
	for name := range fields {
		match := pattern.FindStringSubmatch(name)
		if match == nil {
			continue
		}
		index, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		question := strings.TrimSpace(fields[name])
		if question == "" {
			continue
		}
		answer := strings.TrimSpace(fields[fmt.Sprintf(scheme.Answer, index)])
		pairs = append(pairs, pair{index: index, question: question, answer: answer})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].index < pairs[j].index })
	var builder strings.Builder
	for _, item := range pairs {
		fmt.Fprintf(&builder, "Вопрос: %s\nОтвет: %s\n\n", item.question, orNothing(item.answer))
	}
	return strings.TrimSpace(builder.String())
}

// orNothing подписывает вопрос, у которого нет ответа: это находка, а не пустое место.
func orNothing(answer string) string {
	if answer == "" {
		return "(ответа нет)"
	}
	return answer
}
