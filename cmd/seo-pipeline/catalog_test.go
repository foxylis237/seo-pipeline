package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/catalog"
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
	err := runCatalogSync(context.Background(), source, &stubStore{},
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
	if _, err := catalog.Sync(context.Background(), stubSource{posts: map[string][]catalog.SourcePost{
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
