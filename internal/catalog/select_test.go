package catalog

import (
	"reflect"
	"testing"
)

func program(id int64, postType, title string) Program {
	category, priority, _ := DPOProf().describe(postType)
	return Program{
		PostID: id, PostType: postType, Category: category, Priority: priority,
		Slug: "slug", URL: "https://dpoprof.ru/x", Title: title, Name: ShortName(title),
		Profession: ProfessionOf(title),
	}
}

func names(programs []Program) []string {
	got := make([]string, 0, len(programs))
	for _, item := range programs {
		got = append(got, item.Name+"/"+item.PostType)
	}
	return got
}

// От каждой профессии берётся по одной услуге: три ссылки на одного и того же сварщика
// читателю бесполезны.
func TestSelectSpreadsAcrossProfessions(t *testing.T) {
	programs := []Program{
		program(1, "obuch", "Сантехник"),
		program(2, "perepod", "Сантехник"),
		program(3, "obuch", "Слесарь"),
		program(4, "obuch", "Монтажник"),
	}
	got := names(Select(programs, Request{Professions: "сантехник, слесарь, монтажник"}))
	want := []string{"Сантехник/obuch", "Слесарь/obuch", "Монтажник/obuch"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("подбор = %q, ожидалось %q", got, want)
	}
}

// Обучение идёт первым: статью про профессию читает тот, кто входит в неё с нуля.
func TestSelectPrefersInitialTraining(t *testing.T) {
	programs := []Program{
		program(1, "povysh", "Сварщик"),
		program(2, "perepod", "Сварщик"),
		program(3, "obuch", "Сварщик"),
	}
	got := names(Select(programs, Request{Professions: "сварщик"}))
	want := []string{"Сварщик/obuch", "Сварщик/perepod", "Сварщик/povysh"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("подбор = %q, ожидалось %q", got, want)
	}
}

// Внутри профессии выбор уточняет тема статьи: «аппаратчиков» на площадке две сотни, и без
// темы первые три были бы случайными.
func TestSelectPrefersProgramCloserToTopic(t *testing.T) {
	programs := []Program{
		program(1, "obuch", "Аппаратчик получения пата"),
		program(2, "obuch", "Аппаратчик химводоочистки"),
	}
	got := Select(programs, Request{Professions: "аппаратчик", Topic: "аппаратчик химводоочистки", Limit: 1})
	if len(got) != 1 || got[0].PostID != 2 {
		t.Fatalf("подбор = %q", names(got))
	}
}

// У статьи 14 в колонке professions оказались ссылки вместо профессий — ошибка ручного
// заполнения. Тема статьи спасает подбор.
func TestSelectFallsBackToTopic(t *testing.T) {
	programs := []Program{program(1, "obuch", "Газосварщик"), program(2, "obuch", "Повар")}
	got := names(Select(programs, Request{
		Professions: "https://dpoprof.ru/obuchenie/jelektrogazosvarshhik/",
		Topic:       "обучение на газосварщика",
	}))
	if !reflect.DeepEqual(got, []string{"Газосварщик/obuch"}) {
		t.Fatalf("подбор = %q", got)
	}
}

// Обзорная статья («профессии 2026») ни к одной профессии каталога не привязана. Пустой
// подбор — законный исход: блок под статьёй заполнит сама тема.
func TestSelectReturnsNothingForGeneralArticle(t *testing.T) {
	programs := []Program{program(1, "obuch", "Сварщик")}
	if got := Select(programs, Request{Professions: "карьера, зарплата", Topic: "высокооплачиваемые профессии"}); len(got) != 0 {
		t.Fatalf("ожидался пустой подбор, получено %q", names(got))
	}
}

// Второй круг идёт только после того, как каждая профессия дала по услуге.
func TestSelectSecondRoundAfterEveryProfession(t *testing.T) {
	programs := []Program{
		program(1, "obuch", "Сантехник"),
		program(2, "perepod", "Сантехник"),
		program(3, "obuch", "Слесарь"),
	}
	got := names(Select(programs, Request{Professions: "сантехник, слесарь"}))
	want := []string{"Сантехник/obuch", "Слесарь/obuch", "Сантехник/perepod"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("подбор = %q, ожидалось %q", got, want)
	}
}
