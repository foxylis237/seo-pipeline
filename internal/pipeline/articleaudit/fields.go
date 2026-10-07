package articleaudit

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// FieldCheck — что код нашёл в полях записи до всякой модели.
// Незаведённое поле (Missing) и пустое (Empty) — разные находки и лечатся по-разному.
type FieldCheck struct {
	Missing []string
	Empty   []string
	// TooLong — SEO-поля, которые не влезают в выдачу, независимо от обязательности.
	TooLong []LengthMeasure
	// Measured — длина всех полей выдачи, включая те, что в пределах: план показывает запас.
	Measured []LengthMeasure
	// FAQ — блок частых вопросов; нулевой минимум означает «считаем, но не судим».
	FAQ FAQCheck
	// Links — перелинковка в теле страницы.
	Links LinkCheck
}

// RequiredProblems — сколько обязательных полей не заведено или не заполнено; переполненная длина сюда не входит.
func (c FieldCheck) RequiredProblems() int { return len(c.Missing) + len(c.Empty) }

// OK отвечает, чисто ли в полях: и обязательные заполнены, и длина в пределах.
func (c FieldCheck) OK() bool { return c.RequiredProblems() == 0 && len(c.TooLong) == 0 }

// Проверяемое, что полем записи не является, но называется в том же списке обязательного.
const (
	// RecordCategory — рубрика записи, любая таксономия площадки.
	RecordCategory = "category"
	// RecordTitle — название записи в списке админки.
	RecordTitle = "post_title"
	// RecordThumbnail — обложка записи.
	RecordThumbnail = "post_thumbnail"
	// RecordTags — метки записи, отдельная от рубрики находка.
	RecordTags = tagTaxonomy
)

// tagTaxonomy — таксономия меток WordPress, общая у всех типов записей.
const tagTaxonomy = "post_tag"

// fieldLabels переводит имя проверяемого в то, как его называет человек в админке.
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

// CheckRequired проверяет заполненность обязательного в записи, включая рубрику, метки, обложку и название.
// Пустой список выключает проверку; значение из одних пробелов считается пустым.
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

// hasCategory считает рубрикой термин любой таксономии, кроме меток: имя таксономии у каждого
// типа записи своё («category», «cat_rabprof»), а их список живёт у каталога площадки.
func hasCategory(terms map[string][]int64) bool {
	for taxonomy, ids := range terms {
		if taxonomy != tagTaxonomy && len(ids) > 0 {
			return true
		}
	}
	return false
}

// Поля выдачи и их пределы — приблизительные: поисковик режет сниппет по пикселям, а не по буквам.
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

var seoLimits = []struct {
	field string
	limit int
}{
	{SEOTitleField, SEOTitleLimit},
	{SEOMetaDescriptionField, SEOMetaDescriptionLimit},
}

// MeasureSEO измеряет в знаках, а не байтах, все поля выдачи, включая те, что в пределах.
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

// CheckLength находит поля выдачи, которые в неё не помещаются; пустые оставлены CheckRequired.
func CheckLength(fields map[string]string) []LengthMeasure {
	var problems []LengthMeasure
	for _, measure := range MeasureSEO(fields) {
		if measure.Filled() && !measure.Fits() {
			problems = append(problems, measure)
		}
	}
	return problems
}

// FAQScheme — как площадка называет поля блока частых вопросов (blog_faq_0_question,
// faq_loop_0_faq_question) и сколько их должно быть.
type FAQScheme struct {
	// Question и Answer — форматы имён полей с одним %d, номером вопроса.
	Question string
	Answer   string
	// Min — минимум заполненных вопросов; ноль — считать, но не судить.
	Min int
}

// Формат полей по умолчанию — репитер ACF faq_loop страниц услуг.
const (
	defaultFAQQuestion = "faq_loop_%d_faq_question"
	defaultFAQAnswer   = "faq_loop_%d_faq_answer"
)

// withDefaults подставляет имена полей по умолчанию вместо незаполненных.
func (s FAQScheme) withDefaults() FAQScheme {
	if strings.TrimSpace(s.Question) == "" {
		s.Question = defaultFAQQuestion
	}
	if strings.TrimSpace(s.Answer) == "" {
		s.Answer = defaultFAQAnswer
	}
	return s
}

// validate checks that both field formats carry exactly one %d.
func (s FAQScheme) validate() error {
	s = s.withDefaults()
	for _, format := range []string{s.Question, s.Answer} {
		if strings.Count(format, "%d") != 1 {
			return fmt.Errorf("формат поля FAQ %q: нужен ровно один %%d — номер вопроса", format)
		}
	}
	return nil
}

// questionRE собирает из формата имени выражение: формат экранируется целиком, только %d становится числом.
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

func (c FAQCheck) Enabled() bool { return c.Min > 0 }

// Enough отвечает, набралось ли вопросов до нормы; без нормы — всегда да.
func (c FAQCheck) Enough() bool { return !c.Enabled() || c.Count >= c.Min }

// CountFAQ считает заполненные вопросы, а не счётчик репитера: счётчик переживает вычищенный вопрос.
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

// FormatFAQ собирает блок частых вопросов парами по порядку номеров, а не карты: промпт должен быть воспроизводим.
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

// orNothing подписывает вопрос без ответа.
func orNothing(answer string) string {
	if answer == "" {
		return "(ответа нет)"
	}
	return answer
}
