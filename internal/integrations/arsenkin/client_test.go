package arsenkin

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeResults(t *testing.T) {
	rows := []rawKeywordFrequency{
		{Query: " второй запрос ", Frequency: "1 200"},
		{Query: "первый запрос", Frequency: "2 500"},
		{Query: "второй запрос", Frequency: "900"},
		{Query: "", Frequency: "9999"},
	}
	want := []KeywordFrequency{
		{Query: "первый запрос", Frequency: 2500},
		{Query: "второй запрос", Frequency: 1200},
	}

	got, err := normalizeResults(rows)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeResults() = %#v, want %#v", got, want)
	}
}

func TestUniqueNonEmptyPreservesOrderAndRemovesExactDuplicates(t *testing.T) {
	got := uniqueNonEmpty([]string{" образование ", "", "Профессия", "образование", "ОБРАЗОВАНИЕ"})
	want := []string{"образование", "Профессия", "ОБРАЗОВАНИЕ"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uniqueNonEmpty() = %#v, want %#v", got, want)
	}
}

func TestNormalizeCompetitorStructure(t *testing.T) {
	input := "  H1 Как стать логопедом\r\n\r\n| H2 Обучение\r\r+++ H3 Где учиться\n\n\n+++   |H3 Зарплата  "
	want := "H1 Как стать логопедом\n\nH2 Обучение\n\nH3 Где учиться\n\nH3 Зарплата"
	got := normalizeCompetitorStructure(input)
	if got != want {
		t.Fatalf("normalizeCompetitorStructure() = %q, want %q", got, want)
	}
}

func TestNormalizeResultsLimitsToFifty(t *testing.T) {
	rows := make([]rawKeywordFrequency, 55)
	for index := range rows {
		rows[index] = rawKeywordFrequency{Query: fmt.Sprintf("query-%02d", index), Frequency: fmt.Sprint(index)}
	}
	got, err := normalizeResults(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 50 {
		t.Fatalf("len(normalizeResults()) = %d, want 50", len(got))
	}
	if got[0].Frequency != 54 || got[49].Frequency != 5 {
		t.Fatalf("unexpected sorted range: first=%d last=%d", got[0].Frequency, got[49].Frequency)
	}
}

func TestNormalizeResultsRejectsInvalidFrequency(t *testing.T) {
	_, err := normalizeResults([]rawKeywordFrequency{{Query: "query", Frequency: "нет данных"}})
	if err == nil {
		t.Fatal("expected invalid frequency error")
	}
}

func TestNormalizeResultsRejectsFrequencyWithUnexpectedText(t *testing.T) {
	_, err := normalizeResults([]rawKeywordFrequency{{Query: "query", Frequency: "100 показов"}})
	if err == nil {
		t.Fatal("expected frequency with text error")
	}
}

func TestNormalizeResultsKeepsStableOrderForEqualFrequency(t *testing.T) {
	rows := []rawKeywordFrequency{
		{Query: "второй по алфавиту", Frequency: "100"},
		{Query: "первый по алфавиту", Frequency: "100"},
	}
	got, err := normalizeResults(rows)
	if err != nil {
		t.Fatal(err)
	}
	want := []KeywordFrequency{
		{Query: "второй по алфавиту", Frequency: 100},
		{Query: "первый по алфавиту", Frequency: 100},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeResults() = %#v, want %#v", got, want)
	}
}

func TestParseWordstatRowsFindsHeadersAndNormalizesResults(t *testing.T) {
	rows := [][]string{
		{"Отчёт Wordstat"},
		{"Фраза", "Весь мир(WS)"},
		{"первый запрос", "2 500"},
		{"второй запрос", "1200"},
	}
	want := []KeywordFrequency{
		{Query: "первый запрос", Frequency: 2500},
		{Query: "второй запрос", Frequency: 1200},
	}
	got, err := parseWordstatRows(rows)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseWordstatRows() = %#v, want %#v", got, want)
	}
}

func TestParseWordstatRowsRejectsUnknownHeaders(t *testing.T) {
	_, err := parseWordstatRows([][]string{{"foo", "bar"}, {"query", "1"}})
	if err == nil {
		t.Fatal("expected unknown Wordstat headers error")
	}
}

func TestNormalizeInputQueriesUsesOneUnnumberedPhrasePerLine(t *testing.T) {
	got := normalizeInputQueries([]string{" первый запрос ", "", "второй запрос", "первый запрос"})
	want := []string{"первый запрос", "второй запрос"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeInputQueries() = %#v, want %#v", got, want)
	}
}

func TestAcceptCopywritersResultRejectsPreviousTask(t *testing.T) {
	tests := []struct {
		name       string
		previous   copywritersTask
		current    copywritersTask
		wantErr    bool
		wantSubstr string
	}{
		{
			name:     "новая задача принимается",
			previous: copywritersTask{ID: "1001", Theme: "старые слова", Structure: "старая структура"},
			current:  copywritersTask{ID: "1002", Theme: "новые слова", Structure: "новая структура"},
		},
		{
			name:     "первая задача в чистом профиле принимается",
			previous: copywritersTask{},
			current:  copywritersTask{ID: "1002", Theme: "новые слова", Structure: "новая структура"},
		},
		{
			name:       "прежний task_id не принимается",
			previous:   copywritersTask{ID: "1001", Theme: "старые слова", Structure: "старая структура"},
			current:    copywritersTask{ID: "1001", Theme: "старые слова", Structure: "старая структура"},
			wantErr:    true,
			wantSubstr: "не изменился",
		},
		{
			name:       "результат без task_id не принимается",
			previous:   copywritersTask{ID: "1001"},
			current:    copywritersTask{Theme: "слова", Structure: "структура"},
			wantErr:    true,
			wantSubstr: "не выдал task_id",
		},
		{
			name:       "неизменённые данные без task_id не принимаются",
			previous:   copywritersTask{Theme: "слова", Structure: "структура"},
			current:    copywritersTask{ID: "1002", Theme: "слова", Structure: "структура"},
			wantErr:    true,
			wantSubstr: "неизменённый результат",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := acceptCopywritersResult(test.previous, test.current)
			if test.wantErr {
				if err == nil {
					t.Fatal("результат предыдущей задачи принят как новый")
				}
				if !strings.Contains(err.Error(), test.wantSubstr) {
					t.Fatalf("error = %v, want substring %q", err, test.wantSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("новая задача отклонена: %v", err)
			}
		})
	}
}

func TestSelectNewWordstatTaskAcceptsOnlyTheTaskOfThisRun(t *testing.T) {
	tests := []struct {
		name       string
		known      []string
		completed  []string
		want       string
		wantErr    bool
		wantSubstr string
	}{
		{
			name:      "новая задача среди прежних",
			known:     []string{"9001", "9002"},
			completed: []string{"9001", "9002", "9003"},
			want:      "9003",
		},
		{
			name:      "первая задача в чистом профиле",
			known:     nil,
			completed: []string{"9003"},
			want:      "9003",
		},
		{
			name:       "на странице только прежние задачи",
			known:      []string{"9001", "9002"},
			completed:  []string{"9001", "9002"},
			wantErr:    true,
			wantSubstr: "не создал новую задачу",
		},
		{
			name:       "пустой список завершённых задач",
			known:      []string{"9001"},
			completed:  nil,
			wantErr:    true,
			wantSubstr: "не создал новую задачу",
		},
		{
			name:       "две новые задачи неразличимы",
			known:      []string{"9001"},
			completed:  []string{"9001", "9003", "9004"},
			wantErr:    true,
			wantSubstr: "несколько новых задач",
		},
		{
			name:      "повтор одной и той же новой задачи",
			known:     []string{"9001"},
			completed: []string{"9003", "9003"},
			want:      "9003",
		},
		{
			name:      "пробелы вокруг идентификаторов не создают новую задачу",
			known:     []string{" 9001 "},
			completed: []string{"9001", "  ", "9003"},
			want:      "9003",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectNewWordstatTask(test.known, test.completed)
			if test.wantErr {
				if err == nil {
					t.Fatalf("чужая задача принята как новая: %q", got)
				}
				if !strings.Contains(err.Error(), test.wantSubstr) {
					t.Fatalf("error = %v, want substring %q", err, test.wantSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("новая задача отклонена: %v", err)
			}
			if got != test.want {
				t.Fatalf("task_id = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAcceptWordstatResultRejectsForeignTable(t *testing.T) {
	submitted := []string{"как стать логопедом", "логопед обучение", "профессия логопед"}

	if err := acceptWordstatResult(submitted, []KeywordFrequency{
		{Query: "Как стать логопедом", Frequency: 5400},
		{Query: "логопед  обучение", Frequency: 3100},
	}); err != nil {
		t.Fatalf("свой результат отклонён: %v", err)
	}

	err := acceptWordstatResult(submitted, []KeywordFrequency{
		{Query: "такелажник работа", Frequency: 900},
		{Query: "кто такие такелажники", Frequency: 400},
		{Query: "такелажником", Frequency: 200},
	})
	if err == nil {
		t.Fatal("таблица другой задачи принята")
	}
	if !strings.Contains(err.Error(), "результат другой задачи") || !strings.Contains(err.Error(), "такелажник работа") {
		t.Fatalf("error = %v", err)
	}

	if err := acceptWordstatResult(submitted, nil); err != nil {
		t.Fatalf("пустая таблица должна разбираться выше по стеку: %v", err)
	}
}
