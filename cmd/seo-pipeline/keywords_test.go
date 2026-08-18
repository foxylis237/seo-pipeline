package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

// fakeKeywordsRepository запоминает вставку вместо PostgreSQL: команда проверяется целиком,
// от разбора колонки до отчёта, а база тут ничего не решает.
type fakeKeywordsRepository struct {
	selected  article.Article
	lookupErr error
	saveErr   error
	savedID   int64
	saved     []string
	calls     int
}

func (r *fakeKeywordsRepository) GetArticleByExternalID(context.Context, string) (article.Article, error) {
	if r.lookupErr != nil {
		return article.Article{}, r.lookupErr
	}
	return r.selected, nil
}

func (r *fakeKeywordsRepository) SaveManualKeywords(_ context.Context, articleID int64, keywords []string) error {
	r.calls++
	if r.saveErr != nil {
		return r.saveErr
	}
	r.savedID = articleID
	r.saved = append([]string(nil), keywords...)
	return nil
}

func testKeywordsArticle() article.Article {
	return article.Article{ID: 7, ExternalID: "21", Title: "Обучение на маляра", Status: "pending"}
}

func runTestKeywords(t *testing.T, repository *fakeKeywordsRepository, input string, interactive bool) (string, error) {
	t.Helper()
	out := &strings.Builder{}
	err := runKeywords(context.Background(), repository, keywordsOptions{
		TaskCommand: "pprof-1",
		Interactive: interactive,
		In:          strings.NewReader(input),
		Out:         out,
	}, testPrepareLogger(), "21")
	return out.String(), err
}

// TestKeywordsSavesPastedColumn — основной сценарий: вставленная колонка уходит в статью как
// есть, по одному запросу в строке, порядок сохраняется.
func TestKeywordsSavesPastedColumn(t *testing.T) {
	repository := &fakeKeywordsRepository{selected: testKeywordsArticle()}

	report, err := runTestKeywords(t, repository, "обучение на маляра\nкурсы маляра\nмаляр обучение\n", false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"обучение на маляра", "курсы маляра", "маляр обучение"}
	if repository.savedID != 7 || !reflect.DeepEqual(repository.saved, want) {
		t.Fatalf("сохранено article_id=%d %v, want 7 %v", repository.savedID, repository.saved, want)
	}
	if !strings.Contains(report, "Сохранено запросов: 3") {
		t.Fatalf("отчёт не называет число запросов:\n%s", report)
	}
	// Подсказка следующего шага обязана называть задачу, из которой команду запустили.
	if !strings.Contains(report, "make pprof-1 run 21") {
		t.Fatalf("отчёт не подсказывает следующий шаг:\n%s", report)
	}
}

// TestKeywordsReportsStateReset: перезапись безусловная, и отчёт обязан назвать её вслух —
// вставка вернула статью к сбору research из того состояния, в котором она была.
func TestKeywordsReportsStateReset(t *testing.T) {
	step := "html_generation"
	selected := testKeywordsArticle()
	selected.Status = "failed"
	selected.CurrentStep = &step
	repository := &fakeKeywordsRepository{selected: selected}

	report, err := runTestKeywords(t, repository, "курсы маляра\n", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"статус pending", "arsenkin_collection", "failed", "html_generation"} {
		if !strings.Contains(report, want) {
			t.Fatalf("отчёт не называет %q:\n%s", want, report)
		}
	}
	// Готовые файлы генерации команда не трогает: возобновление идёт по ним, а не по статусу.
	if !strings.Contains(report, "make pprof-1 regenerate 21") {
		t.Fatalf("отчёт не подсказывает, чем пересобрать текст:\n%s", report)
	}
}

// TestKeywordsNormalizesExcelColumn: Excel приносит колонку с частотностями, кавычками и
// лишними пробелами, а сюда относятся только фразы — частотности даёт один Wordstat.
func TestKeywordsNormalizesExcelColumn(t *testing.T) {
	repository := &fakeKeywordsRepository{selected: testKeywordsArticle()}
	input := "обучение на маляра\t14500\n  \"курсы  маляра\"  \t3200\n\nмаляр   обучение\n"

	if _, err := runTestKeywords(t, repository, input, false); err != nil {
		t.Fatal(err)
	}
	want := []string{"обучение на маляра", "курсы маляра", "маляр обучение"}
	if !reflect.DeepEqual(repository.saved, want) {
		t.Fatalf("сохранено %v, want %v", repository.saved, want)
	}
}

// TestKeywordsDropsRepeatedLines: точные повторы вставки отбрасываются сразу и вслух.
// Словоформенные дубли остаются на чистку Keys.so — это её работа, а не этой команды.
func TestKeywordsDropsRepeatedLines(t *testing.T) {
	repository := &fakeKeywordsRepository{selected: testKeywordsArticle()}

	report, err := runTestKeywords(t, repository, "курсы маляра\nКурсы Маляра\nкурсы маляра \nмаляр\n", false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"курсы маляра", "маляр"}
	if !reflect.DeepEqual(repository.saved, want) {
		t.Fatalf("сохранено %v, want %v", repository.saved, want)
	}
	if !strings.Contains(report, "повторов отброшено: 2") {
		t.Fatalf("отчёт молчит об отброшенных повторах:\n%s", report)
	}
}

// TestKeywordsWarnsAboutWordstatTrap: символы кроме букв, цифр и пробелов роняют задачу
// Wordstat молча. Запрос при этом сохраняется — молча отбросить чужую строку хуже.
func TestKeywordsWarnsAboutWordstatTrap(t *testing.T) {
	repository := &fakeKeywordsRepository{selected: testKeywordsArticle()}

	report, err := runTestKeywords(t, repository, "маляр-штукатур\nобучение на маляра\n", false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(repository.saved, []string{"маляр-штукатур", "обучение на маляра"}) {
		t.Fatalf("сохранено %v, want оба запроса", repository.saved)
	}
	if !strings.Contains(report, "Wordstat") || !strings.Contains(report, "маляр-штукатур") {
		t.Fatalf("отчёт не предупреждает о ловушке Wordstat:\n%s", report)
	}
}

// TestKeywordsStopsAtBlankLineInTerminal: с терминала конец вставки — пустая строка.
// Из файла та же пустая строка означала бы пустую ячейку и чтение не обрывает.
func TestKeywordsStopsAtBlankLineInTerminal(t *testing.T) {
	input := "курсы маляра\n\nэто уже не запрос\n"

	terminal := &fakeKeywordsRepository{selected: testKeywordsArticle()}
	if _, err := runTestKeywords(t, terminal, input, true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(terminal.saved, []string{"курсы маляра"}) {
		t.Fatalf("с терминала сохранено %v, want только строки до пустой", terminal.saved)
	}

	piped := &fakeKeywordsRepository{selected: testKeywordsArticle()}
	if _, err := runTestKeywords(t, piped, input, false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(piped.saved, []string{"курсы маляра", "это уже не запрос"}) {
		t.Fatalf("из файла сохранено %v, want все строки", piped.saved)
	}
}

// TestKeywordsKeepsArticleUntouchedOnEmptyInput: пустая вставка не имеет права стереть уже
// лежащие запросы — она вообще ничего не сохраняет.
func TestKeywordsKeepsArticleUntouchedOnEmptyInput(t *testing.T) {
	repository := &fakeKeywordsRepository{selected: testKeywordsArticle()}

	_, err := runTestKeywords(t, repository, "\n   \n", false)
	if err == nil {
		t.Fatal("пустая вставка принята без ошибки")
	}
	if repository.calls != 0 {
		t.Fatalf("сохранение вызвано %d раз при пустой вставке", repository.calls)
	}
}

// TestKeywordsReportsMissingArticle: несуществующий external_id — отказ до чтения колонки.
func TestKeywordsReportsMissingArticle(t *testing.T) {
	wantErr := errors.New("статья с external_id \"21\" не найдена")
	repository := &fakeKeywordsRepository{lookupErr: wantErr}

	_, err := runTestKeywords(t, repository, "курсы маляра\n", false)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if repository.calls != 0 {
		t.Fatalf("сохранение вызвано %d раз для ненайденной статьи", repository.calls)
	}
}
