package output

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveDiagnosticsWritesJSONIntoArticleDirectory(t *testing.T) {
	root := t.TempDir()
	writer := NewWriter(root)

	path, err := writer.SaveDiagnostics("52", "kak-stat-logopedom", PrepareSubdirectory, "keysso.json", map[string]any{
		"external_id":   "52",
		"cleaned_count": 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "52-kak-stat-logopedom/prepare/keysso.json"; path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("сохранён невалидный JSON: %v", err)
	}
	if decoded["external_id"] != "52" {
		t.Fatalf("содержимое = %v", decoded)
	}
}

func TestSaveDiagnosticsTextWritesRawResponseNextToLogs(t *testing.T) {
	root := t.TempDir()
	writer := NewWriter(root)
	if err := os.MkdirAll(filepath.Join(root, "52-kak-stat-logopedom"), 0o755); err != nil {
		t.Fatal(err)
	}
	response := "```html\n<h1>Тема</h1>"

	// Пустой slug: на упавшей стадии каталог статьи уже есть, а slug знать необязательно.
	path, err := writer.SaveDiagnosticsText("52", "", LogsSubdirectory, "validate_html_failed.txt", response)
	if err != nil {
		t.Fatal(err)
	}
	if want := "52-kak-stat-logopedom/logs/validate_html_failed.txt"; path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != response {
		t.Fatalf("содержимое = %q, want %q", data, response)
	}
}

func TestSaveDiagnosticsReplacesPreviousRun(t *testing.T) {
	root := t.TempDir()
	writer := NewWriter(root)
	if _, err := writer.SaveDiagnostics("52", "tema", PrepareSubdirectory, "input.json", map[string]any{"run": 1}); err != nil {
		t.Fatal(err)
	}
	path, err := writer.SaveDiagnostics("52", "tema", PrepareSubdirectory, "input.json", map[string]any{"run": 2})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["run"] != float64(2) {
		t.Fatalf("повторный прогон не заменил файл: %v", decoded)
	}
	entries, err := os.ReadDir(filepath.Join(root, "52-tema", PrepareSubdirectory))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("в каталоге диагностики %d файлов, ожидался один: %v", len(entries), entries)
	}
}

func TestSaveDiagnosticsRejectsPathEscapes(t *testing.T) {
	writer := NewWriter(t.TempDir())
	if _, err := writer.SaveDiagnostics("52", "tema", "..", "input.json", map[string]any{}); err == nil {
		t.Fatal("подкаталог .. принят")
	}
	if _, err := writer.SaveDiagnostics("52", "tema", PrepareSubdirectory, "../input.json", map[string]any{}); err == nil {
		t.Fatal("имя файла с выходом за каталог принято")
	}
}

func TestOpenArticleLogCreatesAndAppends(t *testing.T) {
	root := t.TempDir()
	writer := NewWriter(root)

	file, path, err := writer.OpenArticleLog("52", "kak-stat-logopedom", "prepare.log")
	if err != nil {
		t.Fatal(err)
	}
	if want := "52-kak-stat-logopedom/logs/prepare.log"; path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if _, err := file.WriteString("первая строка\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	// Каталог статьи уже существует: slug больше не нужен.
	reopened, _, err := writer.OpenArticleLog("52", "", "prepare.log")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.WriteString("вторая строка\n"); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	if want := "первая строка\nвторая строка\n"; string(data) != want {
		t.Fatalf("лог = %q, want %q", data, want)
	}
}

func TestOpenArticleLogFailsWithoutArticleDirectory(t *testing.T) {
	writer := NewWriter(t.TempDir())
	if _, _, err := writer.OpenArticleLog("52", "", "prepare.log"); err == nil {
		t.Fatal("лог открыт для несуществующего каталога статьи")
	}
}

func TestOpenArticleLogRefusesAmbiguousArticleDirectory(t *testing.T) {
	root := t.TempDir()
	writer := NewWriter(root)
	// Slug статьи изменился, и от прошлого прогона остался каталог со старым именем.
	for _, directory := range []string{"52-staryy-slug", "52-novyy-slug"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := writer.OpenArticleLog("52", "", "generate.log")
	if err == nil {
		t.Fatal("лог открыт при двух каталогах одной статьи")
	}
	if !strings.Contains(err.Error(), "несколько каталогов") {
		t.Fatalf("error = %v", err)
	}
	// С известным slug неоднозначности нет.
	file, path, err := writer.OpenArticleLog("52", "novyy-slug", "generate.log")
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if want := "52-novyy-slug/logs/generate.log"; path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestResetDiagnosticsRemovesOnlyPrepareDirectory(t *testing.T) {
	root := t.TempDir()
	writer := NewWriter(root)
	if _, err := writer.SaveDiagnostics("52", "tema", PrepareSubdirectory, "arsenkin.json", map[string]any{"run": "прошлый"}); err != nil {
		t.Fatal(err)
	}
	logFile, _, err := writer.OpenArticleLog("52", "tema", "prepare.log")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := logFile.WriteString("строка прошлого прогона\n"); err != nil {
		t.Fatal(err)
	}
	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(root, "52-tema", "generated")
	if err := os.MkdirAll(generated, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generated, "structure.txt"), []byte("структура"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "52-tema", "result.md"), []byte("результат"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writer.ResetDiagnostics("52", "tema", PrepareSubdirectory); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(root, "52-tema", PrepareSubdirectory)); !os.IsNotExist(err) {
		t.Fatalf("каталог prepare не очищен: %v", err)
	}
	for _, keep := range []string{
		filepath.Join("52-tema", "logs", "prepare.log"),
		filepath.Join("52-tema", "generated", "structure.txt"),
		filepath.Join("52-tema", "result.md"),
	} {
		if _, err := os.Stat(filepath.Join(root, keep)); err != nil {
			t.Errorf("очистка задела %s: %v", keep, err)
		}
	}

	// Повторная очистка и очистка до первого прогона ошибкой не считаются.
	if err := writer.ResetDiagnostics("52", "tema", PrepareSubdirectory); err != nil {
		t.Fatalf("повторная очистка вернула ошибку: %v", err)
	}
	if err := writer.ResetDiagnostics("99", "novaya", PrepareSubdirectory); err != nil {
		t.Fatalf("очистка несуществующей статьи вернула ошибку: %v", err)
	}
}

func TestResetDiagnosticsRejectsPathEscapes(t *testing.T) {
	writer := NewWriter(t.TempDir())
	if err := writer.ResetDiagnostics("52", "tema", ".."); err == nil {
		t.Fatal("подкаталог .. принят")
	}
	if err := writer.ResetDiagnostics("52", "", PrepareSubdirectory); err == nil {
		t.Fatal("пустой slug принят")
	}
}

func TestResetGeneratedArtifactsKeepsPrepareAndLogs(t *testing.T) {
	root := t.TempDir()
	writer := NewWriter(root)
	if _, err := writer.SaveStructure("38", "tema", "промпт структуры", "структура"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.SaveArticle("38", "tema", "промпт статьи", "статья", "model"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.SaveHTML("38", "tema", "промпт html", "<h1>Тема</h1><p>Текст</p>"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.SaveResult("38", "tema", "итог"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.SaveDiagnostics("38", "tema", PrepareSubdirectory, "arsenkin.json", map[string]any{"lsi": 3}); err != nil {
		t.Fatal(err)
	}
	logFile, _, err := writer.OpenArticleLog("38", "tema", "prepare.log")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := logFile.WriteString("история прогона\n"); err != nil {
		t.Fatal(err)
	}
	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}

	removed, err := writer.ResetGeneratedArtifacts("38", "tema")
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 4 {
		t.Fatalf("удалено %v, ожидались generated, prompts, article.html, result.md", removed)
	}
	for _, gone := range []string{"generated", "prompts", "article.html", "result.md"} {
		if _, err := os.Stat(filepath.Join(root, "38-tema", gone)); !os.IsNotExist(err) {
			t.Errorf("%s не удалён: %v", gone, err)
		}
	}
	for _, kept := range []string{
		filepath.Join("38-tema", PrepareSubdirectory, "arsenkin.json"),
		filepath.Join("38-tema", LogsSubdirectory, "prepare.log"),
	} {
		if _, err := os.Stat(filepath.Join(root, kept)); err != nil {
			t.Errorf("сброс задел %s: %v", kept, err)
		}
	}

	// Повторный сброс и сброс статьи без сгенерированных файлов ошибкой не считаются.
	second, err := writer.ResetGeneratedArtifacts("38", "tema")
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("повторный сброс удалил %v", second)
	}
}
