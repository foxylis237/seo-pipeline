package catalog

import (
	"reflect"
	"testing"
)

// Названия услуг взяты с площадки как есть, включая сущности HTML: именно в таком виде их
// отдаёт wp.getPosts, и разбор обязан работать по ним, а не по причёсанным строкам.
func TestShortNameCutsDescription(t *testing.T) {
	cases := map[string]string{
		"Сантехник &#8212; Дистанционное обучение рабочей профессии": "Сантехник",
		"Экология - дистанционное обучение с внесением в ФИС ФРДО":   "Экология",
		"Монтажник 5 разряда":          "Монтажник 5 разряда",
		"Кузнец-штамповщик — обучение": "Кузнец-штамповщик",
	}
	for title, want := range cases {
		if got := ShortName(title); got != want {
			t.Errorf("ShortName(%q) = %q, ожидалось %q", title, got, want)
		}
	}
}

// Услуга, названная профессией, даёт рубрику из одного слова: разряд и специализация —
// уточнения той же профессии, и разводить их по разным рубрикам нельзя.
func TestProfessionOfSingleWordForOccupation(t *testing.T) {
	cases := map[string]string{
		"Сварщик 6 разряда (высший)":             "svarshhik",
		"Сварщик трубопроводов":                  "svarshhik",
		"Монтажник — дистанционное обучение":     "montazhnik",
		"Промышленный альпинист":                 "alpinist",
		"Дистанционное обучение на газосварщика": "gazosvarshhik",
	}
	for title, want := range cases {
		if got := ProfessionOf(title).Slug; got != want {
			t.Errorf("ProfessionOf(%q).Slug = %q, ожидалось %q", title, got, want)
		}
	}
}

// Услуга, названная направлением, требует двух слов: «дело» само по себе рубрикой быть не
// может — под ним сошлись бы банковское, библиотечное и сестринское.
func TestProfessionOfTwoWordsForTopic(t *testing.T) {
	cases := map[string]string{
		"Маркшейдерское дело":                               "markshejdersko-delo",
		"Радиационная безопасность и радиационный контроль": "radiacionna-bezopasnost",
		"Организация перевозок и управление на транспорте":  "organizaci-perevozok",
	}
	for title, want := range cases {
		if got := ProfessionOf(title).Slug; got != want {
			t.Errorf("ProfessionOf(%q).Slug = %q, ожидалось %q", title, got, want)
		}
	}
}

// Имя рубрики читает человек — оно собирается из тех же слов, что и ключ, но не из основ.
func TestProfessionOfKeepsReadableName(t *testing.T) {
	if got := ProfessionOf("Маркшейдерское дело").Name; got != "Маркшейдерское дело" {
		t.Errorf("имя рубрики %q", got)
	}
	if got := ProfessionOf("Сварщик 4 разряда").Name; got != "Сварщик" {
		t.Errorf("имя рубрики %q", got)
	}
}

// Колонка professions книги заполняется человеком: порядок в ней значим, повторы не нужны.
func TestMatchKeysKeepsOrderAndDropsDuplicates(t *testing.T) {
	got := MatchKeys("сантехник, слесарь, сантехника, монтажник")
	want := []string{"сантехник", "слесарь", "монтажник"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MatchKeys = %q, ожидалось %q", got, want)
	}
}

// Падежи одной профессии обязаны сойтись на одной строке каталога.
func TestStemUnifiesCases(t *testing.T) {
	for _, word := range []string{"сантехник", "сантехника", "сантехники", "сантехникам"} {
		if got := Stem(word); got != "сантехник" {
			t.Errorf("Stem(%q) = %q", word, got)
		}
	}
	// Короткое слово не режется: от «повар» после отсечения не осталось бы профессии.
	if got := Stem("повар"); got != "повар" {
		t.Errorf("Stem(повар) = %q", got)
	}
}

// Тема статьи разбирается по словам: профессия в ней стоит где угодно.
func TestTopicKeysTakesEveryWord(t *testing.T) {
	got := TopicKeys("обучение на газосварщика: с чего начать")
	want := []string{"газосварщик", "чего", "начать"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TopicKeys = %q, ожидалось %q", got, want)
	}
}

// Рубрика второй площадки становится профессией как есть: слаг у неё уже латинский, имя
// написано людьми. Своя транслитерация разошлась бы с тем, что человек видит на сайте.
func TestProfessionFromIndustryTakesTermAsIs(t *testing.T) {
	got := ProfessionFromIndustry(Industry{
		Taxonomy: "cat_rabprof", TermID: 3062,
		Slug: "apparatchik-avtoklavhiki", Name: "Аппаратчики и автоклавщики",
	})
	if got.Slug != "apparatchik-avtoklavhiki" {
		t.Fatalf("слаг = %q", got.Slug)
	}
	if got.Name != "Аппаратчики и автоклавщики" {
		t.Fatalf("имя = %q", got.Name)
	}
}

// Имя рубрики называет одну профессию или две через «и». Каждая обязана дать свой синоним:
// иначе половина рубрики осталась бы ненайденной — «наладчик» не нашёлся бы в «Слесарь и
// наладчик». Таких имён 58 из 184.
func TestProfessionFromIndustrySplitsPairedNames(t *testing.T) {
	cases := map[string][]string{
		"Слесарь и наладчик": {"слесарь", "наладчик"},
		"Повар":              {"повар"},
		"Кузнец и литейщик":  {"кузнец", "литейщик"},
	}
	for name, want := range cases {
		got := ProfessionFromIndustry(Industry{Slug: "slug", Name: name}).Aliases
		if !reflect.DeepEqual(got, want) {
			t.Errorf("синонимы %q = %v, ожидались %v", name, got, want)
		}
	}
}

// Пустая рубрика даёт пустую профессию, и такая услуга в каталог не попадает: предлагать
// читателю услугу без профессии хуже, чем не предлагать ничего.
func TestProfessionFromIndustryEmptyWithoutTerm(t *testing.T) {
	for _, industry := range []Industry{{}, {Slug: "slug"}, {Name: "Повар"}} {
		if got := ProfessionFromIndustry(industry); got.Slug != "" {
			t.Errorf("рубрика %+v дала профессию %q", industry, got.Slug)
		}
	}
}
