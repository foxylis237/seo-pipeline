package output

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriterSaveArticle(t *testing.T) {
	root := t.TempDir()
	writer := NewWriter(root)

	paths, err := writer.SaveStructure("42", "тестовая-статья", "Промпт структуры", "Итоговая структура")
	if err != nil {
		t.Fatal(err)
	}
	paths, err = writer.SaveArticle("42", "тестовая-статья", "Промпт на русском", "Текст статьи")
	if err != nil {
		t.Fatal(err)
	}
	if paths.StructurePromptPath != "42-тестовая-статья/prompts/structure_prompt.txt" {
		t.Fatalf("StructurePromptPath = %q", paths.StructurePromptPath)
	}
	if paths.StructurePath != "42-тестовая-статья/generated/structure.txt" {
		t.Fatalf("StructurePath = %q", paths.StructurePath)
	}
	if paths.ArticlePromptPath != "42-тестовая-статья/prompts/article_prompt.txt" {
		t.Fatalf("ArticlePromptPath = %q", paths.ArticlePromptPath)
	}
	if paths.ArticlePath != "42-тестовая-статья/generated/article.txt" {
		t.Fatalf("ArticlePath = %q", paths.ArticlePath)
	}
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.StructurePromptPath)), "Промпт структуры")
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.StructurePath)), "Итоговая структура")
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.ArticlePromptPath)), "Промпт на русском")
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.ArticlePath)), "Текст статьи")
	readArticle, err := writer.Read(paths.ArticlePath)
	if err != nil || readArticle != "Текст статьи" {
		t.Fatalf("Read() = %q, %v", readArticle, err)
	}
	if _, err := writer.Read("../secret"); err == nil {
		t.Fatal("Read() accepted a path outside output root")
	}

	if _, err := writer.SaveArticle("42", "тестовая-статья", "другой промпт", "другой текст"); err == nil {
		t.Fatal("SaveArticle() overwrote existing files")
	}
	assertFileText(t, filepath.Join(root, filepath.FromSlash(paths.ArticlePath)), "Текст статьи")
}

func TestWriterRejectsUnsafePathParts(t *testing.T) {
	writer := NewWriter(t.TempDir())
	for _, testCase := range []struct{ externalID, slug string }{
		{"../42", "slug"},
		{"42", "../slug"},
		{"42", `folder\slug`},
		{"", "slug"},
	} {
		if _, err := writer.SaveArticle(testCase.externalID, testCase.slug, "prompt", "article"); err == nil {
			t.Fatalf("SaveArticle(%q, %q) error = nil", testCase.externalID, testCase.slug)
		}
	}
}

func TestWriterResetArticleRemovesWholeArticleDirectory(t *testing.T) {
	root := t.TempDir()
	writer := NewWriter(root)
	articleDirectory := filepath.Join(root, "42-slug")
	if err := os.MkdirAll(filepath.Join(articleDirectory, "generated"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"article.html", "article_info.xlsx"} {
		if err := os.WriteFile(filepath.Join(articleDirectory, name), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(articleDirectory, "generated", "old.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writer.ResetArticle("42", "slug"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(articleDirectory); !os.IsNotExist(err) {
		t.Fatalf("article directory still exists after reset: %v", err)
	}
}

func assertFileText(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("file %s = %q, want %q", path, got, want)
	}
}
