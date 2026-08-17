package diagnostics

import (
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

func TestCheckKeywordRelevanceAcceptsInflectedQueries(t *testing.T) {
	result := CheckKeywordRelevance("бариста", "Как стать бариста с нуля", []string{
		"работа бариста",
		"обучение баристе",
		"курсы бариста москва",
	})
	if !result.KeywordBased {
		t.Fatal("keyword must be used as the reference")
	}
	if result.Matched != result.Checked {
		t.Fatalf("matched = %d of %d, unmatched: %v", result.Matched, result.Checked, result.Unmatched)
	}
}

func TestCheckKeywordRelevanceToleratesDeclension(t *testing.T) {
	tests := []struct {
		keyword string
		query   string
		want    bool
	}{
		{keyword: "курсы", query: "курсов бариста", want: true},
		{keyword: "сварщик", query: "обучение сварщиков", want: true},
		{keyword: "врач", query: "зарплата врача", want: true},
		{keyword: "окна", query: "пластиковые окна цена", want: true},
		{keyword: "окна", query: "замена окон", want: false},
		{keyword: "повар", query: "поход в горы", want: false},
		{keyword: "бариста", query: "монтаж стеклопакета", want: false},
	}
	for _, test := range tests {
		result := CheckKeywordRelevance(test.keyword, "", []string{test.query})
		if got := result.Matched == 1; got != test.want {
			t.Errorf("keyword %q vs query %q: matched = %t, want %t (stems %v)",
				test.keyword, test.query, got, test.want, result.Reference)
		}
	}
}

func TestCheckKeywordRelevanceRejectsQueriesOfAnotherArticle(t *testing.T) {
	result := CheckKeywordRelevance("бариста", "Как стать бариста с нуля", []string{
		"монтаж пластиковых окон",
		"замена стеклопакета цена",
	})
	if result.Matched != 0 {
		t.Fatalf("matched = %d, want 0", result.Matched)
	}
	if len(result.Unmatched) != 2 {
		t.Fatalf("unmatched sample = %v", result.Unmatched)
	}
	if result.Ratio() != 0 {
		t.Fatalf("ratio = %f, want 0", result.Ratio())
	}
}

func TestCheckKeywordRelevanceSkipsWithoutUsableReference(t *testing.T) {
	result := CheckKeywordRelevance("", "SEO", []string{"любой запрос"})
	if !result.Skipped || result.KeywordBased {
		t.Fatalf("result = %+v, want skipped without keyword reference", result)
	}
}

func TestCheckKeywordRelevanceIsNotKeywordBasedWhenOnlyTitleIsUsable(t *testing.T) {
	result := CheckKeywordRelevance("", "Профессия бариста", []string{"работа бариста"})
	if result.KeywordBased {
		t.Fatal("empty keyword must not enable blocking on zero matches")
	}
	if result.Matched != 1 {
		t.Fatalf("matched = %d, want 1", result.Matched)
	}
}

func TestCheckQueryMembershipMatchesSubmittedPhrases(t *testing.T) {
	submitted := []string{"работа бариста", "курсы  Бариста"}
	result := CheckQueryMembership(submitted, []string{"Работа бариста", "курсы бариста"})
	if result.Matched != 2 || result.Ratio() != 1 {
		t.Fatalf("result = %+v, want every returned phrase matched", result)
	}
}

func TestCheckQueryMembershipDetectsForeignResult(t *testing.T) {
	result := CheckQueryMembership([]string{"работа бариста"}, []string{"монтаж окон", "замена стеклопакета"})
	if result.Matched != 0 {
		t.Fatalf("matched = %d, want 0", result.Matched)
	}
	if result.Returned != 2 || len(result.Unmatched) != 2 {
		t.Fatalf("result = %+v", result)
	}
}

func TestTraceMismatchDetectsChangedIdentity(t *testing.T) {
	expected := article.Trace{ArticleID: 7, ExternalID: "37", Title: "Бариста"}
	if err := TraceMismatch(expected, expected); err != nil {
		t.Fatalf("identical traces reported as mismatched: %v", err)
	}
	changed := article.Trace{ArticleID: 8, ExternalID: "38", Title: "Окна"}
	if err := TraceMismatch(expected, changed); err == nil {
		t.Fatal("changed identity was not reported")
	}
}

func TestFingerprintIsStableAndDistinguishing(t *testing.T) {
	if Fingerprint("структура  ") != Fingerprint("структура") {
		t.Fatal("fingerprint must ignore surrounding whitespace")
	}
	if Fingerprint("структура A") == Fingerprint("структура B") {
		t.Fatal("different payloads must have different fingerprints")
	}
}
