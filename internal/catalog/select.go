package catalog

import (
	"sort"
	"strings"
)

// Request — что известно о статье, под которую подбираются услуги.
type Request struct {
	// Professions — колонка professions книги импорта; первая профессия — главная.
	Professions string
	// Topic — тема статьи (ключевой запрос или заголовок); упорядочивает услуги внутри профессии.
	Topic string
	// Links — колонка links книги импорта: программы, уже стоящие ссылками в тексте.
	Links string
	// Limit — сколько услуг нужно; ноль означает три.
	Limit int
}

const defaultLimit = 3

// Select подбирает услуги под статью: по одной от каждой профессии по порядку, внутри
// профессии — по приоритету, затем по близости к теме. Пустой результат допустим.
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
	// Профессии не сошлись с каталогом: колонка professions бывает пустой или заполненной неверно.
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

func matchedKey(program Program, rank map[string]int) (string, bool) {
	for _, alias := range program.Profession.Aliases {
		if _, found := rank[alias]; found {
			return alias, true
		}
	}
	return "", false
}

// topicScore — сколько слов темы статьи нашлось в названии услуги.
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
// В отличие от MatchKeys слова берутся по отдельности: профессия бывает не первым словом.
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

// PostIDs возвращает идентификаторы записей подобранных услуг.
func PostIDs(programs []Program) []int64 {
	ids := make([]int64, 0, len(programs))
	for _, program := range programs {
		ids = append(ids, program.PostID)
	}
	return ids
}

// Describe перечисляет подобранные услуги словами: название, тип и адрес.
func Describe(programs []Program) string {
	parts := make([]string, 0, len(programs))
	for _, program := range programs {
		parts = append(parts, program.Name+" ("+program.Category+", "+program.URL+")")
	}
	return strings.Join(parts, "; ")
}
