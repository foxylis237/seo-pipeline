package catalog

import (
	"html"
	"regexp"
	"strings"
	"unicode"
)

// Разбор названия услуги в профессию. Названия шаблонные: до первого тире — услуга
// («Сварщик — дистанционное обучение…»), поэтому разбор делает код, а не модель.

// titleSeparator требует пробелов вокруг тире: в «Кузнец-штамповщик» дефис — часть слова.
var titleSeparator = regexp.MustCompile(`\s+[-–—]\s+`)

var parenthesized = regexp.MustCompile(`\([^)]*\)`)

// rankSuffix снимает разряд: сварщик второго и шестого разряда — одна рубрика.
var rankSuffix = regexp.MustCompile(`(?i)\d+\s*разряд\w*`)

// serviceWords — слова о форме услуги, а не о профессии («Дистанционное обучение на токаря»).
var serviceWords = map[string]struct{}{
	"дистанционное": {}, "дистанционного": {}, "обучение": {}, "обучения": {}, "курс": {},
	"курсы": {}, "профессия": {}, "профессии": {}, "переподготовка": {}, "переподготовки": {},
	"повышение": {}, "повышения": {}, "квалификации": {}, "аттестация": {}, "аттестации": {},
	"на": {}, "по": {}, "для": {}, "с": {}, "и": {}, "в": {}, "от": {}, "до": {},
	"внесением": {}, "фис": {}, "фрдо": {}, "программа": {}, "программе": {},
}

// adjectiveEndings — окончания прилагательных: в «Промышленный альпинист» рубрика —
// следующее слово, а не признак.
var adjectiveEndings = []string{"ый", "ий", "ой", "ая", "яя", "ое", "ее", "ые", "ие"}

// agentEndings — окончания слов, называющих человека по занятию. Профессия («Сварщик»)
// даёт рубрику из одного слова, направление («Маркшейдерское дело») — из двух.
var agentEndings = []string{"щик", "чик", "ник", "тель", "лог", "ист", "арь", "ар", "ер",
	"ёр", "ор", "ач", "ец", "ант", "ент", "мен", "вод", "ир", "сестра", "врач", "граф"}

// nounEndings — окончания, снимаемые огрублением до основы, чтобы падежи колонки
// professions сошлись. Длинные окончания идут первыми.
var nounEndings = []string{"ами", "ями", "ам", "ям", "ов", "ев", "ей", "ах", "ях", "ом", "ем",
	"ой", "ы", "и", "а", "я", "у", "ю", "е"}

// minStemLength — короче этого основа не режется: «врача» → «врач», но «дело» остаётся.
const minStemLength = 5

// ShortName возвращает короткое название услуги — то, что стоит до тире.
func ShortName(title string) string {
	cleaned := strings.TrimSpace(html.UnescapeString(title))
	parts := titleSeparator.Split(cleaned, 2)
	return strings.TrimSpace(parts[0])
}

// ProfessionOf разбирает название услуги в профессию; пустая — разобрать не удалось.
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

// headWords выбирает слова рубрики: одно для профессии, два для направления
// («радиационная безопасность», «организация перевозок»), в исходном порядке.
func headWords(words []string) []string {
	if len(words) == 0 {
		return nil
	}
	noun := 0
	for noun < len(words) && isAdjective(words[noun]) {
		noun++
	}
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

// MatchKeys приводит перечисление профессий из книги импорта к основам слов без повторов.
// Порядок сохраняется: первая профессия — главная, с неё начинается подбор.
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

// translitTable повторяет транслитерацию слагов площадки (svarshhik, santehnik).
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

// industrySeparator делит имя рубрики на профессии («Слесарь и наладчик»); пробелы вокруг
// «и» не дают резать внутри слова.
var industrySeparator = regexp.MustCompile(`\s+и\s+|\s*[,/]\s*`)

// ProfessionFromIndustry берёт профессию из рубрики площадки: слаг и имя термина как есть.
// Для площадок с дробными рубриками, где разбор названий дробит услуги до одиночек.
func ProfessionFromIndustry(industry Industry) Profession {
	slug := strings.TrimSpace(industry.Slug)
	name := strings.TrimSpace(html.UnescapeString(industry.Name))
	if slug == "" || name == "" {
		return Profession{}
	}
	return Profession{Slug: slug, Name: name, Aliases: industryAliases(name)}
}

// industryAliases даёт по синониму на каждую профессию имени рубрики в форме MatchKeys,
// с которой они сравниваются напрямую.
func industryAliases(name string) []string {
	var aliases []string
	seen := make(map[string]struct{})
	for _, part := range industrySeparator.Split(name, -1) {
		words := significantWords(part)
		stems := make([]string, 0, len(words))
		for _, word := range words {
			if stem := Stem(word); stem != "" {
				stems = append(stems, stem)
			}
		}
		if len(stems) == 0 {
			continue
		}
		alias := strings.Join(stems, " ")
		if _, found := seen[alias]; found {
			continue
		}
		seen[alias] = struct{}{}
		aliases = append(aliases, alias)
	}
	return aliases
}
