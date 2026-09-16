package catalog

import (
	"html"
	"regexp"
	"strings"
	"unicode"
)

// Разбор названия услуги в профессию.
//
// Названия на площадке устроены шаблонно: «Сварщик — дистанционное обучение с внесением в
// ФИС ФРДО», «Монтажник 5 разряда», «Санитар». До первого тире стоит собственно услуга,
// после — как она устроена, и это одинаково у всех полутора тысяч страниц. Поэтому
// профессия выводится кодом, а не спрашивается у модели: разбор воспроизводим, повторный
// сбор даёт тот же результат, а ошибки видно списком и правятся строкой в таблице.

// titleSeparator — тире, отделяющее название услуги от её описания. Пробелы обязательны:
// в «Кузнец-штамповщик» дефис часть слова, и резать по нему нельзя.
var titleSeparator = regexp.MustCompile(`\s+[-–—]\s+`)

// parenthesized — уточнение в скобках: «(высший разряд)», «(MIG/MAG)». К профессии не
// относится и мешает разбору.
var parenthesized = regexp.MustCompile(`\([^)]*\)`)

// rankSuffix — «5 разряда», «6 разряд». Разряд — уровень одной и той же профессии, а не
// другая профессия: сварщик второго и шестого разряда обязаны попасть в одну рубрику.
var rankSuffix = regexp.MustCompile(`(?i)\d+\s*разряд\w*`)

// serviceWords — слова, которые говорят о форме услуги, а не о профессии.
//
// Они встречаются в начале названия («Дистанционное обучение на токаря») и увели бы разбор
// в рубрику «дистанционное», где оказалась бы половина каталога.
var serviceWords = map[string]struct{}{
	"дистанционное": {}, "дистанционного": {}, "обучение": {}, "обучения": {}, "курс": {},
	"курсы": {}, "профессия": {}, "профессии": {}, "переподготовка": {}, "переподготовки": {},
	"повышение": {}, "повышения": {}, "квалификации": {}, "аттестация": {}, "аттестации": {},
	"на": {}, "по": {}, "для": {}, "с": {}, "и": {}, "в": {}, "от": {}, "до": {},
	"внесением": {}, "фис": {}, "фрдо": {}, "программа": {}, "программе": {},
}

// adjectiveEndings — окончания прилагательных.
//
// Название вроде «Промышленный альпинист» или «Радиационная безопасность» начинается с
// признака, а само дело стоит следующим словом. Без этой проверки рубрикой стало бы
// «промышленный», куда сошлись бы альпинист, электрик и всё остальное промышленное.
var adjectiveEndings = []string{"ый", "ий", "ой", "ая", "яя", "ое", "ее", "ые", "ие"}

// agentEndings — окончания слов, называющих человека по занятию.
//
// Ими разделяются два вида услуг площадки, и разделение это не косметическое. «Сварщик»,
// «Монтажник», «Охранник» — профессия, и рубрикой обязано стать одно слово: сварщик
// шестого разряда и сварщик трубопроводов — один и тот же сварщик. «Радиационная
// безопасность», «Маркшейдерское дело», «Организация перевозок» — направление, и одного
// слова там мало: «дело» само по себе не рубрика, под ним сошлись бы банковское,
// библиотечное и сестринское.
var agentEndings = []string{"щик", "чик", "ник", "тель", "лог", "ист", "арь", "ар", "ер",
	"ёр", "ор", "ач", "ец", "ант", "ент", "мен", "вод", "ир", "сестра", "врач", "граф"}

// nounEndings — окончания, которые снимаются при приведении слова к основе.
//
// Это не морфология русского языка, а огрубление: колонка professions книги заполняется
// человеком в произвольном падеже («сантехник», «сантехника», «сантехники»), и сойтись эти
// формы обязаны на одной строке каталога. Порядок важен — длинные окончания идут первыми.
var nounEndings = []string{"ами", "ями", "ам", "ям", "ов", "ев", "ей", "ах", "ях", "ом", "ем",
	"ой", "ы", "и", "а", "я", "у", "ю", "е"}

// minStemLength — короче этого слово не режется. «Врача» → «врач», но «дело» осталось бы
// без половины себя, а «повар» и «токарь» и без того основы.
const minStemLength = 5

// ShortName возвращает короткое название услуги — то, что стоит до тире.
//
// Оно же годится анкором перелинковки: «Сварщик» вместо «Сварщик — дистанционное обучение
// с внесением в ФИС ФРДО».
func ShortName(title string) string {
	cleaned := strings.TrimSpace(html.UnescapeString(title))
	parts := titleSeparator.Split(cleaned, 2)
	return strings.TrimSpace(parts[0])
}

// ProfessionOf разбирает название услуги в профессию — нашу рубрику каталога.
//
// Пустая профессия означает, что разобрать не удалось: в названии не осталось ни одного
// значимого слова. Такая услуга в подбор не попадает — предлагать читателю «услугу без
// профессии» хуже, чем не предлагать ничего.
func ProfessionOf(title string) Profession {
	words := headWords(significantWords(ShortName(title)))
	if len(words) == 0 {
		return Profession{}
	}
	stems := make([]string, 0, len(words))
	for _, word := range words {
		stem := Stem(word)
		if stem == "" {
			return Profession{}
		}
		stems = append(stems, stem)
	}
	key := strings.Join(stems, " ")
	return Profession{
		Slug:    transliterate(strings.Join(stems, "-")),
		Name:    capitalize(strings.Join(words, " ")),
		Aliases: []string{key},
	}
}

// headWords выбирает из названия слова, которые и составят рубрику.
//
// Одно слово, если услуга названа профессией; два — если направлением, и тогда к нему
// присоединяется определяющее прилагательное («радиационная безопасность») или следующее
// существительное («организация перевозок»). Порядок слов сохраняется — из них потом
// собирается имя рубрики, которое читает человек.
func headWords(words []string) []string {
	if len(words) == 0 {
		return nil
	}
	noun := 0
	for noun < len(words) && isAdjective(words[noun]) {
		noun++
	}
	// Все слова оказались признаками — берём первое, другого названия у услуги нет.
	if noun == len(words) {
		return words[:1]
	}
	if isAgent(words[noun]) {
		return []string{words[noun]}
	}
	if noun > 0 {
		return []string{words[noun-1], words[noun]}
	}
	if len(words) > 1 {
		return words[:2]
	}
	return words[:1]
}

// MatchKeys приводит перечисление профессий из книги импорта к основам слов.
//
// Колонка выглядит как «сантехник, слесарь, монтажник, трубопроводчик, водопроводчик»;
// порядок в ней значим — первой человек пишет главную профессию статьи, и подбор начинает
// с неё. Повторы снимаются, порядок сохраняется.
func MatchKeys(professions string) []string {
	var keys []string
	seen := make(map[string]struct{})
	for _, part := range strings.FieldsFunc(professions, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '/'
	}) {
		profession := ProfessionOf(part)
		for _, alias := range profession.Aliases {
			if _, found := seen[alias]; found {
				continue
			}
			seen[alias] = struct{}{}
			keys = append(keys, alias)
		}
	}
	return keys
}

// isAgent отвечает, называет ли слово человека по занятию, а не направление работы.
func isAgent(word string) bool {
	for _, ending := range agentEndings {
		if len([]rune(word)) > len([]rune(ending))+1 && strings.HasSuffix(word, ending) {
			return true
		}
	}
	return false
}

// Stem огрубляет слово до основы, по которой сходятся его падежи.
func Stem(word string) string {
	word = strings.ToLower(strings.TrimSpace(word))
	if word == "" {
		return ""
	}
	runes := []rune(word)
	for _, ending := range nounEndings {
		suffix := []rune(ending)
		if len(runes)-len(suffix) < minStemLength {
			continue
		}
		if strings.HasSuffix(word, ending) {
			return string(runes[:len(runes)-len(suffix)])
		}
	}
	return word
}

// significantWords разбирает название на слова, отбросив служебные и уточняющие.
func significantWords(title string) []string {
	cleaned := parenthesized.ReplaceAllString(html.UnescapeString(title), " ")
	cleaned = rankSuffix.ReplaceAllString(cleaned, " ")
	var words []string
	for _, word := range strings.FieldsFunc(cleaned, func(r rune) bool {
		return !unicode.IsLetter(r) && r != '-'
	}) {
		lower := strings.ToLower(strings.Trim(word, "-"))
		if lower == "" {
			continue
		}
		if _, service := serviceWords[lower]; service {
			continue
		}
		words = append(words, lower)
	}
	return words
}

func isAdjective(word string) bool {
	for _, ending := range adjectiveEndings {
		if len([]rune(word)) > len([]rune(ending))+2 && strings.HasSuffix(word, ending) {
			return true
		}
	}
	return false
}

func capitalize(word string) string {
	runes := []rune(word)
	if len(runes) == 0 {
		return ""
	}
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

// translitTable — та же таблица, по которой слаги собирает сама площадка: «сварщик» там
// стал svarshhik, «сантехник» — santehnik. Совпадение не обязательно, но расхождение
// путало бы человека, который сверяет каталог с сайтом глазами.
var translitTable = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "jo", 'ж': "zh",
	'з': "z", 'и': "i", 'й': "j", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o",
	'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "h", 'ц': "c",
	'ч': "ch", 'ш': "sh", 'щ': "shh", 'ъ': "", 'ы': "y", 'ь': "", 'э': "je", 'ю': "ju",
	'я': "ja",
}

func transliterate(word string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(word) {
		if replacement, found := translitTable[r]; found {
			builder.WriteString(replacement)
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
			continue
		}
		builder.WriteRune('-')
	}
	return strings.Trim(builder.String(), "-")
}
