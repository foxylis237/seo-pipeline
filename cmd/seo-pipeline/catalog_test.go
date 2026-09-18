package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/catalog"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
	"github.com/foxylis237/seo-pipeline/internal/tasks/obuch1"
)

type stubSource struct {
	posts map[string][]catalog.SourcePost
}

func (s stubSource) ListPrograms(_ context.Context, postType string) ([]catalog.SourcePost, error) {
	return s.posts[postType], nil
}

type stubStore struct {
	saved []catalog.Program
}

func (s *stubStore) Replace(_ context.Context, programs []catalog.Program) error {
	s.saved = programs
	return nil
}

func (s *stubStore) List(context.Context) ([]catalog.Program, error) { return s.saved, nil }

func industryTerm() []catalog.Industry {
	return []catalog.Industry{{Taxonomy: "obuch-cat", TermID: 1, Slug: "santehnik", Name: "Сантехническое дело"}}
}

// Сбор отчитывается числами и поимённо называет пропущенные страницы: если однажды среди них
// окажется настоящая услуга, увидеть это надо сразу, а не по разнице итогов.
func TestRunCatalogSyncReportsCounts(t *testing.T) {
	source := stubSource{posts: map[string][]catalog.SourcePost{
		"obuch": {
			{PostID: 1, Slug: "santehnik", Title: "Сантехник", Link: "https://dpoprof.ru/a/", Terms: industryTerm()},
			{PostID: 2, Slug: "test", Title: "Тестовый курс"},
		},
	}}
	var out bytes.Buffer
	err := runCatalogSync(context.Background(), catalog.DPOProf(), source, &stubStore{},
		slog.New(slog.NewTextHandler(&out, nil)), &out)
	if err != nil {
		t.Fatalf("runCatalogSync: %v", err)
	}
	report := out.String()
	for _, want := range []string{"1 услуг", "obuch          1", "Пропущено", "Тестовый курс"} {
		if !strings.Contains(report, want) {
			t.Fatalf("в отчёте нет %q:\n%s", want, report)
		}
	}
}

// Просмотр показывает не голые идентификаторы, а то, что читатель увидит под статьёй.
func TestRunCatalogShowPrintsSelection(t *testing.T) {
	store := &stubStore{}
	if _, err := catalog.Sync(context.Background(), catalog.DPOProf(), stubSource{posts: map[string][]catalog.SourcePost{
		"obuch": {{PostID: 504, Slug: "santehnik", Title: "Сантехник", Link: "https://dpoprof.ru/a/", Terms: industryTerm()}},
	}}, store); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runCatalogShow(context.Background(), store,
		catalog.Request{Professions: "сантехник", Topic: "обучение на сантехника"}, &out); err != nil {
		t.Fatalf("runCatalogShow: %v", err)
	}
	report := out.String()
	for _, want := range []string{"Связанные курсы", "Сантехник", "Обучение", "запись 504"} {
		if !strings.Contains(report, want) {
			t.Fatalf("в выводе нет %q:\n%s", want, report)
		}
	}
}

// Пустой каталог — не «нет подходящих курсов», а несобранный каталог. Разница существенная:
// в первом случае публикация законна, во втором человек забыл про catalog-sync.
func TestRunCatalogShowRequiresCollectedCatalog(t *testing.T) {
	err := runCatalogShow(context.Background(), &stubStore{}, catalog.Request{Professions: "сантехник"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "catalog-sync") {
		t.Fatalf("ошибка = %v", err)
	}
}

// Каталог принадлежит площадке, и схема у каждой своя. Держится это одной таблицей: профиль
// называет площадку, схему по ней выбирает composition root. Тест закрывает то, ради чего
// таблица и заведена, — сбор начинается с `DELETE FROM <схема>.programs`, и две площадки,
// сошедшиеся в одной схеме, стёрли бы каталог друг друга молча.
func TestEachTaskGetsCatalogOfItsOwnSite(t *testing.T) {
	schemas := make(map[string]string)
	for _, profile := range taskRegistry() {
		site, schema, err := catalogSiteFor(profile)
		if err != nil {
			t.Fatalf("задаче %s не выбран каталог: %v", profile.Command, err)
		}
		if schema == "" {
			t.Fatalf("задаче %s досталась пустая схема каталога", profile.Command)
		}
		if seen, found := schemas[schema]; found && seen != site.Key() {
			t.Fatalf("схему %s делят площадки %s и %s — сбор одной сотрёт каталог другой",
				schema, seen, site.Key())
		}
		schemas[schema] = site.Key()
	}
}

// Задача второй площадки получает свой каталог, а не общий: именно этого отказывал прежний
// признак WithoutSiteCatalog, и именно это теперь обязано выполняться, когда он снят.
func TestForeignSiteTaskGetsOwnCatalogSchema(t *testing.T) {
	foreign, err := lookupTask(obuch1.Command)
	if err != nil {
		t.Fatal(err)
	}
	site, schema, err := catalogSiteFor(foreign)
	if err != nil {
		t.Fatal(err)
	}
	if site.Key() != catalog.SiteObuchim {
		t.Fatalf("площадка задачи %s = %q", foreign.Command, site.Key())
	}
	if schema == "site" {
		t.Fatal("задача второй площадки собирает каталог в схему первой")
	}
}

// Задачи своей площадки остаются на прежней схеме: нулевое значение признака — прежнее
// поведение, и появление соседа с другим сайтом его не меняет.
func TestOwnSiteTasksStayOnDefaultCatalog(t *testing.T) {
	for _, profile := range taskRegistry() {
		if profile.CatalogSite != "" {
			continue
		}
		site, schema, err := catalogSiteFor(profile)
		if err != nil {
			t.Fatalf("задаче %s закрыт каталог своей площадки: %v", profile.Command, err)
		}
		if site.Key() != catalog.SiteDPOProf || schema != "site" {
			t.Fatalf("задача %s съехала с прежнего каталога: %s / %s", profile.Command, site.Key(), schema)
		}
	}
}

// Неизвестная площадка останавливает команду, а не берёт умолчание: молчаливый откат к
// схеме site означал бы, что программы нового сайта легли поверх старого.
func TestUnknownCatalogSiteRefused(t *testing.T) {
	_, _, err := catalogSiteFor(tasks.Profile{Command: "new-1", CatalogSite: "unknown"})
	if err == nil {
		t.Fatal("неизвестная площадка каталога принята")
	}
	if !strings.Contains(err.Error(), "new-1") {
		t.Fatalf("отказ не называет задачу: %v", err)
	}
}
