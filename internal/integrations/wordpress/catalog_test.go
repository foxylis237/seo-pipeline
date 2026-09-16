package wordpress

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// catalogResponse собирает ответ wp.getPosts с рубриками записи — так их отдаёт площадка:
// термины лежат структурами внутри поля terms, вперемешку по таксономиям.
func catalogResponse(posts ...catalogFixture) string {
	var body strings.Builder
	body.WriteString(`<value><array><data>`)
	for _, post := range posts {
		fmt.Fprintf(&body, `<value><struct>`+
			`<member><name>post_id</name><value><string>%d</string></value></member>`+
			`<member><name>post_title</name><value><string>%s</string></value></member>`+
			`<member><name>post_name</name><value><string>%s</string></value></member>`+
			`<member><name>link</name><value><string>%s</string></value></member>`+
			`<member><name>terms</name><value><array><data>`, post.id, post.title, post.slug, post.link)
		for _, term := range post.terms {
			fmt.Fprintf(&body, `<value><struct>`+
				`<member><name>term_id</name><value><string>%d</string></value></member>`+
				`<member><name>taxonomy</name><value><string>%s</string></value></member>`+
				`<member><name>slug</name><value><string>%s</string></value></member>`+
				`<member><name>name</name><value><string>%s</string></value></member>`+
				`</struct></value>`, term.id, term.taxonomy, term.slug, term.name)
		}
		body.WriteString(`</data></array></value></member></struct></value>`)
	}
	body.WriteString(`</data></array></value>`)
	return methodResponse(body.String())
}

type catalogFixture struct {
	id                int64
	title, slug, link string
	terms             []catalogTermFixture
}

type catalogTermFixture struct {
	id                   int64
	taxonomy, slug, name string
}

func TestListCatalogPostsReadsTermsAndUnescapesTitle(t *testing.T) {
	var body string
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, catalogResponse(catalogFixture{
			id: 504, title: "Сантехник &#8212; обучение", slug: "santehnik",
			link:  "https://dpoprof.ru/obuchenie/santehnik/",
			terms: []catalogTermFixture{{id: 1333, taxonomy: "obuch-cat", slug: "santehnik", name: "Сантехническое дело"}},
		}))
	})

	posts, err := client.ListCatalogPosts(context.Background(), "obuch")
	if err != nil {
		t.Fatalf("ListCatalogPosts: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("получено %d записей", len(posts))
	}
	post := posts[0]
	// Сущности разворачиваются у входа: дальше по коду обязан ходить обычный текст.
	if post.ID != 504 || post.Title != "Сантехник — обучение" || post.Slug != "santehnik" {
		t.Fatalf("запись разобрана как %+v", post)
	}
	if len(post.Terms) != 1 || post.Terms[0].Taxonomy != "obuch-cat" || post.Terms[0].TermID != 1333 {
		t.Fatalf("термины разобраны как %+v", post.Terms)
	}
	// Тип записи и статус уходят в запрос явно: черновики и корзина в каталоге не нужны.
	for _, want := range []string{"<name>post_type</name><value><string>obuch</string></value>",
		"<name>post_status</name><value><string>publish</string></value>",
		"<value><string>terms</string></value>"} {
		if !strings.Contains(body, want) {
			t.Fatalf("в запросе нет %q: %s", want, body)
		}
	}
}

// Тип с полной первой страницей читается дальше, пока страницы не кончатся.
func TestListCatalogPostsWalksPages(t *testing.T) {
	page := 0
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		if page == 0 {
			page++
			fixtures := make([]catalogFixture, 0, postsPerPage)
			for index := 0; index < postsPerPage; index++ {
				fixtures = append(fixtures, catalogFixture{
					id: int64(index + 1), title: "Сварщик", slug: "svarshhik", link: "https://dpoprof.ru/x/",
					terms: []catalogTermFixture{{id: 7, taxonomy: "obuch-cat", slug: "svarshhik", name: "Сварочные работы"}},
				})
			}
			fmt.Fprint(w, catalogResponse(fixtures...))
			return
		}
		fmt.Fprint(w, catalogResponse(catalogFixture{
			id: 999, title: "Токарь", slug: "tokar", link: "https://dpoprof.ru/y/",
			terms: []catalogTermFixture{{id: 8, taxonomy: "obuch-cat", slug: "tokar", name: "Токарные работы"}},
		}))
	})

	posts, err := client.ListCatalogPosts(context.Background(), "obuch")
	if err != nil {
		t.Fatalf("ListCatalogPosts: %v", err)
	}
	if len(posts) != postsPerPage+1 {
		t.Fatalf("получено %d записей", len(posts))
	}
}
