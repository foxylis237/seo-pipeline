package catalog

import (
	"net/url"
	"sort"
	"strings"
)

// Подбор блока «Связанные курсы» под статьёй: дополняет перелинковку из колонки links,
// а не повторяет её.

// Related — подобранная услуга и то, откуда она пришла.
type Related struct {
	Program Program
	// Neighbour — услуга смежной профессии, а не той, о которой статья.
	Neighbour bool
	// Widened — услуга, найденная за пределами рубрик статьи, на последней ступени.
	Widened bool
}

// RelatedLimit — размер блока: тема площадки рисует ровно три карточки.
const RelatedLimit = 3

// SelectRelated подбирает услуги для блока под статьёй, исключая программы из Links.
// Круг расширяется, пока не хватает: профессии статьи → их рубрики → те же рубрики в
// соседних таксономиях.
func SelectRelated(programs []Program, request Request) []Related {
	limit := request.Limit
	if limit <= 0 {
		limit = RelatedLimit
	}
	keys := MatchKeys(request.Professions)
	if len(keys) == 0 {
		keys = TopicKeys(request.Topic)
	}
	linkedSlugs := linkedProgramSlugs(request.Links)
	scope := articleScope(programs, keys, linkedSlugs)
	topicWords := topicStems(request.Topic + " " + request.Professions)
	mainProfession := ""
	if len(keys) > 0 {
		mainProfession = keys[0]
	}

	stages := []func(Program) bool{
		func(item Program) bool { return scope.professions[item.Profession.Slug] },
		func(item Program) bool { return scope.industries[industryKey(item.Industry)] },
		func(item Program) bool { return scope.industrySlugs[item.Industry.Slug] },
	}

	var selected []Related
	taken := make(map[int64]struct{})
	for stage, inCircle := range stages {
		if len(selected) >= limit {
			break
		}
		var candidates []Program
		for _, item := range programs {
			if _, done := taken[item.PostID]; done {
				continue
			}
			if linkedSlugs[item.Slug] || !inCircle(item) {
				continue
			}
			candidates = append(candidates, item)
		}
		for _, item := range pickSpread(candidates, keys, scope.linkedProfessions, topicWords, limit-len(selected)) {
			taken[item.PostID] = struct{}{}
			selected = append(selected, Related{
				Program:   item,
				Neighbour: professionKeyOf(item) != mainProfession,
				Widened:   stage == len(stages)-1,
			})
		}
	}
	return selected
}

type scope struct {
	// professions — профессии из колонки и из ссылок в тексте.
	professions map[string]bool
	// industries — ключ «таксономия:термин».
	industries map[string]bool
	// industrySlugs — те же рубрики без таксономии: obuch-cat santehnik и perepod-cat santehnik — одна группа.
	industrySlugs map[string]bool
	// linkedProfessions — профессии, уже представленные ссылками в тексте; они идут последними.
	linkedProfessions map[string]bool
}

func articleScope(programs []Program, keys []string, linkedSlugs map[string]bool) scope {
	wanted := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		wanted[key] = struct{}{}
	}
	result := scope{
		professions:       make(map[string]bool),
		industries:        make(map[string]bool),
		industrySlugs:     make(map[string]bool),
		linkedProfessions: make(map[string]bool),
	}
	for _, item := range programs {
		_, byProfession := wanted[professionKeyOf(item)]
		linked := linkedSlugs[item.Slug]
		if linked {
			result.linkedProfessions[item.Profession.Slug] = true
		}
		if !byProfession && !linked {
			continue
		}
		result.professions[item.Profession.Slug] = true
		if item.Industry.TermID > 0 {
			result.industries[industryKey(item.Industry)] = true
			result.industrySlugs[item.Industry.Slug] = true
		}
	}
	return result
}

// pickSpread берёт по одной услуге от профессии, круг за кругом: сначала профессии, которых
// нет в ссылках текста, затем в порядке колонки professions.
func pickSpread(
	candidates []Program, keys []string, linkedProfessions map[string]bool,
	topicWords map[string]struct{}, need int,
) []Program {
	if need <= 0 || len(candidates) == 0 {
		return nil
	}
	rank := make(map[string]int, len(keys))
	for index, key := range keys {
		rank[key] = index
	}
	groups := make(map[string][]Program)
	for _, item := range candidates {
		groups[item.Profession.Slug] = append(groups[item.Profession.Slug], item)
	}
	order := make([]string, 0, len(groups))
	for slug := range groups {
		order = append(order, slug)
		items := groups[slug]
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
		groups[slug] = items
	}
	sort.SliceStable(order, func(i, j int) bool {
		left, right := groups[order[i]][0], groups[order[j]][0]
		if linkedProfessions[order[i]] != linkedProfessions[order[j]] {
			return !linkedProfessions[order[i]]
		}
		leftRank, leftNamed := rank[professionKeyOf(left)]
		rightRank, rightNamed := rank[professionKeyOf(right)]
		if leftNamed != rightNamed {
			return leftNamed
		}
		if leftNamed && leftRank != rightRank {
			return leftRank < rightRank
		}
		if left.Priority != right.Priority {
			return left.Priority < right.Priority
		}
		leftScore, rightScore := topicScore(left, topicWords), topicScore(right, topicWords)
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		return left.Name < right.Name
	})

	var picked []Program
	for round := 0; len(picked) < need; round++ {
		added := false
		for _, slug := range order {
			items := groups[slug]
			if round >= len(items) {
				continue
			}
			picked = append(picked, items[round])
			added = true
			if len(picked) == need {
				return picked
			}
		}
		if !added {
			break
		}
	}
	return picked
}

// linkedProgramSlugs разбирает колонку links в слаги: адреса одной программы пишут
// по-разному (слеш, http/https), слаг у них один.
func linkedProgramSlugs(links string) map[string]bool {
	slugs := make(map[string]bool)
	for _, field := range strings.Fields(links) {
		field = strings.Trim(strings.TrimSpace(field), ",;")
		if field == "" {
			continue
		}
		parsed, err := url.Parse(field)
		if err != nil {
			continue
		}
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		last := parts[len(parts)-1]
		if last != "" {
			slugs[last] = true
		}
	}
	return slugs
}

// LinkedPrograms возвращает слаги программ, уже стоящих ссылками в тексте статьи.
func LinkedPrograms(links string) []string {
	slugs := linkedProgramSlugs(links)
	names := make([]string, 0, len(slugs))
	for slug := range slugs {
		names = append(names, slug)
	}
	sort.Strings(names)
	return names
}

// professionKeyOf — основа, по которой услуга сходится с колонкой professions статьи.
func professionKeyOf(program Program) string {
	if len(program.Profession.Aliases) == 0 {
		return ""
	}
	return program.Profession.Aliases[0]
}

// RelatedPostIDs возвращает идентификаторы записей блока для связи related_courses.
func RelatedPostIDs(related []Related) []int64 {
	ids := make([]int64, 0, len(related))
	for _, item := range related {
		ids = append(ids, item.Program.PostID)
	}
	return ids
}

// DescribeRelated перечисляет подобранные услуги словами: название и тип услуги.
func DescribeRelated(related []Related) string {
	parts := make([]string, 0, len(related))
	for _, item := range related {
		parts = append(parts, item.Program.Name+" ("+item.Program.Category+")")
	}
	return strings.Join(parts, "; ")
}

// CountNeighbours считает, сколько курсов блока подтянуто из смежных профессий.
func CountNeighbours(related []Related) int {
	count := 0
	for _, item := range related {
		if item.Neighbour {
			count++
		}
	}
	return count
}
