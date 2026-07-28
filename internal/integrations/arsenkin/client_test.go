package arsenkin

import (
	"fmt"
	"reflect"
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
