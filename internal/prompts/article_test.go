package prompts

import (
	"strings"
	"testing"
)

func TestBuildArticlePromptSubstitutesValuesWithoutChangingOrder(t *testing.T) {
	data := ArticlePromptData{
		Title:     "Как стать логопедом",
		Keywords:  "первый запрос\t200\nвторой запрос\t100",
		LSIWords:  "образование\nпрофессия\nпрактика",
		Structure: "H1 - Заголовок\nH2 - Первый раздел\nH3 - Подраздел\nH2 - Второй раздел",
	}

	got, err := BuildArticlePrompt(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{data.Title, data.Keywords, data.LSIWords, data.Structure} {
		if !strings.Contains(got, value) {
			t.Fatalf("готовый промпт не содержит %q", value)
		}
	}
	if strings.Index(got, "H2 - Первый раздел") > strings.Index(got, "H2 - Второй раздел") {
		t.Fatal("порядок структуры изменился")
	}
	for _, placeholder := range []string{"{{.Title}}", "{{.Keywords}}", "{{.LSIWords}}", "{{.Structure}}"} {
		if strings.Contains(got, placeholder) {
			t.Fatalf("в готовом промпте остался placeholder %s", placeholder)
		}
	}
}

func TestBuildArticlePromptAllowsEmptyKeywordsAndLSIWords(t *testing.T) {
	got, err := BuildArticlePrompt(ArticlePromptData{Title: "Название", Structure: "H1 - Структура"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Название") || !strings.Contains(got, "H1 - Структура") {
		t.Fatal("обязательные значения не подставлены")
	}
}

func TestBuildArticlePromptValidatesRequiredFields(t *testing.T) {
	tests := []struct {
		name string
		data ArticlePromptData
		want string
	}{
		{name: "empty title", data: ArticlePromptData{Structure: "H1 - Структура"}, want: "article title is empty"},
		{name: "blank title", data: ArticlePromptData{Title: " \t\n", Structure: "H1 - Структура"}, want: "article title is empty"},
		{name: "empty structure", data: ArticlePromptData{Title: "Название"}, want: "article structure is empty"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := BuildArticlePrompt(test.data)
			if err == nil || err.Error() != test.want {
				t.Fatalf("BuildArticlePrompt() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestBuildArticlePromptCallsAreIndependent(t *testing.T) {
	first, err := BuildArticlePrompt(ArticlePromptData{Title: "Первая статья", Structure: "H1 - Первая структура"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildArticlePrompt(ArticlePromptData{Title: "Вторая статья", Structure: "H1 - Вторая структура"})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("промпты разных статей совпали")
	}
	if strings.Contains(first, "Вторая статья") || strings.Contains(first, "Вторая структура") {
		t.Fatal("данные второй статьи попали в первый промпт")
	}
	if strings.Contains(second, "Первая статья") || strings.Contains(second, "Первая структура") {
		t.Fatal("данные первой статьи попали во второй промпт")
	}
}
