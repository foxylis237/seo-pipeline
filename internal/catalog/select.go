package catalog

import (
	"sort"
	"strings"
)

// Request — что известно о статье, под которую подбираются услуги.
type Request struct {
	// Professions — колонка professions книги импорта: «сантехник, слесарь, монтажник».
	// Порядок значим: первой человек называет главную профессию статьи.
	Professions string
	// Topic — тема статьи: ключевой запрос или заголовок. Служит уточнением внутри
	// профессии, где услуг больше, чем нужно: «аппаратчиков» на площадке 212, и без темы
	// первые три из них были бы случайными.
	Topic string
	// Links — колонка links книги импорта: программы, уже стоящие ссылками в тексте
	// статьи. Блок под статьёй их не повторяет — читатель их там уже видел.
	Links string
	// Limit — сколько услуг нужно. Ноль означает три: столько карточек рисует блок под
	// статьёй.
	Limit int
}

// defaultLimit — размер блока связанных курсов под статьёй.
const defaultLimit = 3

// Select подбирает услуги под статью.
//
// Правило подбора объяснимо человеку, и это требование, а не пожелание: связанные курсы
// видит читатель, и на вопрос «почему здесь этот курс» обязан быть ответ.
//
//  1. Профессии статьи берутся по порядку — первая главная.
//  2. От каждой профессии берётся по одной услуге, и только потом идёт второй круг. Три
//     ссылки на одного и того же сварщика хуже, чем сварщик, слесарь и монтажник.
//  3. Внутри профессии первым идёт обучение, потом переподготовка и повышение
//     квалификации: статью про профессию читает тот, кто входит в неё с нуля.
//  4. При равном приоритете выигрывает услуга, чьё название ближе к теме статьи.
//
// Пустой результат — законный исход: профессии статьи может не быть в каталоге вовсе
// (обзорные статьи вроде «профессии 2026» ни к одной не привязаны). Тогда блок под статьёй
// заполняет сама тема — по совпадению рубрики и меток.
func Select(programs []Program, request Request) []Program {
	limit := request.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	keys := MatchKeys(request.Professions)
	selected := selectByKeys(programs, keys, request.Topic, limit)
	if len(selected) > 0 {
		return selected
	}
	// Профессии не сошлись с каталогом — пробуем тему статьи. Она заполнена всегда, тогда
	// как колонка professions есть не у каждой задачи, а у статьи 14 в ней и вовсе оказались
	// ссылки вместо профессий — ошибка ручного заполнения, из-за которой статья осталась бы
	// без блока вовсе.
	return selectByKeys(programs, TopicKeys(request.Topic), request.Topic, limit)
}

func selectByKeys(programs []Program, keys []string, topic string, limit int) []Program {
	if len(keys) == 0 || limit <= 0 {
		return nil
	}
	rank := make(map[string]int, len(keys))
	for index, key := range keys {
		rank[key] = index
	}
	topicWords := topicStems(topic)
	groups := make(map[string][]Program)
	for _, program := range programs {
		key, found := matchedKey(program, rank)
		if !found {
			continue
		}
		groups[key] = append(groups[key], program)
	}
	for key := range groups {
		items := groups[key]
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].Priority != items[j].Priority {
				return items[i].Priority < items[j].Priority
			}
			left, right := topicScore(items[i], topicWords), topicScore(items[j], topicWords)
			if left != right {
				return left > right
			}
			return items[i].Name < items[j].Name
		})
		groups[key] = items
	}
	// Круг за кругом: по одной услуге от каждой профессии в порядке, заданном статьёй.
	var selected []Program
	for round := 0; len(selected) < limit; round++ {
		added := false
		for _, key := range keys {
			items := groups[key]
			if round >= len(items) {
				continue
			}
			selected = append(selected, items[round])
			added = true
			if len(selected) == limit {
				return selected
			}
		}
		if !added {
			break
		}
	}
	return selected
}

// matchedKey отвечает, какой из ключей статьи назвала эта услуга.
func matchedKey(program Program, rank map[string]int) (string, bool) {
	for _, alias := range program.Profession.Aliases {
		if _, found := rank[alias]; found {
			return alias, true
		}
	}
	return "", false
}

// topicScore — сколько слов темы статьи нашлось в названии услуги.
//
// Грубая мера близости, и большего здесь не нужно: она разводит услуги внутри одной
// профессии, а не выбирает профессию.
func topicScore(program Program, topicWords map[string]struct{}) int {
	if len(topicWords) == 0 {
		return 0
	}
	score := 0
	for _, word := range significantWords(program.Title) {
		if _, found := topicWords[Stem(word)]; found {
			score++
		}
	}
	return score
}

func topicStems(topic string) map[string]struct{} {
	words := significantWords(topic)
	stems := make(map[string]struct{}, len(words))
	for _, word := range words {
		if stem := Stem(word); stem != "" {
			stems[stem] = struct{}{}
		}
	}
	return stems
}

// TopicKeys приводит тему статьи к основам слов, каждое из которых может назвать профессию.
//
// Отличается от MatchKeys тем, что берёт слова по отдельности, а не разбирает фразу в одну
// рубрику: в «обучении на газосварщика» профессия стоит третьим словом, и правило «первое
// значимое слово» её не нашло бы. Слова, не совпавшие ни с одной профессией каталога,
// отсеются сами — сравнение идёт с ключами рубрик, а не с произвольным текстом.
func TopicKeys(topic string) []string {
	var keys []string
	seen := make(map[string]struct{})
	for _, word := range significantWords(topic) {
		stem := Stem(word)
		if stem == "" {
			continue
		}
		if _, found := seen[stem]; found {
			continue
		}
		seen[stem] = struct{}{}
		keys = append(keys, stem)
	}
	return keys
}

// PostIDs возвращает идентификаторы записей подобранных услуг — то, что уходит в связь.
func PostIDs(programs []Program) []int64 {
	ids := make([]int64, 0, len(programs))
	for _, program := range programs {
		ids = append(ids, program.PostID)
	}
	return ids
}

// Describe перечисляет подобранные услуги для человека: план публикации и логи показывают
// не голые идентификаторы, а то, что читатель увидит под статьёй.
func Describe(programs []Program) string {
	parts := make([]string, 0, len(programs))
	for _, program := range programs {
		parts = append(parts, program.Name+" ("+program.Category+", "+program.URL+")")
	}
	return strings.Join(parts, "; ")
}
