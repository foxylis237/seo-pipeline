package catalog

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// SourcePost — запись площадки, как её отдаёт источник каталога.
type SourcePost struct {
	PostID int64
	Slug   string
	Title  string
	Link   string
	// Terms — все термины записи по всем таксономиям. Рубрику среди них выбирает уже
	// каталог: имя таксономии складывается из типа записи, и знать его источнику незачем.
	Terms []Industry
}

// Source — откуда берётся каталог. Сегодня это WordPress, и другого источника не
// предвидится; интерфейс нужен затем, чтобы сбор проверялся без сети.
type Source interface {
	ListPrograms(ctx context.Context, postType string) ([]SourcePost, error)
}

// Store — где каталог лежит.
type Store interface {
	// Replace заменяет каталог целиком: услуга, которой на площадке больше нет, обязана
	// исчезнуть и у нас — иначе она однажды окажется в блоке под статьёй ссылкой в никуда.
	Replace(ctx context.Context, programs []Program) error
	// List отдаёт каталог целиком. Полторы тысячи строк — это меньше, чем одна статья,
	// и подбор идёт в памяти: запрос под каждое правило подбора пришлось бы менять вместе
	// с правилом.
	List(ctx context.Context) ([]Program, error)
}

// Stats — что получилось у сбора. Печатается человеку и в лог: каталог собирается редко, и
// по этим числам видно, не изменилась ли площадка.
type Stats struct {
	// Programs — сколько услуг собрано, ByType — сколько каждого типа.
	Programs int
	ByType   map[string]int
	// Professions — сколько получилось наших рубрик.
	Professions int
	// SkippedNoIndustry — записи без рубрики площадки. Это общие и служебные страницы:
	// у настоящей услуги рубрика есть всегда. В каталог они не попадают.
	SkippedNoIndustry []string
}

// industryTaxonomy — таксономия рубрик у типа записи.
//
// Правило площадки: у типа obuch рубрики лежат в obuch-cat, у perepod — в perepod-cat, и
// так у всех шести. Проверено по картам сайта: обратных примеров нет.
func industryTaxonomy(postType string) string {
	return postType + "-cat"
}

// Sync собирает каталог из источника и заменяет им сохранённый.
//
// Записи без рубрики площадки отбрасываются: у настоящей услуги рубрика есть всегда, а без
// неё приходят общие страницы и остатки вроде «Тестового курса». Их список возвращается —
// пропуск обязан быть виден человеку, а не растворяться в разнице чисел.
func Sync(ctx context.Context, source Source, store Store) (Stats, error) {
	stats := Stats{ByType: make(map[string]int)}
	var programs []Program
	for _, item := range programTypes {
		posts, err := source.ListPrograms(ctx, item.postType)
		if err != nil {
			return Stats{}, fmt.Errorf("прочитать услуги типа %s: %w", item.postType, err)
		}
		taxonomy := industryTaxonomy(item.postType)
		for _, post := range posts {
			industry, found := pickIndustry(post.Terms, taxonomy)
			if !found {
				stats.SkippedNoIndustry = append(stats.SkippedNoIndustry,
					fmt.Sprintf("%d %s", post.PostID, ShortName(post.Title)))
				continue
			}
			profession := ProfessionOf(post.Title)
			if profession.Slug == "" {
				stats.SkippedNoIndustry = append(stats.SkippedNoIndustry,
					fmt.Sprintf("%d %s (название не разобрано)", post.PostID, post.Title))
				continue
			}
			programs = append(programs, Program{
				PostID:     post.PostID,
				PostType:   item.postType,
				Category:   item.category,
				Priority:   item.priority,
				Slug:       post.Slug,
				URL:        post.Link,
				Title:      strings.TrimSpace(post.Title),
				Name:       ShortName(post.Title),
				Industry:   industry,
				Profession: profession,
			})
			stats.ByType[item.postType]++
		}
	}
	unifyProfessionNames(programs)
	stats.Programs = len(programs)
	stats.Professions = countProfessions(programs)
	if err := store.Replace(ctx, programs); err != nil {
		return Stats{}, fmt.Errorf("сохранить каталог: %w", err)
	}
	return stats, nil
}

// pickIndustry выбирает рубрику записи среди её терминов.
//
// Таксономий у записи бывает несколько: помимо своей рубрики площадка вешает на часть услуг
// prof-type («Рабочие профессии»). Берётся только своя — prof-type объединяет четыреста
// с лишним программ и рубрикой ни для чего не годится.
func pickIndustry(terms []Industry, taxonomy string) (Industry, bool) {
	for _, term := range terms {
		if term.Taxonomy == taxonomy && term.TermID > 0 {
			return term, true
		}
	}
	return Industry{}, false
}

// unifyProfessionNames даёт каждой рубрике одно имя — самое частое среди её услуг.
//
// Разбор берёт слово в том падеже, в каком оно стояло в названии: «Охране труда» из
// «Обучение по охране труда сварщиков». Рубрика от этого не портится (ключ у неё общий), но
// человек видит её имя в отчёте и в плане публикации, и «Охрана труда» там уместнее.
func unifyProfessionNames(programs []Program) {
	counts := make(map[string]map[string]int)
	for _, program := range programs {
		slug := program.Profession.Slug
		if counts[slug] == nil {
			counts[slug] = make(map[string]int)
		}
		counts[slug][program.Profession.Name]++
	}
	best := make(map[string]string, len(counts))
	for slug, names := range counts {
		variants := make([]string, 0, len(names))
		for name := range names {
			variants = append(variants, name)
		}
		// Частота решает, а при равной — алфавит: сбор обязан давать один и тот же
		// результат на одних и тех же данных.
		sort.Slice(variants, func(i, j int) bool {
			if names[variants[i]] != names[variants[j]] {
				return names[variants[i]] > names[variants[j]]
			}
			return variants[i] < variants[j]
		})
		best[slug] = variants[0]
	}
	for index := range programs {
		programs[index].Profession.Name = best[programs[index].Profession.Slug]
	}
}

func countProfessions(programs []Program) int {
	seen := make(map[string]struct{})
	for _, program := range programs {
		seen[program.Profession.Slug] = struct{}{}
	}
	return len(seen)
}
