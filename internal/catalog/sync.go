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
	// Terms — все термины записи по всем таксономиям; рубрику среди них выбирает каталог.
	Terms []Industry
}

// Source — откуда берётся каталог (WordPress); интерфейс — чтобы сбор проверялся без сети.
type Source interface {
	ListPrograms(ctx context.Context, postType string) ([]SourcePost, error)
}

// Store — хранилище каталога.
type Store interface {
	// Replace заменяет каталог целиком, чтобы исчезли услуги, пропавшие с площадки.
	Replace(ctx context.Context, programs []Program) error
	// List отдаёт каталог целиком: он мал, и подбор идёт в памяти.
	List(ctx context.Context) ([]Program, error)
}

// Stats — итог сбора каталога для консоли и лога.
type Stats struct {
	// Programs — сколько услуг собрано, ByType — сколько каждого типа.
	Programs int
	ByType   map[string]int
	// Professions — сколько получилось наших рубрик.
	Professions int
	// SkippedNoIndustry — записи, не попавшие в каталог: без рубрики площадки или с
	// неопределённой профессией.
	SkippedNoIndustry []string
}

// Sync собирает каталог площадки из источника и заменяет им сохранённый.
// store должен принадлежать той же площадке: Replace удаляет все услуги схемы.
func Sync(ctx context.Context, site Site, source Source, store Store) (Stats, error) {
	stats := Stats{ByType: make(map[string]int)}
	var programs []Program
	for _, item := range site.types {
		posts, err := source.ListPrograms(ctx, item.postType)
		if err != nil {
			return Stats{}, fmt.Errorf("прочитать услуги типа %s: %w", item.postType, err)
		}
		taxonomy := site.taxonomy(item.postType)
		for _, post := range posts {
			industry, found := pickIndustry(post.Terms, taxonomy)
			if !found {
				stats.SkippedNoIndustry = append(stats.SkippedNoIndustry,
					fmt.Sprintf("%d %s", post.PostID, ShortName(post.Title)))
				continue
			}
			profession := site.profession(post, industry)
			if profession.Slug == "" {
				stats.SkippedNoIndustry = append(stats.SkippedNoIndustry,
					fmt.Sprintf("%d %s (профессия не определена)", post.PostID, post.Title))
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

// pickIndustry берёт термин только своей таксономии: prof-type тоже висит на услугах, но
// объединяет сотни программ и рубрикой не годится.
func pickIndustry(terms []Industry, taxonomy string) (Industry, bool) {
	for _, term := range terms {
		if term.Taxonomy == taxonomy && term.TermID > 0 {
			return term, true
		}
	}
	return Industry{}, false
}

// unifyProfessionNames даёт каждой рубрике одно имя — самое частое среди её услуг: разбор
// берёт слово в падеже названия («Охране труда»).
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
		// При равной частоте — алфавит, чтобы сбор был воспроизводим.
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
