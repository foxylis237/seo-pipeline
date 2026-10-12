package taskflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/logtest"
)

type fakeChat struct {
	answer  string
	prompts []string
}

func (c *fakeChat) Send(_ context.Context, prompt string) (string, error) {
	c.prompts = append(c.prompts, prompt)
	return c.answer, nil
}

func (c *fakeChat) Continue(ctx context.Context, prompt string) (string, error) {
	return c.Send(ctx, prompt)
}

func (c *fakeChat) Close() error { return nil }

type fakeNames struct {
	names map[string]string
	err   error
}

func (n fakeNames) Name(_ context.Context, url string) (string, error) {
	return n.names[url], n.err
}

func TestCompleteLinksAsksModelBelowThreshold(t *testing.T) {
	var recorder logtest.Recorder
	base := newBase(&fakeRepository{}, fakeWriter{}, fakeRenderer{})
	links := "Обучение на логопеда — https://example.test/logoped"
	sentence := `Этому учат на программе <a href="https://example.test/logoped">Обучение на логопеда</a>.`
	chat := &fakeChat{answer: "https://example.test/logoped ;; Второй раздел ;; " + sentence}
	markup := "<h2>Заголовок</h2>\n<p>текст без перелинковки</p>\n<h2>Второй раздел</h2>\n<p>ещё текст.</p>"

	got := base.CompleteLinks(context.Background(), recorder.Logger(), chat, "html", links, markup)

	if len(chat.prompts) != 1 {
		t.Fatalf("доспрос: %d сообщений, ожидалось одно", len(chat.prompts))
	}
	if !strings.Contains(got, "<p>ещё текст. "+sentence+"</p>") {
		t.Fatalf("предложение модели не вписано в раздел: %q", got)
	}
	logtest.AssertVocabulary(t, recorder.Records(), "html")
}

func TestCompleteLinksSkipsRepairWithThreeLinks(t *testing.T) {
	var recorder logtest.Recorder
	base := newBase(&fakeRepository{}, fakeWriter{}, fakeRenderer{})
	links := "https://example.test/a\nhttps://example.test/b\nhttps://example.test/c\nhttps://example.test/d"
	markup := `<h2>Один</h2><p>про <a href="https://example.test/a">a</a></p>` +
		`<h2>Два</h2><p>про <a href="https://example.test/b">b</a></p>` +
		`<h2>Три</h2><p>про <a href="https://example.test/c">c</a></p>` +
		`<h2>Четыре</h2><p>без ссылки</p>`
	chat := &fakeChat{}

	got := base.CompleteLinks(context.Background(), recorder.Logger(), chat, "html", links, markup)

	if len(chat.prompts) != 0 {
		t.Fatalf("трёх ссылок достаточно, а модель доспрошена: %q", chat.prompts)
	}
	if strings.Contains(got, "https://example.test/d") {
		t.Fatalf("недостающую ссылку дописал код: %q", got)
	}
}

func TestNamedLinksKeepsBareURLOnFailure(t *testing.T) {
	var recorder logtest.Recorder
	links := "https://example.test/logoped"

	named := NamedLinks(context.Background(), recorder.Logger(),
		fakeNames{names: map[string]string{links: "Обучение на логопеда"}}, "html", links)
	if named != "Обучение на логопеда — "+links {
		t.Fatalf("NamedLinks() = %q", named)
	}

	bare := NamedLinks(context.Background(), recorder.Logger(),
		fakeNames{err: errors.New("страница ответила 503")}, "html", links)
	if bare != links {
		t.Fatalf("при отказе страницы ждали голый адрес, получили %q", bare)
	}
	logtest.AssertVocabulary(t, recorder.Records(), "html")
}
