package catalog

import (
	"reflect"
	"testing"
)

// Типы и таксономия dpoprof — страж соседа. Каталог собирался под эту площадку, у неё 1658
// живых услуг и подбор связанных курсов у двух задач; изменённый здесь список означал бы
// пересобранный каталог с потерянными услугами, а увидеть это можно было бы только на
// опубликованной статье.
func TestDPOProfKeepsItsTypesAndTaxonomy(t *testing.T) {
	site := DPOProf()
	want := []string{"obuch", "perepod", "povysh", "obuch_med", "bezopasnost", "attestaciya"}
	if got := site.PostTypes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("типы dpoprof = %v, ожидались %v", got, want)
	}
	if got := site.taxonomy("obuch"); got != "obuch-cat" {
		t.Fatalf("таксономия obuch = %q, ожидалась obuch-cat", got)
	}
	// PostTypes пакета — тот же список: его берут задачи аудита, и второй копии быть не должно.
	if got := PostTypes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog.PostTypes() = %v, ожидались %v", got, want)
	}
}

// У второй площадки типы свои, а таксономия названа зеркально: cat_rabprof, а не rabprof-cat.
// Ошибка здесь не ломает сбор — он просто не найдёт ни одной рубрики и объявит все 1528 услуг
// служебными страницами.
func TestObuchimHasItsOwnTypesAndTaxonomy(t *testing.T) {
	site := Obuchim()
	want := []string{"rabprof", "perepodgotovka", "povyshenie", "akkreditaciya", "medpersonal", "attestaciya"}
	if got := site.PostTypes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("типы obuchim = %v, ожидались %v", got, want)
	}
	for postType, want := range map[string]string{
		"rabprof":        "cat_rabprof",
		"perepodgotovka": "cat_perepodgotovka",
		"attestaciya":    "cat_attestaciya",
	} {
		if got := site.taxonomy(postType); got != want {
			t.Errorf("таксономия %s = %q, ожидалась %q", postType, got, want)
		}
	}
}

// Тип с именем attestaciya есть у обеих площадок, и человеческое название у него разное не
// случайно: у dpoprof это шестой тип со своим смыслом, у obuchim — тоже шестой, но список
// целиком свой. Тест держит то, что площадки не делят один набор.
func TestSitesDoNotShareTypeLists(t *testing.T) {
	dpoprof, obuchim := DPOProf(), Obuchim()
	if reflect.DeepEqual(dpoprof.PostTypes(), obuchim.PostTypes()) {
		t.Fatal("у площадок совпали списки типов — одна из них собирает услуги другой")
	}
	if dpoprof.Key() == obuchim.Key() {
		t.Fatal("у площадок совпали ключи — composition root выберет им одну схему")
	}
	for _, site := range []Site{dpoprof, obuchim} {
		if _, _, known := site.describe("нет такого типа"); known {
			t.Fatalf("площадка %s опознала чужой тип записи", site.Key())
		}
	}
}
