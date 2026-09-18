package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/integrations/wordpress"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

// Тело записи переписывается только у опубликованной статьи и только по названному ID:
// массовой правки живого блога у команды нет.
func TestRepublishRequiresExternalID(t *testing.T) {
	_, err := parseTaskCommand([]string{"seo-pipeline", "pprof-1", wordPressRepublishOperation})
	if err == nil {
		t.Fatal("republish без идентификатора принят, хотя правит живую запись блога")
	}
	if !strings.Contains(err.Error(), "external_id") {
		t.Fatalf("ошибка не называет пропущенный идентификатор: %v", err)
	}
}

func TestRepublishParsesExternalID(t *testing.T) {
	command, err := parseTaskCommand([]string{"seo-pipeline", "pprof-1", wordPressRepublishOperation, "3"})
	if err != nil {
		t.Fatalf("republish 3: %v", err)
	}
	if command.Name != wordPressRepublishOperation || command.ExternalID != "3" {
		t.Fatalf("команда разобрана неверно: %+v", command)
	}
}

// Картинка тела живёт только в записи блога: её адрес известен той публикации, что грузила
// вложение. Перезапись обязана переносить её, иначе статья теряет иллюстрацию.
func TestBodyImageRECapturesPublishedImageBlock(t *testing.T) {
	stored := `<p>Лид статьи.</p>` + "\n" +
		`<img class="alignnone size-full wp-image-22627" src="https://dpoprof.ru/img.webp" alt="Заголовок" />` + "\n" +
		`<p>Источник изображения: <a href="https://www.pexels.com/ru-ru/" target="_blank" rel="nofollow noindex noopener">Pexels</a>.</p>` + "\n" +
		`<h2>Раздел</h2>`

	got := bodyImageRE.FindString(stored)
	if !strings.Contains(got, "wp-image-22627") {
		t.Fatalf("картинка не найдена: %q", got)
	}
	if !strings.Contains(got, "Pexels") {
		t.Fatalf("подпись источника не захвачена: %q", got)
	}
	if strings.Contains(got, "<h2>") {
		t.Fatalf("захвачено лишнее: %q", got)
	}
}

// --- связанные курсы при перезаписи ---

// newWPRepublishDeps готовит статью, уже лежащую в блоге: только такую правка и берёт.
// fields — поля, которые у записи уже есть, вместе с идентификаторами postmeta.
func newWPRepublishDeps(fieldIDs map[string]string) (wordPressPublishDeps, *fakeWPClient, *bytes.Buffer) {
	deps, repo, client, _, out := newWPPublishDeps()
	postID := int64(22615)
	input := readyWPPublicationInput()
	input.Publication = article.Publication{
		Status: article.WordPressPublished, PostID: &postID,
		URL: "https://example.test/blog/razryady/",
	}
	repo.input = input
	client.stored = wordpress.StoredPost{
		ID: postID, Title: "Прежний заголовок", ContentHTML: "<p>Прежнее тело статьи</p>",
		Link: "https://example.test/blog/razryady/", Status: wordpress.PostStatusPublish,
		Fields: map[string]string{}, FieldIDs: fieldIDs,
	}
	deps.courses = &stubCourses{related: threeCourses()}
	return deps, client, out
}

// Связь уходит вместе с текстом: перегенерированная статья обязана унести под собой те
// карточки, что напечатаны в её result.md.
func TestRepublishSendsRelatedCourses(t *testing.T) {
	deps, client, out := newWPRepublishDeps(map[string]string{})

	if err := runWordPressRepublish(context.Background(), deps, client, "16"); err != nil {
		t.Fatalf("перезапись: %v", err)
	}
	if len(client.edited) != 1 {
		t.Fatalf("правок ушло %d", len(client.edited))
	}
	fields := client.edited[0].Fields
	var courses *wordpress.FieldUpdate
	for index := range fields {
		if fields[index].Key == blogFieldRelatedCourses {
			courses = &fields[index]
		}
	}
	if courses == nil {
		t.Fatalf("правка не отправила связь курсов: %+v", fields)
	}
	if len(courses.IDs) != 3 {
		t.Fatalf("в связь ушло %v", courses.IDs)
	}
	// Курсы печатаются поимённо: человек смотрит в вывод затем, чтобы убедиться, что под
	// статьёй встали именно они.
	if !strings.Contains(out.String(), "курс:") {
		t.Fatalf("назначенные курсы не показаны: %s", out.String())
	}
}

// У записи, созданной раньше самого блока курсов, поля нет вовсе — и завести его правкой
// единственный способ. Идентификатора postmeta у такого поля быть не может.
func TestRepublishCreatesRelatedCoursesWhenPostHasNone(t *testing.T) {
	deps, client, _ := newWPRepublishDeps(map[string]string{})

	if err := runWordPressRepublish(context.Background(), deps, client, "16"); err != nil {
		t.Fatalf("перезапись: %v", err)
	}
	field := client.edited[0].Fields[0]
	if field.ID != "" {
		t.Fatalf("у нового поля взялся идентификатор %q", field.ID)
	}
	if !field.Create {
		t.Fatal("правка не разрешила завести поле — WordPress отказал бы ей до запроса")
	}
}

// У записи, где поле уже есть, берётся его идентификатор: без него wp.editPost завёл бы
// рядом второе поле с тем же ключом.
func TestRepublishUpdatesExistingRelatedCoursesByFieldID(t *testing.T) {
	deps, client, _ := newWPRepublishDeps(map[string]string{blogFieldRelatedCourses: "88123"})

	if err := runWordPressRepublish(context.Background(), deps, client, "16"); err != nil {
		t.Fatalf("перезапись: %v", err)
	}
	field := client.edited[0].Fields[0]
	if field.ID != "88123" {
		t.Fatalf("правка ушла с идентификатором %q", field.ID)
	}
	if field.Create {
		t.Fatal("существующее поле помечено как заводимое впервые")
	}
}

// Молча отброшенное площадкой поле — отказ команды: текст лёг, а связь нет, и увидеть это
// человек иначе смог бы только глазами в админке.
func TestRepublishFailsWhenRelatedCoursesDropped(t *testing.T) {
	deps, client, _ := newWPRepublishDeps(map[string]string{})
	client.dropField = blogFieldRelatedCourses

	err := runWordPressRepublish(context.Background(), deps, client, "16")
	if err == nil {
		t.Fatal("отброшенное поле принято за успех")
	}
	if !strings.Contains(err.Error(), blogFieldRelatedCourses) {
		t.Fatalf("ошибка не называет поле: %v", err)
	}
}

// Задаче без каталога связь курсов не отправляется: пустое поле означало бы «снять курсы», а
// не «оставить как есть». Краткое содержание и время чтения уходят всё равно — их пишет
// стадия info по тому самому тексту, который правка и заливает.
func TestRepublishWithoutCatalogSendsMetadataButNoCourses(t *testing.T) {
	deps, client, _ := newWPRepublishDeps(map[string]string{})
	deps.courses = nil

	if err := runWordPressRepublish(context.Background(), deps, client, "16"); err != nil {
		t.Fatalf("перезапись: %v", err)
	}
	keys := make(map[string]bool, len(client.edited[0].Fields))
	for _, field := range client.edited[0].Fields {
		keys[field.Key] = true
	}
	if keys[blogFieldRelatedCourses] {
		t.Fatalf("без каталога ушла связь курсов: %+v", client.edited[0].Fields)
	}
	if !keys[blogFieldTLDR] || !keys[blogFieldReadTime] {
		t.Fatalf("правка не отправила метаданные статьи: %+v", client.edited[0].Fields)
	}
}

// Отказ площадки на самой правке ничего не сверяет и не пересобирает: запись осталась
// прежней, и об этом надо сказать ошибкой.
func TestRepublishReturnsWriteError(t *testing.T) {
	deps, client, _ := newWPRepublishDeps(map[string]string{})
	client.editErr = errors.New("площадка недоступна")

	if err := runWordPressRepublish(context.Background(), deps, client, "16"); err == nil {
		t.Fatal("отказ площадки принят за успех")
	}
}
