package catalog

import (
	"reflect"
	"strings"
	"testing"
)

func withIndustry(id int64, postType, title, industrySlug string) Program {
	item := program(id, postType, title)
	item.Slug = ShortName(title)
	item.Industry = Industry{Taxonomy: postType + "-cat", TermID: 100, Slug: industrySlug, Name: industrySlug}
	return item
}

func relatedNames(related []Related) []string {
	names := make([]string, 0, len(related))
	for _, item := range related {
		names = append(names, item.Program.Name)
	}
	return names
}

// Блок под статьёй не повторяет перелинковку: эти курсы читатель уже видел в тексте.
func TestSelectRelatedSkipsLinkedPrograms(t *testing.T) {
	programs := []Program{
		withIndustry(1, "obuch", "Сантехник", "santehnik"),
		withIndustry(2, "obuch", "Слесарь", "santehnik"),
		withIndustry(3, "obuch", "Монтажник", "santehnik"),
		withIndustry(4, "obuch", "Трубопроводчик", "santehnik"),
	}
	programs[0].Slug = "distancionnoe-obuchenie-santehnik"

	related := SelectRelated(programs, Request{
		Professions: "сантехник, слесарь, монтажник, трубопроводчик",
		Links:       "https://dpoprof.ru/obuchenie/distancionnoe-obuchenie-santehnik/",
	})
	got := relatedNames(related)
	if len(got) != 3 {
		t.Fatalf("подобрано %d курсов: %q", len(got), got)
	}
	for _, name := range got {
		if name == "Сантехник" {
			t.Fatalf("в блок попала программа из перелинковки: %q", got)
		}
	}
}

// Профессия, которой в тексте не было, идёт первой — ради неё блок и существует.
func TestSelectRelatedPrefersProfessionsAbsentFromText(t *testing.T) {
	programs := []Program{
		// Программа, стоящая в тексте статьи: сама она в блок не попадёт, но своей
		// профессией скажет, что сантехник читателю уже показан.
		withIndustry(1, "obuch", "Сантехник", "santehnik"),
		withIndustry(2, "obuch", "Сантехник разряды", "santehnik"),
		withIndustry(3, "obuch", "Монтажник", "santehnik"),
	}
	programs[0].Slug = "distancionnoe-obuchenie-santehnik"
	programs[1].Profession = ProfessionOf("Сантехник")
	programs[1].Slug = "santehnik-razryady"

	related := SelectRelated(programs, Request{
		Professions: "сантехник, монтажник",
		Links:       "https://dpoprof.ru/obuchenie/distancionnoe-obuchenie-santehnik/",
		Limit:       2,
	})
	if got := relatedNames(related); len(got) == 0 || got[0] != "Монтажник" {
		t.Fatalf("порядок подбора = %q, первым ожидался Монтажник", got)
	}
}

// Обучение первым: статью про профессию читает тот, кто входит в неё с нуля.
func TestSelectRelatedPrefersInitialTraining(t *testing.T) {
	programs := []Program{
		withIndustry(1, "povysh", "Сварщик", "svarshhik"),
		withIndustry(2, "obuch", "Сварщик", "svarshhik"),
		withIndustry(3, "perepod", "Сварщик", "svarshhik"),
	}
	related := SelectRelated(programs, Request{Professions: "сварщик"})
	want := []string{"обучение", "переподготовка", "повышение квалификации"}
	got := make([]string, 0, len(related))
	for _, item := range related {
		got = append(got, lowerFirst(item.Program.Category))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("порядок типов = %q, ожидался %q", got, want)
	}
}

// Круг расширяется, пока три не наберутся: у профессии статьи может не быть трёх программ,
// и тогда блок добирает соседей по рубрике площадки.
func TestSelectRelatedWidensToIndustry(t *testing.T) {
	programs := []Program{
		withIndustry(1, "obuch", "Кровельщик", "krovelshhik-izolirovshhik"),
		withIndustry(2, "obuch", "Изолировщик", "krovelshhik-izolirovshhik"),
		withIndustry(3, "obuch", "Жестянщик", "krovelshhik-izolirovshhik"),
		withIndustry(4, "obuch", "Повар", "povar"),
	}
	related := SelectRelated(programs, Request{Professions: "кровельщик"})
	got := relatedNames(related)
	if len(got) != 3 {
		t.Fatalf("подобрано %d курсов: %q", len(got), got)
	}
	for _, name := range got {
		if name == "Повар" {
			t.Fatalf("подбор вышел за рубрику статьи: %q", got)
		}
	}
	if CountNeighbours(related) != 2 {
		t.Fatalf("смежных %d, ожидалось 2: %q", CountNeighbours(related), got)
	}
}

// Соседние таксономии — последняя ступень: обучение, переподготовка и повышение нарезаны
// почти одинаково, и рубрика santehnik в них одна и та же профессиональная группа.
func TestSelectRelatedWidensToNeighbourTaxonomy(t *testing.T) {
	programs := []Program{
		withIndustry(1, "obuch", "Сантехник", "santehnik"),
		withIndustry(2, "perepod", "Слесарь сантехник", "santehnik"),
		withIndustry(3, "povysh", "Трубопроводчик", "santehnik"),
	}
	programs[1].Industry.TermID = 200
	programs[2].Industry.TermID = 300

	related := SelectRelated(programs, Request{Professions: "сантехник"})
	if got := relatedNames(related); len(got) != 3 {
		t.Fatalf("подобрано %d курсов: %q", len(got), got)
	}
	if related[2].Widened != true {
		t.Fatalf("последний курс обязан быть помечен расширенным подбором: %+v", related[2])
	}
}

// Одна и та же услуга не может занять в блоке два места.
func TestSelectRelatedNeverRepeatsProgram(t *testing.T) {
	programs := []Program{withIndustry(1, "obuch", "Сантехник", "santehnik")}
	related := SelectRelated(programs, Request{Professions: "сантехник"})
	if len(related) != 1 {
		t.Fatalf("подобрано %d курсов", len(related))
	}
}

func lowerFirst(value string) string {
	return strings.ToLower(value)
}
