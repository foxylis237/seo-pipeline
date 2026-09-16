package catalog

import (
	"net/url"
	"sort"
	"strings"
)

// Подбор блока «Связанные курсы» под статьёй.
//
// Он не тот же, что подбор перелинковки, и отличие принципиальное: ссылки внутри текста
// человек уже расставил колонкой links, и повторить их карточками под статьёй значит
// показать читателю одно и то же дважды. Блок обязан вести дальше — к соседним профессиям
// той же рубрики, которых в тексте не было.
//
// Курсов всегда ровно три: столько карточек рисует тема, и неполный блок выглядит ошибкой
// вёрстки. Поэтому круг поиска расширяется, пока три не наберутся, — от профессий статьи к
// их рубрикам и к тем же рубрикам в соседних таксономиях.

// Related — подобранная услуга и то, откуда она пришла.
type Related struct {
	Program Program
	// Neighbour — услуга смежной профессии, а не той, о которой статья. Признак нужен
	// человеку: в result.md видно, сколько курсов блока про саму профессию, а сколько
	// подтянуто из соседних.
	Neighbour bool
	// Widened — услуга, найденная за пределами рубрик статьи, когда трёх не набралось иначе.
	// Такие стоит просмотреть глазами: связь с темой у них слабее.
	Widened bool
}

// RelatedLimit — размер блока. Тема рисует три карточки, и это её вёрстка, а не наше
// предпочтение: четвёртая никуда не поместится, а двух не хватит на ряд.
const RelatedLimit = 3

// SelectRelated подбирает услуги для блока под статьёй.
//
// Порядок отбора:
//
//  1. Из круга кандидатов вычёркиваются программы, уже стоящие ссылками в тексте (Links).
//  2. Первыми идут профессии, которых в тексте не было вовсе, — блок ведёт дальше статьи.
//  3. Внутри профессии первым идёт обучение, потом переподготовка и повышение.
//  4. От каждой профессии берётся по одной услуге, и лишь потом второй круг.
//
// Круг кандидатов расширяется тремя ступенями и ровно настолько, насколько не хватило:
// профессии статьи → их рубрики площадки → те же рубрики в соседних таксономиях
// (обучение ↔ переподготовка ↔ повышение нарезаны почти одинаково).
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

	// Ступени круга: профессии статьи, их рубрики, те же рубрики в соседних таксономиях.
	// Каждая следующая шире предыдущей, и берётся ровно столько, сколько не хватило.
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

// scope — тематика статьи, очерченная тремя кругами.
type scope struct {
	// professions — профессии, которые статья назвала сама или показала ссылками в тексте.
	professions map[string]bool
	// industries — рубрики площадки этих профессий, ключ «таксономия:термин».
	industries map[string]bool
	// industrySlugs — те же рубрики без таксономии: obuch-cat santehnik и perepod-cat
	// santehnik — одна профессиональная группа, разложенная по типам услуг.
	industrySlugs map[string]bool
	// linkedProfessions — профессии, уже представленные ссылками в тексте статьи. Блок
	// начинает не с них: читатель эти курсы уже видел.
	linkedProfessions map[string]bool
}

// articleScope очерчивает тематику статьи.
//
// Опора двойная: профессии из колонки и программы, уже стоящие в тексте. Второе точнее
// любого разбора — выбирая ссылки перелинковки, человек тем самым назвал рубрику.
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

// pickSpread разбирает кандидатов по одному от профессии, круг за кругом.
//
// Порядок профессий: сначала те, которых в тексте статьи не было, — блок ведёт дальше; за
// ними те, что статья уже показывала. Внутри группы держится порядок колонки professions:
// первой человек называет главную.
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
		// Профессия, которой в тексте не было, идёт первой: ради неё блок и существует.
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

// linkedProgramSlugs разбирает колонку links в слаги программ.
//
// Ссылки записаны адресами страниц («https://dpoprof.ru/obuchenie/santehnik/»), а сравнивать
// их с каталогом надёжнее по слагу: адрес одной и той же программы пишут и со слешем на
// конце, и без, и с http вместо https.
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
//
// Нужна командам, которые показывают подбор человеку: сколько программ статья уже назвала
// сама — половина ответа на вопрос, почему в блоке именно эти три.
func LinkedPrograms(links string) []string {
	slugs := linkedProgramSlugs(links)
	names := make([]string, 0, len(slugs))
	for slug := range slugs {
		names = append(names, slug)
	}
	sort.Strings(names)
	return names
}

// professionKeyOf — ключ профессии услуги: та самая основа слова, по которой она сходится с
// колонкой professions статьи.
func professionKeyOf(program Program) string {
	if len(program.Profession.Aliases) == 0 {
		return ""
	}
	return program.Profession.Aliases[0]
}

// RelatedPostIDs возвращает идентификаторы записей блока — то, что уходит в связь.
func RelatedPostIDs(related []Related) []int64 {
	ids := make([]int64, 0, len(related))
	for _, item := range related {
		ids = append(ids, item.Program.PostID)
	}
	return ids
}

// DescribeRelated перечисляет подобранные услуги для человека: план публикации, лог и
// result.md показывают не голые идентификаторы, а то, что читатель увидит под статьёй.
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
