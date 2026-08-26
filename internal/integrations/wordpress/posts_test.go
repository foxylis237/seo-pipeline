package wordpress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// postsResponse собирает ответ wp.getPosts из пар «идентификатор, заголовок».
func postsResponse(posts ...[2]string) string {
	var body strings.Builder
	body.WriteString(`<value><array><data>`)
	for _, post := range posts {
		fmt.Fprintf(&body, `<value><struct>`+
			`<member><name>post_id</name><value><string>%s</string></value></member>`+
			`<member><name>post_title</name><value><string>%s</string></value></member>`+
			`<member><name>post_type</name><value><string>teachers</string></value></member>`+
			`</struct></value>`, post[0], post[1])
	}
	body.WriteString(`</data></array></value>`)
	return methodResponse(body.String())
}

func TestFindPostIDByTitleMatchesExactTitle(t *testing.T) {
	var body string
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "text/xml")
		// Поиск идёт подстрокой: WordPress возвращает и однофамильца, которого брать нельзя.
		fmt.Fprint(w, postsResponse(
			[2]string{"21999", "Перов Андрей Викторович"},
			[2]string{"21785", "Перов Андрей Валерьевич"},
		))
	})

	id, err := client.FindPostIDByTitle(context.Background(), "teachers", "Перов Андрей Валерьевич")
	if err != nil {
		t.Fatalf("FindPostIDByTitle: %v", err)
	}
	if id != 21785 {
		t.Fatalf("идентификатор = %d, ожидался 21785", id)
	}
	for _, want := range []string{
		"<methodName>wp.getPosts</methodName>",
		"<name>post_type</name><value><string>teachers</string></value>",
		// Черновики и корзина не годятся: такой преподаватель в блоге не отображается.
		"<name>post_status</name><value><string>publish</string></value>",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("в запросе нет %q:\n%s", want, body)
		}
	}
}

// Похожий заголовок совпадением не считается: связь ACF уходит в опубликованную страницу,
// и чужой преподаватель там дороже отказа.
func TestFindPostIDByTitleReportsMissingWithoutGuessing(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, postsResponse([2]string{"21999", "Перов Андрей Викторович"}))
	})

	_, err := client.FindPostIDByTitle(context.Background(), "teachers", "Перов Андрей Валерьевич")
	var notFound *ErrPostNotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("ошибка = %v, ожидалась ErrPostNotFound", err)
	}
	if notFound.PostType != "teachers" {
		t.Fatalf("тип записи в ошибке = %q", notFound.PostType)
	}
}

// Две записи с одинаковым заголовком — не повод выбрать первую: выбирает человек.
func TestFindPostIDByTitleRefusesAmbiguousMatch(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, postsResponse(
			[2]string{"21785", "Перов Андрей Валерьевич"},
			[2]string{"31000", "Перов Андрей Валерьевич"},
		))
	})

	_, err := client.FindPostIDByTitle(context.Background(), "teachers", "Перов Андрей Валерьевич")
	var ambiguous *ErrPostAmbiguous
	if !errors.As(err, &ambiguous) {
		t.Fatalf("ошибка = %v, ожидалась ErrPostAmbiguous", err)
	}
	if len(ambiguous.IDs) != 2 {
		t.Fatalf("в ошибке %d записей, ожидалось 2", len(ambiguous.IDs))
	}
}

// Регистр и сущности HTML совпадению не мешают: WordPress отдаёт заголовки закодированными.
func TestFindPostIDByTitleIgnoresCaseAndEntities(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, postsResponse([2]string{"21785", "Пётр &amp; Сын"}))
	})

	id, err := client.FindPostIDByTitle(context.Background(), "teachers", "пётр & сын")
	if err != nil {
		t.Fatalf("FindPostIDByTitle: %v", err)
	}
	if id != 21785 {
		t.Fatalf("идентификатор = %d", id)
	}
}

// termsResponse собирает ответ wp.getTerms.
func termsResponse(terms ...[2]string) string {
	var body strings.Builder
	body.WriteString(`<value><array><data>`)
	for _, term := range terms {
		fmt.Fprintf(&body, `<value><struct>`+
			`<member><name>term_id</name><value><string>%s</string></value></member>`+
			`<member><name>name</name><value><string>%s</string></value></member>`+
			`</struct></value>`, term[0], term[1])
	}
	body.WriteString(`</data></array></value>`)
	return methodResponse(body.String())
}

// Рубрика страницы услуги живёт в своей таксономии: её имя уходит в запрос как есть, без
// догадок о маршруте REST.
func TestFindTermIDInTaxonomyMatchesExactName(t *testing.T) {
	var body string
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, termsResponse(
			[2]string{"1106952", "Обучение медперсонала среднего звена"},
			[2]string{"1106951", "Обучение медперсонала"},
		))
	})

	id, err := client.FindTermIDInTaxonomy(context.Background(), "obuch_med-cat", "Обучение медперсонала")
	if err != nil {
		t.Fatalf("FindTermIDInTaxonomy: %v", err)
	}
	if id != 1106951 {
		t.Fatalf("идентификатор рубрики = %d, ожидался 1106951", id)
	}
	for _, want := range []string{
		"<methodName>wp.getTerms</methodName>",
		"<value><string>obuch_med-cat</string></value>",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("в запросе нет %q:\n%s", want, body)
		}
	}
}

// Ненайденная рубрика останавливает публикацию: заводить термины приложению запрещено.
func TestFindTermIDInTaxonomyReportsMissing(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, termsResponse([2]string{"1106952", "Обучение медперсонала среднего звена"}))
	})

	_, err := client.FindTermIDInTaxonomy(context.Background(), "obuch_med-cat", "Обучение медперсонала")
	var notFound *ErrTermNotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("ошибка = %v, ожидалась ErrTermNotFound", err)
	}
	if notFound.Taxonomy != "obuch_med-cat" {
		t.Fatalf("таксономия в ошибке = %q", notFound.Taxonomy)
	}
}
