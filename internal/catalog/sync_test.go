package catalog

import (
	"context"
	"errors"
	"testing"
)

type fakeSource struct {
	posts map[string][]SourcePost
	err   error
}

func (s fakeSource) ListPrograms(_ context.Context, postType string) ([]SourcePost, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.posts[postType], nil
}

type fakeStore struct {
	saved []Program
	err   error
}

func (s *fakeStore) Replace(_ context.Context, programs []Program) error {
	if s.err != nil {
		return s.err
	}
	s.saved = programs
	return nil
}

func (s *fakeStore) List(context.Context) ([]Program, error) { return s.saved, nil }

func industry(taxonomy, slug string, id int64) Industry {
	return Industry{Taxonomy: taxonomy, TermID: id, Slug: slug, Name: slug}
}

// Услуга без рубрики площадки в каталог не попадает: рубрика есть у каждой настоящей
// услуги, а без неё приходят общие и служебные страницы вроде «Тестового курса».
func TestSyncSkipsPostsWithoutIndustry(t *testing.T) {
	source := fakeSource{posts: map[string][]SourcePost{
		"obuch": {
			{PostID: 1, Slug: "santehnik", Title: "Сантехник", Terms: []Industry{industry("obuch-cat", "santehnik", 10)}},
			{PostID: 2, Slug: "test", Title: "Тестовый курс"},
			// prof-type — служебная метка площадки, объединяющая четыреста программ.
			// Рубрикой она быть не может, и запись с одной такой меткой не услуга.
			{PostID: 3, Slug: "hub", Title: "Обучение", Terms: []Industry{industry("prof-type", "rabochie", 20)}},
		},
	}}
	store := &fakeStore{}
	stats, err := Sync(context.Background(), DPOProf(), source, store)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Programs != 1 || stats.ByType["obuch"] != 1 {
		t.Fatalf("собрано %d услуг: %+v", stats.Programs, stats.ByType)
	}
	if len(stats.SkippedNoIndustry) != 2 {
		t.Fatalf("пропущено %q", stats.SkippedNoIndustry)
	}
	if len(store.saved) != 1 || store.saved[0].PostID != 1 {
		t.Fatalf("в хранилище легло %+v", store.saved)
	}
}

// Разбор берёт слово в том падеже, в каком оно стояло в названии. Рубрике достаётся самая
// частая форма — её видит человек в отчёте и в плане публикации.
func TestSyncUnifiesProfessionName(t *testing.T) {
	term := []Industry{industry("obuch-cat", "ohrana", 11)}
	source := fakeSource{posts: map[string][]SourcePost{
		"obuch": {
			{PostID: 1, Slug: "a", Title: "Обучение по охране труда сварщиков", Terms: term},
			{PostID: 2, Slug: "b", Title: "Охрана труда", Terms: term},
			{PostID: 3, Slug: "c", Title: "Охрана труда руководителей", Terms: term},
		},
	}}
	store := &fakeStore{}
	if _, err := Sync(context.Background(), DPOProf(), source, store); err != nil {
		t.Fatal(err)
	}
	for _, program := range store.saved {
		if program.Profession.Name != "Охрана труда" {
			t.Fatalf("имя рубрики %q у услуги %d", program.Profession.Name, program.PostID)
		}
	}
}

// Отказ площадки не оставляет каталог наполовину собранным: хранилище не трогается вовсе.
func TestSyncKeepsStoreUntouchedOnSourceError(t *testing.T) {
	source := fakeSource{err: errors.New("площадка недоступна")}
	store := &fakeStore{saved: []Program{program(1, "obuch", "Сварщик")}}
	if _, err := Sync(context.Background(), DPOProf(), source, store); err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if len(store.saved) != 1 {
		t.Fatalf("каталог изменён: %+v", store.saved)
	}
}

// Сбор второй площадки берёт профессию из её рубрики, а не из названия услуги. Название
// здесь нарочно то самое, на котором разбор ломается: ProfessionOf сделал бы из него
// «Профессиональная газосварщика», и рубрикой стало бы прилагательное.
func TestSyncUsesIndustryAsProfessionForObuchim(t *testing.T) {
	const title = "Дистанционная профессиональная переподготовка на газосварщика"
	source := fakeSource{posts: map[string][]SourcePost{
		"rabprof": {{
			PostID: 19282, Slug: "gazosvarshhik", Title: title,
			Terms: []Industry{{Taxonomy: "cat_rabprof", TermID: 3062, Slug: "svarshhik", Name: "Сварщик и газорезчик"}},
		}},
	}}
	store := &fakeStore{}
	stats, err := Sync(context.Background(), Obuchim(), source, store)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Programs != 1 {
		t.Fatalf("собрано %d услуг", stats.Programs)
	}
	got := store.saved[0].Profession
	if got.Slug != "svarshhik" {
		t.Fatalf("профессия = %q, ожидалась рубрика площадки svarshhik", got.Slug)
	}
	if mangled := ProfessionOf(title).Slug; got.Slug == mangled {
		t.Fatalf("профессия совпала с разбором названия (%q) — сбор пошёл не тем путём", mangled)
	}
	if len(got.Aliases) != 2 {
		t.Fatalf("синонимы парной рубрики = %v", got.Aliases)
	}
}

// Таксономия у площадок названа зеркально, и перепутанная означает не ошибку, а тихо пустой
// каталог: рубрика не найдётся ни у одной услуги, и все они уйдут в пропущенные.
func TestSyncFindsNothingWithWrongTaxonomy(t *testing.T) {
	source := fakeSource{posts: map[string][]SourcePost{
		"rabprof": {{
			PostID: 1, Slug: "povar", Title: "Повар",
			Terms: []Industry{industry("rabprof-cat", "povar", 10)},
		}},
	}}
	store := &fakeStore{}
	stats, err := Sync(context.Background(), Obuchim(), source, store)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Programs != 0 || len(stats.SkippedNoIndustry) != 1 {
		t.Fatalf("собрано %d услуг, пропущено %d", stats.Programs, len(stats.SkippedNoIndustry))
	}
}

// Каждая площадка обходит только свои типы: сбор obuchim не должен спрашивать у источника
// типы dpoprof, иначе один запуск смешал бы каталоги двух сайтов.
func TestSyncAsksOnlyOwnPostTypes(t *testing.T) {
	asked := make(map[string]struct{})
	source := askedSource{asked: asked}
	if _, err := Sync(context.Background(), Obuchim(), source, &fakeStore{}); err != nil {
		t.Fatal(err)
	}
	for _, foreign := range DPOProf().PostTypes() {
		if _, found := asked[foreign]; found && foreign != "attestaciya" {
			t.Fatalf("сбор obuchim спросил тип dpoprof %q", foreign)
		}
	}
	for _, own := range Obuchim().PostTypes() {
		if _, found := asked[own]; !found {
			t.Fatalf("сбор obuchim пропустил свой тип %q", own)
		}
	}
}

type askedSource struct{ asked map[string]struct{} }

func (s askedSource) ListPrograms(_ context.Context, postType string) ([]SourcePost, error) {
	s.asked[postType] = struct{}{}
	return nil, nil
}
