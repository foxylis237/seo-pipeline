package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/foxylis237/seo-pipeline/internal/catalog"
	"github.com/foxylis237/seo-pipeline/internal/integrations/wordpress"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/result"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
)

// Каталог услуг площадки: сбор и просмотр.
//
// Операции объявлены у задачи, а данные лежат в схеме площадки — и это не противоречие.
// Каталог принадлежит сайту, а не задаче: доступ к сайту у каждой задачи свой
// (PPROF_1_WORDPRESS_*, OBUCH_1_WORDPRESS_*), но каталог от этого не меняется, потому что
// читаются публичные страницы услуг.
//
// Площадок у проекта две, и у каждой свой каталог в своей схеме. Пару «описание площадки —
// схема» сводит catalogSiteFor, и только он: схема начинает сбор с удаления всех услуг, и
// перепутанная пара стёрла бы чужой каталог.
const (
	catalogSyncOperation = "catalog-sync"
	catalogShowOperation = "catalog-show"
)

// catalogSites — площадки проекта и схемы их каталогов.
//
// Таблица одна на приложение, и другого места, где схема каталога называется, нет. Профиль
// задачи называет только площадку: пара сводится здесь, поэтому «сайт obuchim со схемой
// site» невыразим — а именно он стёр бы каталог dpoprof вместе с подбором курсов у соседей.
var catalogSites = map[string]struct {
	site   catalog.Site
	schema string
}{
	catalog.SiteDPOProf: {site: catalog.DPOProf(), schema: "site"},
	catalog.SiteObuchim: {site: catalog.Obuchim(), schema: "site_obuchim"},
}

// catalogSiteFor отвечает, чей каталог читает задача и где он лежит.
//
// Пустая площадка профиля — прежнее поведение (dpoprof, схема site): так остаются все задачи
// первой площадки, включая те, что каталогом не пользуются. Неизвестная останавливает
// команду: молча взять умолчание значило бы записать программы нового сайта поверх старого.
func catalogSiteFor(profile tasks.Profile) (catalog.Site, string, error) {
	key := strings.TrimSpace(profile.CatalogSite)
	if key == "" {
		key = catalog.SiteDPOProf
	}
	known, found := catalogSites[key]
	if !found {
		return catalog.Site{}, "", fmt.Errorf(
			"задача %s объявила площадку каталога %q, а такой у проекта нет: "+
				"каталог новой площадки заводится своей схемой и строкой в catalogSites, "+
				"а не пересбором чужой", profile.Command, key)
	}
	return known.site, known.schema, nil
}

// catalogStoreFor открывает каталог той площадки, с которой работает задача.
func catalogStoreFor(profile tasks.Profile, pool *pgxpool.Pool) (*catalog.PostgresStore, error) {
	_, schema, err := catalogSiteFor(profile)
	if err != nil {
		return nil, err
	}
	return catalog.NewPostgresStore(pool, schema)
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

// HasProgram отвечает потоку obuch_1: есть ли такая страница программы в каталоге площадки.
//
// Тот же прочитанный один раз каталог, что и у подбора: спрашивают его на статью один раз, и
// второго запроса в базу за этим не стоит. Пустой каталог — ошибка, а не «нет такой
// программы»: поток обязан отличать несобранный каталог от выдуманного моделью адреса.
func (c *catalogCourses) HasProgram(ctx context.Context, url string) (bool, error) {
	programs, err := c.load(ctx)
	if err != nil {
		return false, err
	}
	wanted := normalizedProgramURL(url)
	if wanted == "" {
		return false, nil
	}
	for _, program := range programs {
		if normalizedProgramURL(program.URL) == wanted {
			return true, nil
		}
	}
	return false, nil
}

// normalizedProgramURL приводит адрес к виду, в котором два написания одной страницы
// совпадают: завершающий слеш площадка ставит сама, а модель его роняет.
func normalizedProgramURL(url string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(url)), "/")
}

// runCatalogSync собирает каталог заново.
//
// Команда читающая для площадки и переписывающая для нашей базы: в блог она не пишет ничего,
// а таблицу услуг заменяет целиком. Занимает около двух десятков запросов — по восемь
// страниц на самый крупный тип и по одной на самый мелкий.
func runCatalogSync(
	ctx context.Context, site catalog.Site, source catalog.Source, store catalog.Store,
	logger *slog.Logger, out io.Writer,
) error {
	logger.Info("сбор каталога услуг начат", "stage", "catalog_sync", "site", site.Key())
	stats, err := catalog.Sync(ctx, site, source, store)
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
	logger.Info("сбор каталога услуг завершён", "stage", "catalog_sync", "site", site.Key(),
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
