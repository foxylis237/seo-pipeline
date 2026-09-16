package articlefix

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	preparedAnswerKey   = "faq_loop_0_faq_answer"
	preparedQuestionKey = "faq_loop_0_faq_question"
	preparedOldAnswer   = "<p>Очно, 160 часов.</p>"
	preparedNewAnswer   = "<p>Дистанционно, 72 часа.</p>"
)

// preparedFixture — статья в блоге и её готовая правка на диске.
type preparedFixture struct {
	flow     *Flow
	root     string
	dir      string
	articles *fakeArticles
	blog     *fakeBlog
}

func newPreparedFixture(t *testing.T) preparedFixture {
	t.Helper()
	root := t.TempDir()
	promptPath := filepath.Join(root, "rewrite.txt")
	writePreparedFile(t, promptPath, "Правь статью {{.URL}}\n{{.OriginalHTML}}")
	templatePath := filepath.Join(root, "result.md.tmpl")
	writePreparedFile(t, templatePath, "{{.Title}}\n{{.URL}}")

	article := testArticle()
	preparedRoot := t.TempDir()
	dir := filepath.Join(preparedRoot, DirectoryName(article.ExternalID, article.Slug))
	fields := map[string]string{preparedAnswerKey: preparedOldAnswer, preparedQuestionKey: "Как проходит обучение?"}
	base, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("снимок полей: %v", err)
	}
	writePreparedFile(t, filepath.Join(dir, PreparedBaseFile), originalArticle)
	writePreparedFile(t, filepath.Join(dir, PreparedArticleFile),
		strings.Replace(originalArticle, "Дистанционно, в удобном темпе", "Дистанционно, по гибкому графику", 1))
	writePreparedFile(t, filepath.Join(dir, PreparedBaseFieldsFile), string(base))
	writePreparedFile(t, filepath.Join(dir, PreparedFieldsFolder, preparedAnswerKey+".html"), preparedNewAnswer)
	// Вопрос заготовка скопировала, но не поменяла — в блог он уйти не должен.
	writePreparedFile(t, filepath.Join(dir, PreparedFieldsFolder, preparedQuestionKey+".html"), "Как проходит обучение?")

	articles := &fakeArticles{article: article}
	blog := &fakeBlog{post: Post{
		ID: 42, Title: "Медсестра", ContentHTML: originalArticle, Link: article.SourceURL,
		Fields:   map[string]string{preparedAnswerKey: preparedOldAnswer, preparedQuestionKey: "Как проходит обучение?"},
		FieldIDs: map[string]string{preparedAnswerKey: "901", preparedQuestionKey: "902"},
	}}
	flow, err := NewFlow(articles, blog, fakeChats{}, NewArtifacts(root), KeepTitle{},
		promptPath, templatePath, nil, Prepared(preparedRoot, PreparedFAQFields))
	if err != nil {
		t.Fatalf("NewFlow: %v", err)
	}
	return preparedFixture{flow: flow, root: root, dir: dir, articles: articles, blog: blog}
}

func writePreparedFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("создать каталог %q: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("записать %q: %v", path, err)
	}
}

// Готовая правка уходит в блог без модели: тело целиком, из полей — только изменённое, с
// идентификатором postmeta, а прежнее значение поля ложится в original/ до записи.
func TestFlowWritesPreparedRewrite(t *testing.T) {
	fx := newPreparedFixture(t)
	if err := fx.flow.Run(context.Background(), fx.articles.article.ExternalID); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fx.blog.writes != 1 {
		t.Fatalf("записей в блог %d, ожидалась одна", fx.blog.writes)
	}
	if !strings.Contains(fx.blog.post.ContentHTML, "по гибкому графику") {
		t.Fatalf("в блог ушло не исправленное тело:\n%s", fx.blog.post.ContentHTML)
	}
	want := Field{ID: "901", Key: preparedAnswerKey, Value: preparedNewAnswer}
	if len(fx.blog.written) != 1 || fx.blog.written[0] != want {
		t.Fatalf("ушли поля %+v, ожидалось ровно %+v", fx.blog.written, want)
	}
	original, err := os.ReadFile(filepath.Join(fx.root,
		DirectoryName(fx.articles.article.ExternalID, fx.articles.article.Slug), OriginalFolder, OriginalFieldsFile))
	if err != nil {
		t.Fatalf("прежние поля не сохранены: %v", err)
	}
	if !strings.Contains(string(original), "Очно, 160 часов.") {
		t.Fatalf("в original/%s нет прежнего ответа: %s", OriginalFieldsFile, original)
	}
	if last := fx.articles.calls[len(fx.articles.calls)-1]; !strings.HasPrefix(last, "updated:") {
		t.Fatalf("статья не отмечена переписанной, последний шаг %q", last)
	}
}

// Страницу поправили в админке после подготовки — правка не имеет права её затереть.
func TestFlowRefusesStalePreparedRewrite(t *testing.T) {
	cases := map[string]func(fx preparedFixture){
		"тело": func(fx preparedFixture) {
			fx.blog.post.ContentHTML = strings.Replace(originalArticle, "Вводный абзац", "Поправленный абзац", 1)
		},
		"поле": func(fx preparedFixture) {
			fx.blog.post.Fields[preparedAnswerKey] = "<p>Поправлено руками.</p>"
		},
	}
	for name, stale := range cases {
		t.Run(name, func(t *testing.T) {
			fx := newPreparedFixture(t)
			stale(fx)
			err := fx.flow.Run(context.Background(), fx.articles.article.ExternalID)
			if !errors.Is(err, ErrPreparedStale) {
				t.Fatalf("Run вернул %v, ожидалась ErrPreparedStale", err)
			}
			if fx.blog.writes != 0 {
				t.Fatalf("в блог ушло %d записей при устаревшей правке", fx.blog.writes)
			}
		})
	}
}

// Файл чужого поля в каталоге правок до блога не доходит.
func TestFlowRefusesForeignPreparedField(t *testing.T) {
	fx := newPreparedFixture(t)
	writePreparedFile(t, filepath.Join(fx.dir, PreparedFieldsFolder, HeaderField+".html"), "Новый H1")
	err := fx.flow.Run(context.Background(), fx.articles.article.ExternalID)
	if err == nil || !strings.Contains(err.Error(), HeaderField) {
		t.Fatalf("Run вернул %v, ожидался отказ по полю %s", err, HeaderField)
	}
	if fx.blog.writes != 0 {
		t.Fatalf("в блог ушло %d записей с чужим полем", fx.blog.writes)
	}
}

// Правка, которая ничего не меняет, — пропущенная страница, а не готовая.
func TestFlowRefusesEmptyPreparedRewrite(t *testing.T) {
	fx := newPreparedFixture(t)
	writePreparedFile(t, filepath.Join(fx.dir, PreparedArticleFile), originalArticle)
	writePreparedFile(t, filepath.Join(fx.dir, PreparedFieldsFolder, preparedAnswerKey+".html"), preparedOldAnswer)
	err := fx.flow.Run(context.Background(), fx.articles.article.ExternalID)
	if err == nil || !strings.Contains(err.Error(), "ничего не меняет") {
		t.Fatalf("Run вернул %v, ожидался отказ пустой правки", err)
	}
	if fx.blog.writes != 0 {
		t.Fatalf("в блог ушло %d записей с пустой правкой", fx.blog.writes)
	}
}

// Оборванная готовая правка ловится тем же признаком, что ответ модели.
func TestFlowRefusesTruncatedPreparedRewrite(t *testing.T) {
	fx := newPreparedFixture(t)
	cut := originalArticle[:strings.Index(originalArticle, "<h2>Какой документ выдаётся</h2>")]
	writePreparedFile(t, filepath.Join(fx.dir, PreparedArticleFile), cut)
	if err := fx.flow.Run(context.Background(), fx.articles.article.ExternalID); err == nil {
		t.Fatal("Run принял правку без последнего раздела")
	}
	if fx.blog.writes != 0 {
		t.Fatalf("в блог ушло %d записей с оборванной правкой", fx.blog.writes)
	}
}

// План показывает готовность правки и её объём, ничего не записывая.
func TestPlanShowsPreparedRewrite(t *testing.T) {
	fx := newPreparedFixture(t)
	planned, err := fx.flow.Plan(context.Background(), fx.articles.article)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !planned.PreparedReady || !planned.PreparedTextChanged || planned.PreparedFields != 1 || planned.PreparedProblem != "" {
		t.Fatalf("план правки: %+v", planned)
	}

	fx.blog.post.ContentHTML = strings.Replace(originalArticle, "Вводный абзац", "Поправленный абзац", 1)
	planned, err = fx.flow.Plan(context.Background(), fx.articles.article)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if planned.PreparedReady || !strings.Contains(planned.PreparedProblem, "изменилась") {
		t.Fatalf("план не показал устаревшую правку: %+v", planned)
	}
	if fx.blog.writes != 0 {
		t.Fatalf("план записал в блог %d раз", fx.blog.writes)
	}
}
