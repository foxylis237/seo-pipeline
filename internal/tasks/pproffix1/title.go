package pproffix1

import (
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TitleRule — правило переименования, выведенное из одной пары «было — стало».
//
// Модель заголовок не пишет. Причина не в экономии запроса, а в предсказуемости: правка
// одна и та же во всей пачке («добавить „с практикой“»), и человек уже показал её примером.
// Модель на сотне статей сформулировала бы её сотней разных способов, и проверить результат
// можно было бы только глазами.
//
// Правило хранится как замена подстроки: что искать в заголовке и чем заменить. Границы
// подстроки расширены до целых слов — так замена не срабатывает на середине слова.
type TitleRule struct {
	Search  string
	Replace string
}

// NewTitleRule выводит правило из примера.
//
// Общее начало и общий конец у пары отбрасываются, остаётся изменившаяся середина; она и
// становится заменой. Чистая вставка (середина в «было» пуста) якорится соседним словом —
// иначе искать в заголовке было бы нечего.
func NewTitleRule(from, to string) (TitleRule, error) {
	from = collapseSpaces(from)
	to = collapseSpaces(to)
	if from == "" || to == "" {
		return TitleRule{}, fmt.Errorf("пример переименования неполон: нужны обе строки, «было» и «стало»")
	}
	if from == to {
		return TitleRule{}, fmt.Errorf("пример переименования ничего не меняет: «было» и «стало» совпадают")
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
		return TitleRule{}, fmt.Errorf("по примеру не видно, к чему привязать вставку: "+
			"в «было» нет соседнего слова у места правки (%q → %q)", from, to)
	}
	return TitleRule{Search: search, Replace: replace}, nil
}

// Apply применяет правило к заголовку.
//
// Уже переименованный заголовок возвращается как есть: прогон повторяют после обрыва, и
// вторая правка дала бы «с практикой и с практикой и». Отсутствие якоря — ошибка, а не
// молчаливый пропуск: заголовок, который правило не берёт, человек должен увидеть.
func (r TitleRule) Apply(title string) (string, error) {
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

// LoadTitleRule читает пример переименования из файла задачи.
//
// Формат — две строки, «было:» и «стало:»; строки, начинающиеся с #, игнорируются. Файл, а
// не колонка входа: правка одна на всю пачку, и держать её сотней копий в таблице значило бы
// сверять их между собой руками.
func LoadTitleRule(path string) (TitleRule, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return TitleRule{}, fmt.Errorf("прочитать правило переименования %q: %w", path, err)
	}
	var from, to string
	var plain []string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "было:"):
			from = strings.TrimSpace(line[len("было:"):])
		case strings.HasPrefix(lower, "стало:"):
			to = strings.TrimSpace(line[len("стало:"):])
		default:
			plain = append(plain, line)
		}
	}
	if from == "" && to == "" && len(plain) >= 2 {
		from, to = plain[0], plain[1]
	}
	rule, err := NewTitleRule(from, to)
	if err != nil {
		return TitleRule{}, fmt.Errorf("%s: %w", path, err)
	}
	return rule, nil
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
