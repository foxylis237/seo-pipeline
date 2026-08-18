package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/integrations/wordpress"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

// systemFailure — отказ площадки: именно такие выключают публикацию в прогоне.
func systemFailure() error {
	return fmt.Errorf("публикация статьи: %w",
		&wordpress.StatusError{StatusCode: http.StatusBadGateway, Endpoint: "/xmlrpc.php"})
}

// articleFailure — негодные данные одной статьи. Соседние статьи здесь ни при чём.
func articleFailure() error {
	return fmt.Errorf("метка статьи 16: %w", &wordpress.ErrTermNotFound{Taxonomy: "метки", Name: "Метка"})
}

// runPublisherWith собирает публикатор на заданной последовательности исходов.
func runPublisherWith(results ...error) (*runPublisher, *[]string) {
	attempted := &[]string{}
	index := 0
	publish := func(_ context.Context, externalID string) error {
		*attempted = append(*attempted, externalID)
		if index >= len(results) {
			return nil
		}
		err := results[index]
		index++
		return err
	}
	return newRunPublisher(publish, slog.New(slog.DiscardHandler)), attempted
}

func TestRunPublisherDisablesPublishAfterThreeSystemFailures(t *testing.T) {
	publisher, attempted := runPublisherWith(systemFailure(), systemFailure(), systemFailure())

	outcomes := make([]publishOutcome, 0, 5)
	for _, externalID := range []string{"1", "2", "3", "4", "5"} {
		outcomes = append(outcomes, publisher.publishAfterRun(context.Background(), externalID))
	}

	want := []publishOutcome{
		publishOutcomeFailed, publishOutcomeFailed, publishOutcomeFailed,
		publishOutcomeDisabled, publishOutcomeDisabled,
	}
	for index, outcome := range want {
		if outcomes[index] != outcome {
			t.Fatalf("статья %d: исход %q, ожидался %q", index+1, outcomes[index], outcome)
		}
	}
	// В лежащую площадку больше не стучимся: четвёртая и пятая статьи даже не пробуют.
	if len(*attempted) != 3 {
		t.Fatalf("попыток публикации %d, ожидались три: %v", len(*attempted), *attempted)
	}
	if publisher.enabled {
		t.Fatal("публикация осталась включённой после трёх отказов площадки")
	}

	out := &bytes.Buffer{}
	publisher.printSummary(out)
	report := out.String()
	for _, want := range []string{"отказов площадки 3", "выключена по ходу прогона", "publish"} {
		if !strings.Contains(report, want) {
			t.Errorf("в итоге нет %q: %s", want, report)
		}
	}
}

// Успех означает, что площадка отвечает, — значит предыдущие отказы были временными.
func TestRunPublisherResetsStreakAfterSuccess(t *testing.T) {
	publisher, attempted := runPublisherWith(systemFailure(), systemFailure(), nil, systemFailure(), systemFailure())

	for _, externalID := range []string{"1", "2", "3", "4", "5"} {
		publisher.publishAfterRun(context.Background(), externalID)
	}

	if !publisher.enabled {
		t.Fatal("публикация выключена, хотя трёх отказов подряд не было")
	}
	if publisher.failStreak != 2 {
		t.Fatalf("счётчик отказов = %d, ожидались два после успеха", publisher.failStreak)
	}
	if len(*attempted) != 5 {
		t.Fatalf("попыток публикации %d, ожидались пять: %v", len(*attempted), *attempted)
	}
	if len(publisher.published) != 1 || publisher.published[0] != "3" {
		t.Fatalf("опубликованы %v", publisher.published)
	}
}

// Нет обложки, нет метки, не заполнено поле — это про одну статью. Выключать из-за них
// публикацию остальных нельзя.
func TestRunPublisherKeepsPublishingAfterArticleFailures(t *testing.T) {
	publisher, attempted := runPublisherWith(
		articleFailure(), articleFailure(), articleFailure(), articleFailure(), articleFailure())

	for _, externalID := range []string{"1", "2", "3", "4", "5"} {
		if outcome := publisher.publishAfterRun(context.Background(), externalID); outcome != publishOutcomeSkipped {
			t.Fatalf("статья %s: исход %q, ожидался skipped", externalID, outcome)
		}
	}
	if !publisher.enabled {
		t.Fatal("публикация выключена из-за данных отдельных статей")
	}
	if publisher.failStreak != 0 {
		t.Fatalf("счётчик отказов площадки = %d, данные статьи его не трогают", publisher.failStreak)
	}
	if len(*attempted) != 5 {
		t.Fatalf("попыток публикации %d, ожидались пять", len(*attempted))
	}
}

// Публикацию не просили — обычный прогон выглядит ровно как раньше: ни отметок, ни итога.
func TestRunPublisherStaysSilentWhenPublishWasNotRequested(t *testing.T) {
	publisher := newRunPublisher(nil, slog.New(slog.DiscardHandler))

	if outcome := publisher.publishAfterRun(context.Background(), "1"); outcome != publishOutcomeDisabled {
		t.Fatalf("исход %q, ожидался disabled", outcome)
	}
	out := &bytes.Buffer{}
	publisher.printSummary(out)
	if out.String() != "" {
		t.Fatalf("прогон без публикации печатает лишнее: %q", out.String())
	}
}

// Площадка не настроена: об этом сказано один раз, а статьи всё равно генерируются и
// попадают в итог как неопубликованные.
func TestRunPublisherReportsDisabledSite(t *testing.T) {
	publisher := newRunPublisher(nil, slog.New(slog.DiscardHandler))
	publisher.disable("площадка не настроена")

	if outcome := publisher.publishAfterRun(context.Background(), "1"); outcome != publishOutcomeDisabled {
		t.Fatalf("исход %q, ожидался disabled", outcome)
	}
	out := &bytes.Buffer{}
	publisher.printSummary(out)
	if !strings.Contains(out.String(), "пропущено 1") || !strings.Contains(out.String(), "площадка не настроена") {
		t.Fatalf("итог: %q", out.String())
	}
}

// Главное правило: генерация не зависит от WordPress.
func TestRunContinuesGenerationWhenPublishFails(t *testing.T) {
	publisher, attempted := runPublisherWith(systemFailure(), systemFailure(), systemFailure(), systemFailure())
	var generated []string
	runOne := func(_ context.Context, externalID string) error {
		generated = append(generated, externalID)
		return nil
	}
	selected := []article.Article{
		{ID: 1, ExternalID: "1"}, {ID: 2, ExternalID: "2"}, {ID: 3, ExternalID: "3"},
		{ID: 4, ExternalID: "4"}, {ID: 5, ExternalID: "5"},
	}

	err := runSelectedArticles(context.Background(), selected, "run",
		publisher.wrap(runOne), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("прогон упал из-за публикации: %v", err)
	}
	// Все статьи прошли генерацию, включая те, что публиковать уже не пытались.
	if strings.Join(generated, ",") != "1,2,3,4,5" {
		t.Fatalf("сгенерированы %v", generated)
	}
	if len(*attempted) != maxPublishSystemFailures {
		t.Fatalf("попыток публикации %d, ожидалось %d", len(*attempted), maxPublishSystemFailures)
	}
}

// Ошибка генерации до публикации не доходит: публиковать недоделанную статью нечего.
func TestRunPublisherSkipsArticleThatFailedGeneration(t *testing.T) {
	publisher, attempted := runPublisherWith()
	runOne := func(context.Context, string) error { return errors.New("стадия html не удалась") }

	if err := publisher.wrap(runOne)(context.Background(), "1"); err == nil {
		t.Fatal("ошибка генерации проглочена")
	}
	if len(*attempted) != 0 {
		t.Fatalf("публиковали недоделанную статью: %v", *attempted)
	}
}
