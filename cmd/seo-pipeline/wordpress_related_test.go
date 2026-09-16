package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/catalog"
)

// stubCourses — подбор связанных курсов, заданный тестом.
type stubCourses struct {
	related []catalog.Related
	err     error
	asked   []catalog.Request
}

func (s *stubCourses) SelectRelated(_ context.Context, request catalog.Request) ([]catalog.Related, error) {
	s.asked = append(s.asked, request)
	return s.related, s.err
}

func threeCourses() []catalog.Related {
	return []catalog.Related{
		{Program: catalog.Program{PostID: 507, Name: "Газосварщик", Category: "Обучение"}},
		{Program: catalog.Program{PostID: 16122, Name: "Сварщик", Category: "Обучение"}, Neighbour: true},
		{Program: catalog.Program{PostID: 15941, Name: "Слесарь", Category: "Обучение"}, Neighbour: true},
	}
}

// Связь уходит списком идентификаторов в том же wp.newPost, что и остальные поля: отдельного
// запроса на неё нет и быть не может — редактировать созданную запись публикации нельзя.
func TestPublishSendsRelatedCourses(t *testing.T) {
	deps, _, client, _, _ := newWPPublishDeps()
	courses := &stubCourses{related: threeCourses()}
	deps.courses = courses

	if err := runWordPressPublish(context.Background(), deps, "16"); err != nil {
		t.Fatalf("публикация: %v", err)
	}
	var field *struct {
		IDs []int64
	}
	for _, item := range client.created[0].Fields {
		if item.Key == blogFieldRelatedCourses {
			field = &struct{ IDs []int64 }{IDs: item.IDs}
		}
	}
	if field == nil {
		t.Fatalf("поля %s в нагрузке нет", blogFieldRelatedCourses)
	}
	if len(field.IDs) != 3 || field.IDs[0] != 507 || field.IDs[2] != 15941 {
		t.Fatalf("в связь ушло %v", field.IDs)
	}
	// Подбору отдаются и профессии, и перелинковка: без второй блок повторил бы ссылки из
	// текста статьи.
	if len(courses.asked) == 0 {
		t.Fatal("подбор не спрошен")
	}
	asked := courses.asked[0]
	if strings.TrimSpace(asked.Professions) == "" || strings.TrimSpace(asked.Topic) == "" {
		t.Fatalf("подбор спрошен без данных статьи: %+v", asked)
	}
}

// Неполный блок — отказ, и отказ до единого запроса в блог: тема рисует три карточки, и ряд
// из двух выглядит ошибкой вёрстки.
func TestPublishStopsWhenFewerThanThreeCourses(t *testing.T) {
	deps, _, client, _, _ := newWPPublishDeps()
	deps.courses = &stubCourses{related: threeCourses()[:2]}

	err := runWordPressPublish(context.Background(), deps, "16")
	if err == nil {
		t.Fatal("ожидался отказ публикации")
	}
	if !strings.Contains(err.Error(), "связанных курсов") {
		t.Fatalf("ошибка не о курсах: %v", err)
	}
	if len(client.created) != 0 {
		t.Fatalf("в блоге создано %d записей — отказ обязан быть до записи", len(client.created))
	}
}

// Несобранный каталог тоже останавливает публикацию, и по той же причине: молча выложить
// статью без блока значило бы отдать читателю пустой ряд карточек.
func TestPublishStopsWhenCatalogUnavailable(t *testing.T) {
	deps, _, client, _, _ := newWPPublishDeps()
	deps.courses = &stubCourses{err: errors.New("каталог услуг пуст — соберите его командой catalog-sync")}

	err := runWordPressPublish(context.Background(), deps, "16")
	if err == nil || !strings.Contains(err.Error(), "catalog-sync") {
		t.Fatalf("ошибка = %v", err)
	}
	if len(client.created) != 0 {
		t.Fatalf("в блоге создано %d записей", len(client.created))
	}
}

// Задача без блока под статьёй публикуется как раньше: поля в нагрузке нет вовсе, и тема
// подбирает курсы сама по совпадению рубрики и меток.
func TestPublishWithoutCatalogKeepsPayloadUnchanged(t *testing.T) {
	deps, _, client, _, _ := newWPPublishDeps()

	if err := runWordPressPublish(context.Background(), deps, "16"); err != nil {
		t.Fatalf("публикация: %v", err)
	}
	for _, item := range client.created[0].Fields {
		if item.Key == blogFieldRelatedCourses {
			t.Fatalf("поле %s отправлено задачей без каталога", blogFieldRelatedCourses)
		}
	}
}

// Сухой прогон показывает курсы человеку: перед необратимой командой видно не только
// идентификаторы, но и названия с признаком смежности.
func TestPublishPlanPrintsRelatedCourses(t *testing.T) {
	deps, _, _, _, out := newWPPublishDeps()
	deps.courses = &stubCourses{related: threeCourses()}

	if err := runWordPressPublishPlan(context.Background(), deps, "16"); err != nil {
		t.Fatalf("сухой прогон: %v", err)
	}
	report := out.String()
	for _, want := range []string{"Связанные курсы (3, смежных 2)", "Газосварщик", "смежная профессия",
		"= 507, 16122, 15941"} {
		if !strings.Contains(report, want) {
			t.Fatalf("в отчёте нет %q:\n%s", want, report)
		}
	}
}
