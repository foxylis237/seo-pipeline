package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/foxylis237/seo-pipeline/internal/catalog"
	"github.com/foxylis237/seo-pipeline/internal/integrations/wordpress"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/result"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
)

// Каталог услуг площадки: сбор и просмотр.
//
// Операции объявлены у задачи, а данные лежат в общей схеме site — и это не противоречие.
// Площадка одна на все задачи, но доступ к ней у каждой свой (PPROF_1_WORDPRESS_*), и
// собрать каталог можно только чьими-то учётными данными. Чьими именно — неважно: каталог
// от этого не меняется, потому что читаются публичные страницы услуг.
//
// «Площадка одна» перестало быть правдой с появлением задачи на другом сайте, и отсюда
// ensureOwnSiteCatalog.
const (
	catalogSyncOperation = "catalog-sync"
	catalogShowOperation = "catalog-show"
)

// ensureOwnSiteCatalog запрещает команды каталога задаче с другой площадкой.
//
// Схема site рассчитана на один сайт: признака площадки в её таблицах нет, а
// catalog.PostgresStore.Sync начинается с `DELETE FROM site.programs`. Значит `catalog-sync`
// задачи, работающей с другим сайтом, стёр бы собранный каталог и записал на его место чужие
// программы — а сломалось бы это не у неё, а у соседей: подбор связанных курсов молча начал бы
// ставить под статьи dpoprof программы другого сайта.
//
// Отказ стоит здесь, в composition root, и по признаку профиля, а не по имени задачи: движок
// про площадки не знает, а список задач известен ровно тут.
func ensureOwnSiteCatalog(profile tasks.Profile) error {
	if !profile.WithoutSiteCatalog {
		return nil
	}
	return fmt.Errorf(
		"каталог услуг в схеме site собран для другой площадки, а задача %s работает со своей: "+
			"команды каталога ей недоступны, иначе сбор стёр бы чужой каталог. "+
			"Понадобится свой — заводится отдельной схемой, а не пересбором общей",
		profile.Command)
}

// catalogSource — клиент WordPress в роли источника каталога.
//
// Адаптер, а не прямая зависимость: пакет catalog не знает про XML-RPC, а пакет wordpress —
// про рубрики и профессии. Встречаются они здесь, в composition root.
type catalogSource struct {
	client *wordpress.Client
}

func (s catalogSource) ListPrograms(ctx context.Context, postType string) ([]catalog.SourcePost, error) {
	posts, err := s.client.ListCatalogPosts(ctx, postType)
	if err != nil {
		return nil, err
	}
	converted := make([]catalog.SourcePost, 0, len(posts))
	for _, post := range posts {
		terms := make([]catalog.Industry, 0, len(post.Terms))
		for _, term := range post.Terms {
			terms = append(terms, catalog.Industry{
				Taxonomy: term.Taxonomy, TermID: term.TermID, Slug: term.Slug, Name: term.Name,
			})
		}
		converted = append(converted, catalog.SourcePost{
			PostID: post.ID, Slug: post.Slug, Title: post.Title, Link: post.Link, Terms: terms,
		})
	}
	return converted, nil
}

// catalogCourses — каталог услуг в роли подборщика связанных курсов.
//
// Один тип отвечает на два узких вопроса — публикации («какие три записи связать») и сборке
// result.md («что написать в листе»), — потому что вопрос по сути один, а ответ обязан
// совпадать: в лист человек смотрит именно затем, чтобы увидеть, что уйдёт в блог.
//
// Каталог читается один раз за прогон и держится в памяти: полторы тысячи строк весят
// меньше одной статьи, а массовая публикация иначе перечитывала бы их на каждую.
type catalogCourses struct {
	store catalog.Store

	once     sync.Once
	programs []catalog.Program
	loadErr  error
}

func newCatalogCourses(store catalog.Store) *catalogCourses {
	return &catalogCourses{store: store}
}

func (c *catalogCourses) load(ctx context.Context) ([]catalog.Program, error) {
	c.once.Do(func() {
		c.programs, c.loadErr = c.store.List(ctx)
	})
	if c.loadErr != nil {
		return nil, c.loadErr
	}
	if len(c.programs) == 0 {
		return nil, fmt.Errorf("каталог услуг пуст — соберите его командой catalog-sync")
	}
	return c.programs, nil
}

// SelectRelated отвечает публикации: три услуги, которые уйдут в связь related_courses.
func (c *catalogCourses) SelectRelated(
	ctx context.Context, request catalog.Request,
) ([]catalog.Related, error) {
	programs, err := c.load(ctx)
	if err != nil {
		return nil, err
	}
	return catalog.SelectRelated(programs, request), nil
}

// RelatedCourses отвечает сборке result.md: тот же подбор, но строками для листа.
func (c *catalogCourses) RelatedCourses(
	ctx context.Context, input article.ResultInput,
) ([]result.RelatedCourse, error) {
	related, err := c.SelectRelated(ctx, catalog.Request{
		Professions: input.Professions,
		Topic:       strings.TrimSpace(input.Keyword + " " + input.Header),
		Links:       input.Links,
	})
	if err != nil {
		return nil, err
	}
	courses := make([]result.RelatedCourse, 0, len(related))
	for _, item := range related {
		courses = append(courses, result.RelatedCourse{
			Name:      item.Program.Name,
			Category:  item.Program.Category,
			URL:       item.Program.URL,
			Neighbour: item.Neighbour,
		})
	}
	return courses, nil
}

// runCatalogSync собирает каталог заново.
//
// Команда читающая для площадки и переписывающая для нашей базы: в блог она не пишет ничего,
// а таблицу услуг заменяет целиком. Занимает около двух десятков запросов — по восемь
// страниц на самый крупный тип и по одной на самый мелкий.
func runCatalogSync(
	ctx context.Context, source catalog.Source, store catalog.Store, logger *slog.Logger, out io.Writer,
) error {
	logger.Info("сбор каталога услуг начат", "stage", "catalog_sync")
	stats, err := catalog.Sync(ctx, source, store)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Каталог услуг собран: %d услуг, %d профессий\n", stats.Programs, stats.Professions)
	types := make([]string, 0, len(stats.ByType))
	for postType := range stats.ByType {
		types = append(types, postType)
	}
	sort.Strings(types)
	for _, postType := range types {
		fmt.Fprintf(out, "  %-14s %d\n", postType, stats.ByType[postType])
	}
	if len(stats.SkippedNoIndustry) > 0 {
		// Пропущенные показываются целиком, а не числом: это общие и служебные страницы, и
		// если однажды среди них окажется настоящая услуга, увидеть это надо сразу.
		fmt.Fprintf(out, "Пропущено (нет рубрики площадки — общие и служебные страницы): %d\n",
			len(stats.SkippedNoIndustry))
		for _, skipped := range stats.SkippedNoIndustry {
			fmt.Fprintf(out, "  %s\n", skipped)
		}
	}
	logger.Info("сбор каталога услуг завершён", "stage", "catalog_sync",
		"programs", stats.Programs, "professions", stats.Professions,
		"skipped", len(stats.SkippedNoIndustry))
	return nil
}

// runCatalogShow показывает, что подберётся под статью.
//
// Нужен ровно затем, чтобы правило подбора можно было проверить до публикации: связанные
// курсы уходят в живую запись блога, и увидеть их заранее дешевле, чем править потом.
func runCatalogShow(
	ctx context.Context, store catalog.Store, request catalog.Request, out io.Writer,
) error {
	programs, err := store.List(ctx)
	if err != nil {
		return err
	}
	if len(programs) == 0 {
		return fmt.Errorf("каталог услуг пуст — соберите его командой catalog-sync")
	}
	fmt.Fprintf(out, "В каталоге %d услуг\n", len(programs))
	if strings.TrimSpace(request.Professions) != "" {
		fmt.Fprintf(out, "Профессии статьи: %s\n", request.Professions)
	}
	if strings.TrimSpace(request.Topic) != "" {
		fmt.Fprintf(out, "Тема статьи: %s\n", request.Topic)
	}
	if linked := len(catalog.LinkedPrograms(request.Links)); linked > 0 {
		fmt.Fprintf(out, "В тексте статьи уже стоят %d программ — блок их не повторяет\n", linked)
	}
	selected := catalog.SelectRelated(programs, request)
	if len(selected) < catalog.RelatedLimit {
		fmt.Fprintf(out, "Подобрано %d курсов из %d — публикация такую статью остановит.\n",
			len(selected), catalog.RelatedLimit)
		fmt.Fprintln(out, "Проверьте колонку professions статьи и полноту каталога (catalog-sync).")
	}
	if len(selected) == 0 {
		return nil
	}
	fmt.Fprintf(out, "Связанные курсы (смежных %d):\n", catalog.CountNeighbours(selected))
	for index, item := range selected {
		origin := "профессия статьи"
		if item.Neighbour {
			origin = "смежная профессия"
		}
		if item.Widened {
			origin = "расширенный подбор — проверьте глазами"
		}
		fmt.Fprintf(out, "  %d. %-40s %-22s %s\n", index+1,
			item.Program.Name, item.Program.Category, origin)
		fmt.Fprintf(out, "     %s · рубрика площадки: %s · запись %d\n     %s\n",
			item.Program.Profession.Name, item.Program.Industry.Name, item.Program.PostID, item.Program.URL)
	}
	return nil
}

// wordPressCourses отдаёт подборщик публикации, сохраняя пустоту пустой.
//
// Нужен из-за типизированного nil: *catalogCourses, положенный в поле интерфейса напрямую,
// перестал бы быть nil — и публикация задачи без блока пошла бы подбирать курсы по
// несобранному каталогу.
func wordPressCourses(courses *catalogCourses) wordPressCourseSelector {
	if courses == nil {
		return nil
	}
	return courses
}
