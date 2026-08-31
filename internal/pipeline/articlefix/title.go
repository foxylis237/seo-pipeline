package articlefix

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TitleRule — то, как задача переименовывает заголовок.
//
// Интерфейс, а не структура: переименование у задач правки разное, а всё остальное в потоке
// одинаково. Реализация обязана быть идемпотентной — прогон повторяют после обрыва, и второй
// проход не должен делать «с практикой и с практикой и». Заголовок, к которому правило не
// подходит, обязан возвращать ошибку, а не молча остаться прежним: статью с новым текстом под
// старым названием человек не увидит, пока не откроет её в блоге.
type TitleRule interface {
	Apply(title string) (string, error)
}

// PairRule — правило переименования, выведенное из одной пары «было — стало».
//
// Модель заголовок не пишет. Причина не в экономии запроса, а в предсказуемости: правка
// одна и та же во всей пачке («добавить „с практикой“»), и человек уже показал её примером.
// Модель на сотне статей сформулировала бы её сотней разных способов, и проверить результат
// можно было бы только глазами.
//
// Правило хранится как замена подстроки: что искать в заголовке и чем заменить. Границы
// подстроки расширены до целых слов — так замена не срабатывает на середине слова.
type PairRule struct {
	Search  string
	Replace string
}

// NewPairRule выводит правило из примера.
//
// Общее начало и общий конец у пары отбрасываются, остаётся изменившаяся середина; она и
// становится заменой. Чистая вставка (середина в «было» пуста) якорится соседним словом —
// иначе искать в заголовке было бы нечего.
func NewPairRule(from, to string) (PairRule, error) {
	from = collapseSpaces(from)
	to = collapseSpaces(to)
	if from == "" || to == "" {
		return PairRule{}, fmt.Errorf("пример переименования неполон: нужны обе строки, «было» и «стало»")
	}
	if from == to {
		return PairRule{}, fmt.Errorf("пример переименования ничего не меняет: «было» и «стало» совпадают")
	}
	prefix := commonPrefixLen(from, to)
	suffix := commonSuffixLen(from, to, prefix)

	start := prefix
	for start > 0 {
		symbol, size := utf8.DecodeLastRuneInString(from[:start])
		if unicode.IsSpace(symbol) {
			break
		}
		start -= size
	}
	end := len(from) - suffix
	for end < len(from) {
		symbol, size := utf8.DecodeRuneInString(from[end:])
		if unicode.IsSpace(symbol) {
			break
		}
		end += size
	}
	tail := len(from) - end
	search := from[start:end]
	replace := to[start : len(to)-tail]
	if strings.TrimSpace(search) == "" {
		return PairRule{}, fmt.Errorf("по примеру не видно, к чему привязать вставку: "+
			"в «было» нет соседнего слова у места правки (%q → %q)", from, to)
	}
	return PairRule{Search: search, Replace: replace}, nil
}

// Apply применяет правило к заголовку.
//
// Уже переименованный заголовок возвращается как есть: прогон повторяют после обрыва, и
// вторая правка дала бы «с практикой и с практикой и». Отсутствие якоря — ошибка, а не
// молчаливый пропуск: заголовок, который правило не берёт, человек должен увидеть.
func (r PairRule) Apply(title string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", fmt.Errorf("заголовок статьи пуст")
	}
	if strings.Contains(title, r.Replace) {
		return title, nil
	}
	if !strings.Contains(title, r.Search) {
		return "", fmt.Errorf("правило переименования не подходит заголовку %q: в нём нет %q", title, r.Search)
	}
	return strings.Replace(title, r.Search, r.Replace, 1), nil
}

// RuleSet — несколько правил переименования, применяемых к заголовку по очереди.
//
// Одной пары «было — стало» хватает не всегда: одна и та же правка встречается в пачке в
// нескольких видах записи («с внесением в ФИС ФРДО» и «с внесением в ФРДО»), и человек
// показывает их примерами, а не регулярным выражением. Побеждает первое подходящее правило,
// а порядок задаётся не файлом, а длиной якоря: более длинный проверяется раньше, иначе
// короткий откусывал бы часть длинного и результат зависел бы от порядка строк в файле.
type RuleSet []PairRule

// Apply применяет первое подходящее правило.
//
// Уже переименованный заголовок вернёт первое же правило, у которого совпало «стало», —
// набор идемпотентен ровно настолько, насколько идемпотентны его правила.
func (rs RuleSet) Apply(title string) (string, error) {
	if len(rs) == 0 {
		return "", fmt.Errorf("правил переименования нет")
	}
	var anchors []string
	for _, rule := range rs {
		renamed, err := rule.Apply(title)
		if err == nil {
			return renamed, nil
		}
		anchors = append(anchors, fmt.Sprintf("%q", rule.Search))
	}
	// Ошибка называет все якоря сразу: человек правит файл целиком, и знать, какой из
	// примеров не подошёл, ему бесполезно — не подошёл ни один.
	return "", fmt.Errorf("правило переименования не подходит заголовку %q: в нём нет ни %s",
		strings.TrimSpace(title), strings.Join(anchors, ", ни "))
}

// LoadPairRule читает примеры переименования из файла задачи.
//
// Формат — пары строк «было:» и «стало:»; пар может быть несколько, строки с # игнорируются.
// Файл, а не колонка входа: правка одна на всю пачку, и держать её сотней копий в таблице
// значило бы сверять их между собой руками.
//
// Возвращается TitleRule, а не PairRule: одна пара даёт одно правило, несколько — набор, и
// потоку эта разница не видна.
func LoadPairRule(path string) (TitleRule, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("прочитать правило переименования %q: %w", path, err)
	}
	pairs, err := parsePairs(string(content))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var rules RuleSet
	for _, pair := range pairs {
		rule, ruleErr := NewPairRule(pair[0], pair[1])
		if ruleErr != nil {
			return nil, fmt.Errorf("%s: %w", path, ruleErr)
		}
		rules = append(rules, rule)
	}
	// Длинный якорь раньше короткого: «в ФИС ФРДО» обязан проверяться прежде «в ФРДО», иначе
	// заголовок переименовался бы по-разному в зависимости от порядка строк в файле.
	sort.SliceStable(rules, func(i, j int) bool { return len(rules[i].Search) > len(rules[j].Search) })
	if len(rules) == 1 {
		return rules[0], nil
	}
	return rules, nil
}

// parsePairs вынимает из файла пары «было — стало» в порядке появления.
//
// Пара закрывается вторым «стало:»: так файл читается сверху вниз, а не собирается в две
// кучи, и перепутанный порядок строк виден сразу.
func parsePairs(content string) ([][2]string, error) {
	var pairs [][2]string
	var plain []string
	var from string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "было:"):
			if from != "" {
				return nil, fmt.Errorf("у примера %q нет строки «стало:»", from)
			}
			from = strings.TrimSpace(line[len("было:"):])
		case strings.HasPrefix(lower, "стало:"):
			to := strings.TrimSpace(line[len("стало:"):])
			if from == "" {
				return nil, fmt.Errorf("у примера %q нет строки «было:»", to)
			}
			pairs = append(pairs, [2]string{from, to})
			from = ""
		default:
			plain = append(plain, line)
		}
	}
	if from != "" {
		return nil, fmt.Errorf("у примера %q нет строки «стало:»", from)
	}
	// Файл без пометок «было/стало» — две строки подряд. Формат остался от первой задачи
	// правки, и ломать его незачем: он читается так же однозначно.
	if len(pairs) == 0 && len(plain) >= 2 {
		pairs = append(pairs, [2]string{plain[0], plain[1]})
	}
	if len(pairs) == 0 {
		return nil, fmt.Errorf("пример переименования неполон: нужны обе строки, «было» и «стало»")
	}
	return pairs, nil
}

func collapseSpaces(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// commonPrefixLen возвращает длину общего начала в байтах, выровненную по рунам.
func commonPrefixLen(left, right string) int {
	limit := min(len(left), len(right))
	index := 0
	for index < limit {
		leftRune, leftSize := utf8.DecodeRuneInString(left[index:])
		rightRune, rightSize := utf8.DecodeRuneInString(right[index:])
		if leftRune != rightRune || leftSize != rightSize {
			break
		}
		index += leftSize
	}
	return index
}

// commonSuffixLen возвращает длину общего конца, не заходя левее уже найденного начала:
// иначе у «АБА» и «АА» начало и конец перекрылись бы и середина ушла в минус.
func commonSuffixLen(left, right string, floor int) int {
	length := 0
	for len(left)-length > floor && len(right)-length > floor {
		leftRune, leftSize := utf8.DecodeLastRuneInString(left[:len(left)-length])
		rightRune, rightSize := utf8.DecodeLastRuneInString(right[:len(right)-length])
		if leftRune != rightRune || leftSize != rightSize {
			break
		}
		length += leftSize
	}
	return length
}

// KeepTitle — правило «заголовок не трогать».
//
// Вторая реализация TitleRule и причина, по которой он интерфейс. Задача правки может менять
// только текст: заголовок уже правильный, а любое автоматическое переименование на живой
// странице пришлось бы откатывать руками. Отдельная реализация, а не пустой PairRule и не
// признак в потоке: поток спрашивает у правила «во что превратится этот заголовок», и ответ
// «в него же» — такой же полноценный ответ, как замена подстроки.
//
// Пустой заголовок остаётся ошибкой: запись без названия — не «нечего менять», а сорванное
// чтение из блога, и записывать поверх неё нечего.
type KeepTitle struct{}

// Apply возвращает заголовок как есть.
func (KeepTitle) Apply(title string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", fmt.Errorf("заголовок статьи пуст")
	}
	return title, nil
}
