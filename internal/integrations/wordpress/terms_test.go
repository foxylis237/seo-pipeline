package wordpress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestFindCategoryIDMatchesExactName(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != categoriesPath {
			t.Errorf("путь запроса = %q", r.URL.Path)
		}
		fmt.Fprint(w, `[{"id":2564,"name":"IT и цифровые профессии"},
			{"id":2575,"name":"Сварка, слесарка и металлообработка"},
			{"id":2576,"name":"Строительство"}]`)
	})

	id, err := client.FindCategoryID(context.Background(), "Сварка, слесарка и металлообработка")
	if err != nil {
		t.Fatalf("FindCategoryID: %v", err)
	}
	if id != 2575 {
		t.Fatalf("идентификатор рубрики = %d, ожидался 2575", id)
	}
}

func TestFindCategoryIDReportsMissingWithoutGuessing(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":2576,"name":"Строительство"}]`)
	})

	_, err := client.FindCategoryID(context.Background(), "Строительство и ремонт")
	var notFound *ErrTermNotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("ошибка %v не разбирается как *ErrTermNotFound", err)
	}
	// Заводить рубрики запрещено, поэтому ближайшее по написанию имя брать нельзя.
	if !strings.Contains(err.Error(), "Строительство и ремонт") {
		t.Fatalf("ошибка не называет искомое имя: %v", err)
	}
}

func TestFindTagIDIgnoresSubstringMatchesFromSearch(t *testing.T) {
	// search у WordPress ищет подстрокой: на «Как стать» он возвращает два десятка меток.
	// Взять первую значило бы повесить на статью чужую метку.
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("search") != "Как стать" {
			t.Errorf("search = %q", r.URL.Query().Get("search"))
		}
		fmt.Fprint(w, `[{"id":1831,"name":"аппаратчик как стать"},
			{"id":1771,"name":"как стать автослесарем"},
			{"id":48323,"name":"Как стать"},
			{"id":1787,"name":"как стать сварщиком"}]`)
	})

	id, err := client.FindTagID(context.Background(), "Как стать")
	if err != nil {
		t.Fatalf("FindTagID: %v", err)
	}
	if id != 48323 {
		t.Fatalf("идентификатор метки = %d, ожидался 48323 (точное совпадение)", id)
	}
}

func TestFindTagIDMatchesCaseInsensitivelyAndUnescapes(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// WordPress отдаёт имена закодированными: сырой амперсанд из Excel с ними не совпал бы.
		fmt.Fprint(w, `[{"id":4242,"name":"Финансы &amp; право"}]`)
	})

	id, err := client.FindTagID(context.Background(), "финансы & право")
	if err != nil {
		t.Fatalf("FindTagID: %v", err)
	}
	if id != 4242 {
		t.Fatalf("идентификатор метки = %d", id)
	}
}

func TestFindCategoryIDWalksPages(t *testing.T) {
	var pages []string
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		if page == "1" {
			// Полная страница означает, что справочник на этом не кончился.
			var items []string
			for index := 0; index < termsPerPage; index++ {
				items = append(items, fmt.Sprintf(`{"id":%d,"name":"Рубрика %d"}`, 1000+index, index))
			}
			fmt.Fprint(w, "["+strings.Join(items, ",")+"]")
			return
		}
		fmt.Fprint(w, `[{"id":2575,"name":"Сварка, слесарка и металлообработка"}]`)
	})

	id, err := client.FindCategoryID(context.Background(), "Сварка, слесарка и металлообработка")
	if err != nil {
		t.Fatalf("FindCategoryID: %v", err)
	}
	if id != 2575 {
		t.Fatalf("идентификатор рубрики = %d", id)
	}
	if !reflect.DeepEqual(pages, []string{"1", "2"}) {
		t.Fatalf("запрошены страницы %v, ожидались 1 и 2", pages)
	}
}

// Найденная метка возвращается как есть: ни одного записывающего запроса от EnsureTag быть
// не должно, иначе повторная публикация плодила бы термины.
func TestEnsureTagReusesExistingTag(t *testing.T) {
	var methods []string
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		fmt.Fprint(w, `[{"id":1801,"name":"Газосварщик"}]`)
	})

	tag, err := client.EnsureTag(context.Background(), "Газосварщик")
	if err != nil {
		t.Fatalf("EnsureTag: %v", err)
	}
	if tag.ID != 1801 || tag.Created {
		t.Fatalf("метка = %+v, ожидалась существующая 1801", tag)
	}
	if !reflect.DeepEqual(methods, []string{http.MethodGet}) {
		t.Fatalf("запросы %v, ожидался один GET", methods)
	}
}

func TestEnsureTagCreatesMissingTag(t *testing.T) {
	var createdName string
	var methods []string
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method == http.MethodPost {
			if r.URL.Path != tagsPath {
				t.Errorf("путь создания = %q", r.URL.Path)
			}
			var payload struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("тело запроса: %v", err)
			}
			createdName = payload.Name
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":48401,"name":"Мастер СМР"}`)
			return
		}
		// Поиск подстрокой возвращает соседей, но точного совпадения среди них нет.
		fmt.Fprint(w, `[{"id":1902,"name":"мастер смр участка"}]`)
	})

	tag, err := client.EnsureTag(context.Background(), "Мастер СМР")
	if err != nil {
		t.Fatalf("EnsureTag: %v", err)
	}
	if tag.ID != 48401 || !tag.Created {
		t.Fatalf("метка = %+v, ожидалась заведённая 48401", tag)
	}
	// Имя уходит в том написании, в котором его дал человек: нормализация нужна сравнению,
	// а не блогу.
	if createdName != "Мастер СМР" {
		t.Fatalf("метка заведена под именем %q", createdName)
	}
	if !reflect.DeepEqual(methods, []string{http.MethodGet, http.MethodPost}) {
		t.Fatalf("запросы %v, ожидались поиск и создание", methods)
	}
}

// Гонка: между нашим поиском и нашей записью метку успели завести. WordPress отвечает
// 400 term_exists и называет term_id — это успех, а не отказ, и дубля не возникает.
func TestEnsureTagAcceptsTermExistsRace(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"code":"term_exists","message":"Term already exists.",
				"data":{"status":400,"term_id":48402}}`)
			return
		}
		fmt.Fprint(w, `[]`)
	})

	tag, err := client.EnsureTag(context.Background(), "Мастер СМР")
	if err != nil {
		t.Fatalf("EnsureTag: %v", err)
	}
	if tag.ID != 48402 {
		t.Fatalf("идентификатор метки = %d, ожидался 48402 из term_exists", tag.ID)
	}
	// Заведение приписывать себе нельзя: метку создал не этот вызов, и в лог о создании она
	// попадать не должна.
	if tag.Created {
		t.Fatal("метка из term_exists помечена как заведённая нами")
	}
}

// Отказ, который не term_exists, идентификатора не даёт: взять число из чужого data значило
// бы повесить на статью произвольный термин.
func TestEnsureTagKeepsOtherFailures(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"code":"rest_cannot_create","message":"Sorry, you are not allowed to create terms",
				"data":{"status":403,"term_id":777}}`)
			return
		}
		fmt.Fprint(w, `[]`)
	})

	_, err := client.EnsureTag(context.Background(), "Мастер СМР")
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("ошибка %v не разбирается как *StatusError", err)
	}
	if statusErr.StatusCode != http.StatusForbidden {
		t.Fatalf("код отказа = %d", statusErr.StatusCode)
	}
}

// Площадка ответила успехом, но именем не тем: фильтр темы или плагин подрезал строку.
// Принять такую метку нельзя — на статью встал бы термин, которого человек не заказывал.
func TestEnsureTagRejectsRenamedTag(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":48403,"name":"Мастер"}`)
			return
		}
		fmt.Fprint(w, `[]`)
	})

	_, err := client.EnsureTag(context.Background(), "Мастер СМР")
	var responseErr *ResponseError
	if !errors.As(err, &responseErr) {
		t.Fatalf("ошибка %v не разбирается как *ResponseError", err)
	}
}

// Пустое имя не ищется и не заводится: заводить нечего, а пустая метка в блоге неудаляема.
func TestEnsureTagRejectsEmptyName(t *testing.T) {
	var requests int
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { requests++ })

	if _, err := client.EnsureTag(context.Background(), "  "); err == nil {
		t.Fatal("пустое имя метки принято")
	}
	if requests != 0 {
		t.Fatalf("запросов %d, пустое имя не должно уходить в сеть", requests)
	}
}

func TestFindTermIDRejectsEmptyName(t *testing.T) {
	var requests int
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { requests++ })

	if _, err := client.FindCategoryID(context.Background(), "   "); err == nil {
		t.Fatal("пустое имя рубрики принято")
	}
	if _, err := client.FindTagID(context.Background(), ""); err == nil {
		t.Fatal("пустое имя метки принято")
	}
	if requests != 0 {
		t.Fatalf("запросов %d, пустое имя не должно уходить в сеть", requests)
	}
}

func TestSplitTermNames(t *testing.T) {
	cases := map[string]struct {
		raw  string
		want []string
	}{
		"обычный список": {
			"Газосварщик, Обучение рабочим профессиям, Должностные обязанности",
			[]string{"Газосварщик", "Обучение рабочим профессиям", "Должностные обязанности"}},
		"лишние пробелы и пустые": {
			" Газосварщик ,, Как стать ,",
			[]string{"Газосварщик", "Как стать"}},
		// Одна и та же метка дважды — не две метки, а лишний идентификатор в запросе.
		"повторы отбрасываются": {
			"Газосварщик, газосварщик, Как стать",
			[]string{"Газосварщик", "Как стать"}},
		"пусто": {"  ,  ", nil},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			got := SplitTermNames(testCase.raw)
			if len(got) == 0 && len(testCase.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("получено %q, ожидалось %q", got, testCase.want)
			}
		})
	}
}
