package output

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// newClearArticle раскладывает каталог статьи так, как его оставляет полный прогон: research,
// логи, промпты, сгенерированное, DEMO и итоговые файлы.
func newClearArticle(t *testing.T) (*Writer, string) {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, "23-kak-vybrat-frezu")

	directories := []string{
		filepath.Join(directory, "prepare"),
		filepath.Join(directory, "logs"),
		filepath.Join(directory, "prompts"),
		filepath.Join(directory, "generated"),
		filepath.Join(directory, "DEMO", "generated"),
	}
	for _, path := range directories {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("создать каталог %s: %v", path, err)
		}
	}
	files := []string{
		filepath.Join(directory, "prepare", "keysso.json"),
		filepath.Join(directory, "logs", "prepare.log"),
		filepath.Join(directory, "logs", "generate.log"),
		filepath.Join(directory, "prompts", "article_prompt.txt"),
		filepath.Join(directory, "generated", "article.txt"),
		filepath.Join(directory, "DEMO", "result.md"),
		filepath.Join(directory, "DEMO", "generated", "article.txt"),
		filepath.Join(directory, "article.html"),
		filepath.Join(directory, "result.md"),
	}
	for _, path := range files {
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("создать файл %s: %v", path, err)
		}
	}
	return NewWriter(root), directory
}

// От очищенной статьи на диске не остаётся ничего, включая логи и сам каталог: у ни разу не
// запускавшейся статьи каталога нет вовсе, а уцелевший лог со status=completed врал бы про
// её состояние.
func TestClearArticleArtifactsRemovesEverythingIncludingLogs(t *testing.T) {
	writer, directory := newClearArticle(t)

	removed, err := writer.ClearArticleArtifacts("23")
	if err != nil {
		t.Fatalf("ClearArticleArtifacts вернул ошибку: %v", err)
	}

	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		entries, readErr := os.ReadDir(directory)
		if readErr != nil {
			t.Fatalf("каталог статьи должен быть удалён, получено: %v", err)
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("каталог статьи должен быть удалён, в нём осталось: %s", strings.Join(names, ", "))
	}

	for _, expected := range []string{
		"23-kak-vybrat-frezu/DEMO",
		"23-kak-vybrat-frezu/article.html",
		"23-kak-vybrat-frezu/generated",
		"23-kak-vybrat-frezu/logs",
		"23-kak-vybrat-frezu/prepare",
		"23-kak-vybrat-frezu/prompts",
		"23-kak-vybrat-frezu/result.md",
	} {
		if !slices.Contains(removed, expected) {
			t.Fatalf("в списке удалённого нет %q: %v", expected, removed)
		}
	}
}

// Статью могли импортировать и ни разу не запускать. Это и есть то состояние, к которому
// ведёт очистка, поэтому отсутствие каталога — не ошибка.
func TestClearArticleArtifactsWithoutDirectoryIsNotAnError(t *testing.T) {
	writer := NewWriter(t.TempDir())

	removed, err := writer.ClearArticleArtifacts("23")
	if err != nil {
		t.Fatalf("отсутствие каталога не должно быть ошибкой: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("удалять нечего, получено %v", removed)
	}
}

func TestClearArticleArtifactsIsIdempotent(t *testing.T) {
	writer, _ := newClearArticle(t)

	if _, err := writer.ClearArticleArtifacts("23"); err != nil {
		t.Fatalf("первая очистка вернула ошибку: %v", err)
	}
	removed, err := writer.ClearArticleArtifacts("23")
	if err != nil {
		t.Fatalf("повторная очистка вернула ошибку: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("повторная очистка не должна ничего удалять, получено %v", removed)
	}
}

// Два каталога на один external_id — обычно из-за смены slug. Гадать нельзя: стереть чужой
// каталог здесь дороже, чем отказаться.
func TestClearArticleArtifactsRefusesAmbiguousDirectory(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"23-staryj-slug", "23-novyj-slug"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatalf("создать каталог %s: %v", name, err)
		}
	}
	writer := NewWriter(root)

	if _, err := writer.ClearArticleArtifacts("23"); err == nil {
		t.Fatal("при двух каталогах на один external_id очистка обязана отказаться")
	}

	for _, name := range []string{"23-staryj-slug", "23-novyj-slug"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("каталог %s не должен быть тронут: %v", name, err)
		}
	}
}

// Префикс external_id не должен цеплять соседей: 2 и 23 — разные статьи.
func TestClearArticleArtifactsDoesNotMatchOtherArticleByPrefix(t *testing.T) {
	writer, _ := newClearArticle(t)

	root := filepath.Dir(filepath.Join(writer.root, "23-kak-vybrat-frezu"))
	other := filepath.Join(root, "2-drugaya-statya")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatalf("создать каталог соседа: %v", err)
	}
	if err := os.WriteFile(filepath.Join(other, "result.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("создать файл соседа: %v", err)
	}

	if _, err := writer.ClearArticleArtifacts("2"); err != nil {
		t.Fatalf("ClearArticleArtifacts вернул ошибку: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "23-kak-vybrat-frezu", "result.md")); err != nil {
		t.Fatalf("статья 23 не должна пострадать от очистки статьи 2: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "result.md")); !os.IsNotExist(err) {
		t.Fatalf("файл статьи 2 должен быть удалён, получено: %v", err)
	}
}

func TestCountArticleArtifactsCountsEverything(t *testing.T) {
	writer, _ := newClearArticle(t)

	directory, count, err := writer.CountArticleArtifacts("23")
	if err != nil {
		t.Fatalf("CountArticleArtifacts вернул ошибку: %v", err)
	}
	if directory != "23-kak-vybrat-frezu" {
		t.Fatalf("ожидался каталог 23-kak-vybrat-frezu, получен %q", directory)
	}
	// prepare, logs, prompts, generated, DEMO, article.html, result.md.
	if count != 7 {
		t.Fatalf("ожидалось 7 удаляемых элементов, получено %d", count)
	}
}

func TestCountArticleArtifactsWithoutDirectory(t *testing.T) {
	writer := NewWriter(t.TempDir())

	directory, count, err := writer.CountArticleArtifacts("23")
	if err != nil {
		t.Fatalf("отсутствие каталога не должно быть ошибкой: %v", err)
	}
	if directory != "" || count != 0 {
		t.Fatalf("ожидались пустой каталог и ноль элементов, получено %q и %d", directory, count)
	}
}
